package observe

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// The platform scope's checks, case by case: each table names the evidence that changes from a healthy platform's and
// the entry that must follow. These are the cases verify-platform's Python checks were tested with, kept here when the
// checks moved.

const (
	stagingDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	otherDigest   = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	keySet        = `{"keys":[{"kid":"a","kty":"RSA"}]}`
	issuer        = "https://kubernetes.default.svc.cluster.local"
	routes        = `{"channels":{"platform":"ntfy"},"teams":{"demo":["platform"]}}`
)

func policyItem(name, app, ready, message, tag string) string {
	return `{"metadata":{"name":"` + name + `","namespace":"flux-system","labels":{"platform.swhurl.com/app":"` + app + `"}},` +
		`"status":{"conditions":[{"type":"Ready","status":"` + ready + `","message":"` + message + `"}],"latestRef":{"tag":"` + tag + `"}}}`
}

// compiledHello is one instance as v2 compiles it: its namespace, quota, policy, stream and a consumer.
func compiledHello() CompiledInstance {
	return CompiledInstance{Application: "hello", Environment: "staging", Files: map[string][]byte{
		"namespace.json": []byte(`{"kind":"Namespace","metadata":{"name":"hello-staging","labels":{"platform.swhurl.com/app":"hello",` +
			`"platform.swhurl.com/team":"demo"}}}`),
		"resourcequota.json": []byte(`{"kind":"ResourceQuota","metadata":{"name":"hello","namespace":"hello-staging"},"spec":{"hard":{"pods":"10"}}}`),
		"networkpolicy.json": []byte(`{"kind":"NetworkPolicy","metadata":{"name":"hello","namespace":"hello-staging"},` +
			`"spec":{"podSelector":{"matchLabels":{"app":"hello"}}}}`),
		"stream.json":            []byte(`{"kind":"Stream","metadata":{"name":"staging-hello","namespace":"hello-staging"}}`),
		"consumer-world-ev.json": []byte(`{"kind":"Consumer","metadata":{"name":"world-ev","namespace":"hello-staging"}}`),
	}}
}

const readyStatus = `"status":{"conditions":[{"type":"Ready","status":"True"}]}`

func healthyScope() ScopeEvidence {
	backupStamp := platformNow.Add(-time.Hour).Format("20060102T150405Z")
	listing := func(prefix, name, suffix string) ArchiveList {
		return ArchiveList{Status: Present, Objects: []Archive{{Key: prefix + name + "-" + backupStamp + suffix},
			{Key: prefix + name + "-" + backupStamp + ".json"}}}
	}
	push, images, tokens := healthyGitHub()
	return ScopeEvidence{
		Ingestion:    IngestionEvidence{CollectorPresent: true, TeamAvailable: true, Equal: true},
		Registration: RegistrationEvidence{Readable: true, TeamExists: true},
		Retention:    RetentionEvidence{Telemetry: TelemetryTTL([]byte("9\t9"), false), SystemLogs: SystemLogTTL([]byte("0"), false), Merges: MergeFailures([]byte("0\t"), false), Volume: Read{Status: Present, Object: []byte(`{"spec":{"persistentVolumeReclaimPolicy":"Retain"}}`)}},
		PushWebhook:  push, ImageWebhooks: images, ConsoleTokens: tokens,
		Compiled: []CompiledInstance{compiledHello()},
		Images: ImageEvidence{
			Policies: collectionOf(PlatformImagePolicies, policyItem("hello-staging", "hello", "True", "", "42-abc1234")),
			Records:  map[string]StagingRecord{"hello": {Image: "ghcr.io/samclement/hello", Digest: stagingDigest}},
			Newest:   map[string]NewestImage{"hello-staging": {Digest: stagingDigest}},
		},
		Isolation: IsolationEvidence{
			Namespaces: collectionOf(PlatformNamespaces, `{"metadata":{"name":"hello-staging","labels":{"platform.swhurl.com/app":"hello",`+
				`"platform.swhurl.com/team":"demo","kubernetes.io/metadata.name":"hello-staging"}}}`),
			Quotas:   map[string]List{"hello-staging": collectionOf(PlatformQuotas, `{"metadata":{"name":"hello"},"spec":{"hard":{"pods":"10"}}}`)},
			Policies: map[string]List{"hello-staging": collectionOf(PlatformNetworkPolicies, `{"metadata":{"name":"hello"},"spec":{"podSelector":{"matchLabels":{"app":"hello"}}}}`)},
			Teams:    map[string]bool{"demo": true},
		},
		Signing: SigningEvidence{Issuer: issuer, Keys: []byte(keySet), StatedIssuer: issuer, StatedKeys: `{"keys": [{"kty":"RSA","kid":"a"}]}`},
		Events: EventEvidence{
			Server:     presentRead(PlatformNATS, `{"status":{"readyReplicas":1}}`),
			Controller: presentRead(PlatformNATSController, `{"status":{"readyReplicas":1}}`),
			Streams:    map[string]List{"hello-staging": collectionOf(PlatformStreams, `{"metadata":{"name":"staging-hello"},`+readyStatus+`}`)},
			Consumers:  map[string]List{"hello-staging": collectionOf(PlatformConsumers, `{"metadata":{"name":"world-ev"},`+readyStatus+`}`)},
		},
		Egress: EgressEvidence{Proxy: presentRead(PlatformEgress, `{"spec":{"replicas":2},"status":{"readyReplicas":2}}`),
			Ports: []InstancePort{{"hello", "staging", 30100}}},
		Channels: ChannelEvidence{Wanted: []byte(routes), Live: []byte(`{"teams":{"demo":["platform"]},"channels":{"platform":"ntfy"}}`),
			Secrets: []string{"platform"}, Owners: map[string][]string{"demo": {"hello"}}},
		Backups: MongoBackupEvidence{Job: presentRead(PlatformMongoBackup, mongoJob("s3://bucket-a/clickstack-mongodb/")),
			Listing: listing("clickstack-mongodb/", "clickstack-mongodb", ".archive.gz.age")},
		Databases: DatabaseEvidence{Location: ArchiveQuery{Bucket: "bucket-a", Prefix: "app-sqlite/"},
			Databases: []DatabaseBackup{{"hello-prod", listing("app-sqlite/hello-prod/", "sqlite", ".db.age")}}},
		Console:     ConsoleEvidence{Jobs: true, Tag: "src-abc"},
		Flux:        healthyFlux(),
		Heartbeat:   healthyHeartbeat(),
		Credentials: true,
		MaxAge:      26 * time.Hour,
	}
}

func mongoJob(uri string) string {
	return `{"spec":{"jobTemplate":{"spec":{"template":{"spec":{"containers":[{"name":"backup","env":[` +
		`{"name":"MONGO_DATABASE","value":"hyperdx"},{"name":"BACKUP_S3_URI","value":"` + uri + `"}]}]}}}}}}`
}

// scopeEntries is one scope check's entries, by name.
func scopeEntries(t *testing.T, s ScopeEvidence, name string) []PlatformEntry {
	t.Helper()
	for _, check := range PlatformScopeChecks(healthy(), s) {
		if check.Name == name {
			return check.Entries
		}
	}
	t.Fatalf("no check %s", name)
	return nil
}

// scopeCase is one changed fact and an entry it must produce: its level and a part of its message.
type scopeCase struct {
	name   string
	change func(*ScopeEvidence)
	level  CheckLevel
	want   string
}

func runScopeCases(t *testing.T, check string, cases []scopeCase) {
	t.Helper()
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			evidence := healthyScope()
			test.change(&evidence)
			entries := scopeEntries(t, evidence, check)
			for _, entry := range entries {
				if entry.Level == test.level && strings.Contains(entry.Message, test.want) {
					return
				}
			}
			t.Fatalf("no %s entry containing %q in %+v", test.level, test.want, entries)
		})
	}
}

func unchanged(*ScopeEvidence) {}

// The scope's checks follow the six, in Python's order, and a healthy platform passes every one.
func TestScopeChecksFollowTheClusterChecksAndPass(t *testing.T) {
	checks := PlatformScopeChecks(healthy(), healthyScope())
	names := []string{}
	for _, check := range checks {
		names = append(names, check.Name)
		for _, entry := range check.Entries {
			if entry.Level != CheckOK {
				t.Errorf("%s: %+v", check.Name, entry)
			}
		}
		if len(check.Entries) == 0 {
			t.Errorf("%s has no entries", check.Name)
		}
	}
	want := []string{}
	for _, entry := range PlatformCatalogue {
		want = append(want, entry.Name)
	}
	if strings.Join(names, " ") != strings.Join(want, " ") {
		t.Fatalf("%v", names)
	}
}

func TestImageAutomation(t *testing.T) {
	runScopeCases(t, "image-automation", []scopeCase{
		{"the record says staging runs the picked image", unchanged, CheckOK, "hello-staging: newest image 42-abc1234, deployed"},
		{"a stale digest warns", func(s *ScopeEvidence) { s.Images.Newest["hello-staging"] = NewestImage{Digest: otherDigest} },
			CheckWarn, "newest image 42-abc1234 is not what v2 records state: v2-records/hello/staging/release.json names " +
				stagingDigest + ", the registry says " + otherDigest + ". The Deploy v2 workflow deploys it within the hour (git pull: " +
				"it may already have); to deploy it now: make v2-deploy"},
		{"a registry answer without the platform is unchecked", func(s *ScopeEvidence) {
			s.Images.Newest["hello-staging"] = NewestImage{Problem: "no_platform_manifest: manifest: linux/amd64"}
		}, CheckWarn, "hello-staging: cannot read the digest of samclement/hello:42-abc1234 from the registry (no_platform_manifest: " +
			"manifest: linux/amd64), so whether v2-records/hello/staging/release.json is deployed is unchecked"},
		{"an instance with no record warns", func(s *ScopeEvidence) { delete(s.Images.Records, "hello") },
			CheckWarn, "hello-staging: newest image 42-abc1234, but this checkout has no v2-records/hello/staging/release.json"},
		{"an unreadable record fails", func(s *ScopeEvidence) { s.Images.Records["hello"] = StagingRecord{Problem: "invalid: release"} },
			CheckBad, "v2-records/hello/staging/release.json cannot be read (invalid: release), so whether staging runs 42-abc1234 is unchecked"},
		{"a record with no image fails", func(s *ScopeEvidence) { s.Images.Records["hello"] = StagingRecord{Digest: stagingDigest} },
			CheckBad, "v2-records/hello/staging/release.json states no image"},
		{"a policy that is not Ready fails", func(s *ScopeEvidence) {
			s.Images.Policies = collectionOf(PlatformImagePolicies, policyItem("hello-staging", "hello", "False", "no tags match", "1.27-alpine"))
		}, CheckBad, "ImagePolicy hello-staging is not Ready, so staging gets no new images: no tags match (kubectl -n flux-system " +
			"describe imagerepository hello; make reconcile UNIT=app-hello-staging)"},
		{"a failed list fails", func(s *ScopeEvidence) { s.Images.Policies = List{Status: Unavailable, Reason: ReasonForbidden} },
			CheckBad, "cannot read the apps' ImagePolicies, so no staging deploy can be checked"},
		{"no policy says so", func(s *ScopeEvidence) { s.Images.Policies = collectionOf(PlatformImagePolicies) },
			CheckOK, "no app has an ImagePolicy"},
	})
}

func TestIsolation(t *testing.T) {
	runScopeCases(t, "isolation", []scopeCase{
		{"an isolated instance passes", unchanged, CheckOK, "1 app namespace(s) have a registered team, the platform quota and a NetworkPolicy"},
		{"a namespace kept for its data is not an instance", func(s *ScopeEvidence) {
			s.Isolation.Namespaces.Items = append(s.Isolation.Namespaces.Items, []byte(`{"metadata":{"name":"gone-prod","labels":{"platform.swhurl.com/app":"gone"}}}`))
		}, CheckOK, "1 app namespace(s) have"},
		{"a missing namespace fails", func(s *ScopeEvidence) { s.Isolation.Namespaces = collectionOf(PlatformNamespaces) },
			CheckBad, "hello-staging: the namespace does not exist, so the instance is not running (make app-reconcile APP=hello ENV=staging"},
		{"an unregistered team, no quota and no policy fail together", func(s *ScopeEvidence) {
			s.Isolation.Teams = map[string]bool{}
			s.Isolation.Quotas["hello-staging"] = collectionOf(PlatformQuotas)
			s.Isolation.Policies["hello-staging"] = collectionOf(PlatformNetworkPolicies)
		}, CheckBad, "hello-staging: team 'demo' is not in v2-records/teams.json (make team-add NAME=demo DESCRIPTION=\"...\"); " +
			"no ResourceQuota as Git declares it; no NetworkPolicy on the app's pods (make check-apps, then make app-reconcile APP=hello ENV=staging)"},
		{"labels that differ from Git fail", func(s *ScopeEvidence) {
			s.Isolation.Namespaces = collectionOf(PlatformNamespaces, `{"metadata":{"name":"hello-staging","labels":{"platform.swhurl.com/app":"hello"}}}`)
		}, CheckBad, "hello-staging: the namespace labels differ from Git (platform.swhurl.com/team)"},
		{"a quota that is not Git's fails", func(s *ScopeEvidence) {
			s.Isolation.Quotas["hello-staging"] = collectionOf(PlatformQuotas, `{"metadata":{"name":"hello"},"spec":{"hard":{"pods":"20"}}}`)
		}, CheckBad, "no ResourceQuota as Git declares it"},
		{"an unreadable compiled object fails", func(s *ScopeEvidence) { delete(s.Compiled[0].Files, "namespace.json") },
			CheckBad, "v2-apps/instances/hello/staging: its compiled objects cannot be read (namespace.json is missing)"},
		{"a failed list fails", func(s *ScopeEvidence) { s.Isolation.Quotas["hello-staging"] = List{Status: Unavailable} },
			CheckBad, "cannot read app namespaces, ResourceQuotas or NetworkPolicies, so app isolation is unchecked (kubectl get namespaces -l platform.swhurl.com/app)"},
		{"an unreadable registry fails", func(s *ScopeEvidence) { s.Isolation.TeamsProblem = "invalid: teams" },
			CheckBad, "v2-records/teams.json cannot be read (invalid: teams)"},
		{"no compiled instance says so", func(s *ScopeEvidence) { s.Compiled = nil }, CheckOK, "no instance is compiled"},
	})
}

func TestSigningKeys(t *testing.T) {
	runScopeCases(t, "signing-keys", []scopeCase{
		{"the profile states the cluster's keys", unchanged, CheckOK, "v2/testdata/profile.json states the cluster's 1 signing key(s)"},
		{"and its issuer", unchanged, CheckOK, "token issuer is " + issuer},
		{"rotated keys fail", func(s *ScopeEvidence) { s.Signing.Keys = []byte(`{"keys":[{"kid":"rotated"}]}`) },
			CheckBad, "v2/testdata/profile.json does not state the cluster's signing key set, so apps refuse every call between them: " +
				"put the cluster's key set in its auth block and render platform/auth from it (docs/operations.md#signing-keys)"},
		{"another issuer fails", func(s *ScopeEvidence) { s.Signing.Issuer = "https://elsewhere" },
			CheckBad, "token issuer is https://elsewhere, but the profile states " + issuer},
		{"a failed read fails", func(s *ScopeEvidence) { s.Signing = SigningEvidence{Unread: ReasonForbidden} },
			CheckBad, "cannot read the cluster's token issuer or signing keys (kubectl get --raw /openid/v1/jwks)"},
		{"a profile with no keys fails", func(s *ScopeEvidence) { s.Signing.StatedKeys = "" },
			CheckBad, "v2/testdata/profile.json states no signing keys"},
		{"an unreadable profile fails", func(s *ScopeEvidence) { s.Signing.Unstated = "invalid: profile" },
			CheckBad, "v2/testdata/profile.json states no signing keys"},
	})
}

func TestEvents(t *testing.T) {
	notReady := `{"metadata":{"name":"world-ev"},"status":{"conditions":[{"type":"Ready","status":"False","message":"stream not found"}]}}`
	runScopeCases(t, "events", []scopeCase{
		{"NATS runs", unchanged, CheckOK, "NATS is running"},
		{"its controller runs", unchanged, CheckOK, "the stream controller is running"},
		{"every declaration exists", unchanged, CheckOK, "2 declared stream(s) and consumer(s) exist in NATS"},
		{"NATS not running fails", func(s *ScopeEvidence) { s.Events.Server = presentRead(PlatformNATS, `{"status":{}}`) },
			CheckBad, "NATS is not running: no events are published or delivered (make reconcile UNIT=platform-nats)"},
		{"NATS absent fails", func(s *ScopeEvidence) { s.Events.Server = Read{Ref: PlatformNATS, Status: Absent} },
			CheckBad, "NATS is missing"},
		{"a consumer NATS lacks fails", func(s *ScopeEvidence) { s.Events.Consumers["hello-staging"] = collectionOf(PlatformConsumers) },
			CheckBad, "Consumer hello-staging/world-ev is what Git declares, but NATS has no such consumer (make reconcile UNIT=platform-nats; " +
				"make check-apps says whether Git holds the declaration)"},
		{"a consumer that is not Ready fails", func(s *ScopeEvidence) {
			s.Events.Consumers["hello-staging"] = collectionOf(PlatformConsumers, notReady)
		}, CheckBad, "Consumer hello-staging/world-ev is not Ready, so its events are not kept or delivered: stream not found " +
			"(kubectl -n hello-staging describe consumer world-ev)"},
		{"a failed list fails", func(s *ScopeEvidence) { s.Events.Streams["hello-staging"] = List{Status: Unavailable} },
			CheckBad, "cannot read NATS, its controller, or the streams and consumers (make reconcile UNIT=platform-nats)"},
		{"a failed read fails", func(s *ScopeEvidence) { s.Events.Controller = Read{Status: Unavailable} },
			CheckBad, "cannot read NATS, its controller"},
	})
}

func TestEgress(t *testing.T) {
	runScopeCases(t, "egress", []scopeCase{
		{"both replicas run", unchanged, CheckOK, "the egress proxy runs 2 replicas"},
		{"every instance that reaches the proxy has its port", proxied("staging", 30100), CheckOK,
			"1 instance(s) use the proxy, each on a port of its own"},
		{"an instance that reaches no proxy needs no port", func(s *ScopeEvidence) { s.Egress.Ports = nil }, CheckOK,
			"0 instance(s) use the proxy, each on a port of its own"},
		{"one replica warns", func(s *ScopeEvidence) {
			s.Egress.Proxy = presentRead(PlatformEgress, `{"spec":{"replicas":2},"status":{"readyReplicas":1}}`)
		}, CheckWarn, "the egress proxy runs 1 of 2 replicas: a restart would stop outbound traffic meanwhile"},
		{"no replica fails", func(s *ScopeEvidence) {
			s.Egress.Proxy = presentRead(PlatformEgress, `{"spec":{"replicas":2},"status":{}}`)
		},
			CheckBad, "the egress proxy is not running: apps cannot reach outside the cluster"},
		{"a failed read fails", func(s *ScopeEvidence) { s.Egress.Proxy = Read{Status: Unavailable} },
			CheckBad, "cannot read the egress proxy (egress/egress): apps cannot reach outside the cluster (make reconcile UNIT=platform-egress)"},
		{"an absent proxy fails", func(s *ScopeEvidence) { s.Egress.Proxy = Read{Status: Absent} }, CheckBad, "the egress proxy (egress/egress) is missing"},
		{"a proxy user with no port fails", func(s *ScopeEvidence) { proxied("staging", 30100)(s); s.Egress.Ports = nil },
			CheckBad, "hello/staging reaches the proxy on port 30100, which the allocation does not give it: its records render the allocation (docs/services.md#egress-proxy)"},
		{"a proxy user on another port fails", proxied("staging", 30101),
			CheckBad, "hello/staging reaches the proxy on port 30101, but the allocation gives it 30100"},
		{"a shared port fails", func(s *ScopeEvidence) {
			proxied("staging", 30100)(s)
			prod := compiledHello()
			prod.Environment = "prod"
			s.Compiled = append(s.Compiled, prod)
			proxied("prod", 30100)(s)
			s.Egress.Ports = append(s.Egress.Ports, InstancePort{"hello", "prod", 30100})
		}, CheckBad, "port 30100 is shared by hello/staging, hello/prod"},
		{"an unreadable allocation fails", func(s *ScopeEvidence) { s.Egress.Problem = "invalid: egress" },
			CheckBad, "platform/egress/allocation.json cannot be read (invalid: egress), so the proxy's ports are unchecked"},
	})
}

// proxied gives the compiled hello instance in env the NetworkPolicy rule the render writes for one that reaches
// the proxy on port.
func proxied(env string, port int) func(*ScopeEvidence) {
	return func(s *ScopeEvidence) {
		for i := range s.Compiled {
			if s.Compiled[i].Environment == env {
				s.Compiled[i].Files["networkpolicy.json"] = []byte(fmt.Sprintf(`{"kind":"NetworkPolicy","spec":{"egress":[`+
					`{"ports":[{"port":%d,"protocol":"TCP"}],"to":[{"namespaceSelector":{"matchLabels":`+
					`{"kubernetes.io/metadata.name":"egress"}}}]}]}}`, port))
			}
		}
	}
}

func TestChannels(t *testing.T) {
	runScopeCases(t, "channels", []scopeCase{
		{"the routes are Git's", unchanged, CheckOK, "1 team(s) notify a channel, as in Git"},
		{"every channel has its Secret", unchanged, CheckOK, "1 channel(s) have their Secret"},
		{"other routes fail", func(s *ScopeEvidence) { s.Channels.Live = []byte(`{"teams":{"ghosts":["nowhere"]}}`) },
			CheckBad, "the team routes in the cluster are not those of platform/notify/routes.json: a team or channel change " +
				"rewrites them, so commit, push and make flux-reconcile"},
		{"no routes fail", func(s *ScopeEvidence) { s.Channels.Live = nil }, CheckBad, "the team routes in the cluster are not"},
		{"a failed read fails", func(s *ScopeEvidence) { s.Channels.Unread = ReasonForbidden },
			CheckBad, "could not read the notification routes and channels (make reconcile UNIT=platform-notify)"},
		{"a channel without the Secret the service reads fails", func(s *ScopeEvidence) { s.Channels.Secrets = nil },
			CheckBad, "channel platform has no Secret in the cluster: its teams get no notifications (make reconcile UNIT=platform-notify)"},
		{"a team that owns apps and has no channel warns", func(s *ScopeEvidence) { s.Channels.Owners["payments"] = []string{"pay", "bill"} },
			CheckWarn, "team payments owns 2 app(s) and has no notification channel: their alerts and messages reach nobody (make team-channel NAME=payments)"},
		{"a registry that renders no routes fails", func(s *ScopeEvidence) { s.Channels.Problem = "invalid: teams" },
			CheckBad, "v2-records/teams.json renders no notification routes (invalid: teams)"},
	})
	for _, entry := range scopeEntries(t, healthyScope(), "channels") {
		if entry.Level == CheckWarn {
			t.Fatalf("a team with a channel was warned about: %+v", entry)
		}
	}
}

func TestMongoBackups(t *testing.T) {
	old := platformNow.Add(-33 * time.Hour).Format("20060102T150405Z")
	newer := platformNow.Add(-8 * time.Hour).Format("20060102T150405Z")
	listed := func(names ...string) ArchiveList {
		list := ArchiveList{Status: Present}
		for _, name := range names {
			list.Objects = append(list.Objects, Archive{Key: "clickstack-mongodb/clickstack-mongodb-" + name})
		}
		return list
	}
	runScopeCases(t, "backups", []scopeCase{
		{"a fresh backup passes", unchanged, CheckOK, "newest MongoDB backup in s3://bucket-a/clickstack-mongodb/ is 1.0 h old"},
		{"a stale backup fails", func(s *ScopeEvidence) { s.Backups.Listing = listed(old+".archive.gz.age", old+".json") },
			CheckBad, "newest MongoDB backup in s3://bucket-a/clickstack-mongodb/ is 33 h old; check: kubectl -n observability get jobs -l platform.swhurl.com/backup=true"},
		{"no backup fails", func(s *ScopeEvidence) { s.Backups.Listing = listed() },
			CheckBad, "no MongoDB backup in s3://bucket-a/clickstack-mongodb/ (run: make backup-mongodb)"},
		{"an archive without its metadata does not count", func(s *ScopeEvidence) {
			s.Backups.Listing = listed(newer+".archive.gz.age", old+".archive.gz.age", old+".json")
		}, CheckBad, "is 33 h old"},
		{"an unlistable prefix fails", func(s *ScopeEvidence) { s.Backups.Listing = ArchiveList{Status: Unavailable, Reason: ReasonForbidden} },
			CheckBad, "cannot list backups in s3://bucket-a/clickstack-mongodb/: forbidden"},
		{"a tighter limit fails", func(s *ScopeEvidence) {
			s.MaxAge = 6 * time.Hour
			s.Backups.Listing = listed(newer+".archive.gz.age", newer+".json")
		}, CheckBad, "is 8 h old"},
		{"no credentials is not supplied", func(s *ScopeEvidence) { s.Credentials = false },
			CheckBad, "cannot list backups in s3://bucket-a/clickstack-mongodb/: AWS credentials not supplied"},
		{"an absent backup job fails", func(s *ScopeEvidence) { s.Backups.Job = Read{Status: Absent} },
			CheckBad, "the MongoDB backup job (observability/backup-mongodb) is missing"},
		{"a failed read fails", func(s *ScopeEvidence) { s.Backups.Job = Read{Status: Unavailable, Reason: ReasonForbidden} },
			CheckBad, "cannot read the MongoDB backup job (observability/backup-mongodb) (forbidden)"},
		{"a job with no location fails", func(s *ScopeEvidence) { s.Backups.Job = presentRead(PlatformMongoBackup, mongoJob("")) },
			CheckBad, "states no BACKUP_S3_URI of the form s3://<bucket>/<prefix>/"},
	})
}

func TestSQLiteBackups(t *testing.T) {
	runScopeCases(t, "sqlite-backups", []scopeCase{
		{"a fresh backup passes", unchanged, CheckOK, "hello-prod: newest SQLite backup in s3://bucket-a/app-sqlite/hello-prod/ is 1.0 h old"},
		{"no backup fails", func(s *ScopeEvidence) { s.Databases.Databases[0].Listing = ArchiveList{Status: Present} },
			CheckBad, "hello-prod: no SQLite backup in s3://bucket-a/app-sqlite/hello-prod/ (run: make backup-sqlite)"},
		{"no credentials is not supplied", func(s *ScopeEvidence) { s.Credentials = false },
			CheckBad, "hello-prod: cannot list backups in s3://bucket-a/app-sqlite/hello-prod/: AWS credentials not supplied"},
		{"no database says so", func(s *ScopeEvidence) { s.Databases.Databases = nil }, CheckOK, "no app has a SQLite database"},
		{"an unreadable release fails", func(s *ScopeEvidence) {
			s.Databases.Problem = "v2-records/hello/prod/release.json: cannot read it: invalid"
		}, CheckBad, "v2-records/hello/prod/release.json: cannot read it: invalid"},
	})
}

func TestConsole(t *testing.T) {
	release := func(tag string) func(*ScopeEvidence) {
		return func(s *ScopeEvidence) { s.Console.Tag = tag }
	}
	runScopeCases(t, "console", []scopeCase{
		{"the content tag is current", unchanged, CheckOK, "console image src-abc is built from the current tooling"},
		{"another content tag warns", release("src-new"), CheckWarn, "the console runs image src-abc, older than the tooling in this " +
			"checkout. Validate deploys a new one about 2 minutes after a push"},
		{"no tag supplied fails", release(""), CheckBad, "this checkout's console content tag was not supplied, so console image src-abc is unchecked"},
		{"a missing job record fails", func(s *ScopeEvidence) { s.Console.Jobs = false },
			CheckBad, "console/console-jobs is missing: the console forgets its jobs on restart (make reconcile UNIT=platform-console)"},
		{"an unread job record fails", func(s *ScopeEvidence) { s.Console.JobsUnread = ReasonForbidden },
			CheckBad, "cannot read console/console-jobs (forbidden)"},
	})
	commit := healthy()
	commit.Console = presentRead(PlatformConsole, strings.Replace(consoleAt, "src-abc", "b63d2aff9dfd4cf9df0307e382038dcbd15e29ed", 1))
	if entries := platformConsole(commit.Console, healthyScope().Console); entries[0] != warn("cannot compare console image b63d2af "+
		"with this checkout: it is not a content tag (src-…)") {
		t.Fatalf("a commit tag: %+v", entries)
	}
	if entries := platformConsole(Read{Status: Unavailable}, healthyScope().Console); entries[0].Level != CheckBad ||
		!strings.Contains(entries[0].Message, "cannot read the console HelmRelease image tag (console/console)") {
		t.Fatalf("an unread release: %+v", entries)
	}
}

// The backup set's names: only <name>-<stamp><suffix> is an archive, and only with <name>-<stamp>.json beside it.
func TestBackupNames(t *testing.T) {
	set := backupSet{name: "sqlite", suffix: ".db.age"}
	present := map[string]bool{"sqlite-20260929T033000Z.db.age": true, "sqlite-20260929T033000Z.json": true,
		"sqlite-20260930T033000Z.db.age": true, "other-20261001T033000Z.db.age": true, "other-20261001T033000Z.json": true,
		"sqlite-2026.db.age": true}
	taken, found := set.newest(func() []Archive {
		objects := []Archive{}
		for name := range present {
			objects = append(objects, Archive{Key: "p/" + name})
		}
		return append(objects, Archive{Key: "elsewhere/sqlite-20261002T033000Z.db.age"})
	}(), "p/")
	if !found || taken != time.Date(2026, 9, 29, 3, 30, 0, 0, time.UTC) {
		t.Fatalf("%v %v", taken, found)
	}
	if _, found := set.newest([]Archive{{Key: "p/sqlite-20260929T033000Z.json"}, {Key: "p/notes.txt"}}, "p/"); found {
		t.Fatal("a metadata file alone is a backup")
	}
	for uri, ok := range map[string]bool{"s3://bucket/p/": true, "s3://bucket/": true, "s3://bucket/p": false, "bucket/p/": false, "s3:///p/": false} {
		if _, found := BackupLocation(uri); found != ok {
			t.Errorf("%s: %v", uri, found)
		}
	}
}

func TestCompiledObjectsAndEventNamespaces(t *testing.T) {
	for name, want := range map[string]bool{"namespace.json": true, "consumer-a.json": true, "stream.json": true,
		"helmrelease.json": false, "consumer-a.yaml": false} {
		if CompiledObject(name) != want {
			t.Errorf("%s", name)
		}
	}
	if got := EventNamespaces([]CompiledInstance{compiledHello(), {Application: "x", Environment: "prod"}}); strings.Join(got, ",") != "hello-staging" {
		t.Fatalf("%v", got)
	}
	if compiledHello().Namespace() != "hello-staging" || (CompiledInstance{}).Namespace() != "" {
		t.Fatal("namespace")
	}
}
