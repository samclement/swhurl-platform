// swhurl-v2 is an offline adapter. Only explicitly named files are read.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

const usage = `usage:
  swhurl-v2 release-id RELEASE.json
  swhurl-v2 instance-revision INSTANCE.json
  swhurl-v2 select-release --instance INSTANCE.json --release RELEASE.json --expect REVISION
  swhurl-v2 compile --instance INSTANCE.json --release RELEASE.json --profile PROFILE.json [--out DIR]
  swhurl-v2 observe --instance INSTANCE.json --release RELEASE.json --profile PROFILE.json \
      --revision COMMIT --kubeconfig KUBECONFIG.json [--source NAME]
  swhurl-v2 preview --app APP.json --settings INSTANCE.json --release RELEASE.json \
      --committed HELMRELEASE.json --profile PROFILE.json

Input files are never overwritten. Without --out the compilation document goes to stdout and nothing
is written. With --out every compiled file is written under DIR, one "written PATH" or
"unchanged PATH" line each; DIR must not be inside apps/, and nothing outside it is touched.

observe compiles the instance, reads its unit, HelmRelease, Deployment and pods from the API server the
kubeconfig names (the JSON form that "kubectl config view --raw -o json" prints), and writes the
observation report to stdout with a one-line summary on stderr. It reads only, and --revision is the
source revision Flux should have applied.

preview renders one predecessor instance from its records and compares the result with the HelmRelease
that predecessor committed, writing a v2-preview document to stdout. It reads only the five named JSON
files and applies nothing: the legacy records are given as JSON, so no legacy YAML is read here.
Exit 0: success/no-op (for observe: a report was produced, whatever its verdict); 2: usage/validation/
refusal; 1: file or output I/O failure.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, readInput, files.New()))
}

// maxInput bounds what is read from a named file; a descriptor is a few hundred bytes.
const maxInput = 1 << 20

var errTooLarge = errors.New("input file is larger than 1 MiB")

func readInput(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxInput+1))
	if err == nil && len(data) > maxInput {
		return nil, errTooLarge
	}
	return data, err
}

func run(args []string, out, diagnostic io.Writer, readFile func(string) ([]byte, error), writer files.Writer) int {
	r, outcome := parse(args)
	switch outcome {
	case help:
		return emit(out, diagnostic, []byte(usage))
	case misuse:
		fmt.Fprint(diagnostic, usage)
		return 2
	}
	c := cli{out, diagnostic, readFile, writer}
	switch r.command {
	case "release-id":
		return c.releaseID(r.release)
	case "instance-revision":
		return c.instanceRevision(r.instance)
	case "compile":
		return c.compile(r)
	case "observe":
		return c.observe(r)
	case "preview":
		return c.preview(r)
	}
	return c.selectRelease(r)
}

// request is one parsed command line: the command, the files, revision, credentials and source it
// names, and where to write.
type request struct {
	command, instance, release, expected, profile, out string
	revision, kubeconfig, source                       string
	app, settings, committed                           string
	outFlag                                            *once
}

type outcome int

const (
	parsed outcome = iota
	help
	misuse
)

func parse(args []string) (request, outcome) {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		return request{}, help
	}
	if len(args) == 0 || !slices.Contains([]string{"release-id", "instance-revision", "select-release", "compile", "observe", "preview"}, args[0]) {
		return request{}, misuse
	}
	r := request{command: args[0]}
	flags, err := r.parseFlags(args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return r, help
	}
	if err != nil || !r.take(flags) {
		return r, misuse
	}
	return r, parsed
}

// parseFlags parses the flags select-release and compile take into r; the other commands take none.
func (r *request) parseFlags(args []string) (*flag.FlagSet, error) {
	flags := flag.NewFlagSet(r.command, flag.ContinueOnError)
	flags.SetOutput(io.Discard) // flag errors can echo rejected values
	if r.command == "select-release" || r.command == "compile" || r.command == "observe" {
		flags.Var(&once{value: &r.instance}, "instance", "instance file")
		flags.Var(&once{value: &r.release}, "release", "release file")
	}
	if r.command == "preview" {
		flags.Var(&once{value: &r.app}, "app", "legacy app record")
		flags.Var(&once{value: &r.settings}, "settings", "legacy instance record")
		flags.Var(&once{value: &r.release}, "release", "legacy release record")
		flags.Var(&once{value: &r.committed}, "committed", "the HelmRelease the predecessor committed")
		flags.Var(&once{value: &r.profile}, "profile", "platform profile file")
	}
	if r.command == "observe" {
		flags.Var(&once{value: &r.profile}, "profile", "platform profile file")
		flags.Var(&once{value: &r.revision}, "revision", "source revision Flux should have applied")
		flags.Var(&once{value: &r.kubeconfig}, "kubeconfig", "kubeconfig file, in JSON")
		flags.Var(&once{value: &r.source}, "source", "name of the evidence source (default: the kubeconfig's current context)")
	}
	if r.command == "select-release" {
		flags.Var(&once{value: &r.expected}, "expect", "reviewed instance revision")
	}
	if r.command == "compile" {
		flags.Var(&once{value: &r.profile}, "profile", "platform profile file")
		r.outFlag = &once{value: &r.out}
		flags.Var(r.outFlag, "out", "destination directory")
	}
	return flags, flags.Parse(args)
}

// take fills r from the arguments left after the flags; false is a usage error.
func (r *request) take(flags *flag.FlagSet) bool {
	if named := r.named(); named != nil {
		if flags.NArg() != 0 || slices.Contains(named, "") {
			return false
		}
		// An explicitly empty destination is a usage error, not a request to print the document.
		return r.outFlag == nil || !r.outFlag.set || r.out != ""
	}
	if flags.NArg() != 1 {
		return false
	}
	if r.command == "release-id" {
		r.release = flags.Arg(0)
	} else {
		r.instance = flags.Arg(0)
	}
	return true
}

// named is the flag values a command requires, or nil for a command that takes one file argument.
func (r *request) named() []string {
	switch r.command {
	case "select-release":
		return []string{r.instance, r.release, r.expected}
	case "compile":
		return []string{r.instance, r.release, r.profile}
	case "observe":
		return []string{r.instance, r.release, r.profile, r.revision, r.kubeconfig}
	case "preview":
		return []string{r.app, r.settings, r.release, r.committed, r.profile}
	}
	return nil
}

// once is a flag that may be given at most once: a repeat is a usage error, not the last one winning.
type once struct {
	value *string
	set   bool
}

func (o *once) String() string { return "" }

func (o *once) Set(value string) error {
	if o.set {
		return errors.New("flag repeated")
	}
	o.set, *o.value = true, value
	return nil
}

// cli runs one parsed command. Each step returns 0 or the exit status after its diagnostic.
type cli struct {
	out, diagnostic io.Writer
	readFile        func(string) ([]byte, error)
	writer          files.Writer
}

func (c cli) releaseID(path string) int {
	release, status := c.release(path)
	if status != 0 {
		return status
	}
	return emit(c.out, c.diagnostic, []byte(release.ReleaseID))
}

func (c cli) instanceRevision(path string) int {
	spec, status := c.instance(path)
	if status != 0 {
		return status
	}
	revision, err := spec.Revision()
	if err != nil {
		return fail(c.diagnostic, err)
	}
	return emit(c.out, c.diagnostic, []byte(revision))
}

func (c cli) selectRelease(r request) int {
	spec, status := c.instance(r.instance)
	if status != 0 {
		return status
	}
	release, status := c.release(r.release)
	if status != 0 {
		return status
	}
	selected, err := kernel.SelectRelease(spec, release, kernel.InstanceRevision(r.expected))
	if err != nil {
		return fail(c.diagnostic, err)
	}
	data, err := kernel.EncodeInstance(selected)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	return emit(c.out, c.diagnostic, data)
}

func (c cli) compile(r request) int {
	spec, status := c.instance(r.instance)
	if status != 0 {
		return status
	}
	release, status := c.release(r.release)
	if status != 0 {
		return status
	}
	data, status := c.read(r.profile, "profile")
	if status != 0 {
		return status
	}
	profile, err := kernel.DecodeProfile(data)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	compiled, err := kernel.Compile(spec, release, profile)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	if r.out != "" {
		return c.write(r.out, compiled)
	}
	return emit(c.out, c.diagnostic, kernel.EncodeCompilation(compiled))
}

// write is the only effect the CLI has: it hands the compiled files to the adapter and reports each
// one. A destination inside the checkout's apps/ is refused, because that is where v1's instances are
// recorded. Outcomes are printed before any failure, so a partly written destination is visible.
func (c cli) write(dir string, compiled kernel.Compilation) int {
	if _, inside := appTree(dir); inside {
		fmt.Fprintf(c.diagnostic, "%s: destination: %s\n", files.RefusedDestination, "inside the v1 app tree")
		return 2
	}
	outcomes, err := c.writer.Write(dir, compiled.Files)
	for _, outcome := range outcomes {
		if _, writeErr := fmt.Fprintf(c.out, "%s %s\n", outcome.State, outcome.Path); writeErr != nil {
			fmt.Fprintln(c.diagnostic, "io: output: cannot write result")
			return 1
		}
	}
	if err == nil {
		return 0
	}
	fmt.Fprintln(c.diagnostic, err)
	var refusal *files.Error
	if errors.As(err, &refusal) && refusal.Code == files.RefusedDestination {
		return 2
	}
	return 1
}

// appTree reports the v1 app tree a destination would write into, if any: the apps/ directory of the
// checkout that contains it. A checkout has .git or clusters/ beside apps/, which is what makes an
// apps/ directory this repository's rather than an unrelated folder that happens to be called apps.
func appTree(dir string) (string, bool) {
	resolved, err := resolve(dir)
	if err != nil {
		return "", false
	}
	for current := resolved; ; current = filepath.Dir(current) {
		if isCheckout(current) {
			tree := filepath.Join(current, kernel.AppTree)
			if resolved == tree || strings.HasPrefix(resolved, tree+string(filepath.Separator)) {
				return tree, true
			}
			return "", false
		}
		if parent := filepath.Dir(current); parent == current {
			return "", false
		}
	}
}

func isCheckout(dir string) bool {
	for _, marker := range []string{".git", "clusters"} {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return true
		}
	}
	return false
}

// resolve is filepath.Abs plus the symlinks of the nearest existing ancestor: the destination may not
// exist yet, but a link the operator passes must not hide where the files would really go.
func resolve(dir string) (string, error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	existing, rest := absolute, ""
	for {
		if _, err := os.Lstat(existing); err == nil {
			resolved, err := filepath.EvalSymlinks(existing)
			if err != nil {
				return "", err
			}
			return filepath.Join(resolved, rest), nil
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return absolute, nil
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}
}

func (c cli) release(path string) (kernel.ReleaseBundle, int) {
	data, status := c.read(path, "release")
	if status != 0 {
		return kernel.ReleaseBundle{}, status
	}
	release, err := kernel.DecodeRelease(data)
	if err != nil {
		return kernel.ReleaseBundle{}, fail(c.diagnostic, err)
	}
	return release, 0
}

// profile reads and decodes the platform profile, which the compiler and the preview both need.
func (c cli) profile(path string) (kernel.PlatformProfile, int) {
	data, status := c.read(path, "profile")
	if status != 0 {
		return kernel.PlatformProfile{}, status
	}
	profile, err := kernel.DecodeProfile(data)
	if err != nil {
		return kernel.PlatformProfile{}, fail(c.diagnostic, err)
	}
	return profile, 0
}

func (c cli) instance(path string) (kernel.InstanceSpec, int) {
	data, status := c.read(path, "instance")
	if status != 0 {
		return kernel.InstanceSpec{}, status
	}
	spec, err := kernel.DecodeInstance(data)
	if err != nil {
		return kernel.InstanceSpec{}, fail(c.diagnostic, err)
	}
	return spec, 0
}

// read refuses a file over maxInput (exit 2, like any refused input); other read errors are I/O (exit 1).
func (c cli) read(path, what string) ([]byte, int) {
	data, err := c.readFile(path)
	switch {
	case errors.Is(err, errTooLarge):
		fmt.Fprintf(c.diagnostic, "invalid: %s: %v\n", what, errTooLarge)
		return nil, 2
	case err != nil:
		fmt.Fprintf(c.diagnostic, "io: %s: cannot read input file\n", what)
		return nil, 1
	}
	return data, 0
}

func fail(diagnostic io.Writer, err error) int { fmt.Fprintln(diagnostic, err); return 2 }

func emit(out, diagnostic io.Writer, data []byte) int {
	if len(data) == 0 || data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	n, err := out.Write(data)
	if err != nil || n != len(data) {
		fmt.Fprintln(diagnostic, "io: output: cannot write result")
		return 1
	}
	return 0
}
