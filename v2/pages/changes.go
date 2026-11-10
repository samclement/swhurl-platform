package pages

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// How far a change has got, for Activity, a job's page and an app's change list (docs/console.md#activity).
//
// A change is more than its job: a promotion's job ends when its pull request is open, and the change ends when
// production runs it. ProgressOf is the one answer to how far a merged change to an instance has got, from Git's
// facts (Render) and what the cluster shows (AppRow), read fresh every time. Proven absence, a render main has not
// made yet and a failed read are different facts: no failed read proves absence or completion. The one fact kept
// is when the change was first seen at its end (Job.Reached), so a later change can replace it without unsaying
// that it ran.
//
// A Track presents that answer as labelled stages, the last always the terminal stop (Live in prod, Applied,
// Removed from staging), drawn from the start so the outcome is known before it is reached.

// The jobs that create an app, and the action in their pull request's branch.
const (
	NewAppJob    = "new app and repository"
	FinishAppJob = "finish new app"
	onMain       = "On main"
	noteChars    = 160
)

// Stage states, as the mark each is drawn with and the words a screen reader gets.
var stageMarks = map[string][2]string{"done": {"healthy", "done"}, "running": {"updating", "running now"},
	"failed": {"failing", "failed"}, "waiting": {"suspended", "waiting for you"}, "stopped": {"stopped", "stopped"},
	"todo": {"todo", "not reached"}, "reached": {"reached", "reached"}, "planned": {"planned", "where it ends, not reached"}}

// A whole change's status as the mark and word that lead its row; blocked is failed but not over (its pull request
// is still open, or the instance it reached is failing).
var trackStatus = map[string][2]string{"running": {"updating", "In progress"}, "waiting": {"suspended", "Waiting for you"},
	"done": {"healthy", ""}, "failed": {"failing", "Failed"}, "interrupted": {"failing", "Interrupted"},
	"stopped": {"stopped", "Closed"}, "unknown": {"unknown", "Unknown"}, "blocked": {"failing", "Failed"}}

// Stage is one step of a change's path.
type Stage struct {
	Key, Label string
	State      string // a key of stageMarks
	Note, Link string
}

// Mark is the stage's mark.
func (s Stage) Mark() string { return stageMarks[s.State][0] }

// Words is the stage's state for a screen reader.
func (s Stage) Words() string { return stageMarks[s.State][1] }

// Track is a change's whole path as the console shows it.
type Track struct {
	Stages  []Stage
	Status  string // a key of trackStatus
	Outcome string // one sentence: what happens next, or how it ended
}

// Mark is the mark leading the change's row.
func (t Track) Mark() string { return trackStatus[t.Status][0] }

// Label is the word leading the row: for a finished change, where it ended.
func (t Track) Label() string {
	if word := trackStatus[t.Status][1]; word != "" {
		return word
	}
	return t.Stages[len(t.Stages)-1].Label
}

// Settled is whether nothing more will happen to it without someone starting something.
func (t Track) Settled() bool {
	switch t.Status {
	case "done", "failed", "interrupted", "stopped":
		return true
	}
	return false
}

// Pull is what a track knows of a job's pull request.
type Pull struct {
	Number    int
	URL       string
	State     string // open, merged, closed, or unknown (it could not be read)
	Checks    string // Validate on its head: passed, failed, running, pending, conflict, or empty (not read)
	ChecksURL string
	Plan      string // why it needs a person beyond merging it, from its body (empty when nothing else)
	When      string // when it was merged or closed, as words ("3 h ago")
	Commit    string // the commit that merged it into main
}

// OpenPull is an open console pull request as a change's row shows it.
type OpenPull struct {
	Number                     int
	Title, URL, Branch, Opened string
	Body, Checks, ChecksURL    string
}

// PullOf is an open pull request as the track reads it.
func PullOf(open OpenPull) Pull {
	checksURL := open.ChecksURL
	if checksURL == "" {
		checksURL = open.URL
	}
	return Pull{Number: open.Number, URL: open.URL, State: "open", Checks: open.Checks, ChecksURL: checksURL,
		Plan: MergePlan(open.Body)}
}

// Render is an instance in one Git tree, as facts. No records (the zero value) is proven absence once the tree was
// read. Records and manifests land in one commit, so a HelmRelease whose image differs from its records is
// unrendered.
type Render struct {
	Records  string // a hash of its records and committed HelmRelease: the requested change, render included
	Settings string // a hash of its app record and instance record without the release: what the platform decides
	Digest   string // the image digest its release record requests
	Release  bool   // whether a HelmRelease is committed for it
	Runs     string // the image digest its HelmRelease runs
}

// Pending is whether main has records it has not rendered: no HelmRelease, or one with another image.
func (t Render) Pending() bool {
	return t.Records != "" && (!t.Release || t.Runs != t.Digest)
}

// Progress is how far a merged change to an instance has got: the facts after its merge, never saved.
type Progress struct {
	State   string // live, zero, removed; replaced, unchecked, superseded, historical; creating, applying, removing; failing, held, unread
	Detail  string // for replaced and unchecked: when it was first seen at its end
	Applied bool   // Flux applied its render: the instance is Ready at its generation with the target image
	Newer   string // carried by main's newer release: its image digest, or "-" when only the render differs
}

// Reached is whether the progress is where a change ends; Job.Reached records the first time it was seen there. A
// change seen there and not read again since is still there.
func (p Progress) Reached() bool {
	return p.State == "live" || p.State == "zero" || p.State == "removed" || p.State == "unchecked"
}

// actionKind is a console branch's action in the stage vocabulary: v2's scale is still a scale.
func actionKind(action string) string { return strings.TrimPrefix(action, "v2-") }

// Observed is an instance as the cluster was read: Read is false when the cluster could not be read, and Row is nil
// when it was read without the instance (proven absent).
type Observed struct {
	Read bool
	Row  *AppRow
}

func (o Observed) absent() bool { return o.Read && o.Row == nil }

// ProgressOf is how far the change action merged has got. merged and main are the instance in the merge commit's
// tree and in main's (the zero Render when proven absent, nil when the tree could not be read); live is the
// instance on the cluster.
//
// Main's render is this change's while main keeps the records it merged, and carries it when only a newer release
// arrived (same settings: the Deploy workflow). A promotion chooses its release, so a newer one replaces it. reached
// is when the change was first seen at its end: a replaced change still ran.
func ProgressOf(action string, merged, main *Render, live Observed, reached string) Progress {
	switch {
	case merged == nil:
		return unchecked(reached, Progress{State: "unread", Detail: "the target or current render could not be read"})
	case merged.Records == "" && action != "remove":
		return Progress{State: "historical", Detail: "its merge commit has no records for this instance"}
	case main == nil:
		return unchecked(reached, Progress{State: "unread", Detail: "the target or current render could not be read"})
	}
	target, newer, replaced := carried(action, *merged, *main)
	if replaced {
		return ranOr(reached, Progress{State: "superseded"})
	}
	found := Landing(action, live, target)
	if found.State == "unread" {
		found = unchecked(reached, found)
	}
	if newer == "" {
		return found
	}
	if !found.Reached() { // once it ran, what happens to the newer release is not this change's
		return ranOr(reached, Progress{State: found.State, Detail: found.Detail, Applied: found.Applied, Newer: newer})
	}
	found.Newer = newer
	return found
}

// carried is the render the cluster must reach for a merged change: main's while main keeps its records, or carries
// it with a newer release (newer is that release's digest, or "-" when only the render differs). replaced is
// whether main replaced it instead.
func carried(action string, merged, main Render) (target Render, newer string, replaced bool) {
	switch {
	case merged.Records == main.Records:
		return main, "", false
	case action == "promote" || merged.Settings == "" || merged.Settings != main.Settings:
		return Render{}, "", true
	case main.Digest != merged.Digest:
		return main, main.Digest, false
	}
	return main, "-", false
}

// ranOr is a change's progress once it was seen at its end (reached), and otherwise what it is now.
func ranOr(reached string, otherwise Progress) Progress {
	if reached != "" {
		return Progress{State: "replaced", Detail: reached}
	}
	return otherwise
}

// unchecked is a change's progress when the read that would decide it failed: one already seen at its end stays
// there, and its sentence says it could not be checked again. A failed read never undoes what an earlier one found.
func unchecked(reached string, otherwise Progress) Progress {
	if reached != "" {
		return Progress{State: "unchecked", Detail: reached}
	}
	return otherwise
}

// Landing is how far the cluster has got to target, main's render of the change.
func Landing(action string, live Observed, target Render) Progress {
	switch {
	case target.Pending():
		return Progress{State: "applying", Detail: "the HelmRelease does not match the records yet"}
	case !live.Read:
		return Progress{State: "unread", Detail: "the cluster could not be read"}
	case actionKind(action) == "remove" && target.Records == "" && live.absent():
		return Progress{State: "removed"}
	case actionKind(action) == "remove":
		return Progress{State: "removing"}
	case live.absent():
		return Progress{State: "creating", Detail: "not created yet"}
	case live.Row.Report == nil:
		return Progress{State: "unread", Detail: "Go observation unavailable"}
	}
	return running(live.Row.State)
}

// running is how far an instance on the cluster has got, from Go's state of it.
func running(state State) Progress {
	switch state.Key {
	case StateFailing:
		return Progress{State: "failing", Detail: state.Detail}
	case StateSuspended:
		return Progress{State: "held", Detail: state.Detail}
	case StateStopped:
		return Progress{State: "zero", Applied: true}
	case StateHealthy:
		return Progress{State: "live", Applied: true}
	case StateUnknown:
		return Progress{State: "unread", Detail: or(state.Detail, "the instance could not be read")}
	}
	return Progress{State: "applying", Detail: or(state.Detail, "waiting for Go status to be Healthy")}
}

func or(value, otherwise string) string {
	if value != "" {
		return value
	}
	return otherwise
}

// mergedWords is how a merged change's progress reads: the track's status, the sentence under it, and the note on
// its end once there (absent: not there). A lookup table, not a second state model.
var mergedWords = map[string]struct {
	Status, Said string
	There        *string
}{
	"live":      {"done", "{target} runs it{newer}.", ptr("")},
	"zero":      {"done", "{target} is applied and scaled to zero.", ptr("scaled to zero")},
	"removed":   {"done", "{target} is removed.", ptr("")},
	"replaced":  {"done", "{end} from {seen}; a newer configuration has since replaced it.", ptr("then replaced")},
	"unchecked": {"done", "{end} from {seen}{newer}; a later read could not check it again.", ptr("not re-read")},
	"removing":  {"running", "Merged; Flux is removing {target}.", nil},
	"creating":  {"running", "Merged; waiting for Flux to create {target}.", nil},
	"applying":  {"running", "Merged; Flux is applying it to {target}.", nil},
	"failing":   {"blocked", "Merged, but {target} is failing: open its page.", nil},
	"held":      {"waiting", "Merged, but {target} is suspended: open its page.", nil},
}

func ptr(text string) *string { return &text }

// stageOf is a track's status as a stage's state.
var stageOf = map[string]string{"done": "done", "running": "running", "blocked": "failed", "waiting": "waiting"}

var pullLink = regexp.MustCompile(`/pull/(\d+)$`)

// PullNumber is the pull request a job opened, from its link; zero when it opened none.
func PullNumber(job Job) int {
	found := pullLink.FindStringSubmatch(job.Link)
	if found == nil {
		return 0
	}
	number, _ := strconv.Atoi(found[1])
	return number
}

// jobError is why a job stopped, from its output.
func jobError(job Job) string {
	if job.State == "interrupted" {
		return "the console restarted before it finished"
	}
	said := ""
	for i := len(job.Lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(job.Lines[i], "[ERROR] ") {
			said = strings.TrimPrefix(job.Lines[i], "[ERROR] ")
			break
		}
	}
	if runes := []rune(said); len(runes) > noteChars {
		return strings.TrimRight(string(runes[:noteChars]), " \t\n\r\v\f") + "…"
	}
	return said
}

// Prepared is the job's own stage; its output is one chronological log under it.
func Prepared(job Job) Stage {
	state := "failed"
	if job.State == "succeeded" {
		state = "done"
	} else if job.State == "running" {
		state = "running"
	}
	note := ""
	if state == "failed" {
		note = jobError(job)
	} else if state == "done" && PullNumber(job) != 0 {
		note = "PR opened"
	}
	return Stage{Key: "prepared", Label: "Prepared", State: state, Note: note}
}

// endLabel is where a change to target ends once its pull request is merged and applied.
func endLabel(action, target string) string {
	_, env, found := strings.Cut(target, "/")
	if !found || env == "" || strings.HasPrefix(target, "team:") {
		return onMain
	}
	if actionKind(action) == "remove" {
		return "Removed from " + env
	}
	return "Live in " + env
}

// Lands is whether a merged pull request's change ends on the cluster rather than on main.
func Lands(action, target string) bool { return endLabel(action, target) != onMain }

// landingStages are the stages after a merge, not reached yet: Flux applying it and, unless it removes, the instance
// running it.
func landingStages(action, end string) []Stage {
	if end == onMain {
		return nil
	}
	found := []Stage{{Key: "rollout", Label: "Applied", State: "todo"}}
	if actionKind(action) != "remove" {
		found = append(found, Stage{Key: "running", Label: "Running", State: "todo"})
	}
	return found
}

// seen is when a change was first seen at its end, as words.
func seen(when string) string {
	at, err := parseISO(when)
	if err != nil {
		return "an earlier read"
	}
	return fmt.Sprintf("%d %s", at.Day(), at.Format("Jan 15:04 UTC"))
}

// parseISO reads a time as Python's isoformat writes it, with or without an offset.
func parseISO(when string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999"} {
		if at, err := time.Parse(layout, when); err == nil {
			return at, nil
		}
	}
	return time.Time{}, fmt.Errorf("not a time: %q", when)
}

func lowerFirst(text string) string { return strings.ToLower(text[:1]) + text[1:] }

// pullPath is the stages after a job's pull request: Reviewed, Merged, Flux applying it and the terminal stop.
type pullPath struct {
	pull           Pull
	action, target string
	end            string
	instance       bool // whether the change ends on the cluster rather than on main
}

// afterPull is the stages after a pull request, with the track's status and outcome. landed is how far a merged
// change to an instance has got; nil when it was not judged.
func afterPull(pull Pull, action, target string, landed *Progress) ([]Stage, string, string) {
	action = actionKind(action)
	end := endLabel(action, target)
	path := pullPath{pull: pull, action: action, target: target, end: end, instance: end != onMain}
	switch pull.State {
	case "open":
		return path.open()
	case "merged":
		return path.merged(landed)
	case "closed":
		return path.closed()
	}
	return path.track(Stage{Key: "merge", Label: "Merged", State: "todo", Link: pull.URL}, "unknown",
		path.name()+" could not be read from GitHub: open it there.", path.checks(), nil)
}

func (p pullPath) name() string { return "#" + strconv.Itoa(p.pull.Number) }

// track is the path's stages: rest, or by default Flux applying it and the planned end.
func (p pullPath) track(merge Stage, status, outcome string, validate Stage, rest []Stage) ([]Stage, string, string) {
	if rest == nil {
		rest = append(landingStages(p.action, p.end), Stage{Key: "end", Label: p.end, State: "planned"})
	}
	return append([]Stage{validate, merge}, rest...), status, outcome
}

// checks is Validate on the pull request's head, as the Reviewed stage.
func (p pullPath) checks() Stage {
	stage := Stage{Key: "validate", Label: "Reviewed", State: "todo", Link: p.pull.ChecksURL}
	switch p.pull.Checks {
	case "passed":
		stage.State = "done"
	case "failed":
		stage.State, stage.Note = "failed", "its checks failed"
	case "running":
		stage.State = "running"
	case "pending":
		stage.State, stage.Note = "running", "queued"
	}
	return stage
}

func (p pullPath) reviewed() Stage {
	return Stage{Key: "validate", Label: "Reviewed", State: "done", Link: p.pull.ChecksURL}
}

func (p pullPath) open() ([]Stage, string, string) {
	switch p.pull.Checks {
	case "conflict":
		return p.track(Stage{Key: "merge", Label: "Merged", State: "waiting", Note: "merge conflict", Link: p.pull.URL}, "waiting",
			"Waiting for you: "+p.name()+" conflicts with main. Resolve it on the pull request, or close it and start again.", p.checks(), nil)
	case "failed":
		return p.track(Stage{Key: "merge", Label: "Merged", State: "todo", Link: p.pull.URL}, "blocked",
			"Validate failed on "+p.name()+": nothing is merged. Fix it on the pull request, or close it.", p.checks(), nil)
	}
	why := ""
	if p.pull.Plan != "" {
		why = " (" + p.pull.Plan + ")"
	}
	then := "it is then on main"
	if p.instance {
		then = "then " + lowerFirst(p.end)
	}
	return p.track(Stage{Key: "merge", Label: "Merged", State: "waiting", Note: "you merge it", Link: p.pull.URL}, "waiting",
		"Waiting for you: merge "+p.name()+" on GitHub"+why+"; "+then+".", p.checks(), nil)
}

func (p pullPath) closed() ([]Stage, string, string) {
	// A new app's repository and image were made before its pull request: closing that leaves them.
	left := "nothing changed"
	if p.action == NewAppJob || p.action == FinishAppJob || p.action == "new" {
		left = "no instance was added, but the repository and image made for it still exist"
	}
	return p.track(Stage{Key: "merge", Label: "Closed without merging", State: "stopped", Note: p.pull.When, Link: p.pull.URL},
		"stopped", p.name()+" was closed without merging: "+left+".", Stage{Key: "validate", Label: "Reviewed", State: "todo"}, []Stage{})
}

func (p pullPath) merged(landed *Progress) ([]Stage, string, string) {
	merge := Stage{Key: "merge", Label: "Merged", State: "done", Note: strings.TrimSpace("merged " + p.pull.When), Link: p.pull.URL}
	if !p.instance {
		return p.track(merge, "done", strings.ReplaceAll("Merged "+p.pull.When+".", " .", "."), p.reviewed(),
			[]Stage{{Key: "end", Label: p.end, State: "reached"}})
	}
	found := Progress{State: "unread", Detail: "it was not read"}
	if landed != nil {
		found = *landed
	}
	switch found.State {
	case "historical":
		return p.track(merge, "done", strings.ReplaceAll("Merged "+p.pull.When+"; this change predates render hashes, so its rollout cannot be tracked.", " ;", ";"),
			p.reviewed(), []Stage{{Key: "end", Label: "Merged (historical)", State: "reached"}})
	case "superseded":
		return p.track(merge, "done", strings.ReplaceAll("Merged "+p.pull.When+"; a newer configuration of "+p.target+
			" replaced it before it was seen "+lowerFirst(p.end)+".", " ;", ";"), p.reviewed(),
			[]Stage{{Key: "end", Label: "Superseded", State: "reached"}})
	}
	words, known := mergedWords[found.State]
	if !known {
		return p.track(merge, "unknown", "Merged; whether "+p.target+" runs it is not known: "+found.Detail+".", p.reviewed(), nil)
	}
	return p.track(merge, words.Status, p.landedOutcome(words.Said, found), p.reviewed(), p.landedStages(words.Status, words.There, found))
}

// landedOutcome is the sentence under a merged change that reached the cluster.
func (p pullPath) landedOutcome(said string, found Progress) string {
	newer := ""
	if found.Newer == "-" {
		newer = ", with later changes from main"
	} else if found.Newer != "" {
		newer = ", with a newer image"
	}
	return strings.NewReplacer("{target}", p.target, "{end}", p.end, "{seen}", seen(found.Detail), "{newer}", newer).Replace(said)
}

// landedStages are Applied, Running (unless it removes) and the end, as far as the merged change got: applied and
// running are separate facts.
func (p pullPath) landedStages(status string, there *string, found Progress) []Stage {
	ended := found.Reached()
	got := found.Applied || ended // Flux applied it, whatever has happened since
	state, note := stageOf[status], found.Detail
	if ended {
		note = ""
	}
	rollout := Stage{Key: "rollout", Label: "Applied", State: "done"}
	if !got {
		rollout.State, rollout.Note = state, note
	}
	stages := []Stage{rollout}
	if p.action != "remove" {
		running := Stage{Key: "running", Label: "Running", State: "todo", Note: note}
		if got {
			running.State = state
		}
		stages = append(stages, running)
	}
	if there == nil {
		return append(stages, Stage{Key: "end", Label: p.end, State: "planned"})
	}
	return append(stages, Stage{Key: "end", Label: p.end, State: "reached", Note: *there})
}

// TrackOf is the whole path of the change job started.
func TrackOf(job Job, pull *Pull, landed *Progress) Track {
	first := Prepared(job)
	end, flux := jobEnd(job)
	switch {
	case job.State != "succeeded":
		return unfinished(job, first, end, flux)
	case flux:
		return Track{Stages: []Stage{first, {Key: "end", Label: end, State: "reached", Note: appliedRevision(job.Lines)}},
			Status: "done", Outcome: end + "."}
	case pull == nil: // a dry run, or a job that had nothing to propose
		return Track{Stages: []Stage{first, {Key: "end", Label: "No pull request opened", State: "reached"}}, Status: "done",
			Outcome: "No pull request was opened."}
	}
	rest, status, outcome := afterPull(*pull, job.Action, job.Unit, landed)
	return Track{Stages: append([]Stage{first}, rest...), Status: status, Outcome: outcome}
}

// jobEnd is where a job's change ends, and whether the job is a Flux action that ends inside the console.
func jobEnd(job Job) (string, bool) {
	switch job.Action {
	case "reconcile", "resume":
		return "Applied", true
	case "suspend":
		return "Suspended", true
	}
	return endLabel(job.Action, job.Unit), false
}

// unfinished is the track of a job still running, or one that stopped before it ended.
func unfinished(job Job, first Stage, end string, flux bool) Track {
	// What follows the job, not reached while it runs or once it has failed.
	var after []Stage
	if !flux {
		after = append([]Stage{{Key: "validate", Label: "Reviewed", State: "todo"}, {Key: "merge", Label: "Merged", State: "todo"}},
			landingStages(job.Action, end)...)
	}
	planned := Stage{Key: "end", Label: end, State: "planned"}
	if job.State == "running" {
		ends := end
		if !flux {
			ends = end + ", once its pull request is merged"
		}
		return Track{Stages: append(append([]Stage{first}, after...), planned), Status: "running",
			Outcome: "Now: " + first.Label + ". Ends: " + ends + "."}
	}
	status := "failed"
	if job.State == "interrupted" {
		status = "interrupted"
	}
	shown := Stage{Key: first.Key, Label: first.Label, State: first.State} // the reason is the sentence under it
	return Track{Stages: append(append([]Stage{shown}, after...), planned), Status: status,
		Outcome: "Stopped at " + first.Label + ": " + or(first.Note, "see its output") + "."}
}

// appliedRevision is the revision a Flux job's output says it applied.
func appliedRevision(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		if rest, found := strings.CutPrefix(lines[i], "✔ applied revision "); found {
			return rest
		}
	}
	return ""
}

// PullTrack is the path of an open pull request whose job is no longer remembered.
func PullTrack(pull Pull, branch string) Track {
	action, target := BranchTarget(branch)
	rest, status, outcome := afterPull(pull, action, target, nil)
	return Track{Stages: append([]Stage{{Key: "pull", Label: "Pull request #" + strconv.Itoa(pull.Number), State: "done",
		Link: pull.URL}}, rest...), Status: status, Outcome: outcome}
}

// BranchTarget is a console pull request's action and what it changes (<app>/<env> or team:<name>), from its
// branch console/<action>-<name>[-<env>]-<commit>; both empty for any other branch. The kinds are the one table in
// console_change.go, which the command that opens a pull request reads too, so a branch and a change cannot disagree.
func BranchTarget(branch string) (action, target string) {
	change, name, env, ok := ParseConsoleBranch(branch)
	if !ok {
		return "", ""
	}
	return change.Name, change.Target(name, env)
}

var mergePlan = regexp.MustCompile(`(?m)^\*\*Merge plan:\*\* it (.+)\.$`)

// MergePlan is what a person needs before merging a pull request, from its own body: empty when merging is all.
func MergePlan(body string) string {
	if found := mergePlan.FindStringSubmatch(body); found != nil {
		return found[1]
	}
	return ""
}

// MergeMode is how an open console pull request merges: a person, and what it needs first when its body says.
func MergeMode(body string) string {
	if plan := MergePlan(body); plan != "" {
		return "You merge it on GitHub; it " + plan
	}
	return "You merge it on GitHub"
}

// ChangeRow is one change on Activity: a job with what became of its pull request, or an open pull request whose
// job is no longer remembered.
type ChangeRow struct {
	ID    string
	Track Track
	Job   *Job
	PR    *OpenPull // its pull request while it is open
}
