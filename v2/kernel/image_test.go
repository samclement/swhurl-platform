package kernel

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// declaration is one image's statement about itself, as the build writes it into its config labels: a web image
// that publishes one event, states the memory it runs in, and carries its events document.
func declaration(change func(*Declaration)) Declaration {
	d := Declaration{SchemaVersion: 2,
		Runtime:   Runtime{Kind: "web", Port: 8080, HealthPath: "/healthz", UID: 65532},
		Telemetry: true, Publishes: []string{"created"}, MemoryMiB: 128,
		Documents: map[string][]byte{"events": []byte(`{"asyncapi":"3.0.0"}`)}}
	if change != nil {
		change(&d)
	}
	return d
}

func declarationJSON(t *testing.T, d Declaration) []byte {
	t.Helper()
	document, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func TestNewestTag(t *testing.T) {
	tags := []string{"4-aaaaaaaa", "10-bbbbbbbb", "9-cccccccc", "not-a-tag", "12-dddddddd", "3-eeeeeeee"}
	got, found := NewestTag(tags)
	if !found || got.Name != "12-dddddddd" || got.Run != 12 || got.Commit != "dddddddd" {
		t.Fatalf("newest %+v, found %v", got, found)
	}
	// Two tags with the same run: the greater name wins, as v1's ranking does.
	if got, _ := NewestTag([]string{"7-aaaaaaaa", "7-bbbbbbbb"}); got.Name != "7-bbbbbbbb" {
		t.Fatalf("a tie picked %s", got.Name)
	}
	// A tag outside the contract, including a run the platform cannot rank, is not a candidate.
	if _, found := NewestTag([]string{"latest", "v1-aaaaaaaa", "99999999999999999999-aaaaaaaa"}); found {
		t.Fatal("a tag outside the contract was picked")
	}
	// The suffix may be abbreviated; the release refuses a commit that is not forty characters, so the
	// policy carries what the tag says rather than completing it.
	if got, _ := NewestTag([]string{"1-aaaaaaa"}); got.Commit != "aaaaaaa" {
		t.Fatalf("commit %q", got.Commit)
	}
}

func TestImageParts(t *testing.T) {
	host, repository, err := ImageParts("ghcr.io/samclement/demo-orders")
	if err != nil || host != "ghcr.io" || repository != "samclement/demo-orders" {
		t.Fatalf("%q %q %v", host, repository, err)
	}
	if got := ImageRepository("docker.io/nginxinc/nginx-unprivileged"); got != "nginxinc/nginx-unprivileged" {
		t.Fatalf("repository %q", got)
	}
	for _, image := range []string{"", "ghcr.io", "GHCR.IO/owner/app", "ghcr.io/owner/app:tag"} {
		if _, _, err := ImageParts(image); err == nil {
			t.Errorf("%q was split", image)
		}
		if got := ImageRepository(image); got != "" {
			t.Errorf("%q has repository %q", image, got)
		}
	}
}

func TestDecodeDeclaration(t *testing.T) {
	valid := string(declarationJSON(t, declaration(nil)))
	if _, err := DecodeDeclaration([]byte(valid)); err != nil {
		t.Fatalf("a valid declaration: %v", err)
	}
	for _, tt := range []struct {
		name, text string
		want       Code
		field      string
	}{
		{"unknown field", strings.Replace(valid, `"telemetry":true`, `"telemetry":true,"extra":1`, 1), Unsupported, "declaration"},
		{"unknown runtime field", strings.Replace(valid, `"uid":65532`, `"uid":65532,"command":["x"]`, 1), Unsupported, "runtime"},
		{"null", strings.Replace(valid, `"telemetry":true`, `"telemetry":null`, 1), Invalid, "declaration.telemetry"},
		{"missing schema", strings.Replace(valid, `"schemaVersion":2,`, ``, 1), Invalid, "declaration.schemaVersion"},
		{"missing runtime", strings.Replace(valid, `"runtime":{"kind":"web","port":8080,"healthPath":"/healthz","uid":65532},`, ``, 1), Invalid, "declaration.runtime"},
		{"missing uid", strings.Replace(valid, `,"uid":65532`, ``, 1), Invalid, "runtime.uid"},
		{"missing memory", strings.Replace(valid, `"memoryMiB":128,`, ``, 1), Invalid, "declaration.memoryMiB"},
		{"version 1 is refused", strings.Replace(valid, `"schemaVersion":2`, `"schemaVersion":1`, 1), Unsupported, "schemaVersion"},
		{"wrong type", strings.Replace(valid, `"port":8080`, `"port":"8080"`, 1), Invalid, "declaration"},
		{"fraction", strings.Replace(valid, `"port":8080`, `"port":8080.5`, 1), Invalid, "declaration"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeDeclaration([]byte(tt.text))
			refusal(t, err, tt.want, tt.field)
			if strings.Contains(err.Error(), "description") {
				t.Fatal("diagnostic dumped an input value")
			}
		})
	}
	for _, tt := range []struct {
		name   string
		change func(*Declaration)
		want   Code
		field  string
	}{
		{"worker with a port", func(d *Declaration) { d.Runtime = Runtime{Kind: "worker", Port: 8080, UID: 65532} }, Invalid, "runtime.port"},
		{"web without a port", func(d *Declaration) { d.Runtime.Port = 0 }, Invalid, "runtime.port"},
		{"unknown kind", func(d *Declaration) { d.Runtime.Kind = "job" }, Unsupported, "runtime.kind"},
		{"repeated event", func(d *Declaration) { d.Publishes = []string{"created", "created"} }, Invalid, "publishes"},
		{"bad event", func(d *Declaration) { d.Publishes = []string{"Created"} }, Invalid, "publishes"},
		{"zero memory", func(d *Declaration) { d.MemoryMiB = 0 }, Invalid, "declaration.memoryMiB"},
		{"secrets are not modelled", func(d *Declaration) { d.Secrets = []string{"TOKEN"} }, Unsupported, "declaration.secrets"},
		{"api path without its document", func(d *Declaration) { d.APIPath = "/openapi.json" }, Invalid, "declaration.apiPath"},
		{"api document without its path", func(d *Declaration) { d.Documents["api"] = []byte(`{}`) }, Invalid, "declaration.documents.api"},
		{"publishes without its events document", func(d *Declaration) { delete(d.Documents, "events") }, Invalid, "declaration.documents.events"},
		{"database without a volume", func(d *Declaration) { d.Database = "sqlite" }, Invalid, "declaration.databaseSizeMiB"},
		{"volume without a database", func(d *Declaration) { d.DatabaseSizeMiB = 1024 }, Invalid, "declaration.databaseSizeMiB"},
		{"unknown document role", func(d *Declaration) { d.Documents["source"] = []byte(`{}`) }, Invalid, "declaration.documents"},
		{"empty document", func(d *Declaration) { d.Documents["flags"] = []byte{} }, Invalid, "declaration.documents.flags"},
		{"description with a newline", func(d *Declaration) { d.Description = "two\nlines" }, Invalid, "declaration.description"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			change := declaration(tt.change)
			_, err := DecodeDeclaration(declarationJSON(t, change))
			refusal(t, err, tt.want, tt.field)
		})
	}
	// The documents are the file's bytes: the label's base64 decodes to exactly what the build read.
	withDocuments := declaration(func(d *Declaration) {
		d.Documents["api"] = []byte("{\n  \"openapi\": \"3.1.0\"\n}\n")
		d.APIPath = "/openapi.json"
	})
	decoded, err := DecodeDeclaration(declarationJSON(t, withDocuments))
	if err != nil || string(decoded.Documents["api"]) != "{\n  \"openapi\": \"3.1.0\"\n}\n" || decoded.APIPath != "/openapi.json" {
		t.Fatalf("documents %q %v", decoded.Documents, err)
	}
	if files := decoded.OfferedFiles(); len(files) != 2 || files[0].Path != "openapi.json" || files[1].Path != "asyncapi.yaml" {
		t.Fatalf("offered files %+v", files)
	}
}

func TestDeclarationRelease(t *testing.T) {
	d := declaration(nil)
	release, err := d.Release("demo-orders", "samclement/demo-orders", strings.Repeat("c", 40),
		"ghcr.io/samclement/demo-orders", "sha256:"+strings.Repeat("a", 64), ManifestMediaType)
	if err != nil {
		t.Fatal(err)
	}
	if release.ReleaseID == "" || release.Artifact.Platform != "linux/amd64" || release.Database != "" {
		t.Fatalf("release %+v", release)
	}
	// An abbreviated commit is the tag's fact, and a release cannot state one.
	_, err = d.Release("demo-orders", "samclement/demo-orders", strings.Repeat("c", 7),
		"ghcr.io/samclement/demo-orders", "sha256:"+strings.Repeat("a", 64), ManifestMediaType)
	refusal(t, err, Invalid, "source.commit")
	// A Docker media type is a well-formed registry answer the platform does not run.
	_, err = d.Release("demo-orders", "samclement/demo-orders", strings.Repeat("c", 40),
		"ghcr.io/samclement/demo-orders", "sha256:"+strings.Repeat("a", 64),
		"application/vnd.docker.distribution.manifest.v2+json")
	refusal(t, err, Unsupported, "artifact.mediaType")
}

func TestAcceptDeclaration(t *testing.T) {
	spec := instance(t, "staging.json")
	current := releaseFile(t, "r42.json")
	if err := AcceptDeclaration(declaration(nil), spec, current); err != nil {
		t.Fatalf("a declaration the instance can take: %v", err)
	}
	volume := spec
	volume.Storage = Storage{ClaimName: "demo-orders"}
	for _, tt := range []struct {
		name   string
		change func(*Declaration)
		spec   InstanceSpec
		want   string
	}{
		{"new secrets", func(d *Declaration) { d.Secrets = []string{"TOKEN"} }, spec, "declaration.secrets"},
		{"larger memory floor", func(d *Declaration) { d.MemoryMiB = 129 }, spec, "declaration.memoryMiB"},
		{"memory floor equal to the instance", func(d *Declaration) { d.MemoryMiB = 128 }, spec, ""},
		{"another kind", func(d *Declaration) {
			d.Runtime = Runtime{Kind: "worker", UID: 65532}
		}, spec, "runtime.kind"},
		{"a database an image cannot add", func(d *Declaration) { d.Database = "sqlite" }, spec, "declaration.database"},
		{"a database the instance keeps", func(d *Declaration) { d.Database = "" }, volume, "declaration.database"},
		{"another user on a volume", func(d *Declaration) {
			d.Runtime.UID, d.Database = 1000, "sqlite"
		}, volume, "runtime.uid"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			changed := declaration(tt.change)
			err := AcceptDeclaration(changed, tt.spec, sqliteOr(current, tt.spec))
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			refusal(t, err, Unsupported, tt.want)
		})
	}
}

// sqliteOr is the current release a case is about: one that keeps a database when the instance has a volume,
// so a database removal is the change under test.
func sqliteOr(current ReleaseBundle, spec InstanceSpec) ReleaseBundle {
	if spec.Storage.ClaimName == "" {
		return current
	}
	release := current
	release.Database, release.ReleaseID = "sqlite", ""
	id, err := release.ID()
	if err != nil {
		panic(err) // the fixture is valid; a test that changed it knows
	}
	release.ReleaseID = id
	return release
}

// A staging instance that publishes its own image carries the pair v1 puts in its unit; production follows a
// promotion, and a pinned third-party image is not the app's to follow.
func TestImageAutomationRendersOnlyAStagingAppsOwnImage(t *testing.T) {
	p := profile(t)
	staging := compile(t, instance(t, "staging.json"), releaseFile(t, "r42.json"), p)
	f := files(staging)
	repository := f["v2-apps/instances/demo-orders/staging/image-repository.json"]
	policy := f["v2-apps/instances/demo-orders/staging/image-policy.json"]
	for _, want := range []string{`"kind":"ImageRepository"`, `"namespace":"flux-system"`, `"image":"ghcr.io/samclement/demo-orders"`,
		`"platform.swhurl.com/managed":"true"`, `"platform.swhurl.com/app":"demo-orders"`, `"interval":"1h"`} {
		if !strings.Contains(repository, want) {
			t.Errorf("ImageRepository lacks %s:\n%s", want, repository)
		}
	}
	for _, want := range []string{`"kind":"ImagePolicy"`, `"name":"demo-orders-staging"`, `"name":"demo-orders"`,
		`"pattern":"^(?P<run>[0-9]+)-[0-9a-f]{7,40}$"`, `"extract":"$run"`, `"order":"asc"`,
		`"digestReflectionPolicy":"Always"`, `"interval":"1h"`} {
		if !strings.Contains(policy, want) {
			t.Errorf("ImagePolicy lacks %s:\n%s", want, policy)
		}
	}
	kustomization := f["v2-apps/instances/demo-orders/staging/kustomization.yaml"]
	if !strings.Contains(kustomization, `"image-repository.json","image-policy.json"`) {
		t.Errorf("the folder does not list the pair: %s", kustomization)
	}
	unit := f["clusters/home/app-demo-orders-staging.json"]
	if !strings.Contains(unit, `{"name":"infra-base"},{"name":"platform-image-automation"}`) {
		t.Errorf("the unit does not wait for image automation: %s", unit)
	}
	// The pair is owned by the instance, so an ownership check can see two instances cannot both publish it.
	claimed := 0
	for _, claim := range staging.Inventory {
		if claim.Kind == "ImageRepository" || claim.Kind == "ImagePolicy" {
			if claim.Owner != "demo-orders/staging" || claim.Namespace != "flux-system" {
				t.Errorf("claim %+v", claim)
			}
			claimed++
		}
	}
	if claimed != 2 {
		t.Fatalf("inventory claims %d image objects", claimed)
	}

	// Production renders none: a promotion selects the image, not the repository.
	prod := compile(t, instance(t, "prod.json"), releaseFile(t, "r41.json"), p)
	for path := range files(prod) {
		if strings.Contains(path, "image-") {
			t.Errorf("production rendered %s", path)
		}
	}
	if strings.Contains(files(prod)["clusters/home/app-demo-orders-prod.json"], "platform-image-automation") {
		t.Error("production waits for image automation")
	}

	// A staging instance whose image is not published from its own source repository renders none either.
	external := releaseFile(t, "r42.json")
	external.Source.Repository, external.Artifact.Image, external.ReleaseID = "samclement/hello", "docker.io/nginxinc/nginx-unprivileged", ""
	id, err := external.ID()
	if err != nil {
		t.Fatal(err)
	}
	external.ReleaseID = id
	spec := instance(t, "staging.json")
	spec.Release = id
	compiled, err := CompileWith(spec, external, app(t), p, Connections{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for path := range files(compiled) {
		if strings.Contains(path, "image-") {
			t.Errorf("a third-party image rendered %s", path)
		}
	}

	// A worker publishes an image too: only its HTTP surface is absent.
	worker := releaseFile(t, "r42.json")
	worker.Runtime, worker.ReleaseID = Runtime{Kind: "worker", UID: 65532}, ""
	worker.APIPath, worker.Documents = "", nil
	id, err = worker.ID()
	if err != nil {
		t.Fatal(err)
	}
	worker.ReleaseID = id
	workerSpec := instance(t, "staging.json")
	workerSpec.Release = id
	workerCompiled, err := CompileWith(workerSpec, worker, app(t), p, Connections{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := files(workerCompiled)["v2-apps/instances/demo-orders/staging/image-policy.json"]; !found {
		t.Fatal("a worker rendered no image policy")
	}
}

func TestRevisionOf(t *testing.T) {
	full := strings.Repeat("a", 40)
	for _, test := range []struct {
		name     string
		tag      string
		revision string
		ok       bool
	}{
		{name: "the tag abbreviates the revision", tag: "42-" + full[:7], revision: full, ok: true},
		{name: "the tag holds the whole revision", tag: "42-" + full, revision: full, ok: true},
		{name: "an abbreviation is not a commit", tag: "42-" + full[:7], revision: full[:7]},
		{name: "another build's revision", tag: "42-" + full[:7], revision: strings.Repeat("b", 40)},
		{name: "not hexadecimal", tag: "42-" + full[:7], revision: strings.Repeat("z", 40)},
	} {
		t.Run(test.name, func(t *testing.T) {
			tag, found := NewestTag([]string{test.tag})
			if !found {
				t.Fatalf("the tag %q does not match the platform's tag contract", test.tag)
			}
			revision, err := RevisionOf(tag, test.revision)
			if test.ok != (err == nil) {
				t.Fatalf("ok %v, err %v", test.ok, err)
			}
			if test.ok && revision != test.revision {
				t.Fatalf("revision %q", revision)
			}
		})
	}
}

// An image reference an operator names splits into the artifact a record keeps and the tag that states the commit,
// and a reference that states no such tag is refused rather than fitted to one.
func TestSplitImage(t *testing.T) {
	commit := strings.Repeat("d", 40)
	for _, test := range []struct {
		name, reference        string
		image, tag, commitWant string
		ok                     bool
	}{
		{name: "a tag the platform publishes", reference: "ghcr.io/samclement/demo-orders:43-" + commit,
			image: "ghcr.io/samclement/demo-orders", tag: "43-" + commit, commitWant: commit, ok: true},
		{name: "a digest states no commit a tag can carry",
			reference: "ghcr.io/samclement/demo-orders@sha256:" + strings.Repeat("d", 64)},
		{name: "a tag outside the contract", reference: "ghcr.io/samclement/demo-orders:latest"},
		{name: "no tag at all", reference: "ghcr.io/samclement/demo-orders"},
		{name: "a host the platform does not name", reference: "localhost/x:43-" + commit},
	} {
		t.Run(test.name, func(t *testing.T) {
			image, tag, err := SplitImage(test.reference)
			if test.ok != (err == nil) {
				t.Fatalf("ok %v, err %v", test.ok, err)
			}
			if !test.ok {
				var refusal *Error
				if !errors.As(err, &refusal) || refusal.Code != Invalid {
					t.Fatalf("error = %v, want invalid", err)
				}
				return
			}
			if image != test.image || tag.Name != test.tag || tag.Commit != test.commitWant {
				t.Fatalf("image %q tag %+v", image, tag)
			}
		})
	}
}
