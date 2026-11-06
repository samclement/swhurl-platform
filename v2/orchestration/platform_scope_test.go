package orchestration

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/kube/kubetest"
	"github.com/samclement/swhurl-platform/v2/adapters/registry"
	"github.com/samclement/swhurl-platform/v2/adapters/registry/registrytest"
	"github.com/samclement/swhurl-platform/v2/adapters/s3"
	"github.com/samclement/swhurl-platform/v2/adapters/s3/s3test"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// Every object and collection the scope reads is one the adapter states, at the version it states.
func TestPlatformScopeReadsWhatTheAdapterStates(t *testing.T) {
	for _, ref := range []observe.Ref{observe.PlatformImagePolicies, observe.PlatformNamespaces, observe.PlatformQuotas,
		observe.PlatformNetworkPolicies, observe.PlatformNATS, observe.PlatformNATSController, observe.PlatformStreams,
		observe.PlatformConsumers, observe.PlatformEgress, observe.PlatformMongoBackup} {
		group, _, _ := strings.Cut(ref.APIVersion, "/")
		if !strings.Contains(ref.APIVersion, "/") {
			group = ""
		}
		stated, err := kube.RefOf(group, ref.Kind, ref.Namespace, ref.Name)
		if err != nil || stated != ref {
			t.Fatalf("%+v: the adapter states %+v, %v", ref, stated, err)
		}
	}
}

// scopeCluster is a cluster the fake answers for one compiled instance, hello-staging, and the checkout it is
// compared with.
func scopeCluster(t *testing.T) (*kubetest.Fake, PlatformAllInputs) {
	t.Helper()
	fake := kubetest.New(kubetest.Answers{})
	fake.Objects = map[observe.Ref][]byte{}
	in := func(ref observe.Ref, namespace string) observe.Ref { ref.Namespace = namespace; return ref }
	fake.Collections = map[observe.Ref][][]byte{
		observe.PlatformUnits: {}, observe.PlatformAlerts: {}, observe.PlatformProviders: {},
		observe.PlatformImagePolicies: {[]byte(`{"metadata":{"name":"hello-staging","namespace":"flux-system","labels":` +
			`{"platform.swhurl.com/app":"hello"}},"status":{"conditions":[{"type":"Ready","status":"True"}],"latestRef":{"tag":"42-abc1234"}}}`)},
		observe.PlatformNamespaces:                           {},
		in(observe.PlatformQuotas, "hello-staging"):          {},
		in(observe.PlatformNetworkPolicies, "hello-staging"): {},
		in(observe.PlatformStreams, "hello-staging"):         {},
		in(observe.PlatformConsumers, "hello-staging"):       {},
	}
	fake.Signing = kube.SigningKeys{Issuer: "https://kubernetes.default.svc.cluster.local", Keys: []byte(`{"keys":[]}`)}
	fake.SecretData = map[string][]kube.Secret{"notify": {{Name: "channel-platform",
		Labels: map[string]string{"platform.swhurl.com/notify-transport": "ntfy"}, Data: map[string][]byte{"address": []byte("https://ntfy.example/t")}}}}
	release, err := os.ReadFile("../testdata/r42.json")
	if err != nil {
		t.Fatal(err)
	}
	database, err := os.ReadFile("../testdata/r43.json")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := os.ReadFile("../testdata/profile.json")
	if err != nil {
		t.Fatal(err)
	}
	checkout := Checkout{Profile: profile, Releases: map[string][]byte{"hello/staging": release, "hello/prod": database},
		Compiled: []observe.CompiledInstance{{Application: "hello", Environment: "staging", Files: map[string][]byte{
			"namespace.json": []byte(`{"kind":"Namespace","metadata":{"name":"hello-staging"}}`),
			"stream.json":    []byte(`{"kind":"Stream","metadata":{"name":"staging-hello","namespace":"hello-staging"}}`)}}}}
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	return fake, PlatformAllInputs{PlatformInputs: PlatformInputs{Reader: fake, Source: "test", Now: func() time.Time { return now }},
		Data: fake, Issuer: fake, Checkout: checkout, ConsoleTag: "src-abc", MaxAge: 26 * time.Hour,
		Registries: func(string) (registry.Reader, error) {
			return registrytest.New(registrytest.Scenario{Images: map[string]registry.Image{"42-abc1234": {Digest: "sha256:01"}}}), nil
		}}
}

// One run reads the cluster checks' objects, then each scope check's: the collections of every namespace an
// instance is compiled into, NATS, the proxy and the backup job by name, the routes, Secrets and job record through
// Data, the issuer, and the registry for each Ready policy.
func TestPlatformAllReadsEveryCheck(t *testing.T) {
	fake, inputs := scopeCluster(t)
	var listed []observe.ArchiveQuery
	inputs.Objects = func(bucket string) (s3.Reader, error) {
		store := s3test.New(s3test.Scenario{})
		return recordingStore{store, &listed}, nil
	}
	report, err := PlatformAll(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, check := range report.Checks {
		names = append(names, check.Name)
	}
	if len(report.Checks) != 22 || names[6] != "image-automation" || names[14] != "console" || report.Source != "test" {
		t.Fatalf("%v", names)
	}
	reads := []string{}
	for _, ref := range fake.Reads {
		reads = append(reads, ref.Namespace+"/"+ref.Name)
	}
	want := []string{"kube-system/traefik", "notify/notify", "console/console", "console/console-dashboards",
		"console/console-notifications", "messaging/nats", "messaging/nats-controller", "egress/egress", "observability/backup-mongodb", "flux-system/swhurl-platform", "flux-system/github", "flux-system/app-images", "observability/clickstack-app", "observability/data-volume-clickstack-mongodb-0"}
	if !slices.Equal(reads, want) {
		t.Fatalf("reads %v", reads)
	}
	if !slices.Equal(fake.Calls, []string{"signing-keys", "configmaps notification-routes",
		"secrets platform.swhurl.com/notify-transport=ntfy", "configmaps console-jobs", "configmaps platform-settings", "secrets console-github", "secrets clickstack-ingestion-key", "secrets clickstack-mongodb-hyperdx-hyperdx"}) {
		t.Fatalf("calls %v", fake.Calls)
	}
	// The backup job is absent, so its location is unknown and only the database's prefix is listed.
	if len(listed) != 1 || !strings.HasSuffix(listed[0].Prefix, "/hello-prod/") {
		t.Fatalf("listed %v", listed)
	}
}

// recordingStore records each listing an object store is asked for.
type recordingStore struct {
	s3.Reader
	listed *[]observe.ArchiveQuery
}

func (r recordingStore) List(ctx context.Context, query observe.ArchiveQuery) (observe.ArchiveList, error) {
	*r.listed = append(*r.listed, query)
	return r.Reader.List(ctx, query)
}

// Without credentials nothing is listed, and each backup check says they were not supplied.
func TestPlatformAllListsNothingWithoutCredentials(t *testing.T) {
	_, inputs := scopeCluster(t)
	report, err := PlatformAll(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	entries := report.Checks[13].Entries
	if report.Checks[13].Name != "sqlite-backups" || len(entries) != 1 ||
		!strings.Contains(entries[0].Message, "hello-prod: cannot list backups in ") || !strings.Contains(entries[0].Message, "AWS credentials not supplied") {
		t.Fatalf("%+v", report.Checks[13])
	}
}

// A read the cluster could not answer is evidence; a request the adapter refused is an error, and no report is made.
func TestPlatformAllSeparatesFailedReadsFromRefusals(t *testing.T) {
	fake, inputs := scopeCluster(t)
	fake.Failing = map[string]error{"signing-keys": &kube.Error{Code: kube.Failed, Subject: "issuer", Reason: observe.ReasonForbidden}}
	report, err := PlatformAll(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed() {
		t.Fatal("a failed issuer read passed")
	}
	refusal := &kube.Error{Code: kube.RefusedRequest, Subject: "resource", Reason: "unsupported_kind"}
	fake.Failing = map[string]error{"configmaps console-jobs": refusal}
	if _, err := PlatformAll(context.Background(), inputs); !errors.Is(err, refusal) {
		t.Fatalf("a refusal: %v", err)
	}
}

// The databases are the releases that declare SQLite, in order, and each is listed under its namespace's prefix of
// the profile's location; a release that cannot be read stops the check with its path.
func TestDatabaseInstances(t *testing.T) {
	sqlite := []byte(`{"database":"sqlite"}`)
	if _, problem := databaseInstances(map[string][]byte{"hello/prod": sqlite}); !strings.HasPrefix(problem,
		"v2-records/hello/prod/release.json: cannot read it: ") {
		t.Fatalf("%s", problem)
	}
	_, inputs := scopeCluster(t)
	found, problem := databaseInstances(inputs.Checkout.Releases)
	if problem != "" || len(found) != 1 || found[0] != [2]string{"hello", "prod"} {
		t.Fatalf("only r43 keeps a database: %v %s", found, problem)
	}
}
