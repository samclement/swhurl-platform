package kube_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/kube/kubetest"
)

// writing is a server that answers one write at a time and records what it was asked.
type writing struct {
	requests []*http.Request
	bodies   []string
	status   int    // when set, every write has this status
	answer   string // the body a log read gets
}

func (w *writing) RoundTrip(request *http.Request) (*http.Response, error) {
	w.requests = append(w.requests, request)
	if body, err := io.ReadAll(request.Body); err == nil {
		w.bodies = append(w.bodies, string(body))
	}
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	body := ""
	if strings.HasSuffix(request.URL.Path, "/log") {
		body = w.answer
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)),
		Header: http.Header{}, Request: request}, nil
}

// writer is the real adapter over the recording server.
func writer(t *testing.T, server *writing) kube.Writer {
	t.Helper()
	client := &http.Client{Transport: server}
	api, err := kube.New(endpoint, client, "token")
	if err != nil {
		t.Fatal(err)
	}
	return api.(kube.Writer)
}

// The real adapter runs the shared writer contract over a server that answers.
func TestWriteContract(t *testing.T) {
	kubetest.WriteContract(t, "2 tables, integrity ok\n", func(t *testing.T, log string) kube.Writer {
		return writer(t, &writing{answer: log})
	})
}

// The three changes travel to the stated paths: a patch and a delete name one object, and a create posts to
// the collection with its document.
func TestWriterChangesObjects(t *testing.T) {
	server := &writing{}
	w := writer(t, server)
	run, _ := kube.RefOf("batch", "Job", "hello-staging", "restore")
	if err := w.Patch(context.Background(), run, []byte(`{"spec":{"suspend":true}}`)); err != nil {
		t.Fatal(err)
	}
	replicas, _ := kube.RefOf("apps", "Deployment", "hello-staging", "hello")
	if err := w.Patch(context.Background(), replicas, []byte(`{"spec":{"replicas":0}}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.Delete(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	key, err := kube.WritableRefOf("", "Secret", "hello-staging", "hello-restore-age")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Create(context.Background(), key, []byte(`{"apiVersion":"v1","kind":"Secret"}`)); err != nil {
		t.Fatal(err)
	}
	// A Job is creatable too, and only the two table kinds are: a Deployment can be patched and deleted
	// because it can be named, but never created.
	restore, err := kube.WritableRefOf("batch", "Job", "hello-staging", "restore-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Create(context.Background(), restore, []byte(`{"apiVersion":"batch/v1","kind":"Job"}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.Create(context.Background(), replicas, []byte(`{"apiVersion":"apps/v1","kind":"Deployment"}`)); err == nil {
		t.Fatal("a Deployment was created through the writer")
	}
	want := []struct{ method, path string }{
		{http.MethodPatch, "/apis/batch/v1/namespaces/hello-staging/jobs/restore"},
		{http.MethodPatch, "/apis/apps/v1/namespaces/hello-staging/deployments/hello"},
		{http.MethodDelete, "/apis/batch/v1/namespaces/hello-staging/jobs/restore"},
		{http.MethodPost, "/api/v1/namespaces/hello-staging/secrets"},
		{http.MethodPost, "/apis/batch/v1/namespaces/hello-staging/jobs"},
	}
	if len(server.requests) != len(want) {
		t.Fatalf("%d requests", len(server.requests))
	}
	for i, request := range want {
		if server.requests[i].Method != request.method || server.requests[i].URL.Path != request.path {
			t.Errorf("request %d: %s %s", i, server.requests[i].Method, server.requests[i].URL.Path)
		}
		if got := server.requests[i].Header.Get("Authorization"); got != "Bearer token" {
			t.Errorf("request %d authorization %q", i, got)
		}
	}
	// A delete takes the Job's pod with it: the API's own default would orphan it, and a restore would then
	// put the app back over a pod that still holds the claim.
	if got := server.requests[2].URL.Query().Get("propagationPolicy"); got != "Background" {
		t.Fatalf("the delete's propagation is %q", got)
	}
	if server.requests[0].Header.Get("Content-Type") != "application/merge-patch+json" ||
		server.bodies[0] != `{"spec":{"suspend":true}}` {
		t.Fatalf("patch: %q %q", server.requests[0].Header.Get("Content-Type"), server.bodies[0])
	}
}

// A refused change, a missing object on delete, and a pod log: each is its own answer, and a delete of
// something already gone is done.
func TestWriterRefusals(t *testing.T) {
	job, _ := kube.RefOf("batch", "Job", "hello-staging", "restore")
	for _, tt := range []struct {
		status int
		want   string
	}{
		{http.StatusForbidden, "failed: write: forbidden"},
		{http.StatusInternalServerError, "failed: write: status"},
	} {
		server := &writing{status: tt.status}
		if err := writer(t, server).Delete(context.Background(), job); err == nil || err.Error() != tt.want {
			t.Fatalf("status %d: %v", tt.status, err)
		}
	}
	gone := &writing{status: http.StatusNotFound}
	if err := writer(t, gone).Delete(context.Background(), job); err != nil {
		t.Fatalf("an absent object: %v", err)
	}
	pod, _ := kube.RefOf("", "Pod", "hello-staging", "restore-pod")
	logs := &writing{answer: "2 tables, integrity ok\n"}
	body, err := writer(t, logs).Logs(context.Background(), pod, 50)
	if err != nil || body != "2 tables, integrity ok\n" {
		t.Fatalf("logs %q %v", body, err)
	}
	if query := logs.requests[0].URL.Query().Get("tailLines"); query != "50" {
		t.Fatalf("tailLines %q", query)
	}
	// The restore's Job is a kind the write table states, so a create can name it; a Deployment is readable but
	// not creatable.
	if _, err := kube.WritableRefOf("batch", "Job", "hello-staging", "restore-1"); err != nil {
		t.Fatalf("the restore's Job is not writable: %v", err)
	}
	// A kind the write table does not state cannot be created, and a Secret is still not readable.
	if _, err := kube.WritableRefOf("apps", "Deployment", "hello-staging", "hello"); err == nil {
		t.Fatal("a Deployment was made writable")
	}
	secret, err := kube.WritableRefOf("", "Secret", "hello-staging", "s")
	if err != nil {
		t.Fatal(err)
	}
	if api, err := kube.New(endpoint, http.DefaultClient, ""); err != nil {
		t.Fatal(err)
	} else if _, err := api.Read(context.Background(), secret); err == nil {
		t.Fatal("a Secret was read through the reader")
	}
}
