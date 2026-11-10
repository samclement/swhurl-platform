package main

import (
	"context"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/kube/kubetest"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// jobsFake is a cluster whose console namespace holds the record a console started from a checkout keeps — not the
// pod's `console-jobs` — and no pods: every job it holds was run by a pod that is gone, so the server that starts
// afterwards settles it.
func jobsFake(records map[string]string) *kubetest.Fake {
	return &kubetest.Fake{
		ConfigMapData: map[string][]kube.ConfigMap{"console": {{Name: "console-jobs-dev", Data: records}}},
		Objects:       map[observe.Ref][]byte{},
	}
}

// TestConsoleJobsComeFromItsRecord keeps the record the console's page shows: the registry the server builds reads the
// ConfigMap its pod keeps, and calls a job whose pod is gone interrupted rather than leaving it running for ever.
func TestConsoleJobsComeFromItsRecord(t *testing.T) {
	fake := jobsFake(map[string]string{
		"job-7": `{"id":7,"action":"v2-scale","unit":"demo/staging","identity":"sam@swhurl.com",` +
			`"started":"2026-11-09T10:00:00Z","finished":"2026-11-09T10:00:20Z","state":"succeeded",` +
			`"link":"https://github.com/samclement/swhurl-platform/pull/1","owner":"console-old","resume":null,` +
			`"reached":"","lines":["Opened https://github.com/samclement/swhurl-platform/pull/1"]}`,
		"job-8": `{"id":8,"action":"new app and repository","unit":"new-app/demo","identity":"sam@swhurl.com",` +
			`"started":"2026-11-09T10:01:00Z","finished":null,"state":"running","link":"","owner":"console-gone",` +
			`"resume":{"kind":"new-app","name":"demo"},"reached":"","lines":["Rendering the template"]}`,
	})
	server, err := newConsoleServer(consoleConfig{Dev: true, Data: fake, Reader: fake, Owner: "console-new",
		Now: func() time.Time { return time.Date(2026, 11, 9, 10, 2, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	if server.jobs == nil {
		t.Fatal("a server with a record runs jobs")
	}
	jobs := server.jobsOf(context.Background())
	if len(jobs) != 2 || jobs[0].ID != 8 || jobs[1].ID != 7 {
		t.Fatalf("jobs = %+v, want 8 then 7", jobs)
	}
	if jobs[0].State != "interrupted" || jobs[1].State != "succeeded" {
		t.Fatalf("states = %s, %s; want interrupted then succeeded", jobs[0].State, jobs[1].State)
	}
	if jobs[0].Resume["name"] != "demo" {
		t.Fatalf("the interrupted job's resume record was lost: %v", jobs[0].Resume)
	}
}

// TestConsoleWithoutARecordRunsNoJob keeps --dev honest: with no cluster record the server reads no jobs and has no
// stream to offer, so a page shows what Git holds and nothing else.
func TestConsoleWithoutARecordRunsNoJob(t *testing.T) {
	server, err := newConsoleServer(consoleConfig{Dev: true})
	if err != nil {
		t.Fatal(err)
	}
	if server.jobs != nil || server.live != nil {
		t.Fatal("a server without a record must run no job")
	}
	if jobs := server.jobsOf(context.Background()); jobs != nil {
		t.Fatalf("jobs = %+v, want none", jobs)
	}
}
