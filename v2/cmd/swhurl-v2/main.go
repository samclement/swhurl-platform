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
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

const usage = `usage:
  swhurl-v2 release-id RELEASE.json
  swhurl-v2 instance-revision INSTANCE.json
  swhurl-v2 select-release --instance INSTANCE.json --release RELEASE.json --expect REVISION
  swhurl-v2 resolve --snapshot SNAPSHOT.json
  swhurl-v2 egress --snapshot SNAPSHOT.json [--previous EGRESS.json]
  swhurl-v2 proxy --allocation EGRESS.json --profile PROFILE.json [--out DIR]
  swhurl-v2 compile --instance INSTANCE.json --release RELEASE.json --profile PROFILE.json [--calls CALLS.json] [--out DIR]
  swhurl-v2 observe --instance INSTANCE.json --release RELEASE.json --profile PROFILE.json \
      --revision COMMIT --kubeconfig KUBECONFIG.json [--calls CALLS.json] [--egress EGRESS.json] [--source NAME]
  swhurl-v2 preview --app APP.json --settings INSTANCE.json --release RELEASE.json \
      --committed HELMRELEASE.json --profile PROFILE.json
  swhurl-v2 shadow --snapshot SNAPSHOT.json --profile PROFILE.json [--kubeconfig KUBECONFIG.json]
  swhurl-v2 migrate --snapshot SNAPSHOT.json --app APPLICATION --env ENVIRONMENT --profile PROFILE.json \
      --source-repository OWNER/NAME --source-commit COMMIT --out DIR [--calls CALLS.json] [--egress EGRESS.json]

Input files are never overwritten. Without --out the compilation document goes to stdout and nothing
is written. With --out every compiled file is written under DIR, one "written PATH" or
"unchanged PATH" line each; DIR must not be inside apps/, and nothing outside it is touched.

observe compiles the instance, reads its unit, HelmRelease, Deployment and pods from the API server the
kubeconfig names (the JSON form that "kubectl config view --raw -o json" prints), and writes the
observation report to stdout with a one-line summary on stderr. It reads only, and --revision is the
source revision Flux should have applied.

shadow analyses one pinned snapshot: every instance is previewed and compared with its own committed
HelmRelease, and the whole snapshot's ownership is checked, so a preview's collision with what the
predecessor already declares is reported rather than assumed away. It reads only the two named files.
The snapshot is the JSON document "make v2-snapshot REVISION= OUT=" writes. With --kubeconfig it also
reads every object a preview would create from that API server, read-only, and reports each as present,
absent or unavailable; a failed read is never reported as absence.

egress allocates one proxy port per instance, keeping every port the previous document gave and giving a
new instance the lowest free one. compile takes the document with --egress.

compile refuses a database instance without --egress: its backup job may reach only the proxy, so it
could not upload. A database instance also renders its backup from the profile's backup facts.

proxy renders the platform's egress proxy from one allocation: its configuration, with a port per
instance and the hosts each instance's grants allow, and a policy admitting each port from that
instance's namespace only. Grants are enforced, so an instance reaches what it declares and nothing
else. The proxy's deployment stays the platform's hand-written HelmRelease, which reads what proxy
writes; --out writes the rendered files under DIR and nothing else.

resolve reads one snapshot's declarations and writes the resolved connections as a v2-calls document:
which pairs are connected, which wait for the other side and why, and which are refused. compile takes
that document with --calls and renders only the pairs it names for the instance being compiled.

preview renders one predecessor instance from its records and compares the result with the HelmRelease
that predecessor committed, writing a v2-preview document to stdout. It reads only the five named JSON
files and applies nothing: the legacy records are given as JSON, so no legacy YAML is read here.

migrate turns one instance out of a pinned snapshot into v2's own records, compiles them with the profile
and the optional resolution and allocation, and writes both under --out: the record pair at
v2-records/APPLICATION/ENVIRONMENT/ and the compiled files. The source repository and the full commit
its image was built from are the operator's, because the records keep only an abbreviation: they are
checked against it, never expanded from it. The report on stdout says which files it wrote, where every
recorded fact came from, which of the predecessor's declarations the resolution does not connect, and
every legacy fact the pair cannot carry. It deletes nothing: removing v1's records, manifests and unit is
the handover step, and the unit's registration stays a reviewed edit.
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
	return commands[r.command](c, r)
}

// commands is every command and the step that runs it, in one table so the dispatcher and the usage check
// cannot disagree.
var commands = map[string]func(cli, request) int{
	"release-id":        func(c cli, r request) int { return c.releaseID(r.release) },
	"instance-revision": func(c cli, r request) int { return c.instanceRevision(r.instance) },
	"select-release":    cli.selectRelease,
	"compile":           cli.compile,
	"observe":           cli.observe,
	"preview":           cli.preview,
	"shadow":            cli.shadow,
	"resolve":           cli.resolve,
	"egress":            cli.egress,
	"proxy":             cli.proxy,
	"migrate":           cli.migrate,
}

// request is one parsed command line: the command, the files, revision, credentials and source it
// names, and where to write.
type request struct {
	command, instance, release, expected, profile, out string
	revision, kubeconfig, source                       string
	app, settings, committed, snapshot, callFile       string
	env, sourceRepository, sourceCommit                string
	previous, egressFile, allocation                   string
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
	if len(args) == 0 {
		return request{}, misuse
	}
	if _, known := commands[args[0]]; !known {
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

// parseFlags parses the flags a command takes into r; the other commands take none.
func (r *request) parseFlags(args []string) (*flag.FlagSet, error) {
	flags := flag.NewFlagSet(r.command, flag.ContinueOnError)
	flags.SetOutput(io.Discard) // flag errors can echo rejected values
	r.registerInputs(flags)
	r.registerExtras(flags, r.command)
	return flags, flags.Parse(args)
}

// registerInputs adds the file flags the v1-style commands share.
func (r *request) registerInputs(flags *flag.FlagSet) {
	switch r.command {
	case "select-release", "compile", "observe":
		flags.Var(&once{value: &r.instance}, "instance", "instance file")
		flags.Var(&once{value: &r.release}, "release", "release file")
	case "preview":
		flags.Var(&once{value: &r.release}, "release", "legacy release record")
	}
}

// registerExtras adds what each command alone takes: its remaining inputs, and for compile its destination.
func (r *request) registerExtras(flags *flag.FlagSet, command string) {
	switch command {
	case "select-release":
		flags.Var(&once{value: &r.expected}, "expect", "reviewed instance revision")
	case "compile":
		flags.Var(&once{value: &r.profile}, "profile", "platform profile file")
		flags.Var(&once{value: &r.callFile}, "calls", "resolved connections document (optional)")
		flags.Var(&once{value: &r.egressFile}, "egress", "egress allocation document (optional)")
		r.outFlag = &once{value: &r.out}
		flags.Var(r.outFlag, "out", "destination directory")
	case "observe":
		flags.Var(&once{value: &r.profile}, "profile", "platform profile file")
		flags.Var(&once{value: &r.callFile}, "calls", "resolved connections document (optional)")
		flags.Var(&once{value: &r.egressFile}, "egress", "egress allocation document (optional)")
		flags.Var(&once{value: &r.revision}, "revision", "source revision Flux should have applied")
		flags.Var(&once{value: &r.kubeconfig}, "kubeconfig", "kubeconfig file, in JSON")
		flags.Var(&once{value: &r.source}, "source", "name of the evidence source (default: the kubeconfig's current context)")
	case "preview":
		flags.Var(&once{value: &r.app}, "app", "legacy app record")
		flags.Var(&once{value: &r.settings}, "settings", "legacy instance record")
		flags.Var(&once{value: &r.committed}, "committed", "the HelmRelease the predecessor committed")
		flags.Var(&once{value: &r.profile}, "profile", "platform profile file")
	case "shadow":
		flags.Var(&once{value: &r.snapshot}, "snapshot", "snapshot document")
		flags.Var(&once{value: &r.profile}, "profile", "platform profile file")
		flags.Var(&once{value: &r.kubeconfig}, "kubeconfig", "kubeconfig file, in JSON (enables the live ownership check)")
	case "resolve":
		flags.Var(&once{value: &r.snapshot}, "snapshot", "snapshot document")
	case "egress":
		flags.Var(&once{value: &r.snapshot}, "snapshot", "snapshot document")
		flags.Var(&once{value: &r.previous}, "previous", "a previous allocation to keep ports from (optional)")
	case "proxy":
		flags.Var(&once{value: &r.allocation}, "allocation", "egress allocation document")
		flags.Var(&once{value: &r.profile}, "profile", "platform profile file")
		r.outFlag = &once{value: &r.out}
		flags.Var(r.outFlag, "out", "destination directory")
	case "migrate":
		flags.Var(&once{value: &r.snapshot}, "snapshot", "snapshot document")
		flags.Var(&once{value: &r.app}, "app", "application to migrate")
		flags.Var(&once{value: &r.env}, "env", "environment to migrate")
		flags.Var(&once{value: &r.profile}, "profile", "platform profile file")
		flags.Var(&once{value: &r.sourceRepository}, "source-repository", "the app's source repository, owner/name")
		flags.Var(&once{value: &r.sourceCommit}, "source-commit", "the full commit its image was built from")
		flags.Var(&once{value: &r.callFile}, "calls", "resolved connections document (optional)")
		flags.Var(&once{value: &r.egressFile}, "egress", "egress allocation document (optional)")
		r.outFlag = &once{value: &r.out}
		flags.Var(r.outFlag, "out", "destination directory")
	}
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
	case "shadow":
		return []string{r.snapshot, r.profile}
	case "resolve":
		return []string{r.snapshot}
	case "egress":
		return []string{r.snapshot}
	case "proxy":
		return []string{r.allocation, r.profile}
	case "migrate":
		return []string{r.snapshot, r.app, r.env, r.profile, r.sourceRepository, r.sourceCommit, r.out}
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
	inputs, status := c.inputs(r)
	if status != 0 {
		return status
	}
	if r.out != "" {
		if c.appTreeRefused(r.out) {
			return 2
		}
		result, err := orchestration.CompileInto(inputs, r.out, c.writer)
		return c.wrote(result.Written, err)
	}
	compiled, err := orchestration.Compile(inputs)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	return emit(c.out, c.diagnostic, kernel.EncodeCompilation(compiled))
}

// inputs reads the documents a compilation takes: the instance's records, the profile, and the two optional
// documents. Which of them a use case needs is the kernel's to refuse, not this command's to remember.
func (c cli) inputs(r request) (orchestration.Inputs, int) {
	var inputs orchestration.Inputs
	for _, read := range []struct {
		path   string
		what   string
		target *[]byte
	}{
		{r.instance, "instance", &inputs.Instance},
		{r.release, "release", &inputs.Release},
		{r.profile, "profile", &inputs.Profile},
		{r.callFile, "", &inputs.Calls},
		{r.egressFile, "", &inputs.Egress},
	} {
		if read.path == "" {
			continue // an optional document the operator did not give
		}
		data, status := c.read(read.path, read.what)
		if status != 0 {
			return orchestration.Inputs{}, status
		}
		*read.target = data
	}
	return inputs, 0
}

// wrote reports a use case's file outcomes and maps its failure to an exit status. Outcomes are printed
// before the failure, so a partly written destination is visible.
func (c cli) wrote(outcomes []files.Outcome, err error) int {
	for _, outcome := range outcomes {
		if _, writeErr := fmt.Fprintf(c.out, "%s %s\n", outcome.State, outcome.Path); writeErr != nil {
			fmt.Fprintln(c.diagnostic, "io: output: cannot write result")
			return 1
		}
	}
	if err == nil {
		return 0
	}
	return c.failure(err)
}

// failure reports a use case's error and maps it to an exit status. Reading an input is the command's own
// step and already reports its I/O as 1, so what reaches here is a refusal or validation (2) unless the
// filesystem itself failed while writing (1).
func (c cli) failure(err error) int {
	fmt.Fprintln(c.diagnostic, err)
	var failure *files.Error
	if errors.As(err, &failure) && failure.Code == files.IO {
		return 1
	}
	return 2
}

// writeFiles writes rendered files under dir and reports each one, for a command whose use case returns the
// files rather than writing them. A destination inside the checkout's apps/ is refused here: that tree is
// where v1's instances are recorded, which is this checkout's rule rather than a use case's.
func (c cli) writeFiles(dir string, rendered []kernel.File) int {
	if c.appTreeRefused(dir) {
		return 2
	}
	outcomes, err := c.writer.Write(dir, rendered)
	return c.wrote(outcomes, err)
}

// appTreeRefused reports and refuses a destination inside the checkout's apps/ tree.
func (c cli) appTreeRefused(dir string) bool {
	if _, inside := appTree(dir); !inside {
		return false
	}
	fmt.Fprintf(c.diagnostic, "%s: destination: %s\n", files.RefusedDestination, "inside the v1 app tree")
	return true
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
