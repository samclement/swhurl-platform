package github_test

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/github"
	"github.com/samclement/swhurl-platform/v2/adapters/github/githubtest"
)

// consoleServer answers the console's reads: a pinned main, one file, a tarball behind GitHub's redirect to
// codeload, a comparison, open pull requests with their checks, and one merged pull request.
func consoleServer(t *testing.T, fixture githubtest.ConsoleFixture) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/git/ref/heads/main", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"object":{"sha":%q}}`, fixture.Revision)
	})
	mux.HandleFunc("/repos/owner/repo/contents/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/repos/owner/repo/contents/")
		if path != fixture.File || r.URL.Query().Get("ref") != fixture.Revision {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, fixture.Body)
	})
	mux.HandleFunc("/repos/samclement/weather-api/contents/", func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimPrefix(r.URL.Path, "/repos/samclement/weather-api/contents/") != "swhurl.yaml" || r.URL.RawQuery != "" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("an app's declaration was read with a token")
		}
		io.WriteString(w, fixture.AppBody)
	})
	mux.HandleFunc("/repos/samclement/swhurl-app-template-typescript/contents/copier.yml", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ref") != fixture.Revision || r.Header.Get("Authorization") != "" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, fixture.TemplateBody)
	})
	mux.HandleFunc("/repos/owner/repo/git/trees/", func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimPrefix(r.URL.Path, "/repos/owner/repo/git/trees/") != fixture.Revision || r.URL.RawQuery != "recursive=1" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"sha":%q,"truncated":false,"tree":[{"path":"v2-records","mode":"040000","type":"tree","sha":%q},`+
			`{"path":%q,"mode":"100644","type":"blob","sha":%q,"size":%d}]}`,
			strings.Repeat("a", 40), strings.Repeat("b", 40), fixture.File, gitBlob(fixture.Body), len(fixture.Body))
	})
	mux.HandleFunc("/repos/owner/repo/git/blobs/", func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimPrefix(r.URL.Path, "/repos/owner/repo/git/blobs/") != gitBlob(fixture.Body) {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, fixture.Body)
	})
	mux.HandleFunc("/repos/owner/repo/tarball/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://codeload.github.test/owner/repo/tar.gz/"+fixture.Revision, http.StatusFound)
	})
	mux.HandleFunc("/repos/owner/repo/compare/", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"status":"ahead","files":[{"filename":%q},{"filename":%q,"previous_filename":"old.json"}]}`,
			fixture.Changed[0], fixture.Changed[1])
	})
	mux.HandleFunc("/repos/owner/repo/pulls/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/7") {
			fmt.Fprintf(w, `{"number":7,"title":"[console] edit web","html_url":"https://github.com/owner/repo/pull/7",`+
				`"body":"Merge plan","state":"open","created_at":"2026-11-01T09:00:00Z",`+
				`"head":{"ref":"console/edit-web-0123456789ab","sha":%q},"mergeable_state":"clean"}`, fixture.Revision)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/8") {
			fmt.Fprintf(w, `{"number":8,"title":"merged","html_url":"https://github.com/owner/repo/pull/8",`+
				`"state":"closed","created_at":"2026-10-31T09:00:00Z","merged_at":"2026-11-01T10:00:00Z",`+
				`"merge_commit_sha":"fedcba9876543210fedcba9876543210fedcba98",`+
				`"head":{"ref":"console/promote-web-0123456789ab","sha":%q}}`, fixture.Revision)
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/repos/owner/repo/pulls", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `[{"number":9,"title":"by hand","html_url":"https://github.com/owner/repo/pull/9",`+
			`"state":"open","created_at":"2026-11-01T08:00:00Z","head":{"ref":"fix/by-hand","sha":%q}},`+
			`{"number":7,"title":"[console] edit web","html_url":"https://github.com/owner/repo/pull/7",`+
			`"body":"Merge plan","state":"open","created_at":"2026-11-01T09:00:00Z",`+
			`"head":{"ref":"console/edit-web-0123456789ab","sha":%q},"mergeable_state":"clean"}]`,
			fixture.Revision, fixture.Revision)
	})
	mux.HandleFunc("/repos/owner/repo/actions/runs", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"workflow_runs":[{"name":"Validate","head_sha":%q,"status":"completed",`+
			`"conclusion":"success","html_url":"https://github.com/owner/repo/actions/runs/1"}]}`, fixture.Revision)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// consoleTransport sends every request to the scripted server except a codeload redirect, which it serves itself
// after checking that no token travelled with it.
func consoleTransport(t *testing.T, server *httptest.Server) transport {
	return func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "codeload.github.test" {
			if r.Header.Get("Authorization") != "" {
				t.Error("the token followed GitHub's redirect to codeload")
			}
			var archive bytes.Buffer
			writer := tar.NewWriter(&archive)
			_ = writer.WriteHeader(&tar.Header{Name: "owner-repo-abc/v2-records/web/staging/instance.json",
				Mode: 0o644, Size: 3, Typeflag: tar.TypeReg})
			_, _ = writer.Write([]byte("{}\n"))
			_ = writer.Close()
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(&archive),
				Header: http.Header{}}, nil
		}
		target := *r.URL
		target.Scheme, target.Host = "http", strings.TrimPrefix(server.URL, "http://")
		request, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
		if err != nil {
			return nil, err
		}
		request.Header = r.Header
		return http.DefaultTransport.RoundTrip(request)
	}
}

func TestConsoleContract(t *testing.T) {
	githubtest.ConsoleContract(t, func(t *testing.T) (github.Console, githubtest.ConsoleFixture) {
		fixture := githubtest.Scenario()
		server := consoleServer(t, fixture)
		return github.NewConsole(&http.Client{Transport: consoleTransport(t, server)}), fixture
	})
}

// A comparison GitHub truncated names no paths, and one that is not ahead contains nothing.
func TestConsoleComparisonLimits(t *testing.T) {
	client := github.NewConsole(&http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		body := `{"status":"behind","files":[]}`
		if strings.HasSuffix(r.URL.Path, "..."+strings.Repeat("d", 40)) {
			files := make([]string, 0, 300)
			for i := range 300 {
				files = append(files, fmt.Sprintf(`{"filename":"f%d"}`, i))
			}
			body = `{"status":"ahead","files":[` + strings.Join(files, ",") + `]}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})})
	revision := "0123456789abcdef0123456789abcdef01234567"
	behind, full := strings.Repeat("c", 40), strings.Repeat("d", 40)
	if _, contained, err := client.Compare(context.Background(), "owner/repo", "token", revision, behind); err != nil || contained {
		t.Fatalf("a comparison behind its base: contained %v, %v", contained, err)
	}
	paths, contained, err := client.Compare(context.Background(), "owner/repo", "token", revision, full)
	if paths != nil || !contained || err != nil {
		t.Fatalf("a truncated comparison: %v, %v, %v", paths, contained, err)
	}
}
