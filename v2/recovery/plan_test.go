package recovery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/s3"
	"github.com/samclement/swhurl-platform/v2/adapters/s3/s3test"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/observe"
	"github.com/samclement/swhurl-platform/v2/recovery/rules"
)

// planNow is the moment the fixture bucket's archives are judged at.
var planNow = time.Date(2026, 10, 26, 9, 15, 0, 0, time.UTC)

// planBucket holds one archive an hour old with its metadata beside it, naming the fixture instance.
func planBucket() *s3test.Fake {
	prefix := "app-sqlite/demo-orders-staging/"
	modified := planNow.Add(-time.Hour)
	return s3test.New(s3test.Scenario{
		Query: rules.ArchiveQuery{Bucket: "backups", Prefix: prefix},
		Objects: []rules.Archive{{Key: prefix + "sqlite-20261026T081500Z.db.age", LastModified: modified},
			{Key: prefix + "sqlite-20261026T081500Z.json", LastModified: modified}},
		Key: prefix + "sqlite-20261026T081500Z.json",
		Body: `{"sha256":"` + strings.Repeat("a", 64) + `","plain_sha256":"` + strings.Repeat("b", 64) +
			`","tables":2,"source":"demo-orders-staging/demo-orders"}`,
	})
}

// observed is the live observation of the fixture instance with the given backup state; its archive answer is
// the status read's, which the plan's own read must replace.
func observed(backup string) observe.Report {
	report := observe.Report{Target: restoreTarget(), Verdict: observe.IsRunning}
	report.Backup.State = backup
	report.Backup.Archive = rules.ArchiveReport{State: rules.BackupNotAssessed}
	return report
}

// Prepare observes, refuses an instance with no database before reading a bucket, and plans from the
// instance's own archives; the observation's archive answer is the plan's.
func TestPrepareSequencesTheObservationAndThePlan(t *testing.T) {
	for _, tt := range []struct {
		name     string
		observe  func(context.Context) (observe.Report, error)
		archives error
		want     string // the error, or empty for a plan
		read     bool   // whether the bucket was asked for the namespace's archives
	}{
		{"observed and planned", func(context.Context) (observe.Report, error) {
			return observed(rules.BackupCurrent), nil
		}, nil, "", true},
		{"no database", func(context.Context) (observe.Report, error) {
			return observed(rules.BackupNotApplicable), nil
		}, nil, "unsupported: instance: it keeps no database to restore", false},
		{"observation failed", func(context.Context) (observe.Report, error) {
			return observe.Report{}, errors.New("invalid: records: unreadable")
		}, nil, "invalid: records: unreadable", false},
		{"no store", func(context.Context) (observe.Report, error) {
			return observed(rules.BackupCurrent), nil
		}, errors.New("invalid: profile: backup"), "invalid: profile: backup", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bucket := planBucket()
			var asked []string
			prepared, err := Prepare(context.Background(), PrepareInputs{
				Observe: tt.observe,
				Archives: func(namespace string) (s3.Reader, rules.ArchiveQuery, error) {
					asked = append(asked, namespace)
					return bucket, bucket.Scenario.Query, tt.archives
				},
				MaxAge: 26 * time.Hour, Now: func() time.Time { return planNow },
			})
			if (len(asked) > 0) != tt.read || (tt.read && asked[0] != "demo-orders-staging") {
				t.Fatalf("the archives were asked for %v", asked)
			}
			if tt.want != "" {
				if err == nil || err.Error() != tt.want {
					t.Fatalf("error %v, want %q", err, tt.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !prepared.Plan.Ready() || prepared.Plan.Archive.Newest != bucket.Scenario.Objects[0].Key {
				t.Fatalf("plan %s/%s from %q", prepared.Plan.State, prepared.Plan.Reason, prepared.Plan.Archive.Newest)
			}
			if prepared.Report.Backup.Archive != prepared.Plan.Archive || prepared.Report.Verdict != observe.IsRunning {
				t.Fatalf("the observation's archive is %v, the plan's %v", prepared.Report.Backup.Archive,
					prepared.Plan.Archive)
			}
		})
	}
}

// Confirm admits only a confirmation naming this instance, and only a ready plan.
func TestConfirmAdmitsOnlyThisInstanceAndAReadyPlan(t *testing.T) {
	spec := kernel.InstanceSpec{Application: "demo-orders", Environment: "staging"}
	ready := Prepared{Plan: readyPlan(t)}
	stale := Prepared{Plan: rules.RestorePlan{State: rules.RestoreStale}}
	for _, tt := range []struct {
		name     string
		prepared Prepared
		confirm  string
		want     string
	}{
		{"this instance", ready, "demo-orders/staging", ""},
		{"another instance", ready, "demo-orders/prod", "invalid: confirm: expected demo-orders/staging"},
		{"no confirmation", ready, "", "invalid: confirm: expected demo-orders/staging"},
		{"not ready", stale, "demo-orders/staging", "refused: restore.plan: stale"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := ""
			if err := tt.prepared.Confirm(spec, tt.confirm); err != nil {
				got = err.Error()
			}
			if got != tt.want {
				t.Fatalf("Confirm(%q) = %q, want %q", tt.confirm, got, tt.want)
			}
		})
	}
}
