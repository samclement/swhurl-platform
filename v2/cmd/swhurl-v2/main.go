// swhurl-v2 is an offline adapter. Only explicitly named files are read; no files are written.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/samclement/swhurl-platform/v2/kernel"
)

const usage = `usage:
  swhurl-v2 release-id RELEASE.json
  swhurl-v2 instance-revision INSTANCE.json
  swhurl-v2 select-release --instance INSTANCE.json --release RELEASE.json --expect REVISION
  swhurl-v2 compile --instance INSTANCE.json --release RELEASE.json --profile PROFILE.json

Offline only. Results go to stdout; input files are never overwritten.
Exit 0: success/no-op; 2: usage/validation/refusal; 1: file/output I/O failure.
`

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, readInput)) }

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

func run(args []string, out, diagnostic io.Writer, readFile func(string) ([]byte, error)) int {
	r, outcome := parse(args)
	switch outcome {
	case help:
		return emit(out, diagnostic, []byte(usage))
	case misuse:
		fmt.Fprint(diagnostic, usage)
		return 2
	}
	c := cli{out, diagnostic, readFile}
	switch r.command {
	case "release-id":
		return c.releaseID(r.release)
	case "instance-revision":
		return c.instanceRevision(r.instance)
	case "compile":
		return c.compile(r)
	}
	return c.selectRelease(r)
}

// request is one parsed command line: the command and the files and revision it names.
type request struct{ command, instance, release, expected, profile string }

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
	if len(args) == 0 || !slices.Contains([]string{"release-id", "instance-revision", "select-release", "compile"}, args[0]) {
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
	if r.command == "select-release" || r.command == "compile" {
		flags.Var(&once{value: &r.instance}, "instance", "instance file")
		flags.Var(&once{value: &r.release}, "release", "release file")
	}
	if r.command == "select-release" {
		flags.Var(&once{value: &r.expected}, "expect", "reviewed instance revision")
	}
	if r.command == "compile" {
		flags.Var(&once{value: &r.profile}, "profile", "platform profile file")
	}
	return flags, flags.Parse(args)
}

// take fills r from the arguments left after the flags; false is a usage error.
func (r *request) take(flags *flag.FlagSet) bool {
	if named := r.named(); named != nil {
		return flags.NArg() == 0 && !slices.Contains(named, "")
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
	return emit(c.out, c.diagnostic, kernel.EncodeCompilation(compiled))
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
