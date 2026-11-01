package kernel

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func release(t *testing.T) ReleaseBundle {
	t.Helper()
	r, err := DecodeRelease(fixture(t, "r42.json"))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func instance(t *testing.T, name string) InstanceSpec {
	t.Helper()
	s, err := DecodeInstance(fixture(t, name))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func code(t *testing.T, err error, want Code) {
	t.Helper()
	var refusal *Error
	if !errors.As(err, &refusal) || refusal.Code != want {
		t.Fatalf("error = %v, want code %s", err, want)
	}
}

// refusal checks the code and the field: when a document breaks several rules, which one decides.
func refusal(t *testing.T, err error, want Code, field string) {
	t.Helper()
	var got *Error
	if !errors.As(err, &got) || got.Code != want || got.Field != field {
		t.Fatalf("error = %v, want %s: %s", err, want, field)
	}
}

func TestRoundTrips(t *testing.T) {
	r := release(t)
	b, err := EncodeRelease(r)
	if err != nil {
		t.Fatal(err)
	}
	round, err := DecodeRelease(b)
	if err != nil || !reflect.DeepEqual(round, r) {
		t.Fatalf("release round-trip: %v", err)
	}
	if !bytes.Contains(b, []byte(`"releaseId"`)) {
		t.Fatal("output must assert the release identity")
	}
	for _, file := range []string{"staging.json", "prod.json"} {
		s := instance(t, file)
		b, err = EncodeInstance(s)
		if err != nil {
			t.Fatal(err)
		}
		round, err := DecodeInstance(b)
		if err != nil || !reflect.DeepEqual(round, s) {
			t.Fatalf("instance round-trip: %v", err)
		}
	}
}

func TestSelectionChangesOnlyRelease(t *testing.T) {
	r, prod, staging := release(t), instance(t, "prod.json"), instance(t, "staging.json")
	beforeRelease, beforeProd, beforeStaging := r, prod, staging
	revision, err := prod.Revision()
	if err != nil {
		t.Fatal(err)
	}
	after, err := SelectRelease(prod, r, revision)
	if err != nil {
		t.Fatal(err)
	}
	want := prod
	want.Release = r.ReleaseID
	if !reflect.DeepEqual(after, want) {
		t.Fatalf("selection changed more than the reference: %#v", after)
	}
	if !reflect.DeepEqual(prod, beforeProd) || !reflect.DeepEqual(r, beforeRelease) ||
		!reflect.DeepEqual(staging, beforeStaging) {
		t.Fatal("input mutated")
	}
	if prod.Configuration == staging.Configuration {
		t.Fatal("fixture must distinguish environment configuration")
	}
	if after.Release != staging.Release {
		t.Fatal("production did not select staging's exact release")
	}
	newRevision, err := after.Revision()
	if err != nil {
		t.Fatal(err)
	}
	if newRevision == revision {
		t.Fatal("selection must change instance revision")
	}
	again, err := SelectRelease(after, r, newRevision)
	if err != nil || !reflect.DeepEqual(again, after) {
		t.Fatalf("matching-base no-op: %v", err)
	}
	_, err = SelectRelease(after, r, revision)
	code(t, err, StaleInstance) // stale must precede the already-selected short circuit
	// Mutating the returned value cannot mutate either original (no reference-bearing members).
	after.Configuration.MemoryMiB++
	if !reflect.DeepEqual(prod, beforeProd) || !reflect.DeepEqual(staging, beforeStaging) {
		t.Fatal("returned value aliases an input")
	}
}

func TestSelectionRefusalsLeaveInputsUnchanged(t *testing.T) {
	base, current := release(t), instance(t, "prod.json")
	revision, _ := current.Revision()
	for _, tt := range []struct {
		name   string
		change func(*InstanceSpec, *ReleaseBundle, *InstanceRevision)
		want   Code
	}{
		{"wrong application", func(_ *InstanceSpec, r *ReleaseBundle, _ *InstanceRevision) {
			r.Application = "other"
			r.ReleaseID = ""
		}, WrongApplication},
		{"stale", func(_ *InstanceSpec, _ *ReleaseBundle, rev *InstanceRevision) {
			*rev = InstanceRevision("inst-sha256:" + strings.Repeat("0", 64))
		}, StaleInstance},
		{"bad expected revision", func(_ *InstanceSpec, _ *ReleaseBundle, rev *InstanceRevision) { *rev = "" }, Invalid},
		{"unsupported runtime", func(_ *InstanceSpec, r *ReleaseBundle, _ *InstanceRevision) {
			r.Runtime.Kind = "job"
			r.ReleaseID = ""
		}, Unsupported},
		{"unsupported platform", func(_ *InstanceSpec, r *ReleaseBundle, _ *InstanceRevision) {
			r.Artifact.Platform = "linux/arm64"
			r.ReleaseID = ""
		}, Unsupported},
		{"forged identity", func(_ *InstanceSpec, r *ReleaseBundle, _ *InstanceRevision) { r.Runtime.Port++ }, IdentityMismatch},
		{"unsupported grant", func(s *InstanceSpec, _ *ReleaseBundle, _ *InstanceRevision) { s.Grants.Exposure = "public" }, Unsupported},
		{"invalid current", func(s *InstanceSpec, _ *ReleaseBundle, _ *InstanceRevision) { s.Configuration.MemoryMiB = 0 }, Invalid},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, r, rev := current, base, revision
			tt.change(&s, &r, &rev)
			beforeS, beforeR := s, r
			result, err := SelectRelease(s, r, rev)
			code(t, err, tt.want)
			if !reflect.DeepEqual(result, InstanceSpec{}) {
				t.Fatal("refusal returned a usable result")
			}
			// A record with declaration lists is no longer comparable with ==, so "unchanged" is a deep
			// comparison of both records.
			if !reflect.DeepEqual(s, beforeS) || !reflect.DeepEqual(r, beforeR) {
				t.Fatal("refusal mutated inputs")
			}
		})
	}
}

func TestGoldenIdentities(t *testing.T) {
	var golden struct{ ReleaseCanonical, ReleaseID, InstanceCanonical, InstanceRevision string }
	if err := json.Unmarshal(fixture(t, "identities.json"), &golden); err != nil {
		t.Fatal(err)
	}
	r, s := release(t), instance(t, "prod.json")
	if got := string(releaseCanonical(r)); got != golden.ReleaseCanonical {
		t.Fatalf("release canonical bytes: %q", got)
	}
	if got := string(instanceCanonical(s)); got != golden.InstanceCanonical {
		t.Fatalf("instance canonical bytes: %q", got)
	}
	id, _ := r.ID()
	revision, _ := s.Revision()
	if string(id) != golden.ReleaseID || string(revision) != golden.InstanceRevision {
		t.Fatalf("golden hash mismatch: %s %s", id, revision)
	}
	// Mapping order, whitespace, JSON escapes and an optional identity assertion do not change content.
	var doc map[string]any
	if err := json.Unmarshal(fixture(t, "r42.json"), &doc); err != nil {
		t.Fatal(err)
	}
	doc["releaseId"] = string(id)
	b, _ := json.Marshal(doc) // sorted keys differ from the fixture
	b = bytes.ReplaceAll(b, []byte("demo-orders"), []byte(`demo-\u006frders`))
	round, err := DecodeRelease(append([]byte(" \n"), b...))
	if err != nil || !reflect.DeepEqual(round, r) {
		t.Fatalf("normalization: %v", err)
	}
	var instanceDoc map[string]any
	_ = json.Unmarshal(fixture(t, "prod.json"), &instanceDoc)
	b, _ = json.MarshalIndent(instanceDoc, "", "    ")
	roundSpec, err := DecodeInstance(b)
	if err != nil || !reflect.DeepEqual(roundSpec, s) {
		t.Fatalf("instance normalization: %v", err)
	}
}

func TestIdentityAssertionsAndEscaping(t *testing.T) {
	r := release(t)
	r.Runtime.HealthPath = "/ready&ok"
	r.ReleaseID = ""
	id, err := r.ID()
	if err != nil || id != "rel-sha256:24b7b2e191beec2dfcc64dd77ba8b7d22f8918c35aa57118eba33f6b763290b4" {
		t.Fatalf("independent ASCII escaping vector: %s %v", id, err)
	}
	if !bytes.Contains(releaseCanonical(r), []byte(`"/ready&ok"`)) {
		t.Fatal("canonical encoding must not HTML-escape printable ASCII")
	}
	b, err := EncodeRelease(r)
	if err != nil {
		t.Fatal(err)
	}
	b = bytes.ReplaceAll(b, []byte("/ready"), []byte("/other"))
	decoded, err := DecodeRelease(b)
	code(t, err, IdentityMismatch)
	if !reflect.DeepEqual(decoded, ReleaseBundle{}) {
		t.Fatal("identity mismatch returned a record")
	}
	r.ReleaseID = "not-an-id"
	_, err = r.ID()
	code(t, err, Invalid)
	if b, err := EncodeRelease(r); err == nil || b != nil {
		t.Fatal("encoded invalid identity")
	}
	s := instance(t, "prod.json")
	s.Configuration.Replicas = -1
	if b, err := EncodeInstance(s); err == nil || b != nil {
		t.Fatal("encoded invalid instance")
	}
}

func TestEveryFieldParticipatesInIdentity(t *testing.T) {
	base := release(t)
	id, _ := base.ID()
	for _, change := range []func(*ReleaseBundle){
		func(r *ReleaseBundle) { r.Application = "other" },
		func(r *ReleaseBundle) { r.Source.Repository = "owner/other" },
		func(r *ReleaseBundle) { r.Source.Commit = strings.Repeat("c", 40) },
		func(r *ReleaseBundle) { r.Artifact.Image = "registry.example/owner/other" },
		func(r *ReleaseBundle) { r.Artifact.Digest = "sha256:" + strings.Repeat("c", 64) },
		func(r *ReleaseBundle) { r.Runtime.Port++ },
		func(r *ReleaseBundle) { r.Runtime.HealthPath = "/ready&ok" },
		func(r *ReleaseBundle) { r.Runtime.UID++ },
	} {
		r := base
		r.ReleaseID = ""
		change(&r)
		got, err := r.ID()
		if err != nil || got == id {
			t.Fatalf("changed release identity: %v", err)
		}
	}
	s := instance(t, "prod.json")
	rev, _ := s.Revision()
	for _, change := range []func(*InstanceSpec){
		func(s *InstanceSpec) { s.Application = "other" },
		func(s *InstanceSpec) { s.Environment = "staging" },
		func(s *InstanceSpec) { s.Release = base.ReleaseID },
		func(s *InstanceSpec) { s.Configuration.Replicas = 0 },
		func(s *InstanceSpec) { s.Configuration.MemoryMiB++ },
	} {
		changed := s
		change(&changed)
		got, err := changed.Revision()
		if err != nil || got == rev {
			t.Fatalf("changed instance revision: %v", err)
		}
	}
	// Fixed version/profile fields still appear in canonical bytes, though other values are refused.
	if !strings.Contains(string(releaseCanonical(base)), `"linux/amd64","web"`) {
		t.Fatal("profile missing from identity")
	}
}

func TestStrictDecoding(t *testing.T) {
	r, s := string(fixture(t, "r42.json")), string(fixture(t, "prod.json"))
	for _, tt := range []struct {
		name, text string
		release    bool
		want       Code
	}{
		{"case variant beside the key", strings.Replace(r, `"application":`, `"Application":"demo-orders", "application":`, 1), true, Unsupported},
		{"unknown top", strings.Replace(r, `"schemaVersion": 1`, `"extra": "do-not-echo", "schemaVersion": 1`, 1), true, Unsupported},
		{"grant in release", strings.Replace(r, `"runtime":`, `"grants": {"exposure":"public"}, "runtime":`, 1), true, Unsupported},
		{"unknown runtime", strings.Replace(r, `"kind": "web"`, `"database":"sqlite", "kind":"web"`, 1), true, Unsupported},
		{"case variant", strings.Replace(r, `"application"`, `"Application"`, 1), true, Invalid},
		{"duplicate", strings.Replace(r, `"application":`, `"application":"other", "application":`, 1), true, Invalid},
		{"nested duplicate", strings.Replace(r, `"port": 8080`, `"port":9090, "port":8080`, 1), true, Invalid},
		{"escaped duplicate", strings.Replace(r, `"port": 8080`, `"\u0070ort":9090, "port":8080`, 1), true, Invalid},
		{"null", strings.Replace(r, `"port": 8080`, `"port": null`, 1), true, Invalid},
		{"missing", strings.Replace(r, `"port": 8080,`, ``, 1), true, Invalid},
		{"false", strings.Replace(r, `"port": 8080`, `"port": false`, 1), true, Invalid},
		{"fraction", strings.Replace(r, `"port": 8080`, `"port": 8080.0`, 1), true, Invalid},
		{"exponent", strings.Replace(r, `"port": 8080`, `"port": 8.08e3`, 1), true, Invalid},
		{"string number", strings.Replace(r, `"port": 8080`, `"port": "8080"`, 1), true, Invalid},
		{"overflow", strings.Replace(r, `"port": 8080`, `"port": 2147483648`, 1), true, Invalid},
		{"extra document", r + "{}", true, Invalid},
		{"YAML", "schemaVersion: 1\n", true, Invalid},
		{"array", "[]", true, Invalid},
		{"null document", "null", true, Invalid},
		{"null object", strings.Replace(r, `"source": {`, `"source": null, "unused": {`, 1), true, Unsupported},
		{"null id", strings.Replace(r, `"schemaVersion": 1`, `"releaseId":null, "schemaVersion": 1`, 1), true, Invalid},
		{"empty id", strings.Replace(r, `"schemaVersion": 1`, `"releaseId":"", "schemaVersion": 1`, 1), true, Invalid},
		{"invalid UTF8", strings.Replace(r, "demo-orders", "demo-\xfforders", 1), true, Invalid},
		{"surrogate", strings.Replace(r, "demo-orders", `demo-\ud800orders`, 1), true, Invalid},
		{"non-ASCII", strings.Replace(r, "demo-orders", "démø-orders", 1), true, Invalid},
		{"nonempty bindings", strings.Replace(s, `"bindings": {}`, `"bindings": {"database":"do-not-echo"}`, 1), false, Unsupported},
		{"null bindings", strings.Replace(s, `"bindings": {}`, `"bindings": null`, 1), false, Invalid},
		{"missing configuration", strings.Replace(s, `"replicas": 1,`, ``, 1), false, Invalid},
		{"unknown configuration", strings.Replace(s, `"replicas": 1`, `"replicas":1,"memory":"256Mi"`, 1), false, Unsupported},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := []byte(tt.text)
			copyBefore := bytes.Clone(before)
			var err error
			if tt.release {
				_, err = DecodeRelease(before)
			} else {
				_, err = DecodeInstance(before)
			}
			code(t, err, tt.want)
			if strings.Contains(err.Error(), "do-not-echo") {
				t.Fatal("diagnostic dumped input values")
			}
			if !bytes.Equal(before, copyBefore) {
				t.Fatal("decoder mutated bytes")
			}
		})
	}
}

// TestRefusalPrecedence: a document that breaks several rules always gets the same refusal, the
// first in section 2's order (missing, unsupported, null, type, then the field table).
func TestRefusalPrecedence(t *testing.T) {
	r := string(fixture(t, "r42.json"))
	for _, tt := range []struct {
		name, text string
		want       Code
		field      string
	}{
		{"unsupported before null", strings.Replace(r, `"port": 8080`, `"port": null, "zzz": 1, "aaa": 1`, 1), Unsupported, "runtime"},
		{"outer object first", strings.Replace(strings.Replace(r, `"port": 8080`, `"port": null`, 1),
			`"schemaVersion": 1`, `"extra": 1, "schemaVersion": 1`, 1), Unsupported, "release"},
		{"missing before unsupported", strings.Replace(r, `"application": "demo-orders"`, `"extra": 1`, 1), Invalid, "release.application"},
		{"nulls in key order", strings.Replace(strings.Replace(r, `"uid": 65532`, `"uid": null`, 1), `"port": 8080`, `"port": null`, 1), Invalid, "runtime.port"},
		{"null before type", strings.Replace(strings.Replace(r, `"port": 8080`, `"port": "8080"`, 1), `"uid": 65532`, `"uid": null`, 1), Invalid, "runtime.uid"},
		{"field table order", strings.Replace(strings.Replace(r, `"linux/amd64"`, `"linux/arm64"`, 1), "ghcr.io", "GHCR.io", 1), Invalid, "artifact.image"},
		{"kind before port", strings.Replace(strings.Replace(r, `"web"`, `"job"`, 1), `"port": 8080`, `"port": 0`, 1), Unsupported, "runtime.kind"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for range 50 { // map order varies from run to run; the refusal must not
				_, err := DecodeRelease([]byte(tt.text))
				refusal(t, err, tt.want, tt.field)
			}
		})
	}
}

// TestSelectionCheckOrder: section 2's order is contractual, so each case breaks two checks.
func TestSelectionCheckOrder(t *testing.T) {
	base, current := release(t), instance(t, "prod.json")
	revision, _ := current.Revision()
	stale := InstanceRevision("inst-sha256:" + strings.Repeat("0", 64))
	other := base
	other.Application, other.ReleaseID = "other", ""
	job := base
	job.Runtime.Kind, job.ReleaseID = "job", ""
	forged := base
	forged.Application = "other" // keeps the old identity assertion
	broken := current
	broken.Configuration.MemoryMiB = 0
	for _, tt := range []struct {
		name     string
		current  InstanceSpec
		release  ReleaseBundle
		expected InstanceRevision
		want     Code
	}{
		{"wrong application before stale", current, other, stale, WrongApplication},
		{"expected syntax before wrong application", current, other, "", Invalid},
		{"unsupported candidate before stale", current, job, stale, Unsupported},
		{"identity before wrong application", current, forged, revision, IdentityMismatch},
		{"invalid current before wrong application", broken, other, revision, Invalid},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := SelectRelease(tt.current, tt.release, tt.expected)
			code(t, err, tt.want)
		})
	}
}

func TestDomainValidation(t *testing.T) {
	for _, tt := range []struct {
		change func(*ReleaseBundle)
		want   Code
	}{
		{func(r *ReleaseBundle) { r.SchemaVersion = 2 }, Unsupported},
		{func(r *ReleaseBundle) { r.Runtime.Port = 0 }, Invalid},
		{func(r *ReleaseBundle) { r.Runtime.Port = 65536 }, Invalid},
		{func(r *ReleaseBundle) { r.Runtime.Kind = "job" }, Unsupported},
		{func(r *ReleaseBundle) { r.Runtime.Kind, r.Runtime.Port, r.Runtime.HealthPath = "worker", 8080, "" }, Invalid},
		{func(r *ReleaseBundle) { r.Runtime.Kind, r.Runtime.Port, r.Runtime.HealthPath = "worker", 0, "/" }, Invalid},
		{func(r *ReleaseBundle) {
			r.Runtime.Kind, r.Runtime.Port, r.Runtime.HealthPath, r.Runtime.UID = "worker", 0, "", 0
		}, Invalid},
		{func(r *ReleaseBundle) { r.Runtime.UID = 0 }, Invalid},
		{func(r *ReleaseBundle) { r.Runtime.HealthPath = "https://example.com/" }, Invalid},
		{func(r *ReleaseBundle) { r.Runtime.HealthPath = "//example.com/" }, Invalid},
		{func(r *ReleaseBundle) { r.Runtime.HealthPath = "/health?secret=x" }, Invalid},
		{func(r *ReleaseBundle) { r.Runtime.HealthPath = "/health#fragment" }, Invalid},
		{func(r *ReleaseBundle) { r.Artifact.Image += ":latest" }, Invalid},
		{func(r *ReleaseBundle) { r.Artifact.Image = "https://registry.example/app" }, Invalid},
		{func(r *ReleaseBundle) { r.Artifact.Digest = "sha512:" + strings.Repeat("a", 64) }, Unsupported},
		{func(r *ReleaseBundle) { r.Artifact.Digest = "sha256:" + strings.Repeat("A", 64) }, Invalid},
		{func(r *ReleaseBundle) { r.Artifact.MediaType = "application/vnd.oci.image.index.v1+json" }, Unsupported},
		{func(r *ReleaseBundle) { r.Artifact.MediaType = "application/vnd.oci.image.config.v1+json" }, Unsupported},
		{func(r *ReleaseBundle) { r.Artifact.Platform = "linux/arm64" }, Unsupported},
		{func(r *ReleaseBundle) { r.Source.Commit = "abcdef0" }, Invalid},
		{func(r *ReleaseBundle) { r.Description = strings.Repeat("x", 201) }, Invalid},
		{func(r *ReleaseBundle) { r.Description = "two\nlines" }, Invalid},
		{func(r *ReleaseBundle) { r.APIPath = "openapi.json" }, Invalid},
		{func(r *ReleaseBundle) {
			r.Runtime.Kind, r.Runtime.Port, r.Runtime.HealthPath, r.APIPath = "worker", 0, "", "/openapi.json"
		}, Invalid},
		{func(r *ReleaseBundle) { r.Documents = []string{"images"} }, Unsupported},
		{func(r *ReleaseBundle) { r.Documents = []string{"api", "api"} }, Invalid},
		{func(r *ReleaseBundle) { r.Documents = nil }, Invalid},
		{func(r *ReleaseBundle) { r.Publishes = []string{"created"} }, Invalid},
	} {
		r := release(t)
		r.ReleaseID = ""
		tt.change(&r)
		_, err := r.ID()
		code(t, err, tt.want)
	}
	for _, change := range []func(*InstanceSpec){
		func(s *InstanceSpec) { s.Configuration.Replicas = -1 },
		func(s *InstanceSpec) { s.Configuration.MemoryMiB = 0 },
		func(s *InstanceSpec) { s.Release = ReleaseID("sha256:" + strings.Repeat("a", 64)) },
		func(s *InstanceSpec) { s.Environment = "preview" },
	} {
		s := instance(t, "prod.json")
		change(&s)
		if _, err := s.Revision(); err == nil {
			t.Fatal("invalid instance accepted")
		}
	}
	// A worker is the same record without an HTTP surface, and its identity says so: the kind and the two
	// facts a worker does not have are part of it.
	worker := release(t)
	worker.ReleaseID = ""
	worker.Runtime.Kind, worker.Runtime.Port, worker.Runtime.HealthPath = "worker", 0, ""
	worker.APIPath, worker.Documents = "", nil
	workerID, err := worker.ID()
	if err != nil {
		t.Fatal(err)
	}
	if webID, _ := release(t).ID(); workerID == webID {
		t.Fatal("a worker and a web release of the same image share an identity")
	}
	if encoded, err := EncodeRelease(worker); err != nil || !bytes.Contains(encoded, []byte(`"kind": "worker"`)) ||
		!bytes.Contains(encoded, []byte(`"healthPath": ""`)) {
		t.Fatalf("a worker's record: %v %s", err, encoded)
	}
	// Restoring identical content restores the revision; this is not an edit-history counter.
	s := instance(t, "prod.json")
	original := s
	rev, _ := s.Revision()
	s.Configuration.Replicas++
	s = original
	if again, _ := s.Revision(); rev != again {
		t.Fatal("revision depends on history")
	}
}

// A team grant is a declaration beside the app names it consents to: the record states the rule, and the
// resolver matches it against each caller's own app record, so membership is one fact in one place. Only the
// side that can have one may name a team, because a call needs one instance's address.
func TestTeamGrants(t *testing.T) {
	granted := instance(t, "prod.json")
	granted.Allows = []string{"team:demo", "alpha"}
	encoded, err := EncodeInstance(granted)
	if err != nil {
		t.Fatalf("an instance that allows a team: %v", err)
	}
	if !bytes.Contains(encoded, []byte(`"team:demo"`)) {
		t.Fatalf("the team grant is not in the record:\n%s", encoded)
	}
	decoded, err := DecodeInstance(encoded)
	if err != nil {
		t.Fatalf("the written record: %v", err)
	}
	if !slices.Equal(decoded.Allows, []string{"alpha", "team:demo"}) {
		t.Fatalf("the grants round-trip as %v", decoded.Allows)
	}
	withTeam, err := granted.Revision()
	if err != nil {
		t.Fatal(err)
	}
	if plain, _ := instance(t, "prod.json").Revision(); plain == withTeam {
		t.Fatal("a team grant does not participate in the instance's identity")
	}
	for _, tt := range []struct {
		field  string
		calls  []string
		allows []string
	}{
		{"calls", []string{"team:demo"}, nil},
		{"allows", nil, []string{"team:"}},
		{"allows", nil, []string{"team:Demo"}},
		{"allows", nil, []string{"team:demo", "team:demo"}},
	} {
		s := instance(t, "prod.json")
		s.Calls, s.Allows = tt.calls, tt.allows
		_, err := s.Revision()
		refusal(t, err, Invalid, tt.field)
	}
}

func TestRecordsAreValues(t *testing.T) {
	// A record is made of values — strings, int32s and nested structs of them — so copying one cannot alias
	// another. The exceptions are lists: `calls`, `allows`, the events a release publishes and the subscriptions
	// an instance receives are slices because the resolver and the renderer read them as such, and each is
	// canonicalised by sorting. A record carrying one is therefore not comparable, and every seam that returns a
	// record clones those lists (TestSelectionDoesNotAliasDeclarations).
	declarations := map[string]bool{"Calls": true, "Allows": true, "Publishes": true, "Subscriptions": true, "Documents": true}
	var check func(typ reflect.Type, owner string)
	check = func(typ reflect.Type, owner string) {
		switch typ.Kind() {
		case reflect.Struct:
			for i := 0; i < typ.NumField(); i++ {
				check(typ.Field(i).Type, typ.Field(i).Name)
			}
		case reflect.String, reflect.Int32, reflect.Bool:
		case reflect.Slice:
			if typ.Elem().Kind() != reflect.String || !declarations[owner] {
				t.Fatalf("reference-bearing or unexpected domain field: %s (in %s)", typ, owner)
			}
		default:
			t.Fatalf("reference-bearing or unexpected domain field: %s", typ)
		}
	}
	check(reflect.TypeFor[ReleaseBundle](), "")
	check(reflect.TypeFor[InstanceSpec](), "")
}

// The declaration lists are a record's only reference-bearing members, so a record that comes back from
// selection owns its lists, and encoding sorts a copy rather than the record it was handed.
func TestSelectionDoesNotAliasDeclarations(t *testing.T) {
	current := instance(t, "prod.json")
	current.Calls, current.Allows = []string{"beta", "alpha"}, []string{"gamma"}
	revision, err := current.Revision()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := SelectRelease(current, release(t), revision)
	if err != nil {
		t.Fatal(err)
	}
	selected.Calls[0], selected.Allows[0] = "changed", "changed"
	if current.Calls[0] != "beta" || current.Allows[0] != "gamma" {
		t.Fatalf("the result aliases its input: %v %v", current.Calls, current.Allows)
	}
	encoded, err := EncodeInstance(current)
	if err != nil {
		t.Fatal(err)
	}
	if current.Calls[0] != "beta" {
		t.Fatal("encoding reordered the record it was given")
	}
	alpha, beta := bytes.Index(encoded, []byte(`"alpha"`)), bytes.Index(encoded, []byte(`"beta"`))
	if alpha < 0 || beta < 0 || alpha > beta {
		t.Fatalf("the record's calls are not sorted when written:\n%s", encoded)
	}
}

// The three facts a release states about its own documents are identity and round-trip: a fact another app reads
// is worth exactly as much as the record that states it.
func TestOfferedFactsRoundTripAndMoveTheIdentity(t *testing.T) {
	base := release(t)
	base.Description, base.APIPath, base.Documents, base.ReleaseID = "", "", nil, ""
	plain, err := base.ID()
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		change func(*ReleaseBundle)
	}{
		{"description", func(r *ReleaseBundle) { r.Description = "Demo orders service" }},
		{"API path and document", func(r *ReleaseBundle) {
			r.APIPath, r.Documents = "/openapi.json", []string{"api"}
		}},
		{"events document", func(r *ReleaseBundle) { r.Documents = []string{"events"} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := base
			tt.change(&r)
			changed, err := r.ID()
			if err != nil {
				t.Fatal(err)
			}
			if changed == plain {
				t.Fatal("the fact does not take part in the identity")
			}
			r.ReleaseID = changed
			encoded, err := EncodeRelease(r)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeRelease(encoded)
			if err != nil || !reflect.DeepEqual(decoded, r) {
				t.Fatalf("the fact does not survive encoding: %v", err)
			}
		})
	}
	// Absent is absent: a record that states none encodes none of them.
	if encoded, err := EncodeRelease(base); err != nil || bytes.Contains(encoded, []byte("description")) ||
		bytes.Contains(encoded, []byte("apiPath")) || bytes.Contains(encoded, []byte("offers")) {
		t.Fatalf("an empty fact is rendered: %v %s", err, encoded)
	}
}
