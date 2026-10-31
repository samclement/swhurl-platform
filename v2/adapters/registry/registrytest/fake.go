// Package registrytest is the fake for registry.Reader and the contract suite both implementations run.
package registrytest

import (
	"context"
	"maps"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/registry"
)

// Scenario is what the contract runs against both implementations: a repository's tags, what each reference —
// a tag, or an image digest — resolves to, and a reference the repository does not hold.
type Scenario struct {
	Tags    []string
	Images  map[string]registry.Image
	Missing string
}

// Fake answers a scripted repository and records what it was asked. It copies the labels it returns, as the
// real adapter does.
type Fake struct {
	Scenario
	TagCalls  int
	Described []string
	Fail      error // returned instead of an answer, after the call is recorded
}

var _ registry.Reader = (*Fake)(nil)

// New returns a fake that answers from the scenario.
func New(scenario Scenario) *Fake { return &Fake{Scenario: scenario} }

// Tags answers the scenario's tags, or a configured failure.
func (f *Fake) Tags(context.Context) ([]string, error) {
	f.TagCalls++
	if f.Fail != nil {
		return nil, f.Fail
	}
	return append([]string{}, f.Scenario.Tags...), nil
}

// Describe answers the scenario's image for a reference, and refuses one it does not name. A digest is a
// reference like a tag: the map is keyed by whatever the caller asks about.
func (f *Fake) Describe(_ context.Context, reference string) (registry.Image, error) {
	f.Described = append(f.Described, reference)
	if f.Fail != nil {
		return registry.Image{}, f.Fail
	}
	image, found := f.Images[reference]
	if !found {
		return registry.Image{}, &registry.Error{Code: registry.Unavailable, Subject: "registry", Reason: "not_found"}
	}
	image.Labels = maps.Clone(image.Labels)
	return image, nil
}

// Contract is the behaviour every registry.Reader must have: the repository's tags, the image a reference
// names, and a reference the repository does not hold as a failed read rather than an empty image.
func Contract(t *testing.T, scenario Scenario, open func(*testing.T, Scenario) registry.Reader) {
	t.Helper()
	reader := open(t, scenario)
	listsTags(t, reader, scenario)
	describesTags(t, reader, scenario)
	missesWhatIsAbsent(t, reader, scenario)
	doesNotAliasLabels(t, reader, scenario)
}

// listsTags: the repository's tags, in the registry's order.
func listsTags(t *testing.T, reader registry.Reader, scenario Scenario) {
	t.Helper()
	tags, err := reader.Tags(context.Background())
	if err != nil {
		t.Fatalf("listing tags: %v", err)
	}
	if len(tags) != len(scenario.Tags) {
		t.Fatalf("tags %v, want %v", tags, scenario.Tags)
	}
	for i, tag := range tags {
		if tag != scenario.Tags[i] {
			t.Fatalf("tag %d is %q, want %q", i, tag, scenario.Tags[i])
		}
	}
}

// describesTags: each reference resolves to the digest, media type and labels it states. The scenario keys one
// reference by its image digest, so a reader that answers only tags fails here.
func describesTags(t *testing.T, reader registry.Reader, scenario Scenario) {
	t.Helper()
	for reference, want := range scenario.Images {
		got, err := reader.Describe(context.Background(), reference)
		if err != nil {
			t.Fatalf("describing %s: %v", reference, err)
		}
		if got.Digest != want.Digest || got.MediaType != want.MediaType {
			t.Fatalf("%s resolved to %+v, want %+v", reference, got, want)
		}
		for key, value := range want.Labels {
			if got.Labels[key] != value {
				t.Fatalf("%s label %s is %q, want %q", reference, key, got.Labels[key], value)
			}
		}
	}
}

// missesWhatIsAbsent: a tag the repository does not hold is a failed read, not an empty image.
func missesWhatIsAbsent(t *testing.T, reader registry.Reader, scenario Scenario) {
	t.Helper()
	if scenario.Missing == "" {
		return
	}
	if _, err := reader.Describe(context.Background(), scenario.Missing); err == nil {
		t.Fatalf("%s answered, but the repository does not hold it", scenario.Missing)
	}
}

// doesNotAliasLabels: a caller that changes what it was given cannot reach back into the reader's answer.
func doesNotAliasLabels(t *testing.T, reader registry.Reader, scenario Scenario) {
	t.Helper()
	tag := scenario.Tags[0]
	if scenario.Images[tag].Labels == nil {
		return
	}
	first, err := reader.Describe(context.Background(), tag)
	if err != nil {
		t.Fatal(err)
	}
	first.Labels["changed"] = "yes"
	again, err := reader.Describe(context.Background(), tag)
	if err != nil || again.Labels["changed"] != "" {
		t.Fatalf("the reader aliased its labels: %v", err)
	}
}
