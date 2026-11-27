package console

import (
	"path/filepath"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/pages"
)

// A change's steps, typed: each one is an operation with what it is given, run as one call to the capability that
// owns it, and quoted in the pull request as the line a person could type in a checkout instead. The line is derived
// from the step, so the two cannot drift: it names the records and the profile as a checkout does, and the base commit
// as @base — the commit the console filled in, which nobody can type before it exists.

// Step is one operation of a change, with what it is given.
type Step interface {
	// Operation is the planned operation the step runs.
	Operation() Operation
	// Quote is the line a person could type in a checkout to run the same step.
	Quote() string
}

// profilePath is where a checkout keeps the platform profile.
const profilePath = "v2/testdata/profile.json"

// WorkingCopy is the checkout a change's steps run in: dir is its root, and the records and documents are where every
// checkout keeps them, the paths each step's line names relative to it.
func WorkingCopy(dir string) files.Checkout {
	return files.Checkout{Root: dir, Records: filepath.Join(dir, pages.ConsoleRecordsRoot),
		Profile: filepath.Join(dir, profilePath), Calls: filepath.Join(dir, pages.ConsoleCallsFile),
		Egress: filepath.Join(dir, pages.ConsoleEgressFile)}
}

// EditStep changes one setting, name=value, of one instance's records; an app-level setting names no environment.
type EditStep struct{ App, Env, Setting string }

func (EditStep) Operation() Operation { return Edit }

func (s EditStep) Quote() string {
	line := "swhurl-v2 edit --records " + pages.ConsoleRecordsRoot + " --app " + s.App
	if s.Env != "" {
		line += " --env=" + s.Env
	}
	return line + " --set " + s.Setting + " --profile " + profilePath + " --out . --calls " + pages.ConsoleCallsFile +
		" --egress " + pages.ConsoleEgressFile
}

// RemoveStep deletes one instance and everything v2 rendered for it.
type RemoveStep struct{ App, Env string }

func (RemoveStep) Operation() Operation { return Remove }

func (s RemoveStep) Quote() string {
	return "swhurl-v2 remove --records " + pages.ConsoleRecordsRoot + " --app " + s.App + " --env=" + s.Env +
		" --profile " + profilePath + " --calls " + pages.ConsoleCallsFile + " --out ."
}

// PromoteStep selects an instance's release from staging's record, if the instance's record is still Expected.
type PromoteStep struct{ App, Env, Expected string }

func (PromoteStep) Operation() Operation { return Promote }

func (s PromoteStep) Quote() string {
	return "swhurl-v2 promote --records " + pages.ConsoleRecordsRoot + " --app " + s.App + " --env=" + s.Env +
		" --profile " + profilePath + " --expect=" + s.Expected + " --out . --calls " + pages.ConsoleCallsFile +
		" --egress " + pages.ConsoleEgressFile
}

// RenderStep writes what the whole tree of records derives, recording the change's base commit.
type RenderStep struct{}

func (RenderStep) Operation() Operation { return Render }

func (RenderStep) Quote() string {
	return "swhurl-v2 render --records " + pages.ConsoleRecordsRoot + " --profile " + profilePath +
		" --out . --revision @base"
}
