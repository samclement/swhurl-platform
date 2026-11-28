package apps

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/files/filestest"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

const removalRevision = "8ae21b491dcbbe7306bc56073460fff24d61a7f2"

// resolution is a call or event edge the removal has to respect, or none.
func resolution(t *testing.T, pairs, subscriptions string) []byte {
	t.Helper()
	return []byte(`{"kind":"v2-calls","version":1,"revision":"` + removalRevision + `","pairs":[` + pairs +
		`],"waiting":[],"refused":[],"subscriptions":[` + subscriptions + `]}`)
}

// registration is a platform kustomization that lists the instance's files.
func registration(path, entry string) Registration {
	return Registration{Path: path, Document: []byte("resources:\n  - infra.yaml\n  - " + entry + "\n")}
}

func removal(t *testing.T, calls []byte) RemovalInputs {
	t.Helper()
	return RemovalInputs{
		Instance: testdata(t, "staging.json"), Release: testdata(t, "r42.json"), Calls: [][]byte{calls},
		Records: "v2-records/demo-orders/staging", Compiled: "v2-apps/instances/demo-orders/staging",
		Unit: "clusters/home/app-demo-orders-staging.json", AppRecord: "v2-records/demo-orders/app.json",
		LastEnvironment: true, AuthFile: "platform/auth/demo-orders-staging.json",
		UnitRegistration: registration("clusters/home/kustomization.yaml", "app-demo-orders-staging.json"),
		AuthRegistration: Registration{Path: "platform/auth/kustomization.yaml",
			Document: []byte(`{"apiVersion":"kustomize.config.k8s.io/v1beta1","kind":"Kustomization",` +
				`"resources":["swhurl-auth-demo-orders-prod.json","swhurl-auth-demo-orders-staging.json"]}`)}}
}

// A removal deletes the instance's own files and the entries that named them, and writes the lists without them first.
func TestRemoveDeletesWhatItNames(t *testing.T) {
	writer := filestest.New()
	writer.Contents["v2-records/demo-orders/staging/instance.json"] = []byte("{}")
	writer.Contents["v2-records/demo-orders/staging/events.sops.yaml"] = []byte("sops:\n")
	writer.Contents["v2-records/demo-orders/app.json"] = []byte("{}")
	writer.Contents["v2-apps/instances/demo-orders/staging/namespace.json"] = []byte("{}")
	writer.Contents["clusters/home/app-demo-orders-staging.json"] = []byte("{}")
	writer.Contents["platform/auth/demo-orders-staging.json"] = []byte("{}")
	result, err := Remove(removal(t, resolution(t, "", "")), t.TempDir(), writer)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"v2-records/demo-orders/staging/instance.json",
		"v2-records/demo-orders/app.json", "v2-apps/instances/demo-orders/staging/namespace.json",
		"clusters/home/app-demo-orders-staging.json", "platform/auth/demo-orders-staging.json"} {
		if _, kept := writer.Contents[path]; kept {
			t.Errorf("%s survived the removal", path)
		}
	}
	unitList, written := writer.Contents["clusters/home/kustomization.yaml"]
	if !written || strings.Contains(string(unitList), "app-demo-orders-staging.json") ||
		!strings.Contains(string(unitList), "infra.yaml") {
		t.Errorf("the unit's registration is %s", unitList)
	}
	authList, written := writer.Contents["platform/auth/kustomization.yaml"]
	if !written || strings.Contains(string(authList), "swhurl-auth-demo-orders-staging.json") ||
		!strings.Contains(string(authList), "swhurl-auth-demo-orders-prod.json") {
		t.Errorf("the auth registration is %s", authList)
	}
	if len(result.Removed) == 0 || len(result.Notes) == 0 {
		t.Errorf("the report is %+v", result)
	}
}

// An instance the resolution still connects is not removed: another instance's files render that edge.
func TestRemoveRefusesAConnectedInstance(t *testing.T) {
	writer := filestest.New()
	pair := `{"caller":"demo-shop","callee":"demo-orders","environment":"staging","port":8080}`
	if _, err := Remove(removal(t, resolution(t, pair, "")), t.TempDir(), writer); err == nil ||
		!strings.Contains(err.Error(), "called_by demo-shop") {
		t.Fatalf("a called instance was removed: %v", err)
	}
	subscription := `{"subscriber":"demo-notifier","publisher":"demo-orders","event":"created","environment":"staging",` +
		`"stream":"staging-demo-orders","consumer":"demo-notifier_created","subject":"staging.demo-orders.created",` +
		`"object":"demo-orders-created"}`
	if _, err := Remove(removal(t, resolution(t, "", subscription)), t.TempDir(), writer); err == nil ||
		!strings.Contains(err.Error(), "subscribed_by demo-notifier") {
		t.Fatalf("a published-to instance was removed: %v", err)
	}
	if writer.Writes != 0 || writer.Removals != 0 {
		t.Error("a refused removal changed something")
	}
}

// The app record outlives the app's first environment: only the last removal takes it.
func TestRemoveKeepsTheAppRecordWhileAnEnvironmentRemains(t *testing.T) {
	writer := filestest.New()
	writer.Contents["v2-records/demo-orders/app.json"] = []byte("{}")
	inputs := removal(t, resolution(t, "", ""))
	inputs.LastEnvironment = false
	if _, err := Remove(inputs, t.TempDir(), writer); err != nil {
		t.Fatal(err)
	}
	if _, kept := writer.Contents["v2-records/demo-orders/app.json"]; !kept {
		t.Error("the app record was removed while production remains")
	}
}

// A registration that lists the instance's file twice is refused: v2 does not know which entry is the instance's.
func TestRemoveRefusesADoubledRegistration(t *testing.T) {
	writer := filestest.New()
	inputs := removal(t, resolution(t, "", ""))
	inputs.UnitRegistration.Document = []byte("resources:\n  - app-demo-orders-staging.json\n  - app-demo-orders-staging.json\n")
	if _, err := Remove(inputs, t.TempDir(), writer); err == nil {
		t.Fatal("a doubled registration was accepted")
	}
	if writer.Removals != 0 {
		t.Error("a refused removal deleted something")
	}
}

// Invalid records must be refused before registrations or instance files change.
func TestRemoveRefusesInvalidRecords(t *testing.T) {
	for _, name := range []string{"instance", "release", "different apps"} {
		t.Run(name, func(t *testing.T) {
			inputs := removal(t, resolution(t, "", ""))
			switch name {
			case "instance":
				inputs.Instance = []byte("{")
			case "release":
				inputs.Release = []byte("{")
			case "different apps":
				inputs.Release = []byte(strings.ReplaceAll(string(inputs.Release), "demo-orders", "demo-other"))
			}
			writer := filestest.New()
			_, err := Remove(inputs, t.TempDir(), writer)
			if err == nil {
				t.Fatal("invalid records were accepted")
			}
			if name == "different apps" {
				var refusal *Refusal
				if !errors.As(err, &refusal) || refusal.Subject != "records" ||
					refusal.Reason != "the_instance_and_its_release_name_different_apps" {
					t.Fatalf("mismatched records returned %v", err)
				}
			}
			if writer.Writes != 0 || writer.Removals != 0 {
				t.Fatal("invalid records changed files")
			}
		})
	}
}

// Registrations must be written successfully before the first file is deleted.
func TestRemoveStopsWhenRegistrationWriteFails(t *testing.T) {
	writer := filestest.New()
	writer.Fail = errors.New("registration write failed")
	result, err := Remove(removal(t, resolution(t, "", "")), t.TempDir(), writer)
	if !errors.Is(err, writer.Fail) {
		t.Fatalf("remove returned %v, want %v", err, writer.Fail)
	}
	if writer.Writes != 1 || writer.Removals != 0 || len(result.Removed) != 0 || len(result.Notes) != 0 {
		t.Fatalf("a failed registration write continued removal: writer=%+v result=%+v", writer, result)
	}
}

// A folder whose records name another instance is refused before anything is written: a removal would check the
// record's dependencies and registrations yet delete the folder's paths, and an edit would write the folder from
// another instance's record.
func TestCheckoutRefusesAFolderHoldingAnotherInstance(t *testing.T) {
	uses := map[string]func(files.Checkout, files.Writer) error{
		"remove": func(checkout files.Checkout, writer files.Writer) error {
			_, err := RemoveIn(checkout, "demo-orders", "staging", writer)
			return err
		},
		"edit": func(checkout files.Checkout, writer files.Writer) error {
			setting, err := ParseSetting("replicas=3")
			if err != nil {
				t.Fatal(err)
			}
			_, err = EditIn(checkout, "demo-orders", "staging", setting, writer)
			return err
		},
		"team": func(checkout files.Checkout, writer files.Writer) error {
			_, err := SetTeamIn(checkout, "demo-orders", "other", writer)
			return err
		},
	}
	// Each folder's two records agree with each other and differ from the path only in the app or the environment.
	staging, release := string(testdata(t, "staging.json")), string(testdata(t, "r42.json"))
	otherRelease := strings.ReplaceAll(release, "demo-orders", "demo-other")
	bundle, err := kernel.DecodeRelease([]byte(otherRelease))
	if err != nil {
		t.Fatal(err)
	}
	otherID, err := bundle.ID()
	if err != nil {
		t.Fatal(err)
	}
	stagingID, err := kernel.DecodeInstance([]byte(staging))
	if err != nil {
		t.Fatal(err)
	}
	held := map[string][2]string{
		"environment": {strings.Replace(staging, `"environment": "staging"`, `"environment": "prod"`, 1), release},
		"app": {strings.ReplaceAll(strings.Replace(staging, string(stagingID.Release), string(otherID), 1),
			"demo-orders", "demo-other"), otherRelease},
	}
	for use, run := range uses {
		for wrong, records := range held {
			t.Run(use+" wrong "+wrong, func(t *testing.T) {
				root := t.TempDir()
				folder := filepath.Join(root, "v2-records", "demo-orders", "staging")
				checkout := files.Checkout{Root: root, Records: filepath.Join(root, "v2-records"),
					Profile: filepath.Join(root, "profile.json"), Calls: filepath.Join(root, "calls.json")}
				for path, data := range map[string][]byte{
					filepath.Join(folder, kernel.InstanceRecordName):                       []byte(records[0]),
					filepath.Join(folder, kernel.ReleaseRecordName):                        []byte(records[1]),
					filepath.Join(root, "v2-records", "demo-orders", kernel.AppRecordName): testdata(t, "app.json"),
					filepath.Join(root, "clusters", "home", "kustomization.yaml"): []byte("resources:\n" +
						"  - app-demo-orders-prod.json\n  - app-demo-orders-staging.json\n  - app-demo-other-staging.json\n"),
					filepath.Join(root, "platform", "auth", "kustomization.yaml"): []byte(`{"resources":[` +
						`"swhurl-auth-demo-orders-prod.json","swhurl-auth-demo-orders-staging.json",` +
						`"swhurl-auth-demo-other-staging.json"]}`),
					checkout.Profile: testdata(t, "profile.json"),
					checkout.Calls:   resolution(t, "", ""),
				} {
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, data, 0o644); err != nil {
						t.Fatal(err)
					}
				}
				writer := filestest.New()
				if err := run(checkout, writer); err == nil || !strings.Contains(err.Error(), "demo-orders/staging holds") {
					t.Fatalf("a folder holding another instance returned %v", err)
				}
				if writer.Writes != 0 || writer.Removals != 0 {
					t.Error("a refused folder changed files")
				}
			})
		}
	}
}
