package main

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/github/githubtest"
	"github.com/samclement/swhurl-platform/v2/orchestration"
	"github.com/samclement/swhurl-platform/v2/pages"
)

// mainCommit is main in the fixture: web/prod as its promotion merged it at mergedCommit.
const mainCommit = "dddddddddddddddddddddddddddddddddddddddd"

// webProdFiles is web/prod as both commits hold it, with a file no render takes beside it.
func webProdFiles() map[string]string {
	return map[string]string{
		"v2-records/web/app.json":                     `{"team":"shop"}`,
		"v2-records/web/prod/instance.json":           `{"replicas":1,"release":"r1"}`,
		"v2-records/web/prod/release.json":            `{"artifact":{"digest":"sha256:x"}}`,
		"v2-apps/instances/web/prod/helmrelease.json": `{"spec":{"values":{"controllers":{"main":{"containers":{"main":{"image":{"digest":"sha256:x"}}}}}}}}`,
		"v2/testdata/profile.json":                    `{"kind":"profile"}`,
	}
}

// tarOf is an archive of files, each under prefix: GitHub's leading folder, or none for a checkout's.
func tarOf(files map[string]string, prefix string) []byte {
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for name, body := range files {
		_ = writer.WriteHeader(&tar.Header{Name: prefix + name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = writer.Write([]byte(body))
	}
	_ = writer.Close()
	return buffer.Bytes()
}

// healthyWebProd is the cluster: the one read these tests inject.
func healthyWebProd(context.Context) (map[string]*pages.AppRow, error) {
	return map[string]*pages.AppRow{"web/prod": {Instance: pages.Instance{App: "web", Env: "prod"},
		State: pages.State{Key: pages.StateHealthy}, Report: &pages.Report{}}}, nil
}

// githubServer is a console reading GitHub through the fixture's fake, wired as newConsoleServer wires its own
// client: main's tarball, and the merge commit's files only by a targeted read.
func githubServer(t *testing.T) (*consoleServer, *githubtest.ConsoleFake) {
	t.Helper()
	server, err := newConsoleServer(consoleConfig{Repository: "owner/repo", Token: "tok", Now: func() time.Time { return activityNow }})
	if err != nil {
		t.Fatal(err)
	}
	fake := activityGitHub()
	fake.Commit, fake.Archive = mainCommit, tarOf(webProdFiles(), "owner-repo-abc/")
	fake.History = map[string]map[string]string{mergedCommit: webProdFiles()}
	server.github, server.appRepos = fake, true
	server.pin(orchestration.ConsoleSourceOf{Console: fake, Repository: "owner/repo", Token: "tok"})
	return server, fake
}

// readsOf is the server's own reads, with only the cluster's injected.
func readsOf(server *consoleServer) orchestration.ChangeReads {
	reads := server.changeReads()
	reads.Rows = healthyWebProd
	return reads
}

// readsNamed is how many of a fake's reads start with prefix.
func readsNamed(fake *githubtest.ConsoleFake, prefix string) int {
	return len(slices.DeleteFunc(slices.Clone(fake.Reads), func(read string) bool { return !strings.HasPrefix(read, prefix) }))
}

// Activity, through the server's own reads, renders the Python console's bytes: main from its tarball, the merge
// commit from one targeted read of web/prod's files, kept, so the next page reads neither again.
func TestActivityReadsAMergeCommitByItsFiles(t *testing.T) {
	server, fake := githubServer(t)
	jobs := activityJobs()
	for page := 1; page <= 2; page++ {
		data := server.readActivity(context.Background(), jobs, readsOf(server))
		data.shell = activityShell("/activity", true, jobs)
		sameAsGolden(t, server, "activity.html", "activity.html", data)
		if readsNamed(fake, "tarball") != 1 || readsNamed(fake, "files "+mergedCommit) != 1 || readsNamed(fake, "files ") != 1 {
			t.Fatalf("page %d read %q", page, fake.Reads)
		}
	}
	if server.bundle.Cached(mergedCommit) {
		t.Fatal("a unit's files were kept as the merge commit's whole tree")
	}
}

// A job's page takes the same reads: its merged change is live from one targeted read of its merge commit.
func TestAJobPageReadsItsMergeCommitByItsFiles(t *testing.T) {
	server, fake := githubServer(t)
	jobs := activityJobs()
	data, found := server.readJob(context.Background(), jobs, 7, readsOf(server))
	if !found {
		t.Fatal("job 7 not found")
	}
	data.shell = activityShell("/jobs/7", false, jobs)
	sameAsGolden(t, server, "job.html", "job-merged.html", data)
	if readsNamed(fake, "files "+mergedCommit) != 1 || readsNamed(fake, "files ") != 1 {
		t.Fatalf("read %q", fake.Reads)
	}
}

// A merge commit GitHub cannot answer leaves the change unknown, keeps nothing, and the next page reads it again.
func TestAMergeCommitGitHubCannotAnswerIsReadAgain(t *testing.T) {
	server, fake := githubServer(t)
	history := fake.History
	fake.History = nil
	jobs := activityJobs()
	if data, _ := server.readJob(context.Background(), jobs, 7, readsOf(server)); data.Track.Status != "unknown" {
		t.Fatalf("an unread merge commit: %+v", data.Track)
	}
	fake.History = history
	data, _ := server.readJob(context.Background(), jobs, 7, readsOf(server))
	if data.Track.Label() != "Live in prod" || readsNamed(fake, "files "+mergedCommit) != 2 {
		t.Fatalf("the retry: %+v, reads %q", data.Track, fake.Reads)
	}
}

// checkoutSnapshots is a checkout of two commits: main at head, and the merge commit before it.
type checkoutSnapshots struct {
	mu       sync.Mutex
	head     string
	archives map[string][]byte
	reads    []string
}

func (c *checkoutSnapshots) Head(string) (string, error) { return c.head, nil }

func (c *checkoutSnapshots) Archive(_, revision string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads = append(c.reads, revision)
	archive, known := c.archives[revision]
	if !known {
		return nil, errors.New("no such commit")
	}
	return archive, nil
}

// Under --dev the merge commit is read from the checkout, as main is: the page is the same, and GitHub is asked for
// pull requests only, never a tree or a file.
func TestADevConsoleReadsMergeCommitsFromTheCheckout(t *testing.T) {
	checkout := &checkoutSnapshots{head: mainCommit, archives: map[string][]byte{
		mainCommit: tarOf(webProdFiles(), ""), mergedCommit: tarOf(webProdFiles(), "")}}
	server, err := newConsoleServer(consoleConfig{Dev: true, Snapshots: checkout, Now: func() time.Time { return activityNow }})
	if err != nil {
		t.Fatal(err)
	}
	fake := activityGitHub()
	server.github, server.hasGitHub, server.token, server.appRepos, server.repository = fake, true, "tok", true, "owner/repo"
	jobs := activityJobs()
	data := server.readActivity(context.Background(), jobs, readsOf(server))
	data.shell = activityShell("/activity", true, jobs)
	sameAsGolden(t, server, "activity.html", "activity.html", data)
	if readsNamed(fake, "tarball") != 0 || readsNamed(fake, "files ") != 0 || readsNamed(fake, "revision") != 0 {
		t.Fatalf("a dev console read GitHub's trees: %q", fake.Reads)
	}
	if !slices.Equal(checkout.reads, []string{mainCommit, mergedCommit}) {
		t.Fatalf("checkout reads %q", checkout.reads)
	}
}
