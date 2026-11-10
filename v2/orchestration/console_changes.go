package orchestration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/samclement/swhurl-platform/v2/pages"
)

// ConsoleRenders is every v2 instance in one revision's tree, as the facts Activity follows a change by: immutable
// Git evidence, not a verdict. The records are the change; the committed HelmRelease states the image it runs. No
// records means absent, even beside stale output.
func ConsoleRenders(tree map[string][]byte) (map[string]pages.Render, error) {
	found := map[string]pages.Render{}
	for file, instance := range tree {
		rest, inRecords := strings.CutPrefix(file, consoleRecords+"/")
		parts := strings.Split(rest, "/")
		if !inRecords || len(parts) != 3 || parts[2] != "instance.json" {
			continue
		}
		app, env := parts[0], parts[1]
		appRecord, hasApp := tree[consoleRecords+"/"+app+"/app.json"]
		release, hasRelease := tree[consoleRecords+"/"+app+"/"+env+"/release.json"]
		if !hasApp || !hasRelease {
			continue
		}
		render, err := renderOf(appRecord, instance, release, tree[consoleApps+"/"+app+"/"+env+"/helmrelease.json"])
		if err != nil {
			return nil, fmt.Errorf("cannot read render targets: %s/%s: %w", app, env, err)
		}
		found[app+"/"+env] = render
	}
	return found, nil
}

// renderOf is one instance's records and HelmRelease as hashes and digests; helm is nil when none is committed.
func renderOf(appRecord, instance, release, helm []byte) (pages.Render, error) {
	var settings map[string]any
	var requested struct {
		Artifact struct {
			Digest string `json:"digest"`
		} `json:"artifact"`
	}
	if json.Unmarshal(instance, &settings) != nil || json.Unmarshal(release, &requested) != nil {
		return pages.Render{}, fmt.Errorf("malformed record")
	}
	delete(settings, "release") // the platform's settings, without the release the image decides
	platform, err := json.Marshal(settings)
	if err != nil {
		return pages.Render{}, err
	}
	render := pages.Render{Records: digestOf(appRecord, instance, release, helm), Settings: digestOf(appRecord, platform),
		Digest: requested.Artifact.Digest}
	if helm == nil {
		return render, nil
	}
	var committed struct {
		Spec struct {
			Values struct {
				Controllers struct {
					Main struct {
						Containers struct {
							Main struct {
								Image struct {
									Digest string `json:"digest"`
								} `json:"image"`
							} `json:"main"`
						} `json:"containers"`
					} `json:"main"`
				} `json:"controllers"`
			} `json:"values"`
		} `json:"spec"`
	}
	if json.Unmarshal(helm, &committed) != nil {
		return pages.Render{}, fmt.Errorf("malformed HelmRelease")
	}
	render.Release, render.Runs = true, committed.Spec.Values.Controllers.Main.Containers.Main.Image.Digest
	return render, nil
}

func digestOf(parts ...[]byte) string {
	sum := sha256.New()
	for i, part := range parts {
		if i > 0 {
			sum.Write([]byte{0})
		}
		sum.Write(part)
	}
	return hex.EncodeToString(sum.Sum(nil))[:16]
}

// ChangeReads are the reads Activity's rows take, each bounded by the page's context. A nil read is one this
// console cannot make (no GitHub token), which leaves what it would have shown unknown rather than assumed.
type ChangeReads struct {
	// OpenPulls lists the open console pull requests, or says why it could not, in the words the page shows.
	OpenPulls func(context.Context) ([]pages.OpenPull, string)
	// Pull is one pull request no longer open: merged or closed, when, and the commit that merged it.
	Pull func(context.Context, int) (pages.Pull, error)
	// Renders is one commit's instances (ConsoleRenders); Main is main's.
	Renders func(context.Context, string) (map[string]pages.Render, error)
	Main    func(context.Context) (map[string]pages.Render, error)
	// Cached is whether one commit's tree is already in hand, so a page's bounded reads are not spent on it (nil:
	// nothing is known to be in hand).
	Cached func(string) bool
	// Rows is every instance on the cluster, by <app>/<env>.
	Rows func(context.Context) (map[string]*pages.AppRow, error)
	// Reach records that a job's change was seen at its end, the first time it is; nil records nothing.
	Reach func(pages.Job)
	Now   time.Time
}

// ConsoleChanges is each job as a change with its whole path and, with others, the open pull requests no
// remembered job opened. The second result is why open pull requests could not be listed, if they could not.
func ConsoleChanges(ctx context.Context, jobs []pages.Job, reads ChangeReads, others bool) ([]pages.ChangeRow, string) {
	proposed := proposals(jobs)
	var open []pages.OpenPull
	prsError := ""
	if others || len(proposed) > 0 {
		open, prsError = reads.OpenPulls(ctx)
	}
	isOpen := map[int]*pages.OpenPull{}
	for i := range open {
		isOpen[open[i].Number] = &open[i]
	}
	pulls := jobPulls(ctx, jobs, proposed, isOpen, reads, prsError == "")
	landed := landings(ctx, jobs, pulls, reads)
	rows := make([]pages.ChangeRow, 0, len(jobs)+len(open))
	for i, job := range jobs {
		var pull *pages.Pull
		if found, known := pulls[job.ID]; known {
			found.When = pages.Ago(found.When, reads.Now)
			pull = &found
		}
		rows = append(rows, pages.ChangeRow{ID: fmt.Sprintf("job-%d", job.ID), Track: pages.TrackOf(job, pull, landed[job.ID]),
			Job: &jobs[i], PR: isOpen[proposed[job.ID]]})
	}
	if others {
		rows = append(rows, unclaimed(open, proposed)...)
	}
	return rows, prsError
}

// proposals is the pull request each job that succeeded opened, by job.
func proposals(jobs []pages.Job) map[int]int {
	proposed := map[int]int{}
	for _, job := range jobs {
		if number := pages.PullNumber(job); job.State == "succeeded" && number != 0 {
			proposed[job.ID] = number
		}
	}
	return proposed
}

// jobPulls is each proposing job's pull request: as listed while it is open, and otherwise read by number when the
// list could be read. The reads by number run at once, as many as concurrentReads.
func jobPulls(ctx context.Context, jobs []pages.Job, proposed map[int]int, isOpen map[int]*pages.OpenPull,
	reads ChangeReads, readable bool) map[int]pages.Pull {
	pulls := map[int]pages.Pull{}
	var unread []pages.Job
	for _, job := range jobs {
		if number, opened := proposed[job.ID]; opened {
			if pr, known := isOpen[number]; known {
				pulls[job.ID] = pages.PullOf(*pr)
			} else {
				unread = append(unread, job)
			}
		}
	}
	read := make([]pages.Pull, len(unread))
	together(len(unread), func(i int) { read[i] = pullOf(ctx, reads, proposed[unread[i].ID], unread[i].Link, readable) })
	for i, job := range unread {
		pulls[job.ID] = read[i]
	}
	return pulls
}

// concurrentReads is how many of one page's reads run at once.
const concurrentReads = 8

// together runs read for each of count items, concurrentReads at a time, and returns when all are done.
func together(count int, read func(int)) {
	slots := make(chan struct{}, concurrentReads)
	var running sync.WaitGroup
	for i := range count {
		running.Add(1)
		slots <- struct{}{}
		go func() {
			defer func() { <-slots; running.Done() }()
			read(i)
		}()
	}
	running.Wait()
}

// unclaimed is the open pull requests no remembered job opened, each as its own change.
func unclaimed(open []pages.OpenPull, proposed map[int]int) []pages.ChangeRow {
	covered := map[int]bool{}
	for _, number := range proposed {
		covered[number] = true
	}
	var rows []pages.ChangeRow
	for i := range open {
		if !covered[open[i].Number] {
			rows = append(rows, pages.ChangeRow{ID: fmt.Sprintf("pr-%d", open[i].Number),
				Track: pages.PullTrack(pages.PullOf(open[i]), open[i].Branch), PR: &open[i]})
		}
	}
	return rows
}

// pullOf is a pull request that is no longer open, or one that could not be read (state unknown).
func pullOf(ctx context.Context, reads ChangeReads, number int, link string, readable bool) pages.Pull {
	unread := pages.Pull{Number: number, URL: link, State: "unknown"}
	if !readable || reads.Pull == nil {
		return unread
	}
	found, err := reads.Pull(ctx, number)
	if err != nil {
		return unread
	}
	found.Number, found.URL = number, link // the link the job opened
	return found
}

// landings is how far each merged change to an instance has got: the cluster, main's renders and each merge
// commit's, read once for the page and only when some change needs them.
func landings(ctx context.Context, jobs []pages.Job, pulls map[int]pages.Pull, reads ChangeReads) map[int]*pages.Progress {
	landing := landingJobs(jobs, pulls)
	found := map[int]*pages.Progress{}
	if len(landing) == 0 {
		return found
	}
	rows, rowsErr := reads.Rows(ctx)
	main := readRenders(func() (map[string]pages.Render, error) { return reads.Main(ctx) })
	trees := mergeTrees(ctx, landing, pulls, reads)
	for _, job := range landing {
		live := pages.Observed{Read: rowsErr == nil}
		if live.Read {
			live.Row = rows[job.Unit]
		}
		progress := pages.ProgressOf(job.Action, renderAt(trees[pulls[job.ID].Commit], job.Unit),
			renderAt(main, job.Unit), live, job.Reached)
		if progress.Reached() && job.Reached == "" && reads.Reach != nil {
			reads.Reach(job)
		}
		found[job.ID] = &progress
	}
	return found
}

// landingJobs are the jobs whose merged change ends on the cluster.
func landingJobs(jobs []pages.Job, pulls map[int]pages.Pull) []pages.Job {
	var landing []pages.Job
	for _, job := range jobs {
		if pull, known := pulls[job.ID]; known && pull.State == "merged" && pages.Lands(job.Action, job.Unit) {
			landing = append(landing, job)
		}
	}
	return landing
}

// historicalTreesPerPage bounds the archives one page fetches: a page decides the change whose merged tree it read
// and leaves the rest unread for a later read, rather than downloading every historical archive at once. One is
// deliberate: the link here carries about 600 kB/s, so two archives of the 8 MB this repository once held take 28
// seconds between them and neither could ever finish inside the page's budget; one archive per page always can, and
// the next read takes the next change. A tree already in hand costs no fetch.
const historicalTreesPerPage = 1

// mergeTrees is the renders of each commit that merged one of the changes, once per commit and at once; a commit
// that could not be read is missing, as is one GitHub gave no commit for, and one this page left for the next.
func mergeTrees(ctx context.Context, landing []pages.Job, pulls map[int]pages.Pull, reads ChangeReads) map[string]map[string]pages.Render {
	var commits []string
	for _, job := range landing {
		if commit := pulls[job.ID].Commit; commit != "" && !slices.Contains(commits, commit) {
			commits = append(commits, commit)
		}
	}
	trees := map[string]map[string]pages.Render{}
	if reads.Renders == nil {
		return trees
	}
	fetch := toFetch(commits, reads.Cached)
	read := make([]map[string]pages.Render, len(fetch))
	together(len(fetch), func(i int) {
		read[i] = readRenders(func() (map[string]pages.Render, error) { return reads.Renders(ctx, fetch[i]) })
	})
	for i, commit := range fetch {
		trees[commit] = read[i]
	}
	return trees
}

// toFetch is the commits one page reads: those already in hand (they cost no fetch), and otherwise the newest
// historicalTreesPerPage, so one slow archive cannot hold the page open. The next read takes the rest.
func toFetch(commits []string, cached func(string) bool) []string {
	inHand := func(commit string) bool { return cached != nil && cached(commit) }
	budget := historicalTreesPerPage
	fetch := make([]string, 0, len(commits))
	for _, commit := range commits {
		if !inHand(commit) {
			if budget == 0 {
				continue
			}
			budget--
		}
		fetch = append(fetch, commit)
	}
	return fetch
}

// readRenders is a tree's renders, nil when they could not be read: never kept, so the next page reads again.
func readRenders(read func() (map[string]pages.Render, error)) map[string]pages.Render {
	found, err := read()
	if err != nil || found == nil {
		return nil
	}
	return found
}

// renderAt is one instance in a read tree (absent: the zero Render), or nil when the tree could not be read.
func renderAt(tree map[string]pages.Render, unit string) *pages.Render {
	if tree == nil {
		return nil
	}
	found := tree[unit]
	return &found
}
