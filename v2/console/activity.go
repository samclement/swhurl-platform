package console

import (
	"context"
	"github.com/samclement/swhurl-platform/v2/adapters/github"
	"github.com/samclement/swhurl-platform/v2/pages"
	"slices"
	"strings"
	"sync"
	"time"
)

// Activity is Activity: every change, what is still moving or waiting for someone first.
type Activity struct {
	Moving, Ended []ChangeView
	PRsError      string
}

// JobPage is one job's page: its change's whole path and its output.
type JobPage struct {
	Job       ChangeJobView
	Track     pages.Track
	Stage     pages.Stage
	PR        *OpenPullView
	CanResume bool
}

// ChangeView is one change's row.
type ChangeView struct {
	ID    string
	Track pages.Track
	Open  bool // its pull request waits for someone: the row opens to its controls
	Job   *ChangeJobView
	PR    *OpenPullView
}

// ChangeJobView is a change's job as its row and its page show it.
type ChangeJobView struct {
	ID                           int
	Title, Label, Link, Unit     string
	Identity, State, PullLink    string
	StartedAt, StartedClock, Ago string
	Finished                     bool
	FinishedClock, Took          string
	Running                      bool
	Lines                        []string
}

// OpenPullView is an open console pull request as a change's row shows it.
type OpenPullView struct {
	Number                                int
	URL, Branch, Title, Opened, OpenedAgo string
	ChecksURL, Checks, Merging            string
}

// readActivity is every job as a change, and the open pull requests no remembered job opened: what is still moving
// or waiting for someone first, then what ended, newest first.
func ActivityOf(ctx context.Context, jobs []pages.Job, reads ChangeReads) Activity {
	rows, prsError := ConsoleChanges(ctx, jobs, reads, true)
	data := Activity{PRsError: prsError}
	for _, row := range rows {
		if row.Track.Settled() {
			data.Ended = append(data.Ended, ChangeViewOf(row, reads.Now))
		} else {
			data.Moving = append(data.Moving, ChangeViewOf(row, reads.Now))
		}
	}
	return data
}

// readJob is one job's page; found is false when no remembered job has the id.
func JobPageOf(ctx context.Context, jobs []pages.Job, id int, reads ChangeReads, canCreate bool) (JobPage, bool) {
	at := slices.IndexFunc(jobs, func(job pages.Job) bool { return job.ID == id })
	if at < 0 {
		return JobPage{}, false
	}
	job := jobs[at]
	rows, _ := ConsoleChanges(ctx, []pages.Job{job}, reads, false)
	row := ChangeViewOf(rows[0], reads.Now)
	return JobPage{Job: *row.Job, Track: row.Track, Stage: pages.Prepared(job), PR: row.PR,
		CanResume: Resumable(job, jobs) && canCreate}, true
}

// Resumable is whether a new-app job ended without its pull request, with nothing since taking its place.
func Resumable(job pages.Job, jobs []pages.Job) bool {
	if (job.State != "failed" && job.State != "interrupted") || job.Link != "" || job.Resume["kind"] != pages.NewAppRecordKind {
		return false
	}
	for _, later := range jobs {
		if later.Unit == job.Unit && later.ID > job.ID && (later.State == "running" || later.State == "succeeded") {
			return false
		}
	}
	return true
}

// ChangeViewOf is one change as its row shows it.
func ChangeViewOf(row pages.ChangeRow, now time.Time) ChangeView {
	view := ChangeView{ID: row.ID, Track: row.Track, Open: row.PR != nil && (row.Track.Status == "waiting" || row.Track.Status == "blocked")}
	if row.PR != nil {
		view.PR = OpenPullViewOf(*row.PR, now)
	}
	if row.Job != nil {
		view.Job = ChangeJobViewOf(*row.Job, now)
	}
	return view
}

func OpenPullViewOf(pr pages.OpenPull, now time.Time) *OpenPullView {
	checksURL := pr.ChecksURL
	if checksURL == "" {
		checksURL = pr.URL
	}
	return &OpenPullView{Number: pr.Number, URL: pr.URL, Branch: pr.Branch, Title: strings.ReplaceAll(pr.Title, "[console] ", ""),
		Opened: pr.Opened, OpenedAgo: pages.Ago(pr.Opened, now), ChecksURL: checksURL, Checks: pr.Checks, Merging: pages.MergeMode(pr.Body)}
}

func ChangeJobViewOf(job pages.Job, now time.Time) *ChangeJobView {
	label, link := pages.Target(job.Unit)
	view := &ChangeJobView{ID: job.ID, Title: capitalize(job.Action), Label: label, Link: link, Unit: job.Unit,
		Identity: job.Identity, State: job.State, PullLink: job.Link, Running: job.Running(), Lines: job.Lines,
		StartedAt: job.Started.Format("2006-01-02 15:04:05"), StartedClock: job.Started.Format("15:04:05"),
		Ago: pages.Ago(job.Started.Format(time.RFC3339Nano), now)}
	if job.Finished != nil {
		view.Finished, view.FinishedClock, view.Took = true, job.Finished.Format("15:04:05"), pages.Took(job.Started, *job.Finished)
	}
	return view
}

// capitalize is Python's str.capitalize: the first letter upper case and the rest lower.
func capitalize(text string) string {
	if text == "" {
		return ""
	}
	runes := []rune(strings.ToLower(text))
	return strings.ToUpper(string(runes[0])) + string(runes[1:])
}

// ActivityFacts keeps only immutable facts: merged pull requests. Its zero value is ready, and it is safe for
// concurrent pages. When a change reached its end is the job record's (Jobs.Reach), not this cache's.
type ActivityFacts struct {
	mu     sync.Mutex
	merged map[int]pages.Pull
}

// keptFacts bounds the merged pull requests kept, oldest first.
const keptFacts = 60

// consolePulls lists the console's open pull requests, or says why it could not.
func OpenPulls(ctx context.Context, client github.Console, repository, token string) ([]pages.OpenPull, string) {
	if client == nil {
		return nil, "No GitHub token is configured, so open pull requests are not listed."
	}
	pulls, err := client.OpenPulls(ctx, repository, token)
	if err != nil {
		return nil, "Could not list open pull requests: " + err.Error()
	}
	found := make([]pages.OpenPull, 0, len(pulls))
	for _, pull := range pulls {
		found = append(found, pages.OpenPull{Number: pull.Number, Title: pull.Title, URL: pull.URL, Branch: pull.Branch,
			Opened: pull.Opened, Body: pull.Body, Checks: pull.Checks, ChecksURL: pull.ChecksURL})
	}
	return found, ""
}

// pull is a pull request that is no longer open: a merged one is kept, since it cannot change again (a closed one
// can be reopened).
func (s *ActivityFacts) Pull(ctx context.Context, client github.Console, repository, token string, number int) (pages.Pull, error) {
	s.mu.Lock()
	kept, known := s.merged[number]
	s.mu.Unlock()
	if known {
		return kept, nil
	}
	read, err := client.Pull(ctx, repository, token, number)
	if err != nil {
		return pages.Pull{}, err
	}
	found := pullFacts(read)
	if found.State == "merged" {
		s.mu.Lock()
		if s.merged == nil {
			s.merged = map[int]pages.Pull{}
		}
		keep(s.merged, number, found)
		s.mu.Unlock()
	}
	return found, nil
}

// pullFacts is a pull request read by number as the track reads it: how it ended, when, and the commit that merged it.
func pullFacts(read github.Pull) pages.Pull {
	found := pages.Pull{Number: read.Number, URL: read.URL, State: read.State}
	switch read.State {
	case "merged":
		found.When, found.Commit = read.MergedAt, read.MergeCommit
	case "closed":
		found.When = read.ClosedAt
	}
	return found
}

// keep remembers one fact, forgetting the lowest key once keptFacts are held.
func keep[V any](facts map[int]V, key int, value V) {
	if len(facts) >= keptFacts {
		oldest := key
		for held := range facts {
			oldest = min(oldest, held)
		}
		delete(facts, oldest)
	}
	facts[key] = value
}

// mainRenders is main's instances, from the whole tree at the commit every other read on the page is pinned to.
func (s Server) MainRenders(ctx context.Context) (map[string]pages.Render, error) {
	revision, err := s.Bundle.Revision(ctx)
	if err != nil {
		return nil, err
	}
	tree, err := s.Bundle.Tree(ctx, revision)
	if err != nil {
		return nil, err
	}
	return ConsoleRenders(tree)
}

// rowsByInstance is every instance on the cluster, by <app>/<env>.
func (s Server) RowsByInstance(ctx context.Context) (map[string]*pages.AppRow, error) {
	_, rows, err := s.Rows(ctx)
	if err != nil {
		return nil, err
	}
	found := make(map[string]*pages.AppRow, len(rows))
	for i := range rows {
		found[rows[i].Instance.Name()] = &rows[i]
	}
	return found, nil
}
