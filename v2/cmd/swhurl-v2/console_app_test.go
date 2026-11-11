package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/orchestration"
	"github.com/samclement/swhurl-platform/v2/pages"
)

// appFixture is testdata/app-fixture.json: the documents, rows and one job the Python console rendered the app,
// pending, promotion and New app pages from (tests/render_console_goldens.py), so both servers start from one fixture.
type appFixture struct {
	Now       string `json:"now"`
	Instances []struct {
		Application string          `json:"application"`
		Environment string          `json:"environment"`
		Report      json.RawMessage `json:"report"`
		Inventory   json.RawMessage `json:"inventory"`
	} `json:"instances"`
	Removing []struct {
		Application string `json:"application"`
		Environment string `json:"environment"`
		Reason      string `json:"reason"`
	} `json:"removing"`
	Files map[string]string `json:"files"`
	Pulls []struct {
		Number int    `json:"number"`
		Branch string `json:"branch"`
		Title  string `json:"title"`
		URL    string `json:"url"`
		Body   string `json:"body"`
		Opened string `json:"opened"`
	} `json:"pulls"`
	Jobs []struct {
		ID       int      `json:"id"`
		Action   string   `json:"action"`
		Unit     string   `json:"unit"`
		Identity string   `json:"identity"`
		Started  string   `json:"started"`
		State    string   `json:"state"`
		Finished string   `json:"finished"`
		Link     string   `json:"link"`
		Lines    []string `json:"lines"`
	} `json:"jobs"`
}

func readAppFixture(t *testing.T) appFixture {
	t.Helper()
	document, err := os.ReadFile("testdata/app-fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture appFixture
	if err := json.Unmarshal(document, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

// fixtureTree is the pinned tree the console's documents come from.
func (f appFixture) tree() map[string][]byte {
	tree := map[string][]byte{}
	for path, text := range f.Files {
		tree[path] = []byte(text)
	}
	return tree
}

// fixtureRows is the rows the pages show: one per instance reported, and one only the cluster still has, as
// orchestration.ConsoleRows builds them from the same status document.
func (f appFixture) fixtureRows(t *testing.T) []pages.AppRow {
	t.Helper()
	rows := []pages.AppRow{}
	teams := map[string]string{}
	for _, instance := range f.Instances {
		named, known := pages.Named(instance.Application, instance.Environment)
		if !known {
			t.Fatalf("fixture names %s/%s", instance.Application, instance.Environment)
		}
		var report pages.Report
		if err := json.Unmarshal(instance.Report, &report); err != nil {
			t.Fatal(err)
		}
		var inventory pages.Inventory
		if err := json.Unmarshal(instance.Inventory, &inventory); err != nil {
			t.Fatal(err)
		}
		suspended := report.Live.Unit.Suspended != nil && *report.Live.Unit.Suspended
		if inventory.Team != "" {
			teams[named.App] = inventory.Team
		}
		rows = append(rows, pages.AppRow{Instance: named, State: pages.StateOf(report.State.Name, report.State.Reason),
			Report: &report, Inventory: &inventory, Teams: teams, Suspended: suspended})
	}
	for _, gone := range f.Removing {
		if row, known := pages.RemovedRow(gone.Application, gone.Environment, gone.Reason); known {
			rows = append(rows, row)
		}
	}
	return rows
}

// fixtureJobs is the console's jobs, as the Activity fixture states them.
func (f appFixture) fixtureJobs(t *testing.T) []pages.Job {
	t.Helper()
	jobs := []pages.Job{}
	for _, job := range f.Jobs {
		started, err := time.Parse(time.RFC3339, job.Started)
		if err != nil {
			t.Fatal(err)
		}
		found := pages.Job{ID: job.ID, Action: job.Action, Unit: job.Unit, Identity: job.Identity, Started: started,
			State: job.State, Link: job.Link, Lines: job.Lines, Resume: map[string]any{"kind": "new-app"}}
		if job.Finished != "" {
			finished, err := time.Parse(time.RFC3339, job.Finished)
			if err != nil {
				t.Fatal(err)
			}
			found.Finished = &finished
		}
		jobs = append(jobs, found)
	}
	return jobs
}

// fixturePulls is the open console pull requests the pages read.
func (f appFixture) fixturePulls() []pages.OpenPull {
	pulls := []pages.OpenPull{}
	for _, pull := range f.Pulls {
		pulls = append(pulls, pages.OpenPull{Number: pull.Number, Branch: pull.Branch, Title: pull.Title,
			URL: pull.URL, Body: pull.Body, Opened: pull.Opened})
	}
	return pulls
}

// fixtureTime is the moment the Python console rendered these pages at.
func (f appFixture) moment(t *testing.T) time.Time {
	t.Helper()
	moment, err := time.Parse(time.RFC3339, f.Now)
	if err != nil {
		t.Fatal(err)
	}
	return moment
}

// fixtureShell is the page around these pages: the console repo, the reader's identity and the fixed clock.
func fixtureShell(path, title string) shell {
	return shell{Title: title, Path: path, Identity: "sam@swhurl.com", ReadAt: "09:43", RefreshURL: path,
		Assets: "93e3b193821e", HasGithub: true}
}

// shellWithJobs is the page with the header's running count, which reads every recent job, not only its own.
func shellWithJobs(t *testing.T, path, title string, jobs []pages.Job) shell {
	t.Helper()
	found := fixtureShell(path, title)
	found.Running = runningJobs(jobViewsOf(jobs))
	return found
}

// fixtureSite is the site the pages link to: the console's repository, whose owner app repositories live under too.
func fixtureSite(t *testing.T, tree map[string][]byte) pages.Site {
	t.Helper()
	site, err := orchestration.ConsoleSite(tree, "samclement/swhurl-platform")
	if err != nil {
		t.Fatal(err)
	}
	return site
}

// compareGolden fails with the first line that differs, so a mismatch names the page's own text.
func compareGolden(t *testing.T, rendered bytes.Buffer, name string) {
	t.Helper()
	want, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if rendered.String() == string(want) {
		return
	}
	os.WriteFile("/tmp/"+name, rendered.Bytes(), 0o644)
	got, expected := bytes.Split(rendered.Bytes(), []byte("\n")), bytes.Split(want, []byte("\n"))
	for i := 0; i < len(got) && i < len(expected); i++ {
		if !bytes.Equal(got[i], expected[i]) {
			t.Fatalf("%s differs at line %d:\n got: %s\nwant: %s", name, i+1, got[i], expected[i])
		}
	}
	t.Fatalf("%s differs in length: got %d lines, want %d", name, len(got), len(expected))
}

// appPageOf is one instance's page from the fixture, read the way the console reads it.
func appPageOf(t *testing.T, f appFixture, tree map[string][]byte, app, env string) pages.AppPage {
	t.Helper()
	instance, known := pages.Named(app, env)
	if !known {
		t.Fatalf("%s/%s is not an instance", app, env)
	}
	rows := f.fixtureRows(t)
	teams, _ := orchestration.ConsoleTeams(tree)
	variables, variablesKnown := orchestration.ConsoleDesiredVariables(tree, app, env)
	now := f.moment(t)
	card := orchestration.Flags(context.Background(), nil, env, app, nil, "", now)
	return pages.AppPageOf(pages.AppInput{Instance: instance, Rows: rows, Site: fixtureSite(t, tree),
		Stacks: map[string]string{app: "typescript"}, Jobs: f.fixtureJobs(t),
		Flags: orchestration.ConsoleFlagView(env, card), FlagsWritable: false, Teams: teams,
		Settings: orchestration.ConsoleSettings(tree, app, env), Variables: variables, VariablesKnown: variablesKnown,
		Connectable: pages.ConnectableTo(rows, orchestration.ConsoleOffers(tree, rows, env), env, app),
		Github:      true})
}

// The app page renders the same bytes as the Python console's: a deployed instance with a team, a host, a database,
// connections, an egress grant and an image, read from the records in testdata/app-fixture.json.
func TestAppPageIsByteIdentical(t *testing.T) {
	server, err := newConsoleServer(consoleConfig{Dev: true})
	if err != nil {
		t.Fatal(err)
	}
	fixture := readAppFixture(t)
	tree := fixture.tree()
	for _, page := range []struct{ Name, App, Env string }{
		{"app.html", "demo-shop", "staging"},
		{"app-prod.html", "demo-shop", "prod"},
	} {
		t.Run(page.Name, func(t *testing.T) {
			data := appData{shell: fixtureShell("/apps/"+page.App+"/"+page.Env, page.App+"/"+page.Env),
				AppPage: appPageOf(t, fixture, tree, page.App, page.Env)}
			var rendered bytes.Buffer
			if err := server.templates["app.html"].ExecuteTemplate(&rendered, "base", data); err != nil {
				t.Fatal(err)
			}
			compareGolden(t, rendered, page.Name)
		})
	}
}

// The pending page renders the same bytes as the Python console's in both states: removed from Git and still live,
// and in Git but not applied yet.
func TestPendingIsByteIdentical(t *testing.T) {
	server, err := newConsoleServer(consoleConfig{Dev: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range []struct {
		Name, App, Env  string
		Instance        pages.Instance
		Removing, InGit bool
	}{
		{"pending-removing.html", "demo-gone", "staging", pages.Instance{App: "demo-gone", Env: "staging"}, true, false},
		{"pending-absent.html", "demo-waiting", "prod", pages.Instance{App: "demo-waiting", Env: "prod"}, false, true},
	} {
		t.Run(page.Name, func(t *testing.T) {
			data := pendingData{shell: fixtureShell("/apps/"+page.App+"/"+page.Env, page.App+"/"+page.Env),
				Pending: pages.PendingOf(page.Instance, page.Removing, page.InGit, nil, nil, "")}
			var rendered bytes.Buffer
			if err := server.templates["pending.html"].ExecuteTemplate(&rendered, "base", data); err != nil {
				t.Fatal(err)
			}
			compareGolden(t, rendered, page.Name)
		})
	}
}

// A new app that has a job and a pull request but no report yet shows both.
func TestPendingNewAppIsByteIdentical(t *testing.T) {
	server, err := newConsoleServer(consoleConfig{Dev: true})
	if err != nil {
		t.Fatal(err)
	}
	fixture := readAppFixture(t)
	instance := pages.Instance{App: "shop", Env: "staging"}
	jobs := fixture.fixtureJobs(t)
	page := pages.PendingOf(instance, false, false, jobs, pages.PullsFor(fixture.fixturePulls(), instance), "")
	data := pendingData{shell: shellWithJobs(t, "/apps/shop/staging", "shop/staging", jobs), Pending: page}
	var rendered bytes.Buffer
	if err := server.templates["pending.html"].ExecuteTemplate(&rendered, "base", data); err != nil {
		t.Fatal(err)
	}
	compareGolden(t, rendered, "pending-new.html")
}

// The promotion review compares staging's image with production's, from the same records.
func TestPromotionIsByteIdentical(t *testing.T) {
	server, err := newConsoleServer(consoleConfig{Dev: true})
	if err != nil {
		t.Fatal(err)
	}
	fixture := readAppFixture(t)
	rows := fixture.fixtureRows(t)
	var staging, prod *pages.AppRow
	for i := range rows {
		switch rows[i].Instance.Name() {
		case "demo-shop/staging":
			staging = &rows[i]
		case "demo-shop/prod":
			prod = &rows[i]
		}
	}
	review := pages.PromotionOf("demo-shop", staging, prod)
	data := promotionData{shell: fixtureShell("/apps/demo-shop/staging/promotion", "Promote demo-shop"),
		PromotionPage: pages.PromotionPage{App: "demo-shop", Review: &review}}
	var rendered bytes.Buffer
	if err := server.templates["promotion.html"].ExecuteTemplate(&rendered, "base", data); err != nil {
		t.Fatal(err)
	}
	compareGolden(t, rendered, "promotion.html")
}

// The New app form renders the same bytes as the Python console's, with the same teams, stacks and exposures.
func TestNewAppIsByteIdentical(t *testing.T) {
	server, err := newConsoleServer(consoleConfig{Dev: true})
	if err != nil {
		t.Fatal(err)
	}
	fixture := readAppFixture(t)
	tree := fixture.tree()
	stacks, err := os.ReadFile("../../contract/stacks.json")
	if err != nil {
		t.Fatal(err)
	}
	tree["v2/contract/stacks.json"] = stacks
	catalogue, err := orchestration.ConsoleStacks(tree)
	if err != nil {
		t.Fatal(err)
	}
	rows := fixture.fixtureRows(t)
	teams, _ := orchestration.ConsoleTeams(tree)
	page := pages.NewAppPageOf(pages.NewAppInput{Catalogue: catalogue, Site: fixtureSite(t, tree), Teams: teams,
		Connectable: pages.ConnectableTo(rows, orchestration.ConsoleOffers(tree, rows, "staging"), "staging", ""),
		Github:      true, AppRepos: true})
	data := newAppData{shell: fixtureShell("/new", "New app"), NewAppPage: page}
	var rendered bytes.Buffer
	if err := server.templates["new_repo.html"].ExecuteTemplate(&rendered, "base", data); err != nil {
		t.Fatal(err)
	}
	compareGolden(t, rendered, "new-repo.html")
}
