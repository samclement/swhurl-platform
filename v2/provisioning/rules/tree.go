package rules

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	connectivityrules "github.com/samclement/swhurl-platform/v2/connectivity/rules"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// A whole-tree render: every file v2 derives from its records, decided from one revision of the tree before anything
// is written. Connectivity decides the resolution and the allocation; this composes them with the platform's own
// files and each instance's compilation inputs, and refuses a tree any of them refuses.

// platformFolder is where the platform's own files are, in a checkout.
const platformFolder = "platform"

// ProvisionalRevision stands in for the commit a check was not given: the documents a compilation takes name one, and
// a check compares neither document's revision.
const ProvisionalRevision = "0000000000000000000000000000000000000000"

var commitRevision = regexp.MustCompile(`^[0-9a-f]{40}$`)

// CommitRevision is whether revision is a full commit, as the documents a render writes record one.
func CommitRevision(revision string) bool { return commitRevision.MatchString(revision) }

// Folder is one instance's folder as a whole-tree render reads it: the names its path gives, its three records, its
// seed when it has one, and the documents it publishes.
type Folder struct {
	Application, Environment             string
	Instance, Release, App, EventsSecret []byte
	Offers                               []kernel.File
}

// TreeInputs is everything a whole-tree render reads: the revision its documents record, the profile, every instance
// folder, the allocation whose ports are kept (nil for none), the team registry, the channel Secret files the tree
// holds, the unit root's kustomization (nil when the tree has none), and where the resolution is written.
type TreeInputs struct {
	Revision         string
	Profile          []byte
	Folders          []Folder
	Previous         []byte
	Teams            []byte
	ChannelSecrets   []string
	UnitRegistration []byte
	CallsPath        string
}

// Tree is one whole-tree render, decided: the two documents that record the revision they were read at, the
// platform's other files, and each instance's compilation inputs against them.
type Tree struct {
	CallsPath  string
	Resolution kernel.ConnectionsDocument
	Allocation kernel.EgressDocument
	Platform   []kernel.File
	Instances  []Inputs
}

// snapshot is the records tree as the platform's documents take it: every instance's records, at one revision.
type snapshot struct {
	revision  string
	instances []connectivityrules.Records
}

// RenderTree decides every platform file and every instance's compilation inputs from the tree's records. A record
// anything refuses stops the render here, so nothing is written for a tree that cannot be rendered whole.
func RenderTree(inputs TreeInputs) (Tree, error) {
	profile, err := kernel.DecodeProfile(inputs.Profile)
	if err != nil {
		return Tree{}, err
	}
	pinned, err := snapshotOf(inputs.Revision, inputs.Folders)
	if err != nil {
		return Tree{}, err
	}
	previous, err := previousAllocation(inputs.Previous)
	if err != nil {
		return Tree{}, err
	}
	tree, err := platformOf(pinned, previous, profile)
	if err != nil {
		return Tree{}, err
	}
	tree.CallsPath = inputs.CallsPath
	if tree.Instances, err = tree.instanceInputs(inputs.Profile, inputs.Folders); err != nil {
		return Tree{}, err
	}
	registration, err := unitRegistration(inputs.UnitRegistration, profile, inputs.Folders)
	if err != nil {
		return Tree{}, err
	}
	notify, err := notifyFiles(inputs.Teams, tree.Instances, inputs.ChannelSecrets)
	if err != nil {
		return Tree{}, err
	}
	tree.Platform = append(append(tree.Platform, registration...), notify...)
	return tree, nil
}

// FolderHolds is an instance folder's two records, decoded and checked to be the instance its path names: a record
// copied into another folder would otherwise render under the wrong name.
func FolderHolds(application, environment string, instance, release []byte) (kernel.InstanceSpec, kernel.ReleaseBundle,
	error) {
	spec, err := kernel.DecodeInstance(instance)
	if err != nil {
		return kernel.InstanceSpec{}, kernel.ReleaseBundle{}, err
	}
	if spec.Application != application || spec.Environment != environment {
		return kernel.InstanceSpec{}, kernel.ReleaseBundle{}, fmt.Errorf("invalid: records: %s/%s holds %s/%s",
			application, environment, spec.Application, spec.Environment)
	}
	bundle, err := kernel.DecodeRelease(release)
	if err != nil {
		return kernel.InstanceSpec{}, kernel.ReleaseBundle{}, err
	}
	if bundle.Application != application {
		return kernel.InstanceSpec{}, kernel.ReleaseBundle{}, fmt.Errorf("invalid: records: %s/%s holds a release for %s",
			application, environment, bundle.Application)
	}
	return spec, bundle, nil
}

// snapshotOf is the folders' records at one revision, each checked to hold the instance its path names.
func snapshotOf(revision string, folders []Folder) (snapshot, error) {
	pinned := snapshot{revision: revision, instances: make([]connectivityrules.Records, 0, len(folders))}
	for _, one := range folders {
		if _, _, err := FolderHolds(one.Application, one.Environment, one.Instance, one.Release); err != nil {
			return snapshot{}, err
		}
		pinned.instances = append(pinned.instances, connectivityrules.Records{Application: one.Application,
			Environment: one.Environment, Instance: one.Instance, Release: one.Release, App: one.App})
	}
	return pinned, nil
}

// previousAllocation is the allocation whose ports are kept, or none.
func previousAllocation(document []byte) (kernel.EgressDocument, error) {
	if document == nil {
		return kernel.EgressDocument{}, nil
	}
	return connectivityrules.DecodeEgressDocument(document)
}

// platformOf decides the platform's documents from the records: the resolution, the allocation, and the files
// rendered from them and from the records — the proxy, the auth folder, NATS's users and Reloader's values.
func platformOf(pinned snapshot, previous kernel.EgressDocument, profile kernel.PlatformProfile) (Tree, error) {
	resolution, err := connectivityrules.Resolve(pinned.revision, pinned.instances)
	if err != nil {
		return Tree{}, err
	}
	allocation, err := connectivityrules.Allocation(pinned.revision, pinned.instances, previous, profile.Backup.Host())
	if err != nil {
		return Tree{}, err
	}
	proxy, err := kernel.RenderProxy(allocation.Instances, profile)
	if err != nil {
		return Tree{}, err
	}
	auth, err := kernel.RenderAuth(namespacesOf(pinned), profile)
	if err != nil {
		return Tree{}, err
	}
	users, err := renderUsers(pinned, profile)
	if err != nil {
		return Tree{}, err
	}
	reloader, err := renderReloader(pinned, profile)
	if err != nil {
		return Tree{}, err
	}
	tree := Tree{Resolution: resolution, Allocation: allocation}
	for _, set := range []kernel.ResourceSet{proxy, auth, users, reloader} {
		tree.Platform = append(tree.Platform, underPlatform(set)...)
	}
	return tree, nil
}

// underPlatform is a rendered set's files as the checkout holds them, under the platform's folder.
func underPlatform(set kernel.ResourceSet) []kernel.File {
	found := make([]kernel.File, 0, len(set.Files))
	for _, file := range set.Files {
		found = append(found, kernel.File{Path: platformFolder + "/" + file.Path, Document: file.Document})
	}
	return found
}

// namespacesOf is every instance's namespace, for the auth objects each one holds.
func namespacesOf(pinned snapshot) []string {
	namespaces := make([]string, 0, len(pinned.instances))
	for _, instance := range pinned.instances {
		namespaces = append(namespaces, kernel.NamespaceOf(instance.Application, instance.Environment))
	}
	return namespaces
}

// renderUsers is the broker's user list for the records. The two service identities are the platform's own published
// public keys, which the profile states.
func renderUsers(pinned snapshot, profile kernel.PlatformProfile) (kernel.ResourceSet, error) {
	instances, err := connectivityrules.EventUsers(pinned.instances)
	if err != nil {
		return kernel.ResourceSet{}, err
	}
	return kernel.RenderUsers(kernel.NATSUsers{ControllerKey: profile.NATS.ControllerKey,
		ConsoleKey: profile.NATS.ConsoleKey, Instances: instances})
}

// renderReloader is Reloader's values for the records: the platform's own namespaces as the profile states them, then
// one per instance that declares a Secret a change must restart it for. An instance with neither a broker key nor a
// Secret is listed and the renderer leaves it out, so the decision stays in one place.
func renderReloader(pinned snapshot, profile kernel.PlatformProfile) (kernel.ResourceSet, error) {
	reloader := kernel.Reloader{Platform: profile.Reloader.Namespaces}
	for _, instance := range pinned.instances {
		reloader.Instances = append(reloader.Instances, kernel.ReloaderInstance{
			Application: instance.Application, Environment: instance.Environment,
			BrokerKey: connectivityrules.EventsKey(instance.Instance), Secrets: declaredSecrets(instance.Release)})
	}
	namespaces, err := reloader.Namespaces()
	if err != nil {
		return kernel.ResourceSet{}, err
	}
	return kernel.RenderReloader(namespaces)
}

// declaredSecrets is the Secret names an instance's release declares its image reads.
func declaredSecrets(release []byte) []string {
	var declared struct{ Secrets []string }
	if err := json.Unmarshal(release, &declared); err != nil {
		return nil
	}
	return declared.Secrets
}

// instanceInputs is every folder's compilation inputs against the tree's resolution and allocation.
func (t Tree) instanceInputs(profile []byte, folders []Folder) ([]Inputs, error) {
	calls, err := connectivityrules.EncodeConnectionsDocument(t.Resolution)
	if err != nil {
		return nil, err
	}
	egress, err := connectivityrules.EncodeEgressDocument(t.Allocation)
	if err != nil {
		return nil, err
	}
	found := make([]Inputs, 0, len(folders))
	for _, one := range folders {
		found = append(found, Inputs{Instance: one.Instance, Release: one.Release, App: one.App, Profile: profile,
			Calls: calls, Egress: egress, EventsSecret: one.EventsSecret, Offers: one.Offers})
	}
	return found, nil
}

// unitRegistration is the unit root's kustomization listing every instance's unit, each one the records hold added
// after the entries already there; none when the tree has no such file. Without its line a merged unit is never
// applied, and its namespace never made, so the render writes it rather than leave it to a hand edit.
func unitRegistration(document []byte, profile kernel.PlatformProfile, folders []Folder) ([]kernel.File, error) {
	if document == nil {
		return nil, nil
	}
	entries := make([]string, 0, len(folders))
	for _, one := range folders {
		unit := kernel.UnitPath(one.Application, one.Environment, profile)
		entries = append(entries, unit[strings.LastIndex(unit, "/")+1:])
	}
	slices.Sort(entries)
	registered, err := kernel.WithEntries(document, entries)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", kernel.UnitRegistrationPath(profile), err)
	}
	return []kernel.File{{Path: kernel.UnitRegistrationPath(profile), Document: registered}}, nil
}

// notifyFiles is what the team registry renders, once every app record and every grant names a registered team and
// each registered channel, and no other, has its Secret: the render writes the list of channel Secrets and never a
// Secret, so a list naming a missing file, or a file nothing lists, is refused here.
func notifyFiles(teams []byte, instances []Inputs, channelSecrets []string) ([]kernel.File, error) {
	registry, err := kernel.DecodeTeams(teams)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", kernel.TeamsRecordName, err)
	}
	for _, inputs := range instances {
		if err := teamsOf(registry, inputs); err != nil {
			return nil, err
		}
	}
	if err := channelSecretsAgree(registry, channelSecrets); err != nil {
		return nil, err
	}
	notify, err := kernel.RenderNotify(registry)
	if err != nil {
		return nil, err
	}
	return underPlatform(notify), nil
}

// teamsOf refuses an instance whose app names, or whose grants admit, a team the registry does not hold, naming it.
func teamsOf(registry kernel.TeamRegistry, inputs Inputs) error {
	app, err := kernel.DecodeApp(inputs.App)
	if err != nil {
		return err
	}
	spec, err := kernel.DecodeInstance(inputs.Instance)
	if err != nil {
		return err
	}
	if err := kernel.CheckTeams(registry, []kernel.AppSpec{app}, []kernel.InstanceSpec{spec}); err != nil {
		return fmt.Errorf("%s/%s: %w (register it with swhurl-v2 team --action add)", spec.Application,
			spec.Environment, err)
	}
	return nil
}

// ChannelsFolder is where the channel Secret files are, in a checkout.
const ChannelsFolder = platformFolder + "/" + kernel.ChannelsFolder

// channelSecretsAgree is whether the channel Secret files the tree holds are exactly one per registered channel.
func channelSecretsAgree(registry kernel.TeamRegistry, channelSecrets []string) error {
	present := map[string]bool{}
	for _, file := range channelSecrets {
		if strings.HasSuffix(file, ".sops.yaml") {
			present[file] = true
		}
	}
	for _, channel := range registry.Channels() {
		file := platformFolder + "/" + kernel.ChannelSecretPath(channel.Name)
		if !present[file] {
			return fmt.Errorf("invalid: channels: %s is missing: give the team its channel with swhurl-v2 team "+
				"--action channel", file)
		}
		delete(present, file)
	}
	if len(present) > 0 {
		return fmt.Errorf("invalid: channels: %s is no team's channel: remove it", slices.Sorted(maps.Keys(present))[0])
	}
	return nil
}

// Documents is the two documents that record a revision, at the revisions given, and the platform's other files. The
// two end in the newline the commands that print them end in.
func (t Tree) Documents(callsRevision, allocationRevision string) ([]kernel.File, error) {
	resolution, allocation := t.Resolution, t.Allocation
	resolution.Revision, allocation.Revision = callsRevision, allocationRevision
	calls, err := connectivityrules.EncodeConnectionsDocument(resolution)
	if err != nil {
		return nil, err
	}
	egress, err := connectivityrules.EncodeEgressDocument(allocation)
	if err != nil {
		return nil, err
	}
	return append([]kernel.File{{Path: t.CallsPath, Document: append(calls, '\n')},
		{Path: AllocationPath, Document: append(egress, '\n')}}, t.Platform...), nil
}

// AllocationPath is where the egress allocation is, in a checkout.
const AllocationPath = platformFolder + "/egress/allocation.json"

// RecordedRevision is the revision a document the tree holds records, or the provisional one when there is none to
// read: a missing or unreadable file differs whatever revision it is compared at.
func RecordedRevision(document []byte) string {
	var recorded struct{ Revision string }
	if json.Unmarshal(document, &recorded) != nil || !CommitRevision(recorded.Revision) {
		return ProvisionalRevision
	}
	return recorded.Revision
}
