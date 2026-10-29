package registrytest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/registry"
	"github.com/samclement/swhurl-platform/v2/adapters/registry/registrytest"
)

// errRefused stands in for a failure the reader would report instead of an answer.
var errRefused = errors.New("refused")

func scenario() registrytest.Scenario {
	return registrytest.Scenario{
		Tags: []string{"42-cccccccccccccccccccccccccccccccccccccccc"},
		Images: map[string]registry.Image{
			"42-cccccccccccccccccccccccccccccccccccccccc": {Digest: "sha256:2222222222222222222222222222222222222222222222222222222222222222",
				MediaType: "application/vnd.oci.image.manifest.v1+json", Labels: map[string]string{"platform.swhurl.com/declaration": "{}"}},
		},
		Missing: "43-dddddddddddddddddddddddddddddddddddddddd",
	}
}

func TestContract(t *testing.T) {
	registrytest.Contract(t, scenario(), func(t *testing.T, answers registrytest.Scenario) registry.Reader {
		return registrytest.New(answers)
	})
}

// The fake records what it was asked, and a configured failure is returned without an answer.
func TestFakeRecordsAndCanFail(t *testing.T) {
	fake := registrytest.New(scenario())
	if _, err := fake.Tags(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fake.TagCalls != 1 {
		t.Fatalf("%d tag calls recorded", fake.TagCalls)
	}
	fake.Fail = errRefused
	if _, err := fake.Tags(context.Background()); err != errRefused {
		t.Fatalf("a configured failure was not returned: %v", err)
	}
	if _, err := fake.Describe(context.Background(), scenario().Tags[0]); err != errRefused {
		t.Fatalf("a configured failure was not returned: %v", err)
	}
}
