package legacy_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/legacy"
)

// records reads the converted v1 fixture: one authenticated-web instance with a tag beside its digest.
func records(t *testing.T) legacy.Records {
	t.Helper()
	read := func(name string) []byte {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	return legacy.Records{App: read("app.json"), Instance: read("instance.json"),
		Release: read("release.json"), Committed: read("helmrelease.json")}
}

// mutate decodes a document, changes it and re-encodes it, so a refusal case is one visible change.
func mutate(t *testing.T, data []byte, change func(map[string]any)) []byte {
	t.Helper()
	document := map[string]any{}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	change(document)
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// values is the committed HelmRelease's values block.
func values(document map[string]any) map[string]any {
	return document["spec"].(map[string]any)["values"].(map[string]any)
}

func controllerOf(document map[string]any) map[string]any {
	return values(document)["controllers"].(map[string]any)["main"].(map[string]any)
}

// The fixture translates into exactly the facts the preview renders, with each fact's source stated and
// every legacy fact it cannot carry named.
func TestTranslateOneInstance(t *testing.T) {
	translation, err := legacy.Translate(records(t))
	if err != nil {
		t.Fatal(err)
	}
	if translation.Application != "hello" || translation.Environment != "staging" {
		t.Fatalf("identity %s/%s", translation.Application, translation.Environment)
	}
	want := legacy.Workload{
		Image: "docker.io/nginxinc/nginx-unprivileged", Tag: "1.27-alpine",
		Digest: "sha256:65e3e85dbaed8ba248841d9d58a899b6197106c23cb0ff1a132b7bfe0547e4c0",
		Kind:   "web", Port: 8080, HealthPath: "/", UID: 101,
	}
	if !reflect.DeepEqual(translation.Workload, want) {
		t.Fatalf("workload %+v", translation.Workload)
	}
	// The record states one replica and the committed output states the effective memory limit; the
	// preview uses the limit, which is the choice the plan names.
	if translation.Instance.Replicas != 1 || translation.Instance.MemoryMiB != 128 {
		t.Fatalf("instance %+v", translation.Instance)
	}
	if translation.Instance.Exposure != "authenticated-web" || translation.Instance.Team != "demo" {
		t.Fatalf("grants %+v", translation.Instance)
	}
	if translation.Sources["instance.replicas"] != "instance.yaml" || translation.Sources["instance.memory"] != "committed HelmRelease" {
		t.Fatalf("sources %v", translation.Sources)
	}
	// A signed-in route and the team are facts the preview carries now; the image provenance still is.
	for _, fact := range []string{"image.provenance"} {
		if !hasGap(translation.Gaps, fact) {
			t.Errorf("no gap for %s: %+v", fact, translation.Gaps)
		}
	}
	// Nothing in the fixture is silently dropped: the committed output is not the translator's evidence
	// of anything it did not take, and the records' unused fields are all named.
	if gaps := translation.Gaps; len(gaps) == 0 || gaps[0].Owner == "" {
		t.Fatalf("gaps %+v", gaps)
	}
	for _, gap := range translation.Gaps {
		if gap.Fact == "" || gap.Where == "" || gap.Owner == "" {
			t.Errorf("an incomplete gap: %+v", gap)
		}
		if strings.Contains(gap.Where, "64") || strings.Contains(gap.Fact, "sha256") {
			t.Errorf("a gap carries a value: %+v", gap)
		}
	}
}

func hasGap(gaps []legacy.Gap, fact string) bool {
	for _, gap := range gaps {
		if gap.Fact == fact {
			return true
		}
	}
	return false
}

// A replica count the record omits comes from the committed output; one that neither states is refused
// rather than defaulted.
func TestReplicasComeFromIdentifiedEvidence(t *testing.T) {
	base := records(t)
	instance, committed := base.Instance, base.Committed
	withoutReplicas := mutate(t, instance, func(document map[string]any) { delete(document, "replicas") })
	translation, err := legacy.Translate(legacy.Records{App: base.App, Instance: withoutReplicas,
		Release: base.Release, Committed: committed})
	if err != nil {
		t.Fatal(err)
	}
	if translation.Instance.Replicas != 1 || translation.Sources["instance.replicas"] != "committed HelmRelease" {
		t.Fatalf("replicas %+v from %s", translation.Instance.Replicas, translation.Sources["instance.replicas"])
	}
	// Neither document states a count: the pinned chart's default applies, and the source says so rather
	// than pretending a record stated it.
	noEvidence := mutate(t, committed, func(document map[string]any) { delete(controllerOf(document), "replicas") })
	translation, err = legacy.Translate(legacy.Records{App: base.App, Instance: withoutReplicas,
		Release: base.Release, Committed: noEvidence})
	if err != nil {
		t.Fatal(err)
	}
	if translation.Instance.Replicas != 1 || translation.Sources["instance.replicas"] != "pinned chart default" {
		t.Fatalf("the chart default: %+v from %s", translation.Instance.Replicas, translation.Sources["instance.replicas"])
	}
}

// A memory limit the committed output states in Gi is read as MiB; a spelling the preview does not carry
// is refused, not rounded.
func TestMemoryQuantities(t *testing.T) {
	base := records(t)
	oneGi := mutate(t, base.Committed, func(document map[string]any) {
		controllerOf(document)["containers"].(map[string]any)["main"].(map[string]any)["resources"].(map[string]any)["limits"].(map[string]any)["memory"] = "1Gi"
	})
	translation, err := legacy.Translate(legacy.Records{App: base.App, Instance: base.Instance, Release: base.Release, Committed: oneGi})
	if err != nil {
		t.Fatal(err)
	}
	if translation.Instance.MemoryMiB != 1024 {
		t.Fatalf("1Gi became %dMi", translation.Instance.MemoryMiB)
	}
	odd := mutate(t, base.Committed, func(document map[string]any) {
		controllerOf(document)["containers"].(map[string]any)["main"].(map[string]any)["resources"].(map[string]any)["limits"].(map[string]any)["memory"] = "0.5Gi"
	})
	if _, err := legacy.Translate(legacy.Records{App: base.App, Instance: base.Instance, Release: base.Release, Committed: odd}); !refusal(err, legacy.Unsupported, "committed HelmRelease: memory", "unsupported_quantity") {
		t.Fatalf("a fractional quantity: %v", err)
	}
}

// Records the preview cannot use are refused with a fixed category: a malformed document, a layout that
// is not the HTTP preview, or a fact no document establishes.
func TestTranslateRefusals(t *testing.T) {
	base := records(t)
	for _, tt := range []struct {
		name   string
		change func(*legacy.Records)
		code   string
		field  string
		reason string
	}{
		{"an app record that is not JSON", func(r *legacy.Records) { r.App = []byte("team: demo") }, legacy.Invalid, "app.yaml", "single_json_object_required"},
		{"a version this translator does not know", func(r *legacy.Records) {
			r.Instance = mutate(t, r.Instance, func(document map[string]any) { document["version"] = 2 })
		}, legacy.Unsupported, "instance.yaml: version", "only_version_1"},
		{"a release with no image", func(r *legacy.Records) {
			r.Release = mutate(t, r.Release, func(document map[string]any) { document["image"] = map[string]any{} })
		}, legacy.Missing, "release.yaml: image.digest", "a_digest_is_required"},
		{"a worker release, which the preview does not compare", func(r *legacy.Records) {
			r.Release = mutate(t, r.Release, func(document map[string]any) {
				document["kind"] = "worker"
				delete(document, "port")
				delete(document, "healthPath")
			})
		}, legacy.Unsupported, "release.yaml: kind", "only_http_preview"},
		{"a worker stating a port", func(r *legacy.Records) {
			r.Release = mutate(t, r.Release, func(document map[string]any) { document["kind"] = "worker" })
		}, legacy.Invalid, "release.yaml: port", "a_worker_has_no_port"},
		{"a kind this v2 does not model", func(r *legacy.Records) {
			r.Release = mutate(t, r.Release, func(document map[string]any) {
				document["kind"] = "job"
				delete(document, "port")
				delete(document, "healthPath")
			})
		}, legacy.Unsupported, "release.yaml: kind", "only_web_and_worker"},
		{"an unreadable committed output", func(r *legacy.Records) { r.Committed = []byte(`{}`) },
			legacy.Invalid, "helmrelease.yaml: metadata", "application_and_environment_required"},
		{"a committed output with no main controller", func(r *legacy.Records) {
			r.Committed = mutate(t, r.Committed, func(document map[string]any) { values(document)["controllers"] = map[string]any{} })
		}, legacy.Invalid, "helmrelease.yaml: values.controllers", "main_controller_required"},
		{"a numeric field of the wrong type", func(r *legacy.Records) {
			r.Release = mutate(t, r.Release, func(document map[string]any) { document["port"] = "8080" })
		}, legacy.Invalid, "release.yaml: port", "expected_an_integer"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			changed := legacy.Records{App: base.App, Instance: base.Instance, Release: base.Release, Committed: base.Committed}
			tt.change(&changed)
			if _, err := legacy.Translate(changed); !refusal(err, tt.code, tt.field, tt.reason) {
				t.Fatalf("got %v, want %s %s/%s", err, tt.code, tt.field, tt.reason)
			}
		})
	}
}

// A field the translator does not know becomes a gap owned by the bridge, never a silent drop.
func TestAnUnknownFieldIsAGap(t *testing.T) {
	base := records(t)
	unknown := mutate(t, base.Instance, func(document map[string]any) { document["somethingNew"] = true })
	translation, err := legacy.Translate(legacy.Records{App: base.App, Instance: unknown, Release: base.Release, Committed: base.Committed})
	if err != nil {
		t.Fatal(err)
	}
	for _, gap := range translation.Gaps {
		if gap.Where == "instance.yaml: somethingNew" {
			if gap.Owner != "transition bridge" {
				t.Fatalf("owner %s", gap.Owner)
			}
			return
		}
	}
	t.Fatalf("the unknown field is not a gap: %+v", translation.Gaps)
}

func refusal(err error, code, field, reason string) bool {
	var refusal *legacy.Error
	return errors.As(err, &refusal) && refusal.Code == code && refusal.Field == field && refusal.Reason == reason
}
