package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/files/filestest"
	"github.com/samclement/swhurl-platform/v2/adapters/registry"
	"github.com/samclement/swhurl-platform/v2/adapters/registry/registrytest"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

const (
	newDigest = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	newCommit = "dddddddddddddddddddddddddddddddddddddddd"
)

// fixture reads one document from the kernel's own fixtures.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// declarationLabel is an image's declaration, as the build writes it into its config labels.
func declarationLabel(change func(map[string]any)) string {
	document := map[string]any{
		"schemaVersion": 1,
		"runtime":       map[string]any{"kind": "web", "port": 8080, "healthPath": "/healthz", "uid": 65532},
		"telemetry":     true,
	}
	if change != nil {
		change(document)
	}
	data, err := json.Marshal(document)
	if err != nil {
		panic(err) // a test's own document
	}
	return string(data)
}

// published is the image a registry answers for one tag: what it needs, and the commit its tag abbreviates.
func published(digest, declaration string) registry.Image {
	return registry.Image{Digest: digest, MediaType: kernel.ManifestMediaType,
		Labels: map[string]string{kernel.DeclarationLabel: declaration, kernel.RevisionLabel: newCommit}}
}

// withoutRevision is an image whose build stated no commit.
func withoutRevision(image registry.Image) registry.Image {
	delete(image.Labels, kernel.RevisionLabel)
	return image
}

// misrevisioned is an image whose stated commit is not the one its tag abbreviates.
func misrevisioned(image registry.Image) registry.Image {
	image.Labels[kernel.RevisionLabel] = strings.Repeat("e", 40)
	return image
}

// tag is a tag the platform's contract accepts, with a full commit in its suffix.
func tag(run int, commit string) string { return fmt.Sprintf("%d-%s", run, commit) }

// images answers one newer tag; an empty declaration leaves the image without one.
func images(declaration string) *registrytest.Fake {
	name := tag(43, newCommit)
	image := published(newDigest, declaration)
	if declaration == "" {
		image.Labels = nil
	}
	return registrytest.New(registrytest.Scenario{Tags: []string{tag(42, strings.Repeat("b", 40)), name},
		Images: map[string]registry.Image{name: image}})
}

// stagingInputs is the fixture staging instance and its selected release, the app record and the profile.
func stagingInputs(t *testing.T) DeployInputs {
	t.Helper()
	spec, err := kernel.DecodeInstance(fixture(t, "staging.json"))
	if err != nil {
		t.Fatal(err)
	}
	revision, err := spec.Revision()
	if err != nil {
		t.Fatal(err)
	}
	return DeployInputs{Instance: fixture(t, "staging.json"), Release: fixture(t, "r42.json"),
		App: fixture(t, "app.json"), Profile: fixture(t, "profile.json"), Expected: revision}
}

func TestDeploySelectsAndWrites(t *testing.T) {
	inputs := stagingInputs(t)
	writer := filestest.New()
	result, err := DeployImage(context.Background(), inputs, images(declarationLabel(nil)), t.TempDir(), writer)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Tag != tag(43, newCommit) {
		t.Fatalf("changed %v tag %s", result.Changed, result.Tag)
	}
	if result.Release.Source.Commit != newCommit || result.Release.Artifact.Digest != newDigest ||
		result.Release.Source.Repository != "samclement/demo-orders" {
		t.Fatalf("release %+v", result.Release)
	}
	if !result.Release.Telemetry {
		t.Fatalf("the image's facts: %+v", result.Release)
	}
	if result.Instance.Release != result.Release.ReleaseID {
		t.Fatal("the instance does not select the release")
	}
	// The instance's own facts are not the image's: configuration, grants, calls and allocations stay.
	current, err := kernel.DecodeInstance(inputs.Instance)
	if err != nil {
		t.Fatal(err)
	}
	if result.Instance.Configuration != current.Configuration || result.Instance.Grants != current.Grants ||
		len(result.Instance.Calls) != len(current.Calls) || len(result.Instance.Allows) != len(current.Allows) {
		t.Fatalf("the instance changed beyond its release: %+v", result.Instance)
	}
	for _, path := range []string{
		"v2-records/demo-orders/staging/release.json",
		"v2-records/demo-orders/staging/instance.json",
		"v2-apps/instances/demo-orders/staging/helmrelease.json",
		"v2-apps/instances/demo-orders/staging/image-policy.json",
		"clusters/home/app-demo-orders-staging.json",
	} {
		if _, found := writer.Contents[path]; !found {
			t.Errorf("the delivery did not write %s", path)
		}
	}
	if !strings.Contains(string(writer.Contents["v2-apps/instances/demo-orders/staging/helmrelease.json"]), newDigest) {
		t.Error("the compiled HelmRelease does not name the new digest")
	}

	// The same registry answer again is a no-op: the request is checked, and nothing is written.
	again := inputs
	again.Instance = writer.Contents["v2-records/demo-orders/staging/instance.json"]
	again.Release = writer.Contents["v2-records/demo-orders/staging/release.json"]
	spec, err := kernel.DecodeInstance(again.Instance)
	if err != nil {
		t.Fatal(err)
	}
	if again.Expected, err = spec.Revision(); err != nil {
		t.Fatal(err)
	}
	second := filestest.New()
	unchanged, err := DeployImage(context.Background(), again, images(declarationLabel(nil)), t.TempDir(), second)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Changed || second.Writes != 0 {
		t.Fatalf("a repeat wrote %d times", second.Writes)
	}
}

func TestDeployRefusals(t *testing.T) {
	for _, tt := range []struct {
		name    string
		images  *registrytest.Fake
		change  func(*DeployInputs)
		want    kernel.Code
		field   string
		subject string
	}{
		{"no tag matches", registrytest.New(registrytest.Scenario{Tags: []string{"latest"}}), nil,
			"", "", "image"},
		{"the declaration is absent", images(""), nil, "", "", "declaration"},
		{"an unsupported version", images(declarationLabel(func(d map[string]any) { d["schemaVersion"] = 2 })),
			nil, kernel.Unsupported, "schemaVersion", ""},
		{"a new secret", images(declarationLabel(func(d map[string]any) { d["secrets"] = []any{"TOKEN"} })),
			nil, kernel.Unsupported, "declaration.secrets", ""},
		{"a larger memory floor", images(declarationLabel(func(d map[string]any) { d["memoryMiB"] = 129 })),
			nil, kernel.Unsupported, "declaration.memoryMiB", ""},
		{"another kind", images(declarationLabel(func(d map[string]any) {
			d["runtime"] = map[string]any{"kind": "worker", "uid": 65532}
		})), nil, kernel.Unsupported, "runtime.kind", ""},
		{"an added database", images(declarationLabel(func(d map[string]any) { d["database"] = "sqlite" })),
			nil, kernel.Unsupported, "declaration.database", ""},
		{"an image with no revision", registrytest.New(registrytest.Scenario{
			Tags:   []string{"43-" + strings.Repeat("d", 7)},
			Images: map[string]registry.Image{"43-" + strings.Repeat("d", 7): withoutRevision(published(newDigest, declarationLabel(nil)))},
		}), nil, kernel.Invalid, "", "source"},
		{"a revision the tag does not abbreviate", registrytest.New(registrytest.Scenario{
			Tags:   []string{"43-" + strings.Repeat("d", 7)},
			Images: map[string]registry.Image{"43-" + strings.Repeat("d", 7): misrevisioned(published(newDigest, declarationLabel(nil)))},
		}), nil, kernel.Invalid, "source.revision", ""},
		{"a stale revision", nil, func(i *DeployInputs) {
			i.Expected = kernel.InstanceRevision("inst-sha256:" + strings.Repeat("0", 64))
		}, kernel.StaleInstance, "expected", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			inputs := stagingInputs(t)
			if tt.change != nil {
				tt.change(&inputs)
			}
			answered := tt.images
			if answered == nil {
				answered = images(declarationLabel(nil))
			}
			writer := filestest.New()
			result, err := DeployImage(context.Background(), inputs, answered, t.TempDir(), writer)
			switch {
			case tt.subject != "":
				var refused *Refusal
				if !errors.As(err, &refused) || refused.Subject != tt.subject {
					t.Fatalf("%v, want a refusal about %s", err, tt.subject)
				}
			default:
				var refused *kernel.Error
				if !errors.As(err, &refused) || refused.Code != tt.want || refused.Field != tt.field {
					t.Fatalf("%v, want %s %s", err, tt.want, tt.field)
				}
			}
			if result.Changed || writer.Writes != 0 {
				t.Fatalf("a refusal wrote %d times", writer.Writes)
			}
		})
	}
}

// A registry failure is not a refusal: nothing is selected, and the caller can try again.
func TestDeployRegistryFailure(t *testing.T) {
	answered := images(declarationLabel(nil))
	answered.Fail = errors.New("registry is down")
	writer := filestest.New()
	if _, err := DeployImage(context.Background(), stagingInputs(t), answered, t.TempDir(), writer); err == nil {
		t.Fatal("a failed registry read selected an image")
	}
	if writer.Writes != 0 {
		t.Fatalf("a failed read wrote %d times", writer.Writes)
	}
}

// A filesystem failure during the write is reported rather than counted as success.
func TestDeployWriteFailure(t *testing.T) {
	writer := filestest.New()
	writer.Fail = &files.Error{Code: files.IO, Path: "destination", Reason: "cannot write the file"}
	if _, err := DeployImage(context.Background(), stagingInputs(t), images(declarationLabel(nil)), t.TempDir(), writer); err == nil {
		t.Fatal("a failed write was reported as success")
	}
}

// A tag carries an abbreviation, never the commit: the image states the full one, and the release is checked
// against it.
func TestDeployTakesTheRevisionFromTheImageAndChecksTheTagAbbreviatesIt(t *testing.T) {
	short := fmt.Sprintf("%d-%s", 43, newCommit[:7])
	image := published(newDigest, declarationLabel(nil))
	registryAnswer := registrytest.New(registrytest.Scenario{Tags: []string{short},
		Images: map[string]registry.Image{short: image}})
	writer := filestest.New()
	result, err := DeployImage(context.Background(), stagingInputs(t), registryAnswer, t.TempDir(), writer)
	if err != nil {
		t.Fatalf("a seven-character tag was refused: %v", err)
	}
	if result.Release.Source.Commit != newCommit || result.Tag != short {
		t.Fatalf("release states %s at %s, tag %s", result.Release.Source.Commit, result.Release.Source.Repository, result.Tag)
	}
}

func TestDeployRefusesAnImageWithoutItsRevision(t *testing.T) {
	image := published(newDigest, declarationLabel(nil))
	delete(image.Labels, kernel.RevisionLabel)
	writer := filestest.New()
	_, err := DeployImage(context.Background(), stagingInputs(t), registrytest.New(registrytest.Scenario{
		Tags: []string{tag(43, newCommit)}, Images: map[string]registry.Image{tag(43, newCommit): image}}),
		t.TempDir(), writer)
	if err == nil || !strings.Contains(err.Error(), "revision_absent") {
		t.Fatalf("an image with no revision was not refused: %v", err)
	}
	if writer.Writes != 0 {
		t.Fatalf("a refusal wrote %d times", writer.Writes)
	}
}

func TestDeployRefusesARevisionTheTagDoesNotAbbreviate(t *testing.T) {
	image := published(newDigest, declarationLabel(nil))
	image.Labels[kernel.RevisionLabel] = strings.Repeat("e", 40)
	_, err := DeployImage(context.Background(), stagingInputs(t), registrytest.New(registrytest.Scenario{
		Tags: []string{tag(43, newCommit)}, Images: map[string]registry.Image{tag(43, newCommit): image}}),
		t.TempDir(), filestest.New())
	if err == nil || !strings.Contains(err.Error(), "source.revision") {
		t.Fatalf("a revision the tag does not abbreviate was not refused: %v", err)
	}
}
