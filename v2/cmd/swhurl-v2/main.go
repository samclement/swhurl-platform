// swhurl-v2 is an offline adapter. Only explicitly named files are read; no files are written.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/samclement/swhurl-platform/v2/kernel"
)

const usage = `usage:
  swhurl-v2 release-id RELEASE.json
  swhurl-v2 instance-revision INSTANCE.json
  swhurl-v2 select-release --instance INSTANCE.json --release RELEASE.json --expect REVISION

Offline only. Results go to stdout; input files are never overwritten.
Exit 0: success/no-op; 2: usage/validation/refusal; 1: file/output I/O failure.
`

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.ReadFile)) }

func run(args []string, out, diagnostic io.Writer, readFile func(string) ([]byte, error)) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		return emit(out, diagnostic, []byte(usage))
	}
	if len(args) == 0 {
		fmt.Fprint(diagnostic, usage)
		return 2
	}
	command := args[0]
	if command != "release-id" && command != "instance-revision" && command != "select-release" {
		fmt.Fprint(diagnostic, usage)
		return 2
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard) // flag errors can echo rejected values
	var instancePath, releasePath, expected string
	if command == "select-release" {
		flags.StringVar(&instancePath, "instance", "", "instance file")
		flags.StringVar(&releasePath, "release", "", "release file")
		flags.StringVar(&expected, "expect", "", "reviewed instance revision")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return emit(out, diagnostic, []byte(usage))
		}
		fmt.Fprint(diagnostic, usage)
		return 2
	}
	if command == "select-release" {
		if flags.NArg() != 0 || instancePath == "" || releasePath == "" || expected == "" {
			fmt.Fprint(diagnostic, usage)
			return 2
		}
	} else {
		if flags.NArg() != 1 {
			fmt.Fprint(diagnostic, usage)
			return 2
		}
		if command == "release-id" {
			releasePath = flags.Arg(0)
		} else {
			instancePath = flags.Arg(0)
		}
	}
	var spec kernel.InstanceSpec
	if instancePath != "" {
		data, err := readFile(instancePath)
		if err != nil {
			fmt.Fprintln(diagnostic, "io: instance: cannot read input file")
			return 1
		}
		spec, err = kernel.DecodeInstance(data)
		if err != nil {
			return fail(diagnostic, err)
		}
		if command == "instance-revision" {
			revision, err := spec.Revision()
			if err != nil {
				return fail(diagnostic, err)
			}
			return emit(out, diagnostic, []byte(revision))
		}
	}
	data, err := readFile(releasePath)
	if err != nil {
		fmt.Fprintln(diagnostic, "io: release: cannot read input file")
		return 1
	}
	release, err := kernel.DecodeRelease(data)
	if err != nil {
		return fail(diagnostic, err)
	}
	if command == "release-id" {
		return emit(out, diagnostic, []byte(release.ReleaseID))
	}
	selected, err := kernel.SelectRelease(spec, release, kernel.InstanceRevision(expected))
	if err != nil {
		return fail(diagnostic, err)
	}
	data, err = kernel.EncodeInstance(selected)
	if err != nil {
		return fail(diagnostic, err)
	}
	return emit(out, diagnostic, data)
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
