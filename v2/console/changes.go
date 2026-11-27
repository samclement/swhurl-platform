package console

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/samclement/swhurl-platform/v2/orchestration"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/pages"
)

// ConsoleRenders is every v2 instance in one revision's tree, as the facts Activity follows a change by: immutable
// Git evidence, not a verdict. The records are the change; the committed HelmRelease states the image it runs. No
// records means absent, even beside stale output.
func ConsoleRenders(tree map[string][]byte) (map[string]pages.Render, error) {
	found := map[string]pages.Render{}
	for file, instance := range tree {
		app, env, isInstance := kernel.InstanceRecordOf(kernel.RecordsRoot, file)
		if !isInstance {
			continue
		}
		appRecord, hasApp := tree[kernel.AppRecordPath(kernel.RecordsRoot, app)]
		release, hasRelease := tree[kernel.ReleaseRecordPath(kernel.RecordsRoot, app, env)]
		if !hasApp || !hasRelease {
			continue
		}
		render, err := renderOf(appRecord, instance, release, tree[pages.ConsoleAppsRoot+"/"+app+"/"+env+"/helmrelease.json"])
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
	// Renders is the named units' instances at one merge commit (ConsoleEvidence), the zero Render for one the
	// commit has none of; Main is every instance on main (ConsoleRenders).
	Renders func(ctx context.Context, commit string, units []string) (map[string]pages.Render, error)
	Main    func(context.Context) (map[string]pages.Render, error)
	// Cached is whether every named unit's render at one commit is already in hand, so a page's bounded reads are
	// not spent on it (nil: nothing is known to be in hand).
	Cached func(commit string, units []string) bool
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
				Track: PullTrack(pages.PullOf(open[i]), open[i].Branch), PR: &open[i]})
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

// historicalTreesPerPage bounds the merge commits one page reads: a page decides the change whose merge commit it
// read and leaves the rest unread for a later read, rather than reading every historical commit at once. A read is
// a commit's tree listing and the few files its changes' units take, under a second where an archive took 14; one
// per page is kept from the archive's time, when two could not finish inside the page's budget. A commit whose
// units are all in hand costs no read; one in hand for another unit does.
const historicalTreesPerPage = 1

// mergeTrees is the renders of each commit that merged one of the changes, once per commit with all of its changes'
// units and at once; a commit that could not be read is missing, as is one GitHub gave no commit for, and one this
// page left for the next.
func mergeTrees(ctx context.Context, landing []pages.Job, pulls map[int]pages.Pull, reads ChangeReads) map[string]map[string]pages.Render {
	commits, units := mergeUnits(landing, pulls)
	trees := map[string]map[string]pages.Render{}
	if reads.Renders == nil {
		return trees
	}
	fetch := toFetch(commits, func(commit string) bool { return reads.Cached != nil && reads.Cached(commit, units[commit]) })
	read := make([]map[string]pages.Render, len(fetch))
	together(len(fetch), func(i int) {
		read[i] = readRenders(func() (map[string]pages.Render, error) { return reads.Renders(ctx, fetch[i], units[fetch[i]]) })
	})
	for i, commit := range fetch {
		trees[commit] = read[i]
	}
	return trees
}

// mergeUnits is each merge commit, newest change first, and the units its changes end on.
func mergeUnits(landing []pages.Job, pulls map[int]pages.Pull) ([]string, map[string][]string) {
	var commits []string
	units := map[string][]string{}
	for _, job := range landing {
		commit := pulls[job.ID].Commit
		if commit == "" {
			continue
		}
		if _, seen := units[commit]; !seen {
			commits = append(commits, commit)
		}
		if !slices.Contains(units[commit], job.Unit) {
			units[commit] = append(units[commit], job.Unit)
		}
	}
	return commits, units
}

// toFetch is the commits one page reads: those already in hand (they cost no fetch), and otherwise the newest
// historicalTreesPerPage, so one slow read cannot hold the page open. The next read takes the rest.
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

// readRenders is a read's renders, nil when they could not be read: never kept, so the next page reads again.
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

// PullTrack follows an open pull request whose job is no longer remembered.
func PullTrack(pull pages.Pull, branch string) pages.Track {
	action, target := BranchTarget(branch)
	return pages.PullTrack(pull, action, target)
}

// ChangeProposal is a validated form's change, ready for v2 to open. Address is
// channel input, never an argument or a quoted command; the HTTP edge registers it for redaction.
type ChangeProposal struct {
	Request
	Kind, Identity, Repository string
	Address                    string
}

// ChangeWork is the change job, through the command's own typed execution port.
// Its result is the pull request address, never a URL extracted from output lines.
func ChangeWork(proposal ChangeProposal,
	open func(context.Context, ChangeProposal, func(string)) (string, error)) orchestration.JobWork {
	return func(ctx context.Context, line func(string)) (string, error) {
		return open(ctx, proposal, line)
	}
}

// AppChange is what one form asks for: the instance's records as the pinned commit states them, the form's own
// refusals and the flags v2's command takes. A problem comes with the status the console answers it with.
func (s Server) AppChange(ctx context.Context, instance pages.Instance, one Change, change string,
	fields map[string][]string, identity string) (*ChangeProposal, string, int) {
	tree, rows, row, err := s.AppRow(ctx, instance)
	if err != nil {
		return nil, err.Error(), http.StatusBadGateway
	}
	if row == nil || row.Inventory == nil {
		return nil, "its records are not in Git, so what it declares is unknown", http.StatusConflict
	}
	if problem, refused := appFormRefusal(change, one, instance.Env, row, rows); refused {
		return nil, problem, http.StatusBadRequest
	}
	teams, _ := TeamRegistry(tree)
	flags, err := pages.ConsoleFormChange(change, instance.App, fields, row.Inventory, teams)
	if err != nil {
		return nil, err.Error(), http.StatusBadRequest
	}
	if problem, refused := offered(change, instance.Env, instance.App, rows, tree, fields); refused {
		return nil, problem, http.StatusBadRequest
	}
	return &ChangeProposal{Kind: one.Name, Identity: identity, Repository: s.Repository,
		Request: Request{App: instance.App, Env: instance.Env, Settings: flags.Settings,
			Calls: flags.Calls, Subscriptions: flags.Subscriptions, DropCalls: flags.DropCalls,
			DropSubscriptions: flags.DropSubscriptions, Allows: flags.Allows, Disallows: flags.Disallows}}, "", 0
}

// appFormRefusal is what a change refuses before its fields are read: a change a page does not offer, an environment
// the kind does not name, and the two things a form cannot ask for at all. A promotion also needs production to exist
// and staging to be promotable, which is the cluster's answer rather than the form's.
func appFormRefusal(change string, one Change, env string, row *pages.AppRow,
	rows []pages.AppRow) (string, bool) {
	switch change {
	case "egress":
		return "the console offers no egress form: grant a host with `swhurl-v2 edit --app " + row.Instance.App +
			" --env=" + env + " --set egress=<host>` and run the render it asks for", true
	case "schedule":
		return "v2 does not model a schedule: the console offers no schedule form", true
	}
	if !one.Allows(env) {
		return change + " from the " + strings.Join(one.Envs, " or ") + " instance", true
	}
	if change != "promote" {
		return "", false
	}
	if !declaredIn(rows, row.Instance.App, "prod") {
		return "v2 has no first promotion: production has no records yet", true
	}
	if row.Report != nil && !row.Report.Promotion.Ready {
		return strings.ReplaceAll(row.Report.Promotion.Reason, "_", " "), true
	}
	return "", false
}

// declaredIn is whether one environment of an app has records the console could see.
func declaredIn(rows []pages.AppRow, app, env string) bool {
	for _, row := range rows {
		if row.Instance.App == app && row.Instance.Env == env && row.Inventory != nil {
			return true
		}
	}
	return false
}

// offered refuses a connection the form did not offer: a call to an app that is not a web app in that environment, or
// an event nothing there publishes. The lists are the page's own, read at the same pinned commit.
func offered(change, env, app string, rows []pages.AppRow, tree map[string][]byte,
	fields map[string][]string) (string, bool) {
	if change != "connect" {
		return "", false
	}
	connectable := pages.ConnectableTo(rows, Offers(tree, rows, env), env, app)
	callable := make(map[string]bool, len(connectable.Apps))
	for _, one := range connectable.Apps {
		callable[one.App] = true
	}
	published := make(map[string]bool, len(connectable.Events))
	for _, one := range connectable.Events {
		published[one.Name] = true
	}
	var missing []string
	for _, name := range SplitNames(fields["calls"]) {
		if !callable[name] {
			missing = append(missing, name)
		}
	}
	for _, name := range SplitNames(fields["subscribe"]) {
		if !published[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return strings.Join(missing, ", ") + ": not a web app, or an event, that staging has now; reload the form", true
	}
	return "", false
}

// SplitNames reads every comma-separated name in a checkbox form, dropping empty values.
func SplitNames(values []string) []string {
	var found []string
	for _, value := range values {
		for _, name := range strings.Split(value, ",") {
			if name = strings.TrimSpace(name); name != "" {
				found = append(found, name)
			}
		}
	}
	return found
}
