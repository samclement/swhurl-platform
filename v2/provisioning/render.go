package provisioning

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	connectivityrules "github.com/samclement/swhurl-platform/v2/connectivity/rules"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/provisioning/rules"
)

// A whole-tree render writes every file v2 derives from a checkout's records, or checks that the checkout already
// holds them. Both read the same tree and decide the same files; a check compares the two documents that record a
// revision at the revision each file on disk records, so only their content can differ.

// Rendered is what a render wrote: the platform's files first, then each instance's, as the writer reported them.
type Rendered struct{ Written []files.Outcome }

// Summary is the render's one line: how many files each outcome has.
func (r Rendered) Summary() string {
	counts := map[string]int{}
	for _, outcome := range r.Written {
		counts[outcome.State]++
	}
	return fmt.Sprintf("render: %d written, %d unchanged, %d removed", counts[files.Written], counts[files.Unchanged],
		counts[files.Removed])
}

// Checked is what a check compared: how many files, and the paths of the ones the checkout does not hold as rendered.
type Checked struct {
	Files       int
	Differences []string
}

// Summary is the check's one line, naming every file that differs.
func (c Checked) Summary() string {
	summary := fmt.Sprintf("render: %d file(s) checked, %d differ", c.Files, len(c.Differences))
	if len(c.Differences) > 0 {
		summary += ": " + strings.Join(c.Differences, ", ")
	}
	return summary
}

// completed is one instance's compilation and the files its folder holds.
type completed struct {
	compilation kernel.Compilation
	files       []kernel.File
}

// Render writes every file the checkout's records derive, with the documents recording revision, the commit the
// records were read at. Every file is decided before the first is written, so a tree anything refuses writes nothing;
// a write that fails part-way reports the outcomes before it.
func Render(checkout files.Checkout, revision string, writer files.Writer) (Rendered, error) {
	if !rules.CommitRevision(revision) {
		return Rendered{}, errors.New("invalid: revision: expected_a_full_commit")
	}
	tree, instances, err := decideTree(checkout, revision)
	if err != nil {
		return Rendered{}, err
	}
	platform, err := tree.Documents(revision, revision)
	if err != nil {
		return Rendered{}, err
	}
	outcomes, err := writer.Write(checkout.Root, platform)
	for _, one := range instances {
		if err != nil {
			break
		}
		var written []files.Outcome
		written, err = WriteCompiled(checkout.Root, one.compilation, one.files, writer)
		outcomes = append(outcomes, written...)
	}
	return Rendered{Written: outcomes}, err
}

// CheckRender compares every file the checkout's records derive with the one the checkout holds, and writes nothing.
func CheckRender(checkout files.Checkout) (Checked, error) {
	tree, instances, err := decideTree(checkout, rules.ProvisionalRevision)
	if err != nil {
		return Checked{}, err
	}
	platform, err := tree.Documents(recordedRevision(checkout, tree.CallsPath),
		recordedRevision(checkout, rules.AllocationPath))
	if err != nil {
		return Checked{}, err
	}
	compared := platform
	for _, one := range instances {
		compared = append(compared, one.files...)
	}
	checked := Checked{Files: len(compared), Differences: []string{}}
	for _, file := range compared {
		same, err := checkout.Holds(file)
		if err != nil {
			return Checked{}, err
		}
		if !same {
			checked.Differences = append(checked.Differences, file.Path)
		}
	}
	return checked, nil
}

// recordedRevision is the revision a document the checkout holds records; one it cannot read differs whatever
// revision it is compared at.
func recordedRevision(checkout files.Checkout, path string) string {
	document, err := checkout.Present(path, path)
	if err != nil {
		return rules.ProvisionalRevision
	}
	return rules.RecordedRevision(document)
}

// DecideTree reads the checkout and decides every file a render writes, refusing a tree any instance's compilation
// refuses, and writes nothing: what a policy check renders through its chart is exactly what a render would write.
func DecideTree(checkout files.Checkout, revision string) (rules.Tree, error) {
	tree, _, err := decideTree(checkout, revision)
	return tree, err
}

// decideTree reads the checkout and decides every file: the platform's, and each instance's compilation.
func decideTree(checkout files.Checkout, revision string) (rules.Tree, []completed, error) {
	inputs, err := treeInputs(checkout, revision)
	if err != nil {
		return rules.Tree{}, nil, err
	}
	tree, err := rules.RenderTree(inputs)
	if err != nil {
		return rules.Tree{}, nil, err
	}
	instances := make([]completed, 0, len(tree.Instances))
	for index, one := range tree.Instances {
		compilation, written, err := Complete(one)
		if err != nil {
			folder := inputs.Folders[index]
			return rules.Tree{}, nil, fmt.Errorf("%s/%s: %w", folder.Application, folder.Environment, err)
		}
		instances = append(instances, completed{compilation: compilation, files: written})
	}
	return tree, instances, nil
}

// treeInputs reads what a whole-tree render takes from the checkout: the profile, every instance's folder, the
// allocation whose ports are kept, the team registry, the channel Secrets the tree holds and the unit root's
// kustomization.
func treeInputs(checkout files.Checkout, revision string) (rules.TreeInputs, error) {
	callsPath, err := checkout.Relative(filepath.Join(checkout.Records, connectivityrules.ResolutionRecordName))
	if err != nil {
		return rules.TreeInputs{}, err
	}
	inputs := rules.TreeInputs{Revision: revision, CallsPath: callsPath}
	if inputs.Profile, err = files.Read(checkout.Profile, "profile"); err != nil {
		return rules.TreeInputs{}, err
	}
	profile, err := kernel.DecodeProfile(inputs.Profile)
	if err != nil {
		return rules.TreeInputs{}, err
	}
	if inputs.Folders, err = treeFolders(checkout); err != nil {
		return rules.TreeInputs{}, err
	}
	if inputs.Previous, err = previousAllocation(checkout); err != nil {
		return rules.TreeInputs{}, err
	}
	if inputs.Teams, err = files.Read(filepath.Join(checkout.Records, kernel.TeamsRecordName), "teams"); err != nil {
		return rules.TreeInputs{}, err
	}
	if inputs.ChannelSecrets, err = checkout.Names(rules.ChannelsFolder); err != nil {
		return rules.TreeInputs{}, err
	}
	inputs.UnitRegistration, err = checkout.Present(kernel.UnitRegistrationPath(profile), "unit registration")
	return inputs, err
}

// treeFolders is every instance folder the checkout's records hold.
func treeFolders(checkout files.Checkout) ([]rules.Folder, error) {
	read, err := checkout.Folders()
	if err != nil {
		return nil, err
	}
	folders := make([]rules.Folder, 0, len(read))
	for _, one := range read {
		folders = append(folders, rules.Folder{Application: one.Application, Environment: one.Environment,
			Instance: one.Instance, Release: one.Release, App: one.App, EventsSecret: one.EventsSecret,
			Offers: one.Offers})
	}
	return folders, nil
}

// previousAllocation is the allocation whose ports are kept: the one the checkout names, else the one it holds, else
// none.
func previousAllocation(checkout files.Checkout) ([]byte, error) {
	if checkout.Previous != "" {
		return files.Read(checkout.Previous, "previous allocation")
	}
	return checkout.Present(rules.AllocationPath, "previous allocation")
}
