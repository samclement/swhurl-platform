package github_test

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/github"
)

const filesCommit = "0123456789abcdef0123456789abcdef01234567"

// gitBlob is Git's identity of one file's text.
func gitBlob(body string) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("blob %d\x00%s", len(body), body)))
	return hex.EncodeToString(sum[:])
}

func answer(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}

// listingOf is a complete recursive listing of regular files, which mutate may then break.
func listingOf(files map[string]string, mutate func(map[string]any)) string {
	nodes := []map[string]any{}
	for file, body := range files {
		nodes = append(nodes, map[string]any{"path": file, "mode": "100644", "type": "blob", "sha": gitBlob(body), "size": len(body)})
	}
	listing := map[string]any{"sha": strings.Repeat("a", 40), "truncated": false, "tree": nodes}
	if mutate != nil {
		mutate(listing)
	}
	encoded, _ := json.Marshal(listing)
	return string(encoded)
}

// filesClient answers the listing and each blob of files; blob, when set, answers the blobs instead. blobs counts
// the blob reads.
func filesClient(t *testing.T, files map[string]string, mutate func(map[string]any), blob func(id string) *http.Response) (github.Console, *atomic.Int64) {
	t.Helper()
	listing := listingOf(files, mutate)
	bodies := map[string]string{}
	for _, body := range files {
		bodies[gitBlob(body)] = body
	}
	blobs := &atomic.Int64{}
	client := github.NewConsole(&http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Host != "api.github.com" || r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Errorf("request outside the read boundary: %s %s", r.Method, r.URL.Host)
		}
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		if r.URL.Path == "/repos/owner/repo/git/trees/"+filesCommit && r.URL.RawQuery == "recursive=1" {
			return answer(http.StatusOK, listing), nil
		}
		id, isBlob := strings.CutPrefix(r.URL.Path, "/repos/owner/repo/git/blobs/")
		if !isBlob {
			t.Errorf("unexpected read %s", r.URL.Path)
			return answer(http.StatusNotFound, ""), nil
		}
		blobs.Add(1)
		if blob != nil {
			return blob(id), nil
		}
		return answer(http.StatusOK, bodies[id]), nil
	})})
	return client, blobs
}

func unitFiles() map[string]string {
	return map[string]string{
		"v2-records/web/app.json":                        `{"team":"shop"}`,
		"v2-records/web/staging/instance.json":           `{"replicas":1}`,
		"v2-records/web/staging/release.json":            `{"artifact":{"digest":"sha256:a"}}`,
		"v2-apps/instances/web/staging/helmrelease.json": `{"kind":"HelmRelease"}`,
		"v2/swhurl-v2": "a binary no read asks for",
	}
}

var unitPaths = []string{"v2-records/web/app.json", "v2-records/web/staging/instance.json",
	"v2-records/web/staging/release.json", "v2-apps/instances/web/staging/helmrelease.json"}

// Only the requested regular files are read, by blob; a path the complete listing does not hold is absent.
func TestCommitFilesReadOnlyTheRequestedBlobs(t *testing.T) {
	files := unitFiles()
	delete(files, "v2-apps/instances/web/staging/helmrelease.json")
	client, blobs := filesClient(t, files, nil, nil)
	found, err := client.CommitFiles(context.Background(), "owner/repo", "secret-token", filesCommit, unitPaths)
	if err != nil || len(found) != 3 || blobs.Load() != 3 {
		t.Fatalf("found %d files with %d blob reads: %v", len(found), blobs.Load(), err)
	}
	for file, body := range found {
		if files[file] != string(body) {
			t.Fatalf("%s = %q", file, body)
		}
	}
	if _, held := found["v2-apps/instances/web/staging/helmrelease.json"]; held {
		t.Fatal("a path the tree does not hold was answered")
	}
}

// A link and a submodule are not files: they are absent, as the archive read skips them, and never read.
func TestCommitFilesSkipLinksAndSubmodules(t *testing.T) {
	client, blobs := filesClient(t, nil, func(listing map[string]any) {
		listing["tree"] = []map[string]any{
			{"path": unitPaths[0], "mode": "120000", "type": "blob", "sha": gitBlob("/etc/passwd"), "size": 11},
			{"path": unitPaths[1], "mode": "160000", "type": "commit", "sha": strings.Repeat("c", 40)},
		}
	}, nil)
	found, err := client.CommitFiles(context.Background(), "owner/repo", "secret-token", filesCommit, unitPaths)
	if err != nil || len(found) != 0 || blobs.Load() != 0 {
		t.Fatalf("followed a link: %q, %d reads, %v", found, blobs.Load(), err)
	}
}

// A listing that is not known complete and sound is refused before any blob is read: never absence.
func TestCommitFilesRefuseAnIncompleteListing(t *testing.T) {
	node := func(file string, size int) map[string]any {
		return map[string]any{"path": file, "mode": "100644", "type": "blob", "sha": strings.Repeat("d", 40), "size": size}
	}
	for name, mutate := range map[string]func(map[string]any){
		"truncated":            func(l map[string]any) { l["truncated"] = true },
		"no completeness flag": func(l map[string]any) { delete(l, "truncated") },
		"no tree":              func(l map[string]any) { delete(l, "tree") },
		"no tree id":           func(l map[string]any) { l["sha"] = "main" },
		"traversal":            func(l map[string]any) { l["tree"] = []map[string]any{node("../escape", 1)} },
		"absolute":             func(l map[string]any) { l["tree"] = []map[string]any{node("/escape", 1)} },
		"duplicate":            func(l map[string]any) { l["tree"] = []map[string]any{node(unitPaths[0], 1), node(unitPaths[0], 1)} },
		"bad blob id": func(l map[string]any) {
			l["tree"] = []map[string]any{{"path": unitPaths[0], "type": "blob", "sha": "x"}}
		},
		"oversized": func(l map[string]any) { l["tree"] = []map[string]any{node(unitPaths[0], 1<<20+1)} },
		"no size": func(l map[string]any) {
			l["tree"] = []map[string]any{{"path": unitPaths[0], "mode": "100644", "type": "blob", "sha": strings.Repeat("d", 40)}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			client, blobs := filesClient(t, unitFiles(), mutate, nil)
			found, err := client.CommitFiles(context.Background(), "owner/repo", "secret-token", filesCommit, unitPaths)
			if err == nil || found != nil || blobs.Load() != 0 {
				t.Fatalf("accepted: %q, %d reads, %v", found, blobs.Load(), err)
			}
		})
	}
}

// A blob that fails, or is not the blob the listing named, fails the whole read; the error says nothing of the
// token, the request or GitHub's answer.
func TestCommitFilesNeverAnswerPartially(t *testing.T) {
	for _, c := range []struct {
		name string
		blob func(string) *http.Response
	}{
		{"404", func(string) *http.Response { return answer(http.StatusNotFound, "secret-token not found") }},
		{"403", func(string) *http.Response { return answer(http.StatusForbidden, "rate limited") }},
		{"429", func(string) *http.Response { return answer(http.StatusTooManyRequests, "") }},
		{"500", func(string) *http.Response { return answer(http.StatusInternalServerError, "") }},
		{"corrupt", func(string) *http.Response { return answer(http.StatusOK, `{"team":"shoP"}`) }},
		{"too large", func(string) *http.Response { return answer(http.StatusOK, strings.Repeat("x", 1<<20+1)) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			client, _ := filesClient(t, unitFiles(), nil, c.blob)
			found, err := client.CommitFiles(context.Background(), "owner/repo", "secret-token", filesCommit, unitPaths)
			if err == nil || found != nil {
				t.Fatalf("a failed read answered %q", found)
			}
			if message := err.Error(); strings.Contains(message, "secret-token") || strings.Contains(message, "api.github.com") ||
				strings.Contains(message, "rate limited") {
				t.Fatalf("the error carries more than its reason: %q", message)
			}
		})
	}
	client, blobs := filesClient(t, unitFiles(), nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if found, err := client.CommitFiles(ctx, "owner/repo", "secret-token", filesCommit, unitPaths); err == nil || found != nil || blobs.Load() != 0 {
		t.Fatalf("a cancelled read answered %q, %v", found, err)
	}
}

// Paths holding the same bytes are one read, and no more than four reads are in flight at once.
func TestCommitFilesReadEachBlobOnceFourAtATime(t *testing.T) {
	files := map[string]string{}
	var paths []string
	for i := range 12 {
		file := fmt.Sprintf("v2-records/app-%d/app.json", i)
		files[file], paths = fmt.Sprint(i/2), append(paths, file)
	}
	var active, peak atomic.Int64
	release := make(chan struct{})
	var once sync.Once
	byID := map[string]string{}
	for _, body := range files {
		byID[gitBlob(body)] = body
	}
	client, blobs := filesClient(t, files, nil, func(id string) *http.Response {
		now := active.Add(1)
		for seen := peak.Load(); now > seen && !peak.CompareAndSwap(seen, now); seen = peak.Load() {
		}
		if now == 4 {
			once.Do(func() { close(release) })
		}
		<-release
		active.Add(-1)
		return answer(http.StatusOK, byID[id])
	})
	found, err := client.CommitFiles(context.Background(), "owner/repo", "secret-token", filesCommit, paths)
	if err != nil || !maps.EqualFunc(found, files, func(a []byte, b string) bool { return string(a) == b }) ||
		blobs.Load() != 6 || peak.Load() != 4 {
		t.Fatalf("%d files, %d reads, peak %d: %v", len(found), blobs.Load(), peak.Load(), err)
	}
}

// Only an immutable commit and paths inside the tree are read; anything else is refused before any request.
func TestCommitFilesNameOnlyACommit(t *testing.T) {
	client := github.NewConsole(&http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		t.Errorf("read %s", r.URL.Path)
		return answer(http.StatusOK, "{}"), nil
	})})
	for _, revision := range []string{"main", "HEAD", "abc", "../" + filesCommit[3:], strings.ToUpper(filesCommit)} {
		if _, err := client.CommitFiles(context.Background(), "owner/repo", "secret-token", revision, unitPaths); err == nil {
			t.Fatalf("revision %q accepted", revision)
		}
	}
	for _, file := range []string{"", "/etc/passwd", "../x", "v2-records/../x", "./x", "v2-records/"} {
		if _, err := client.CommitFiles(context.Background(), "owner/repo", "secret-token", filesCommit, []string{file}); err == nil {
			t.Fatalf("path %q accepted", file)
		}
	}
	if _, err := client.CommitFiles(context.Background(), "owner/repo", "", filesCommit, unitPaths); err == nil {
		t.Fatal("a read without a token was made")
	}
}
