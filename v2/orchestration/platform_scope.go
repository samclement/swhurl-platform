package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/github"
	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/registry"
	"github.com/samclement/swhurl-platform/v2/adapters/s3"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/notify"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// Checkout is the files of the checkout the platform scope compares the cluster with, as the command read them. A
// nil document is a file the checkout does not have.
type Checkout struct {
	Profile, Teams, Allocation []byte
	// FluxSettings is clusters/home/flux-system/install/flux.json: the version and arguments Flux is installed with.
	FluxSettings []byte
	// Apps are the app records, by app; Releases the release records, by "<app>/<env>".
	Apps, Releases map[string][]byte
	Compiled       []observe.CompiledInstance
}

// PlatformAllInputs is one run of every platform check: the cluster checks' inputs, the cluster's other ports, the
// registry, the object store and GitHub, the pod executor, the checkout, and what the caller supplied.
type PlatformAllInputs struct {
	PlatformInputs
	// GitHubToken is the operator credential for hook listings only. Console credentials come from Data.
	GitHub      github.Reader
	GitHubToken string
	Data        kube.Data
	Issuer      kube.Issuer
	Executor    kube.Executor
	Registries  func(image string) (registry.Reader, error)
	// Objects opens the object store for one bucket; nil when no credentials were supplied, and nothing is listed.
	Objects  func(bucket string) (s3.Reader, error)
	Checkout Checkout
	// ConsoleTag is the checkout's console content tag; empty when it was not supplied.
	ConsoleTag string
	MaxAge     time.Duration
	// Heartbeat is the host heartbeat facts the caller supplied, or that it supplied none.
	Heartbeat observe.HeartbeatEvidence
}

// scopeStep makes one check's reads into the scope's evidence.
type scopeStep func(context.Context, PlatformAllInputs, *observe.ScopeEvidence) error

// PlatformAll reads what every platform check needs — the cluster checks' reads, then each scope check's from the
// cluster, the checkout, the registry and the object store — and reports their verdicts. As for Platform, a failed
// read is evidence and a request an adapter refuses to make is an error, with no report.
func PlatformAll(ctx context.Context, inputs PlatformAllInputs) (observe.PlatformReport, error) {
	evidence, err := platformEvidence(ctx, inputs.Reader)
	if err != nil {
		return observe.PlatformReport{}, err
	}
	scope := observe.ScopeEvidence{Compiled: inputs.Checkout.Compiled, Credentials: inputs.Objects != nil,
		MaxAge: inputs.MaxAge, Heartbeat: inputs.Heartbeat}
	for _, step := range []scopeStep{readImages, readIsolation, readSigning, readEvents, readEgress, readChannels,
		readMongoBackups, readDatabases, readConsole, readGitHub, readPodChecks, readFluxControllers, readJetStream} {
		if err := step(ctx, inputs, &scope); err != nil {
			return observe.PlatformReport{}, err
		}
	}
	now := inputs.Now().UTC()
	evidence.Now = now
	return observe.PlatformReport{Source: inputs.Source, ObservedAt: now.Format(time.RFC3339),
		Checks: observe.PlatformScopeChecks(evidence, scope)}, nil
}

// readImages lists the apps' ImagePolicies and, for each Ready one, reads its app's staging record and asks the
// registry, anonymously, which digest the policy's newest tag names.
func readImages(ctx context.Context, inputs PlatformAllInputs, scope *observe.ScopeEvidence) error {
	policies, err := inputs.Reader.List(ctx, observe.PlatformImagePolicies, observe.AppLabel)
	if err != nil {
		return err
	}
	scope.Images = observe.ImageEvidence{Policies: policies, Records: map[string]observe.StagingRecord{},
		Newest: map[string]observe.NewestImage{}}
	for _, policy := range observe.ImagePolicies(policies) {
		data, found := inputs.Checkout.Releases[policy.App+"/"+observe.StagingEnvironment]
		if !policy.Ready() || !found {
			continue
		}
		record := stagingRecord(data)
		scope.Images.Records[policy.App] = record
		if record.Problem == "" && record.Image != "" {
			scope.Images.Newest[policy.Name] = newestImage(ctx, inputs.Registries, record.Image, policy.Tag)
		}
	}
	return nil
}

func stagingRecord(data []byte) observe.StagingRecord {
	release, err := kernel.DecodeRelease(data)
	if err != nil {
		return observe.StagingRecord{Problem: err.Error()}
	}
	return observe.StagingRecord{Image: release.Artifact.Image, Digest: release.Artifact.Digest}
}

func newestImage(ctx context.Context, registries func(string) (registry.Reader, error), image, tag string) observe.NewestImage {
	reader, err := registries(image)
	if err != nil {
		return observe.NewestImage{Problem: err.Error()}
	}
	found, err := reader.Describe(ctx, tag)
	if err != nil {
		return observe.NewestImage{Problem: err.Error()}
	}
	return observe.NewestImage{Digest: found.Digest}
}

// readIsolation lists the app namespaces and, in each compiled instance's namespace, its ResourceQuotas and
// NetworkPolicies, and reads the team registry.
func readIsolation(ctx context.Context, inputs PlatformAllInputs, scope *observe.ScopeEvidence) error {
	namespaces, err := inputs.Reader.List(ctx, observe.PlatformNamespaces, observe.AppLabel)
	if err != nil {
		return err
	}
	scope.Isolation = observe.IsolationEvidence{Namespaces: namespaces, Quotas: map[string]observe.List{},
		Policies: map[string]observe.List{}, Teams: map[string]bool{}}
	for _, instance := range inputs.Checkout.Compiled {
		namespace := instance.Namespace()
		if namespace == "" {
			continue
		}
		if scope.Isolation.Quotas[namespace], err = listIn(ctx, inputs.Reader, observe.PlatformQuotas, namespace); err != nil {
			return err
		}
		if scope.Isolation.Policies[namespace], err = listIn(ctx, inputs.Reader, observe.PlatformNetworkPolicies, namespace); err != nil {
			return err
		}
	}
	registry, problem := teamsOf(inputs.Checkout.Teams)
	scope.Isolation.TeamsProblem = problem
	for name := range registry.Teams {
		scope.Isolation.Teams[name] = true
	}
	return nil
}

// listIn is one namespace's whole collection of a kind.
func listIn(ctx context.Context, reader kube.Reader, kind observe.Ref, namespace string) (observe.List, error) {
	kind.Namespace = namespace
	return reader.ListNamespace(ctx, kind)
}

// teamsOf is the checkout's team registry, or why it has none.
func teamsOf(data []byte) (kernel.TeamRegistry, string) {
	if data == nil {
		return kernel.TeamRegistry{}, "the checkout has no " + observe.CheckoutTeams
	}
	registry, err := kernel.DecodeTeams(data)
	if err != nil {
		return kernel.TeamRegistry{}, err.Error()
	}
	return registry, ""
}

// readSigning reads the cluster's token issuer and signing keys, and what the profile's auth block states.
func readSigning(ctx context.Context, inputs PlatformAllInputs, scope *observe.ScopeEvidence) error {
	keys, err := inputs.Issuer.SigningKeys(ctx)
	unread, err := failedRead(err)
	if err != nil {
		return err
	}
	scope.Signing = observe.SigningEvidence{Issuer: keys.Issuer, Keys: keys.Keys, Unread: unread}
	profile, problem := profileOf(inputs.Checkout.Profile)
	scope.Signing.StatedIssuer, scope.Signing.StatedKeys, scope.Signing.Unstated = profile.Auth.Issuer, profile.Auth.Keys, problem
	return nil
}

// failedRead separates a read the cluster could not answer, which is evidence, from a request the adapter refused.
func failedRead(err error) (string, error) {
	var failure *kube.Error
	switch {
	case err == nil:
		return "", nil
	case errors.As(err, &failure) && failure.Code == kube.Failed:
		return failure.Reason, nil
	}
	return "", err
}

// profileOf is the checkout's platform profile, or why it has none.
func profileOf(data []byte) (kernel.PlatformProfile, string) {
	if data == nil {
		return kernel.PlatformProfile{}, "the checkout has no " + observe.CheckoutProfile
	}
	profile, err := kernel.DecodeProfile(data)
	if err != nil {
		return kernel.PlatformProfile{}, err.Error()
	}
	return profile, ""
}

// readEvents reads NATS and its controller, and the streams and consumers in every namespace an instance declares one.
func readEvents(ctx context.Context, inputs PlatformAllInputs, scope *observe.ScopeEvidence) error {
	events := observe.EventEvidence{Streams: map[string]observe.List{}, Consumers: map[string]observe.List{}}
	var err error
	if events.Server, err = inputs.Reader.Read(ctx, observe.PlatformNATS); err != nil {
		return err
	}
	if events.Controller, err = inputs.Reader.Read(ctx, observe.PlatformNATSController); err != nil {
		return err
	}
	for _, namespace := range observe.EventNamespaces(inputs.Checkout.Compiled) {
		if events.Streams[namespace], err = listIn(ctx, inputs.Reader, observe.PlatformStreams, namespace); err != nil {
			return err
		}
		if events.Consumers[namespace], err = listIn(ctx, inputs.Reader, observe.PlatformConsumers, namespace); err != nil {
			return err
		}
	}
	scope.Events = events
	return nil
}

// readJetStream asks the NATS server's local monitoring port how much of JetStream's file store is reserved, and by
// which streams. It runs after the other pod reads, so their order is unchanged.
func readJetStream(ctx context.Context, inputs PlatformAllInputs, scope *observe.ScopeEvidence) error {
	output, failed := podOutput(ctx, inputs.Executor, kube.ExecRequest{Namespace: natsNamespace, Pod: natsPod, Container: "nats",
		Command: []string{"wget", "-qO-", "http://127.0.0.1:8222/jsz?accounts=true&streams=true&config=true"}})
	scope.Events.Storage = observe.JetStreamStorage{Output: output, Failed: failed}
	return nil
}

// readEgress reads the egress proxy and the checkout's allocation of its ports.
func readEgress(ctx context.Context, inputs PlatformAllInputs, scope *observe.ScopeEvidence) error {
	proxy, err := inputs.Reader.Read(ctx, observe.PlatformEgress)
	if err != nil {
		return err
	}
	scope.Egress = observe.EgressEvidence{Proxy: proxy}
	if inputs.Checkout.Allocation == nil {
		scope.Egress.Problem = "the checkout has no " + observe.CheckoutAllocation
		return nil
	}
	allocation, err := kernel.DecodeEgressDocument(inputs.Checkout.Allocation)
	if err != nil {
		scope.Egress.Problem = err.Error()
		return nil
	}
	for _, allocated := range allocation.Instances {
		scope.Egress.Ports = append(scope.Egress.Ports, observe.InstancePort{Application: allocated.Application,
			Environment: allocated.Environment, Port: allocated.Port})
	}
	return nil
}

// readChannels renders the routes the team registry states, reads the routes and channel Secrets the notify service
// reads, and which team owns each app. The Secrets' addresses are kept from the evidence: only their channels go on.
func readChannels(ctx context.Context, inputs PlatformAllInputs, scope *observe.ScopeEvidence) error {
	channels := observe.ChannelEvidence{Owners: appOwners(inputs.Checkout.Apps)}
	channels.Wanted, channels.Problem = wantedRoutes(inputs.Checkout.Teams)
	live, unread, err := liveRoutes(ctx, inputs.Data)
	if err != nil {
		return err
	}
	channels.Live, channels.Unread = live, unread
	addresses, err := channelAddresses(ctx, inputs.Data)
	if unread, err := failedRead(err); err != nil || unread != "" {
		channels.Unread = unread
		scope.Channels = channels
		return err
	}
	for channel := range addresses {
		channels.Secrets = append(channels.Secrets, channel)
	}
	sort.Strings(channels.Secrets)
	scope.Channels = channels
	return nil
}

// wantedRoutes is the routes document the registry renders, or why it renders none.
func wantedRoutes(teams []byte) ([]byte, string) {
	registry, problem := teamsOf(teams)
	if problem != "" {
		return nil, problem
	}
	rendered, err := kernel.RenderNotify(registry)
	if err != nil {
		return nil, err.Error()
	}
	for _, file := range rendered.Files {
		if file.Path == kernel.NotifyFolder+"/"+notify.RoutesKey {
			return routesDocument(file.Document)
		}
	}
	return nil, "it renders no routes"
}

// routesDocument is the routes a rendered routes ConfigMap holds.
func routesDocument(configMap []byte) ([]byte, string) {
	var document struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(configMap, &document); err != nil {
		return nil, err.Error()
	}
	return []byte(document.Data[notify.RoutesKey]), ""
}

// liveRoutes is the routes document the cluster's routes ConfigMap holds: nil when there is no ConfigMap, and a
// failed read when the ConfigMap has no document.
func liveRoutes(ctx context.Context, data kube.Data) ([]byte, string, error) {
	found, err := data.ConfigMaps(ctx, notify.Namespace, kube.Selector{Name: notify.RoutesName})
	if unread, err := failedRead(err); err != nil || unread != "" {
		return nil, unread, err
	}
	if len(found) == 0 {
		return nil, "", nil
	}
	document, ok := found[0].Data[notify.RoutesKey]
	if !ok {
		return nil, errNoRoutesKey.Error(), nil
	}
	return []byte(document), "", nil
}

// appOwners is each team's apps, from the app records; a record that cannot be read names no owner here (the
// records' own checks refuse it).
func appOwners(apps map[string][]byte) map[string][]string {
	owners := map[string][]string{}
	for _, name := range sortedKeys(apps) {
		if app, err := kernel.DecodeApp(apps[name]); err == nil {
			owners[app.Team] = append(owners[app.Team], name)
		}
	}
	return owners
}

func sortedKeys(documents map[string][]byte) []string {
	keys := make([]string, 0, len(documents))
	for key := range documents {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// readMongoBackups reads the MongoDB backup CronJob and lists where its BACKUP_S3_URI says the backups are.
func readMongoBackups(ctx context.Context, inputs PlatformAllInputs, scope *observe.ScopeEvidence) error {
	job, err := inputs.Reader.Read(ctx, observe.PlatformMongoBackup)
	if err != nil {
		return err
	}
	scope.Backups = observe.MongoBackupEvidence{Job: job}
	if location, found := observe.BackupJobLocation(job); found && inputs.Objects != nil {
		scope.Backups.Listing = listArchives(ctx, inputs.Objects, location)
	}
	return nil
}

// listArchives lists one prefix. The object store's refusal is a failed read here: the location came from the
// cluster or the checkout, not from the caller.
func listArchives(ctx context.Context, objects func(string) (s3.Reader, error), location observe.ArchiveQuery) observe.ArchiveList {
	reader, err := objects(location.Bucket)
	if err != nil {
		return observe.ArchiveList{Query: location, Status: observe.Unavailable, Reason: err.Error()}
	}
	listed, err := reader.List(ctx, location)
	if err != nil {
		return observe.ArchiveList{Query: location, Status: observe.Unavailable, Reason: err.Error()}
	}
	return listed
}

// readDatabases finds the instances whose release declares a SQLite database and lists each one's prefix of the
// profile's backup location.
func readDatabases(ctx context.Context, inputs PlatformAllInputs, scope *observe.ScopeEvidence) error {
	instances, problem := databaseInstances(inputs.Checkout.Releases)
	location, found := observe.ArchiveQuery{}, false
	if problem == "" {
		location, found, problem = databaseLocation(inputs.Checkout.Profile)
	}
	scope.Databases = observe.DatabaseEvidence{Problem: problem, Location: location}
	if !found {
		return nil
	}
	for _, instance := range instances {
		database := observe.DatabaseBackup{Namespace: kernel.NamespaceOf(instance[0], instance[1])}
		if inputs.Objects != nil {
			prefix := location
			prefix.Prefix += database.Namespace + "/"
			database.Listing = listArchives(ctx, inputs.Objects, prefix)
		}
		scope.Databases.Databases = append(scope.Databases.Databases, database)
	}
	return nil
}

// databaseInstances are the application and environment, by the record's folder, of each release that declares a
// SQLite database, in order.
func databaseInstances(releases map[string][]byte) ([][2]string, string) {
	found := [][2]string{}
	for _, key := range sortedKeys(releases) {
		release, err := kernel.DecodeRelease(releases[key])
		if err != nil {
			return nil, fmt.Sprintf("%s/%s/release.json: cannot read it: %v", observe.CheckoutRecords, key, err)
		}
		if application, environment, _ := strings.Cut(key, "/"); release.Database == "sqlite" {
			found = append(found, [2]string{application, environment})
		}
	}
	return found, ""
}

// databaseLocation is the profile's backup location, or why it states none.
func databaseLocation(data []byte) (observe.ArchiveQuery, bool, string) {
	profile, problem := profileOf(data)
	if problem != "" {
		return observe.ArchiveQuery{}, false, observe.CheckoutProfile + ": cannot read backup.s3Uri (" + problem + ")"
	}
	location, found := observe.BackupLocation(profile.Backup.S3URI)
	if !found {
		return observe.ArchiveQuery{}, false, observe.CheckoutProfile + ": the backup location must be s3://<bucket>/<prefix>/"
	}
	return location, true, ""
}

// readFluxControllers reads the settings the checkout installs Flux with and each controller's live Deployment.
// Settings it cannot read leave the version unchecked rather than guessed.
func readFluxControllers(ctx context.Context, inputs PlatformAllInputs, scope *observe.ScopeEvidence) error {
	if inputs.Checkout.FluxSettings == nil {
		scope.Flux = observe.FluxEvidence{Problem: "the checkout has no such file"}
		return nil
	}
	settings, err := observe.DecodeFluxSettings(inputs.Checkout.FluxSettings)
	if err != nil {
		scope.Flux = observe.FluxEvidence{Problem: err.Error()}
		return nil
	}
	scope.Flux = observe.FluxEvidence{Settings: settings, Deployments: map[string]observe.Read{}}
	for _, name := range settings.Controllers() {
		controller := observe.PlatformFluxController
		controller.Name = name
		read, err := inputs.Reader.Read(ctx, controller)
		if err != nil {
			return err
		}
		scope.Flux.Deployments[name] = read
	}
	return nil
}

// readConsole reads whether the console's job record exists; its HelmRelease is the cluster checks' read.
func readConsole(ctx context.Context, inputs PlatformAllInputs, scope *observe.ScopeEvidence) error {
	found, err := inputs.Data.ConfigMaps(ctx, consoleNamespace, kube.Selector{Name: consoleJobs})
	unread, err := failedRead(err)
	if err != nil {
		return err
	}
	scope.Console = observe.ConsoleEvidence{Jobs: len(found) > 0, JobsUnread: unread, Tag: inputs.ConsoleTag}
	return nil
}

// Where the console keeps its jobs across a restart.
const consoleNamespace, consoleJobs = "console", "console-jobs"
