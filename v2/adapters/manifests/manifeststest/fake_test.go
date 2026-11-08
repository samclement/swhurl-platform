package manifeststest_test

import (
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/manifests"
	"github.com/samclement/swhurl-platform/v2/adapters/manifests/manifeststest"
)

func TestContract(t *testing.T) {
	manifeststest.Contract(t, func(*testing.T) manifests.Decoder { return manifeststest.Known() })
}

// The fake counts its calls, so a use case's test can assert what it decoded.
func TestFakeCountsItsCalls(t *testing.T) {
	f := manifeststest.Known()
	if _, err := f.Documents([]byte("")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Documents([]byte("unknown")); err == nil {
		t.Fatal("an unknown text was answered")
	}
	if f.Calls != 2 {
		t.Fatalf("calls = %d, want 2", f.Calls)
	}
}
