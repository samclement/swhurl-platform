package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/orchestration"
)

func TestClickStackBootstrapCLIFlags(t *testing.T) {
	for _, args := range [][]string{
		{"clickstack-bootstrap"}, {"clickstack-bootstrap", "--kubeconfig", ""}, {"clickstack-bootstrap", "--in-cluster"},
		{"clickstack-bootstrap", "--kubeconfig", "x", "--dry-run", "--dry-run"}, {"clickstack-bootstrap", "--kubeconfig", "x", "extra"},
	} {
		if _, outcome := parse(args); outcome != misuse {
			t.Fatalf("accepted %v", args)
		}
	}
	for _, args := range [][]string{{"clickstack-bootstrap", "--kubeconfig", "x"}, {"clickstack-bootstrap", "--kubeconfig", "x", "--dry-run"}} {
		if _, outcome := parse(args); outcome != parsed {
			t.Fatalf("refused %v", args)
		}
	}
	for _, dryRun := range []bool{false, true} {
		args := []string{"clickstack-bootstrap", "--kubeconfig", "x"}
		if dryRun {
			args = append(args, "--dry-run")
		}
		r, _ := parse(args)
		if got := bootstrapOptions(r); got != (orchestration.BootstrapOptions{DryRun: dryRun, Wait: 5 * time.Minute, Interval: 5 * time.Second}) {
			t.Fatalf("%v: options %+v", args, got)
		}
	}
}

// bootstrapAPI answers the inputs Secret by named GET and has no MongoDB Secret, so the bootstrap stops before it
// asks ClickStack anything. Anything but a GET fails the test.
type bootstrapAPI struct {
	t     *testing.T
	paths []string
}

func (a *bootstrapAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.paths = append(a.paths, r.Method+" "+r.URL.Path)
	if r.Method != http.MethodGet {
		a.t.Errorf("an effect: %s %s", r.Method, r.URL.Path)
	}
	if r.URL.Path != "/api/v1/namespaces/observability/secrets/clickstack-runtime-inputs" {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"kind":"Status","code":404,"reason":"NotFound"}`))
		return
	}
	data := map[string]string{}
	for key, value := range map[string]string{"CLICKSTACK_ADMIN_EMAIL": "admin@example.test",
		"CLICKSTACK_ADMIN_PASSWORD": "private-password", "CLICKSTACK_INGESTION_KEY": "private-key"} {
		data[key] = base64.StdEncoding.EncodeToString([]byte(value))
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]string{"name": "clickstack-runtime-inputs", "namespace": "observability"}, "data": data})
}

// Through the real adapters: both Secrets are read by name before anything else, a missing one stops the command
// with [BAD] and exit 1, and nothing printed holds a credential.
func TestClickStackBootstrapReadsItsSecretsFirstAndStopsWithoutOne(t *testing.T) {
	api := &bootstrapAPI{t: t}
	server := httptest.NewTLSServer(api)
	defer server.Close()
	var out, diagnostic bytes.Buffer
	status := runWith([]string{"clickstack-bootstrap", "--kubeconfig", kubeconfig(t, server)}, &out, &diagnostic, cli{readFile: os.ReadFile})
	text := out.String()
	if status != 1 || diagnostic.Len() != 0 || !strings.HasPrefix(text, "\n== ClickStack bootstrap ==\n[BAD] ") ||
		!strings.Contains(text, "clickstack-mongodb-hyperdx-hyperdx") {
		t.Fatalf("%d\nstdout %s\nstderr %s", status, text, diagnostic.String())
	}
	if strings.Contains(text, "private-") {
		t.Fatalf("a credential in the output: %s", text)
	}
	want := []string{"GET /api/v1/namespaces/observability/secrets/clickstack-runtime-inputs",
		"GET /api/v1/namespaces/observability/secrets/clickstack-mongodb-hyperdx-hyperdx"}
	if !slices.Equal(api.paths, want) {
		t.Fatalf("requests:\n%s", strings.Join(api.paths, "\n"))
	}
}
