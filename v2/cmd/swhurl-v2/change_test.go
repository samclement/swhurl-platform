package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/github"
	"github.com/samclement/swhurl-platform/v2/adapters/github/githubtest"
	"github.com/samclement/swhurl-platform/v2/adapters/registry"
	"github.com/samclement/swhurl-platform/v2/adapters/registry/registrytest"
	"github.com/samclement/swhurl-platform/v2/adapters/sops/sopstest"
	"github.com/samclement/swhurl-platform/v2/console"
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

// A promotion expects the revision production's record states, the instance it changes.
func TestChangeStepsPromote(t *testing.T) {
	dir := t.TempDir()
	writeChangeRecord(t, dir, "demo-orders/staging/instance.json", "staging.json")
	writeChangeRecord(t, dir, "demo-orders/prod/instance.json", "prod.json")
	tree := console.WorkingCopy(dir)
	change, _ := pages.ConsoleChangeNamed("v2-promote")
	steps, err := cli{}.changeSteps(change, &changeSpec{kind: "v2-promote", app: "demo-orders", env: "staging"}, tree)
	if err != nil {
		t.Fatal(err)
	}
	promote, isPromote := steps[0].(console.PromoteStep)
	if len(steps) != 2 || !isPromote || steps[1] != (console.RenderStep{}) {
		t.Fatalf("a promotion runs %v", steps)
	}
	revision, err := mustDecodeInstance(t, dir, "demo-orders/prod/instance.json")
	if err != nil {
		t.Fatal(err)
	}
	if promote != (console.PromoteStep{App: "demo-orders", Env: "prod", Expected: string(revision)}) {
		t.Errorf("the promotion is %+v, want production's %q", promote, revision)
	}
	if err := os.Remove(dir + "/v2-records/demo-orders/prod/instance.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := (cli{}).changeSteps(change, &changeSpec{kind: "v2-promote", app: "demo-orders", env: "staging"},
		tree); err == nil || !strings.Contains(err.Error(), "demo-orders/prod") {
		t.Errorf("a promotion without production's record gave %v", err)
	}
}

// A promotion is one promote and a render, and the pull request quotes the promote it ran, expectation included.
func TestChangeCLIPromote(t *testing.T) {
	tree := changeTreeFiles(t)
	tree["v2-records/demo-orders/prod/instance.json"] = string(fixture(t, "prod.json"))
	tree["v2-records/demo-orders/prod/release.json"] = string(fixture(t, "r41.json"))
	prod, err := kernel.DecodeInstance(fixture(t, "prod.json"))
	if err != nil {
		t.Fatal(err)
	}
	revision, err := prod.Revision()
	if err != nil {
		t.Fatal(err)
	}
	source := &githubtest.ConsoleFake{Commit: changeRevision, Archive: changeTarball(t, tree)}
	publisher := githubtest.NewPublisher(githubtest.PublisherScenario())
	status, stdout, diagnostic := changeRun(t, source, publisher, "change", "--kind", "v2-promote",
		"--app", "demo-orders", "--env", "staging", "--requested-by", "operator@example.test")
	if status != 0 {
		t.Fatalf("change exited %d: %s%s", status, diagnostic, stdout)
	}
	if len(publisher.Pulls) != 1 {
		t.Fatalf("the change opened %d pull requests", len(publisher.Pulls))
	}
	if body := publisher.Pulls[0].Body; !strings.Contains(body, "swhurl-v2 promote --records v2-records "+
		"--app demo-orders --env=prod --profile v2/testdata/profile.json --expect="+string(revision)+" ") {
		t.Errorf("the pull request's body is:\n%s", body)
	}
	carried := map[string]bool{}
	for _, entry := range publisher.Trees[0].Entries {
		carried[entry.Path] = true
	}
	if !carried["v2-records/demo-orders/prod/release.json"] {
		t.Errorf("the promotion did not carry production's release: %v", carried)
	}
}

// Every line a change quotes is the request it runs: parsed as the command's own flags, with the working copy's paths
// and the @base it stands for put back, a quoted line is exactly what the step does. And every kind runs only the
// operations console's table plans for it, in their planned order.
func TestChangeStepsQuoteTheirRequests(t *testing.T) {
	dir := t.TempDir()
	writeChangeRecord(t, dir, "demo-orders/staging/instance.json", "staging.json")
	writeChangeRecord(t, dir, "demo-orders/prod/instance.json", "prod.json")
	writeCallingRecord(t, dir, "demo-caller", "demo-orders")
	tree := console.WorkingCopy(dir)
	covered := map[string]bool{}
	for _, spec := range []changeSpec{
		{kind: "new", app: "example", env: "staging", image: "ghcr.io/owner/example:1",
			settings: []string{"team=demo", "exposure=authenticated-web", "replicas=2"}},
		{kind: "new", app: "example", env: "staging", image: "ghcr.io/owner/example:1", settings: []string{"team=demo"}},
		{kind: "v2-scale", app: "demo-orders", env: "staging", settings: []string{"replicas=2"}},
		{kind: "v2-expose", app: "demo-orders", env: "prod", settings: []string{"exposure=public-web"}},
		{kind: "v2-team", app: "demo-orders", env: "staging", settings: []string{"team=payments"}},
		{kind: "v2-connect", app: "demo-orders", env: "staging", calls: []string{"demo-shop"}},
		{kind: "v2-disconnect", app: "demo-caller", env: "staging", dropCalls: []string{"demo-orders"}},
		{kind: "v2-allow", app: "demo-orders", env: "staging", allows: []string{"demo-shop"}},
		{kind: "v2-remove", app: "demo-orders", env: "staging"},
		{kind: "v2-promote", app: "demo-orders", env: "staging"},
		{kind: "team-add", team: "payments", description: "Payments"},
		{kind: "team-edit", team: "payments", description: "Payments", rename: "billing"},
		{kind: "team-remove", team: "payments"},
		{kind: "team-channel-set", team: "payments"},
		{kind: "team-channel-set", team: "payments", transport: "ntfy"},
		{kind: "team-channel-remove", team: "payments"},
	} {
		change, found := console.ChangeNamed(spec.kind)
		if !found {
			t.Fatalf("%s is not a kind", spec.kind)
		}
		steps, err := cli{}.changeSteps(change.ConsoleChange, &spec, tree)
		if err != nil {
			t.Fatalf("%s: %v", spec.kind, err)
		}
		covered[spec.kind] = true
		if ran, planned := stepCommands(steps), change.Operations; !followsPlan(ran, planned) {
			t.Errorf("%s runs %v, but plans %v", spec.kind, ran, planned)
		}
		for _, step := range steps {
			quoted := quotedRequest(t, step.Quote(), tree.Root, changeRevision)
			if want := stepRequest(step, tree, changeRevision); !reflect.DeepEqual(quoted, want) {
				t.Errorf("%s: the line %q is\n %+v\nbut the step runs\n %+v", spec.kind, step.Quote(), quoted, want)
			}
			if step.Operation() == console.Render && !reflect.DeepEqual(quoted.checkout(),
				files.Checkout{Root: tree.Root, Records: tree.Records, Profile: tree.Profile}) {
				t.Errorf("%s: the quoted render reads %+v", spec.kind, quoted.checkout())
			}
		}
	}
	for _, change := range console.Changes() {
		if !covered[change.Name] {
			t.Errorf("%s is a kind no case runs", change.Name)
		}
	}
}

// writeCallingRecord puts a staging record for app in a working copy's records, calling callee, for a disconnect.
func writeCallingRecord(t *testing.T, dir, app, callee string) {
	t.Helper()
	record := map[string]any{}
	if err := json.Unmarshal(fixture(t, "staging.json"), &record); err != nil {
		t.Fatal(err)
	}
	record["application"], record["calls"] = app, []string{callee}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(dir, "v2-records", app, "staging")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "instance.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// stepCommands is the operation each step runs, in order.
func stepCommands(steps []console.Step) []string {
	ran := make([]string, 0, len(steps))
	for _, step := range steps {
		ran = append(ran, string(step.Operation()))
	}
	return ran
}

// stepRequest is the command line a step is the same call as: a typed step's command makes its call from these flags
// (request.checkout and the step's own fields), and a command step runs its request as it is.
func stepRequest(step console.Step, tree files.Checkout, revision string) request {
	switch step := step.(type) {
	case console.EditStep:
		return request{command: "edit", records: tree.Records, app: step.App, env: step.Env, change: step.Setting,
			profile: tree.Profile, out: tree.Root, callFile: tree.Calls, egressFile: tree.Egress}
	case console.RemoveStep:
		return request{command: "remove", records: tree.Records, app: step.App, env: step.Env, profile: tree.Profile,
			callFile: tree.Calls, out: tree.Root}
	case console.PromoteStep:
		return request{command: "promote", records: tree.Records, app: step.App, env: step.Env, profile: tree.Profile,
			expected: step.Expected, out: tree.Root, callFile: tree.Calls, egressFile: tree.Egress}
	case console.RenderStep:
		return request{command: "render", records: tree.Records, profile: tree.Profile, out: tree.Root,
			revision: revision}
	case commandStep:
		r := step.request
		r.command = step.command
		return r
	}
	return request{}
}

// followsPlan is whether commands run only planned operations and never one planned before another that already ran.
func followsPlan(commands []string, planned []console.Operation) bool {
	at := 0
	for _, command := range commands {
		for at < len(planned) && string(planned[at]) != command {
			at++
		}
		if at == len(planned) {
			return false
		}
	}
	return len(commands) > 0
}

// quotedRequest is the request a quoted line parses to in the working copy: its paths under the copy's root and @base
// as the commit the change was applied to. The flags' own bookkeeping is not part of what the line asks for.
func quotedRequest(t *testing.T, line, root, revision string) request {
	t.Helper()
	words := strings.Fields(line)
	if len(words) < 2 || words[0] != "swhurl-v2" {
		t.Fatalf("the quoted line %q is not a swhurl-v2 command", line)
	}
	r, outcome := parse(words[1:])
	if outcome != parsed {
		t.Fatalf("the quoted line %q does not parse", line)
	}
	for _, path := range []*string{&r.records, &r.profile, &r.out, &r.callFile, &r.egressFile} {
		if *path != "" {
			*path = filepath.Join(root, *path)
		}
	}
	if r.revision == "@base" {
		r.revision = revision
	}
	r.outFlag, r.descriptionFlag, r.stateFlag, r.maxAgeFlag = nil, nil, nil, nil
	return r
}

// A team's channel change carries its transport on the command line and its address on standard input, and never
// quotes the address.
func TestChangeStepsChannel(t *testing.T) {
	tree := console.WorkingCopy(t.TempDir())
	steps, err := cli{}.changeSteps(channelChange(t, "team-channel-set"),
		&changeSpec{kind: "team-channel-set", team: "payments"}, tree)
	if err != nil {
		t.Fatal(err)
	}
	set, isCommand := steps[0].(commandStep)
	if len(steps) != 1 || !isCommand || set.request.action != "channel" || set.request.team != "payments" {
		t.Fatalf("a channel change runs %v", steps)
	}
	if strings.Contains(set.Quote(), "address") || !strings.Contains(set.Quote(), "--transport=ntfy") {
		t.Errorf("the quoted channel change is %q", set.Quote())
	}
	steps, err = cli{}.changeSteps(channelChange(t, "team-channel-remove"),
		&changeSpec{kind: "team-channel-remove", team: "payments"}, tree)
	if err != nil {
		t.Fatal(err)
	}
	if drop, isCommand := steps[0].(commandStep); len(steps) != 1 || !isCommand || drop.request.action != "drop-channel" {
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

// changeRunCreating runs one change with the registry, the encrypter and the seed a new app needs, and the real disk as
// the working copy's writer, because a create writes its records into that copy.
func changeRunCreating(t *testing.T, source *githubtest.ConsoleFake, publisher *githubtest.PublisherFake,
	args ...string) (int, string, string) {
	t.Helper()
	images := registrytest.New(registrytest.Scenario{Tags: []string{createTag},
		Images: map[string]registry.Image{createTag: {Digest: createDigest,
			MediaType: "application/vnd.oci.image.manifest.v1+json",
			Labels: map[string]string{"platform.swhurl.com/declaration": createLabel,
				"org.opencontainers.image.revision": createCommit}}}})
	var stdout, diagnostic bytes.Buffer
	c := cli{readFile: os.ReadFile, writer: files.Disk{}, consoleSource: source, publisher: publisher,
		stdin:     strings.NewReader(""),
		seed:      func() ([32]byte, error) { return [32]byte{3}, nil },
		encrypter: sopstest.New(),
		registries: func(string) (registry.Reader, error) {
			return images, nil
		},
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

// A new app is one change: create states its records from the image its repository published, the render derives its
// files and platform documents, and the pull request carries only that app's files and says what made the repository.
func TestChangeCLICreatesANewApp(t *testing.T) {
	source, publisher := changeSources(t)
	preface := "The console created https://github.com/owner/example from [the template](https://github.com/owner/template) " +
		"and waited for its first image.\n\n"
	status, stdout, diagnostic := changeRunCreating(t, source, publisher, "change", "--kind", "new",
		"--app", "example", "--env", "staging", "--image", createRepo+":"+createTag,
		"--set", "team=demo", "--set", "exposure=authenticated-web", "--preface", preface,
		"--requested-by", "operator@example.test")
	if status != 0 {
		t.Fatalf("change exited %d: %s%s", status, diagnostic, stdout)
	}
	if !strings.Contains(stdout, "Merge plan: you merge it on GitHub\n") {
		t.Errorf("a creation needs no Secret values, so merging is all:\n%s", stdout)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if lines[len(lines)-1] != "Opened "+githubtest.PublisherScenario().PullURL {
		t.Errorf("the change ended %q, want the pull request's URL", lines[len(lines)-1])
	}
	if len(publisher.Pulls) != 1 {
		t.Fatalf("the change opened %d pull requests", len(publisher.Pulls))
	}
	body := publisher.Pulls[0].Body
	if !strings.HasPrefix(body, preface+"Generated by the console with:\n\n    swhurl-v2 create --records "+
		"v2-records --app example --team demo --image "+createRepo+":"+createTag) {
		t.Errorf("the pull request's body is:\n%s", body)
	}
	if publisher.Pulls[0].Title != "[console] v2-records: add example/staging" {
		t.Errorf("the pull request's title is %q", publisher.Pulls[0].Title)
	}
	carried := map[string]bool{}
	for _, entry := range publisher.Trees[0].Entries {
		carried[entry.Path] = true
	}
	for _, path := range []string{"v2-records/example/app.json", "v2-records/example/staging/instance.json",
		"v2-records/example/staging/release.json", "v2-records/example/staging/events.sops.yaml",
		"v2-apps/instances/example/staging/helmrelease.json", "clusters/home/app-example-staging.json",
		"platform/auth/swhurl-auth-example-staging.json", "platform/egress/allocation.json"} {
		if !carried[path] {
			t.Errorf("the change did not carry %q", path)
		}
	}
	for path := range carried {
		if strings.HasPrefix(path, "v2-records/demo-orders/") || strings.Contains(path, "/demo-orders/") {
			t.Errorf("the change carried another app's file %q", path)
		}
	}
}

// A change answers the pull request it opened, which is the link the console's job keeps, and a dry run answers none.
func TestOpenChangeAnswersThePullRequest(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		source, publisher := changeSources(t)
		var stdout, diagnostic bytes.Buffer
		c := cli{out: &stdout, diagnostic: &diagnostic, readFile: os.ReadFile, writer: files.Disk{},
			consoleSource: source, publisher: publisher, stdin: strings.NewReader(""),
			getenv: func(name string) string {
				return map[string]string{consoleTokenEnv: "fixture-token", consoleRepositoryEnv: "owner/repo"}[name]
			}}
		opened, status := c.openChange(&changeSpec{kind: "v2-scale", app: "demo-orders", env: "staging",
			settings: []string{"replicas=3"}, requestedBy: "operator@example.test", dryRun: dryRun})
		want := githubtest.PublisherScenario().PullURL
		if dryRun {
			want = ""
		}
		if status != 0 || opened != want {
			t.Errorf("dry run %v: opened %q (%d), want %q: %s", dryRun, opened, status, want, &diagnostic)
		}
		// each typed step reports its one line, and no command's report reaches the job's log
		if !strings.Contains(stdout.String(), "demo-orders/staging replicas: ") ||
			!strings.Contains(stdout.String(), "render: ") || strings.Contains(stdout.String(), `"kind"`) {
			t.Errorf("dry run %v printed:\n%s", dryRun, &stdout)
		}
	}
}
