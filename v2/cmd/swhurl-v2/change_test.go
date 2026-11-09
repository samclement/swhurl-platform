package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/github"
	"github.com/samclement/swhurl-platform/v2/adapters/github/githubtest"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/pages"
)

const changeRevision = "0123456789abcdef0123456789abcdef01234567"

// changeTreeFiles is the repository a change downloads in a test: one instance's records, the resolution and the
// allocation it is compiled against, the registry its team is read from, and the profile.
func changeTreeFiles(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"v2-records/demo-orders/staging/instance.json": string(fixture(t, "staging.json")),
		"v2-records/demo-orders/staging/release.json":  string(fixture(t, "r42.json")),
		"v2-records/demo-orders/app.json":              string(fixture(t, "app.json")),
		"v2-records/teams.json":                        `{"schemaVersion":1,"teams":{"demo":{"description":"demo"}}}`,
		"v2-records/calls.json":                        `{"kind":"v2-calls","version":1,"revision":"` + changeRevision + `","pairs":[]}`,
		"platform/egress/allocation.json":              `{"kind":"v2-egress","version":1,"revision":"` + changeRevision + `","instances":[]}`,
		"v2/testdata/profile.json":                     string(fixture(t, "profile.json")),
	}
}

// changeTarball is a repository as GitHub serves it: a tar archive, gzipped, inside one leading folder.
func changeTarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var archive bytes.Buffer
	gzipped := gzip.NewWriter(&archive)
	written := tar.NewWriter(gzipped)
	for path, body := range files {
		header := &tar.Header{Name: "owner-repo-" + changeRevision[:7] + "/" + path, Mode: 0o755,
			Size: int64(len(body)), Typeflag: tar.TypeReg}
		if err := written.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := written.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := written.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipped.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

// changeSources is the console's read of the repository, and a publisher that records what the change wrote.
func changeSources(t *testing.T) (*githubtest.ConsoleFake, *githubtest.PublisherFake) {
	t.Helper()
	source := &githubtest.ConsoleFake{Commit: changeRevision, Archive: changeTarball(t, changeTreeFiles(t))}
	return source, githubtest.NewPublisher(githubtest.PublisherScenario())
}

// changeRun runs one change against a repository from the fake, as the console runs it: the credential and the
// repository from the environment, and the real disk as the working copy's writer.
func changeRun(t *testing.T, source *githubtest.ConsoleFake, publisher *githubtest.PublisherFake,
	args ...string) (int, string, string) {
	t.Helper()
	var stdout, diagnostic bytes.Buffer
	c := cli{readFile: os.ReadFile, writer: files.Disk{}, consoleSource: source, publisher: publisher,
		stdin: strings.NewReader(""),
		getenv: func(name string) string {
			switch name {
			case consoleTokenEnv:
				return "fixture-token"
			case consoleRepositoryEnv:
				return "owner/repo"
			}
			return ""
		}}
	status := runWith(args, &stdout, &diagnostic, c)
	return status, stdout.String(), diagnostic.String()
}

// A scale is one edit and a render, and the pull request carries exactly what v2 derived from them.
func TestChangeCLIScale(t *testing.T) {
	source, publisher := changeSources(t)
	status, stdout, diagnostic := changeRun(t, source, publisher, "change", "--kind", "v2-scale",
		"--app", "demo-orders", "--env", "staging", "--set", "replicas=3", "--requested-by", "operator@example.test")
	if status != 0 {
		t.Fatalf("change exited %d: %s%s", status, diagnostic, stdout)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	want := []string{
		"Downloading owner/repo main (0123456)",
		"M v2-records/demo-orders/staging/instance.json",
		"Merge plan: you merge it on GitHub",
		"Committed 3333333 [console] apps: scale demo-orders/staging",
		"Creating branch console/v2-scale-demo-orders-staging-0123456",
		"Opened " + githubtest.PublisherScenario().PullURL,
	}
	if len(lines) < len(want) {
		t.Fatalf("the change printed %d lines:\n%s", len(lines), stdout)
	}
	// the download first and the pull request last, with the lines that say what it is between them
	if lines[0] != want[0] || lines[len(lines)-1] != want[len(want)-1] {
		t.Errorf("the change began %q and ended %q", lines[0], lines[len(lines)-1])
	}
	for _, line := range want[2:] {
		if !strings.Contains(stdout, line+"\n") {
			t.Errorf("the change did not print %q", line)
		}
	}
	if len(publisher.Commits) != 1 || len(publisher.Pulls) != 1 {
		t.Fatalf("the change wrote %d commits and %d pull requests", len(publisher.Commits), len(publisher.Pulls))
	}
	commit := publisher.Commits[0]
	if commit.Parent != changeRevision {
		t.Errorf("the commit's parent is %q", commit.Parent)
	}
	body := "Generated by the console with:\n\n    " + strings.Join([]string{
		"swhurl-v2 edit --records v2-records --app demo-orders --env=staging --set replicas=3 --profile " +
			"v2/testdata/profile.json --out . --calls v2-records/calls.json --egress platform/egress/allocation.json",
		"swhurl-v2 render --records v2-records --profile v2/testdata/profile.json --out . --revision @base"},
		" && ")
	if wantMessage := "[console] apps: scale demo-orders/staging\n\n" + body +
		"\n\nRequested-by: operator@example.test\n"; commit.Message != wantMessage {
		t.Errorf("the commit message is:\n%s\nwant:\n%s", commit.Message, wantMessage)
	}
	if commit.Author != (github.Signature{Name: "swhurl console", Email: "console@users.noreply.github.com"}) {
		t.Errorf("the author is %#v", commit.Author)
	}
	// every path the tree states is one the change owns, and an unchanged input is not carried
	change, _ := pages.ConsoleChangeNamed("v2-scale")
	paths := make([]string, 0, len(publisher.Trees[0].Entries))
	for _, entry := range publisher.Trees[0].Entries {
		paths = append(paths, entry.Path)
		if !change.Owns("demo-orders", "staging", entry.Path) {
			t.Errorf("the change carried %q, which it does not own", entry.Path)
		}
	}
	sort.Strings(paths)
	for _, wanted := range []string{"v2-records/demo-orders/staging/instance.json",
		"v2-apps/instances/demo-orders/staging/helmrelease.json", "clusters/home/app-demo-orders-staging.json",
		"platform/auth/swhurl-auth-demo-orders-staging.json"} {
		index := sort.SearchStrings(paths, wanted)
		if index == len(paths) || paths[index] != wanted {
			t.Errorf("the change did not carry %q:\n%s", wanted, strings.Join(paths, "\n"))
		}
	}
	if index := sort.SearchStrings(paths, "v2/testdata/profile.json"); index != len(paths) && paths[index] ==
		"v2/testdata/profile.json" {
		t.Error("the change carried the profile, which it neither wrote nor owns")
	}
	written := false
	for _, blob := range publisher.Blobs {
		if record, err := kernel.DecodeInstance(blob); err == nil && record.Configuration.Replicas == 3 {
			written = true
		}
	}
	if !written {
		t.Error("the pull request does not carry the record the edit wrote")
	}
}

// A dry run applies the change and says what it would carry, and writes nothing to GitHub.
func TestChangeCLIDryRun(t *testing.T) {
	source, publisher := changeSources(t)
	status, stdout, diagnostic := changeRun(t, source, publisher, "change", "--kind", "v2-scale",
		"--app", "demo-orders", "--env", "staging", "--set", "replicas=3", "--requested-by", "operator@example.test",
		"--dry-run")
	if status != 0 {
		t.Fatalf("change exited %d: %s%s", status, diagnostic, stdout)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, "Dry run: would commit ") ||
		!strings.HasSuffix(last, " file(s) to console/v2-scale-demo-orders-staging-0123456; no PR opened") {
		t.Errorf("the dry run ended %q", last)
	}
	if len(publisher.Blobs)+len(publisher.Commits)+len(publisher.Pulls)+len(publisher.Refs) != 0 {
		t.Error("the dry run wrote to GitHub")
	}
	if !strings.Contains(stdout, "Merge plan: you merge it on GitHub\n") {
		t.Error("the dry run did not say how to merge it")
	}
}

// A change refuses a flag its kind does not take before it reads anything, and a kind the console does not open.
func TestChangeCLIRefusesWhatItCannotDo(t *testing.T) {
	source, publisher := changeSources(t)
	status, _, diagnostic := changeRun(t, source, publisher, "change", "--kind", "v2-remove", "--app", "demo-orders",
		"--env", "staging", "--set", "replicas=3", "--requested-by", "operator@example.test")
	if status != 2 || !strings.Contains(diagnostic, "invalid: --set: v2-remove does not take it") {
		t.Errorf("a flag the kind does not take: %d %q", status, diagnostic)
	}
	if len(source.Reads) != 0 {
		t.Error("the change read from GitHub before refusing")
	}
	status, _, diagnostic = changeRun(t, source, publisher, "change", "--kind", "v2-scale", "--app", "demo-orders",
		"--env", "staging", "--requested-by", "operator@example.test")
	if status != 2 || !strings.Contains(diagnostic, "invalid: --set: v2-scale needs it") {
		t.Errorf("a missing setting: %d %q", status, diagnostic)
	}
	status, _, diagnostic = changeRun(t, source, publisher, "change", "--kind", "v2-nowhere", "--app", "demo-orders",
		"--env", "staging", "--requested-by", "operator@example.test")
	if status != 2 || !strings.Contains(diagnostic, `invalid: kind: "v2-nowhere" is not a change the console opens`) {
		t.Errorf("an unknown kind: %d %q", status, diagnostic)
	}
	status, _, diagnostic = changeRun(t, source, publisher, "change", "--kind", "v2-scale", "--app", "demo-orders",
		"--env", "production", "--set", "replicas=3", "--requested-by", "operator@example.test")
	if status != 2 || !strings.Contains(diagnostic, `invalid: env: v2-scale does not name "production"`) {
		t.Errorf("an environment the kind does not name: %d %q", status, diagnostic)
	}
	if len(publisher.Blobs) != 0 {
		t.Error("a refused change wrote to GitHub")
	}
}

// A promotion names the revision staging's record states, so what is merged is what the reviewer saw.
func TestChangeStepsPromote(t *testing.T) {
	dir := t.TempDir()
	writeChangeRecord(t, dir, "demo-orders/staging/instance.json", "staging.json")
	tree := newChangeTree(dir)
	change, _ := pages.ConsoleChangeNamed("v2-promote")
	steps, err := cli{}.changeSteps(change, &changeSpec{kind: "v2-promote", app: "demo-orders", env: "staging"},
		tree, changeRevision)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 || steps[0].command != "promote" || steps[1].command != "render" {
		t.Fatalf("a promotion runs %v", steps)
	}
	revision, err := mustDecodeInstance(t, dir, "demo-orders/staging/instance.json")
	if err != nil {
		t.Fatal(err)
	}
	if steps[0].request.expected != string(revision) {
		t.Errorf("the promotion expects %q, want %q", steps[0].request.expected, revision)
	}
	if steps[0].request.env != "prod" || steps[0].request.app != "demo-orders" {
		t.Errorf("the promotion changes %s/%s", steps[0].request.app, steps[0].request.env)
	}
	if steps[1].request.revision != changeRevision {
		t.Errorf("the render reads %q", steps[1].request.revision)
	}
	if !strings.HasSuffix(steps[1].display, "--revision @base") {
		t.Errorf("the quoted render is %q", steps[1].display)
	}
}

// A team's channel change carries its transport on the command line and its address on standard input, and never
// quotes the address.
func TestChangeStepsChannel(t *testing.T) {
	tree := newChangeTree(t.TempDir())
	steps, err := cli{}.changeSteps(channelChange(t, "team-channel-set"),
		&changeSpec{kind: "team-channel-set", team: "payments"}, tree, changeRevision)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].request.action != "channel" || steps[0].request.team != "payments" {
		t.Fatalf("a channel change runs %v", steps)
	}
	if strings.Contains(steps[0].display, "address") || !strings.Contains(steps[0].display, "--transport=ntfy") {
		t.Errorf("the quoted channel change is %q", steps[0].display)
	}
	steps, err = cli{}.changeSteps(channelChange(t, "team-channel-remove"),
		&changeSpec{kind: "team-channel-remove", team: "payments"}, tree, changeRevision)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].request.action != "drop-channel" {
		t.Errorf("dropping a channel runs %v", steps)
	}
}

// channelChange is one kind of team change, or a test that cannot name it.
func channelChange(t *testing.T, kind string) pages.ConsoleChange {
	t.Helper()
	change, found := pages.ConsoleChangeNamed(kind)
	if !found {
		t.Fatalf("%s is not a kind", kind)
	}
	return change
}

// writeChangeRecord puts one fixture in a working copy's records.
func writeChangeRecord(t *testing.T, dir, path, fixtureName string) {
	t.Helper()
	target := fmt.Sprintf("%s/v2-records/%s", dir, path)
	if err := os.MkdirAll(strings.TrimSuffix(target, "/instance.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, fixture(t, fixtureName), 0o644); err != nil {
		t.Fatal(err)
	}
}

// mustDecodeInstance is one record's revision, as the promotion's expectation states it.
func mustDecodeInstance(t *testing.T, dir, path string) (kernel.InstanceRevision, error) {
	t.Helper()
	body, err := os.ReadFile(fmt.Sprintf("%s/v2-records/%s", dir, path))
	if err != nil {
		t.Fatal(err)
	}
	spec, err := kernel.DecodeInstance(body)
	if err != nil {
		return "", err
	}
	return spec.Revision()
}
