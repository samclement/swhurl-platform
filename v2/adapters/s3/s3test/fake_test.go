package s3test_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/s3"
	"github.com/samclement/swhurl-platform/v2/adapters/s3/s3test"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// errRefused stands in for a failure the adapter would report instead of a listing.
var errRefused = errors.New("refused")

// scenario is a small stand-in for the adapter's recorded answer: the contract is about the three answers,
// not about what one prefix holds.
func scenario() s3test.Scenario {
	modified := time.Date(2026, 10, 26, 8, 21, 21, 0, time.UTC)
	return s3test.Scenario{
		Query:     observe.ArchiveQuery{Bucket: "backups", Prefix: "app-sqlite/hello-staging/"},
		Objects:   []observe.Archive{{Key: "app-sqlite/hello-staging/sqlite-1.db.age", LastModified: modified}},
		Forbidden: observe.ArchiveQuery{Bucket: "backups", Prefix: "app-sqlite/refused/"},
		Empty:     observe.ArchiveQuery{},
	}
}

func TestContract(t *testing.T) {
	s3test.Contract(t, scenario(), func(t *testing.T, answers s3test.Scenario) s3.Reader {
		return s3test.New(answers)
	})
}

// The fake records what it was asked, and a configured failure is returned without a listing.
func TestFakeRecordsTheCallAndCanFail(t *testing.T) {
	fake := s3test.New(scenario())
	if _, err := fake.List(context.Background(), scenario().Query); err != nil {
		t.Fatal(err)
	}
	if len(fake.Lists) != 1 {
		t.Fatalf("%d calls recorded", len(fake.Lists))
	}
	fake.Refuse = errRefused
	if _, err := fake.List(context.Background(), scenario().Query); err != errRefused {
		t.Fatalf("a configured refusal was not returned: %v", err)
	}
}
