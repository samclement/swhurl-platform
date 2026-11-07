package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/ntfy/ntfytest"
)

// heartbeatURL is the platform topic's address, which no log line may hold.
const heartbeatURL = "https://ntfy.example.test/private-heartbeat-sentinel"

// heartbeatAPI is an API server that answers only the two named GETs the heartbeat may make: the CronJob, as
// cronjob says (a status code without a document), and the destination Secret. Anything else fails the test.
type heartbeatAPI struct {
	t       *testing.T
	mu      sync.Mutex
	cronjob string // "" answers 404
	status  int    // non-zero answers this status for the CronJob
	paths   []string
}

func (a *heartbeatAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	a.paths = append(a.paths, r.URL.Path)
	a.mu.Unlock()
	if r.Method != http.MethodGet {
		a.t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusForbidden)
		return
	}
	switch r.URL.Path {
	case "/apis/batch/v1/namespaces/console/cronjobs/console-notifications":
		switch {
		case a.status != 0:
			w.WriteHeader(a.status)
		case a.cronjob == "":
			w.WriteHeader(http.StatusNotFound)
		default:
			fmt.Fprint(w, a.cronjob)
		}
	case "/api/v1/namespaces/console/secrets/notification-ntfy":
		fmt.Fprintf(w, `{"apiVersion":"v1","kind":"Secret","metadata":{"namespace":"console","name":"notification-ntfy"},`+
			`"data":{"NTFY_PLATFORM_URL":%q}}`, base64.StdEncoding.EncodeToString([]byte(heartbeatURL)))
	default:
		a.t.Errorf("unexpected read: %s", r.URL.Path)
		w.WriteHeader(http.StatusForbidden)
	}
}

// succeeded sets the CronJob to one that last succeeded at the given time.
func (a *heartbeatAPI) succeeded(at time.Time) {
	a.cronjob = `{"apiVersion":"batch/v1","kind":"CronJob","metadata":{"namespace":"console","name":"console-notifications"},` +
		`"spec":{"suspend":false},"status":{"lastSuccessfulTime":"` + at.UTC().Format(time.RFC3339) + `"}}`
}

// runHeartbeat runs one pass against the API server with a fake ntfy, and checks that every line is the command's
// JSON log line and that none holds the destination.
func runHeartbeat(t *testing.T, api *heartbeatAPI, platform *ntfytest.Fake, args ...string) (int, []string) {
	t.Helper()
	server := httptest.NewTLSServer(api)
	defer server.Close()
	var out, diagnostic bytes.Buffer
	status := runWith(append([]string{"notifications-heartbeat", "--kubeconfig", kubeconfig(t, server)}, args...),
		&out, &diagnostic, cli{readFile: os.ReadFile, platform: platform})
	if diagnostic.Len() != 0 || strings.Contains(out.String(), "private-heartbeat-sentinel") {
		t.Fatalf("stdout %s\nstderr %s", out.String(), diagnostic.String())
	}
	var messages []string
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var record map[string]string
		if json.Unmarshal([]byte(line), &record) != nil || record["logger"] != "notifications-heartbeat" ||
			record["time"] == "" || record["level"] == "" || len(record) != 4 {
			t.Fatalf("not a heartbeat JSON log line: %s", line)
		}
		messages = append(messages, record["level"]+" "+record["message"])
	}
	return status, messages
}

func TestNotificationsHeartbeatCLIFlags(t *testing.T) {
	for _, args := range [][]string{
		{"notifications-heartbeat"}, {"notifications-heartbeat", "--kubeconfig", ""},
		{"notifications-heartbeat", "--in-cluster"}, {"notifications-heartbeat", "--kubeconfig", "x", "--dry-run", "--dry-run"},
		{"notifications-heartbeat", "--kubeconfig", "x", "--state", ""}, {"notifications-heartbeat", "--kubeconfig", "x", "--max-age", ""},
		{"notifications-heartbeat", "--kubeconfig", "x", "--max-age", "0s"}, {"notifications-heartbeat", "--kubeconfig", "x", "extra"},
	} {
		if _, outcome := parse(args); outcome != misuse {
			t.Fatalf("accepted %v", args)
		}
	}
	for _, args := range [][]string{{"notifications-heartbeat", "--kubeconfig", "x"},
		{"notifications-heartbeat", "--dry-run", "--max-age", "1s", "--state", "/tmp/s.json", "--kubeconfig=x"}} {
		if _, outcome := parse(args); outcome != parsed {
			t.Fatalf("refused %v", args)
		}
	}
}

// --max-age is Python's: a positive number followed by s, m or h.
func TestHeartbeatMaxAgeParser(t *testing.T) {
	for value, want := range map[string]float64{"1s": 1, "10m": 600, "2h": 7200, "1.5m": 90, "": 0} {
		if got, ok := heartbeatMaxAge(value); !ok || got != want {
			t.Fatalf("%q: %v %t", value, got, ok)
		}
	}
	for _, value := range []string{"0s", "0.0m", "-1s", "1", "1h30m", " 1s", "1S", "1e3s", ".5s", "1.s", strings.Repeat("9", 400) + "s"} {
		if _, ok := heartbeatMaxAge(value); ok {
			t.Fatalf("accepted %q", value)
		}
	}
	if jobs := heartbeatJobs(90); len(jobs) != 1 || jobs[0].MaxAge != 90 {
		t.Fatalf("%+v", jobs)
	}
	if jobs := heartbeatJobs(0); jobs[0].MaxAge != 600 {
		t.Fatalf("%+v", jobs)
	}
}

// A dry run reads only the CronJob, plans the notice and neither posts nor writes the state file.
func TestNotificationsHeartbeatDryRunDoesNotReadSecretPostOrSave(t *testing.T) {
	api := &heartbeatAPI{t: t}
	api.succeeded(time.Now().Add(-time.Minute))
	platform := ntfytest.New()
	state := filepath.Join(t.TempDir(), "state", "notification-heartbeat.json")
	status, messages := runHeartbeat(t, api, platform, "--dry-run", "--max-age", "1s", "--state", state)
	if status != 0 || len(messages) != 1 || messages[0] != "INFO Would notify: notification checker stale (priority 4)" {
		t.Fatalf("%d %v", status, messages)
	}
	if len(api.paths) != 1 || platform.Attempts != 0 {
		t.Fatalf("reads %v, %d posts", api.paths, platform.Attempts)
	}
	if _, err := os.Stat(filepath.Dir(state)); !os.IsNotExist(err) {
		t.Fatalf("a dry run made the state directory: %v", err)
	}
}

// An alert, the run after it, a recovery and a fresh run, through the default state file under HOME: the state is
// private and written only once ntfy accepts.
func TestNotificationsHeartbeatAlertRecoverInPythonsWords(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	state := filepath.Join(home, ".local", "state", "swhurl-platform", "notification-heartbeat.json")
	api := &heartbeatAPI{t: t}
	api.succeeded(time.Now().Add(-11 * time.Minute))
	platform := ntfytest.New()
	for _, want := range []string{"INFO Notification heartbeat sent: notification checker stale",
		"INFO Heartbeat for notification checker: stale; reminder not due"} {
		if status, messages := runHeartbeat(t, api, platform); status != 0 || len(messages) != 1 || messages[0] != want {
			t.Fatalf("%d %v, want %s", status, messages, want)
		}
	}
	for path, mode := range map[string]os.FileMode{filepath.Dir(state): 0o700, state: 0o600} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != mode {
			t.Fatalf("%s: %v", path, err)
		}
	}
	api.succeeded(time.Now())
	for _, want := range []string{"INFO Notification heartbeat sent: notification checker recovered",
		"INFO Heartbeat for notification checker: fresh"} {
		if status, messages := runHeartbeat(t, api, platform); status != 0 || len(messages) != 1 || messages[0] != want {
			t.Fatalf("%d %v, want %s", status, messages, want)
		}
	}
	if received := platform.Received(heartbeatURL); len(received) != 2 {
		t.Fatalf("%d posts", len(received))
	}
	if saved, _ := os.ReadFile(state); string(saved) != `{"alerting_since":null,"last_alert":null}`+"\n" {
		t.Fatalf("saved %s", saved)
	}
}

// A CronJob read that fails is exit 1 with nothing sent or saved; a missing one alerts; an invalid state file stops
// before the cluster is asked anything; a refused send saves nothing. Each leaves the state file as it was.
func TestNotificationsHeartbeatFailures(t *testing.T) {
	original := `{"alerting_since":12,"last_alert":13}` + "\n"
	for _, tt := range []struct {
		name, state string
		status      int
		refuse      bool
		want        string
		reads       int
	}{
		{"forbidden", original, 403, false, "ERROR cannot read notification checker", 1},
		{"invalid state", `{"alerting_since":12}`, 0, false, "ERROR heartbeat state is invalid; leaving it unchanged", 0},
		{"refused send", "", 0, true, "ERROR ntfy delivery failed; notification remains pending: failed: publish: status_403", 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			api := &heartbeatAPI{t: t, status: tt.status}
			platform := ntfytest.New()
			platform.Refusing[heartbeatURL] = tt.refuse
			path := filepath.Join(t.TempDir(), "notification-heartbeat.json")
			if tt.state != "" {
				if err := os.WriteFile(path, []byte(tt.state), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			status, messages := runHeartbeat(t, api, platform, "--state", path)
			if status != 1 || len(messages) != 1 || messages[0] != tt.want || len(api.paths) != tt.reads || len(platform.Sent) != 0 {
				t.Fatalf("%d %v, reads %v", status, messages, api.paths)
			}
			if saved, _ := os.ReadFile(path); string(saved) != tt.state {
				t.Fatalf("state changed to %q", saved)
			}
		})
	}
	// A missing CronJob is stale, and the alert is sent.
	api, platform := &heartbeatAPI{t: t}, ntfytest.New()
	status, messages := runHeartbeat(t, api, platform, "--state", filepath.Join(t.TempDir(), "s.json"))
	if status != 0 || messages[0] != "INFO Notification heartbeat sent: notification checker stale" ||
		!strings.Contains(string(platform.Sent[0].Document), "CronJob is missing") {
		t.Fatalf("%d %v", status, messages)
	}
}

// Without --state the file is under HOME; without HOME either, nothing is read and the pass fails.
func TestNotificationsHeartbeatWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	api := &heartbeatAPI{t: t}
	status, messages := runHeartbeat(t, api, ntfytest.New())
	if status != 1 || len(messages) != 1 || messages[0] != "ERROR heartbeat state path is unknown: HOME is not set" || len(api.paths) != 0 {
		t.Fatalf("%d %v %v", status, messages, api.paths)
	}
}
