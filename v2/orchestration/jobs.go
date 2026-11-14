package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/samclement/swhurl-platform/v2/pages"
)

// The console's jobs: one process runs a change or a unit action and reports its lines, and every job's record is
// kept outside the pod so that a pod that replaces it shows what ran and calls a job whose pod is gone interrupted.
// One job per target runs at a time; a job's lines are its record. The bounds are the console's: the newest
// jobKept records, each cut from the front to jobRecordBytes, every line capped at jobLineChars. Nothing here prints,
// reads the clock or touches a cluster: the store, the clock and the audit sink are the caller's.

const (
	jobKept        = 30 // records kept outside the pod, well inside a ConfigMap's size
	jobRecordBytes = 20_000
	jobLineChars   = 1_000
	jobKeptMemory  = 50 // finished jobs kept in this process
	jobSaveEvery   = 2 * time.Second
	jobForeignTTL  = 5 * time.Second
	jobDrainPoll   = 500 * time.Millisecond
)

// earlierOutput replaces the lines a record had to drop: the end of a job's output is the part that says how it went.
const earlierOutput = "… earlier output is not kept"

// JobWork is one job's work. It reports its progress through line, returns the link it opened (a pull request's URL)
// or "", and an error if it failed: the error is the job's last line, in v2's own words.
type JobWork func(ctx context.Context, line func(string)) (link string, err error)

// JobStore is where a job's record outlives this process: the newest records as JSON documents, written by the pod
// that runs each job, and the pods that are still alive. A store is best effort: a record that cannot be read or kept
// never fails its job, so a console that cannot reach its ConfigMap still runs changes.
type JobStore interface {
	// Load is every record the store holds, as written; anything malformed is not a job.
	Load(ctx context.Context) ([]string, error)
	// Save writes each record, and deletes the records whose value is nil, in one change: two pods writing at once
	// cannot drop each other's keys.
	Save(ctx context.Context, records map[int]*string) error
	// Alive is whether the pod that ran a job still exists. An unreadable answer counts as alive.
	Alive(ctx context.Context, owner string) (bool, error)
}

// JobEvent is one state change of a job, as the registry tells it: what ran, for whom, how it ended and how long it
// took. State is "started", "succeeded", "failed" or "interrupted"; Duration is set once the job has ended, and Link
// is the pull request an ended job opened, if any. The registry prints nothing: the caller writes the event as its
// log line.
type JobEvent struct {
	ID       int
	Action   string
	Target   string
	Identity string
	State    string
	Link     string
	Duration time.Duration
}

// JobsInputs is what a registry of jobs is assembled from. Owner is the pod's name; Redact keeps a secret out of
// every line and record (nil redacts nothing); Audit receives each job's start and end, which reaches the pod's log.
type JobsInputs struct {
	Store  JobStore
	Owner  string
	Now    func() time.Time
	Sleep  func(time.Duration)
	Redact func(string) string
	Audit  func(JobEvent)
}

// Jobs is the console's registry of jobs: this process's own, and the records another pod left.
type Jobs struct {
	mu       sync.Mutex
	inputs   JobsInputs
	live     map[int]*pages.Job // this process's jobs, by id
	before   map[int]pages.Job  // records from the store: what ran before this process, or runs in another pod
	ids      int
	read     time.Time
	draining bool
}

// NewJobs is a registry with the records the store holds, and settles a job whose pod is gone.
func NewJobs(ctx context.Context, inputs JobsInputs) *Jobs {
	jobs := &Jobs{inputs: inputs, live: map[int]*pages.Job{}, before: map[int]pages.Job{}, ids: 1}
	jobs.load(ctx)
	jobs.settle(ctx)
	return jobs
}

// load reads the store's records into the registry's remembered jobs, keeping the next id above every one of them.
func (j *Jobs) load(ctx context.Context) {
	if j.inputs.Store == nil {
		return
	}
	records, err := j.inputs.Store.Load(ctx)
	if err != nil {
		return
	}
	for _, raw := range records {
		job, ok := decodeJob(raw)
		if !ok {
			continue
		}
		j.before[job.ID] = job
		j.ids = max(j.ids, job.ID+1)
	}
}

// settle calls a remembered job interrupted once the pod that ran it is gone, and records that it was.
func (j *Jobs) settle(ctx context.Context) {
	for id, job := range j.before {
		if !job.Running() || j.alive(ctx, job.Owner) {
			continue
		}
		finished := j.inputs.Now()
		job = job.Interrupted(finished)
		j.before[id] = job
		j.save(ctx, job, nil)
		j.audit(JobEvent{ID: job.ID, Action: job.Action, Target: job.Unit, Identity: job.Identity,
			State: job.State, Duration: finished.Sub(job.Started)})
	}
}

// alive is whether a job's pod still exists; an unreadable answer counts as alive, so a job is called interrupted
// only when its pod is known to be gone. No owner at all is no pod.
func (j *Jobs) alive(ctx context.Context, owner string) bool {
	if owner == "" || j.inputs.Store == nil {
		return false
	}
	alive, err := j.inputs.Store.Alive(ctx, owner)
	return err != nil || alive
}

// refresh reads again the jobs another pod is still running, no more often than jobForeignTTL: their output grows
// there, not here. Only a record this registry already remembers is read again, and none is while none of them runs.
func (j *Jobs) refresh(ctx context.Context) {
	if !j.foreignRunning() || j.inputs.Now().Sub(j.read) < jobForeignTTL {
		return
	}
	j.read = j.inputs.Now()
	records, err := j.inputs.Store.Load(ctx)
	if err != nil {
		return
	}
	for _, raw := range records {
		job, ok := decodeJob(raw)
		if _, remembered := j.before[job.ID]; !ok || !remembered || j.live[job.ID] != nil {
			continue
		}
		j.before[job.ID] = job
	}
	j.settle(ctx)
}

// foreignRunning is whether a remembered record is of a job still running: the only output that can have grown.
func (j *Jobs) foreignRunning() bool {
	for _, job := range j.before {
		if job.Running() {
			return true
		}
	}
	return false
}

// Start runs one job in the background and returns it as it started. A target that already has a running job is
// refused, as is every job while the pod is draining. The job keeps ctx's values but not its cancellation: a job
// outlives the request that started it, and only the work's own bounds end it.
func (j *Jobs) Start(ctx context.Context, action, target, identity string, resume map[string]any,
	work JobWork) (pages.Job, error) {
	j.mu.Lock()
	if j.draining {
		j.mu.Unlock()
		return pages.Job{}, errors.New("the console is restarting and takes no new jobs; try again in a minute")
	}
	j.refresh(ctx)
	if running := j.running(target); running != 0 {
		j.mu.Unlock()
		return pages.Job{}, fmt.Errorf("a job for %s is already running", target)
	}
	job := &pages.Job{ID: j.ids, Action: action, Unit: target, Identity: identity, Started: j.inputs.Now(),
		State: "running", Owner: j.inputs.Owner, Resume: resume, Live: true}
	j.ids++
	j.live[job.ID] = job
	j.forgetMemory()
	forget := j.forgetStore()
	started := *job
	j.mu.Unlock()
	j.save(ctx, started, forget)
	j.audit(JobEvent{ID: started.ID, Action: action, Target: target, Identity: identity, State: "started"})
	go j.run(context.WithoutCancel(ctx), job, work)
	return started, nil
}

// run performs one job's work, keeping its record as its output grows, then records how it ended.
func (j *Jobs) run(ctx context.Context, job *pages.Job, work JobWork) {
	link, err := work(ctx, j.liner(ctx, job))
	j.finish(ctx, job, link, err)
}

// liner is where a job's work reports its lines: each is redacted and capped, and the record is written as it grows,
// no more often than jobSaveEvery.
func (j *Jobs) liner(ctx context.Context, job *pages.Job) func(string) {
	var saved time.Time
	return func(text string) {
		j.mu.Lock()
		job.Lines = append(job.Lines, capLine(j.redact(text)))
		write := j.inputs.Now().Sub(saved) >= jobSaveEvery
		if write {
			saved = j.inputs.Now()
		}
		snapshot := *job
		j.mu.Unlock()
		if write {
			j.save(ctx, snapshot, nil)
		}
	}
}

// finish ends one job with its link and its failure, writes the record and audits it.
func (j *Jobs) finish(ctx context.Context, job *pages.Job, link string, err error) {
	j.mu.Lock()
	job.Link = link
	finished := j.inputs.Now()
	job.State, job.Finished = "succeeded", &finished
	if err != nil {
		job.State = "failed"
		job.Lines = append(job.Lines, capLine(j.redact("[ERROR] "+err.Error())))
	}
	ended := *job
	j.mu.Unlock()
	j.save(ctx, ended, nil)
	j.audit(JobEvent{ID: ended.ID, Action: ended.Action, Target: ended.Unit, Identity: ended.Identity,
		State: ended.State, Link: link, Duration: finished.Sub(ended.Started)})
}

// running is the id of the running job for one target, or 0.
func (j *Jobs) running(target string) int {
	for id, job := range j.live {
		if job.Unit == target && job.Running() {
			return id
		}
	}
	for id, job := range j.before {
		if job.Unit == target && job.Running() {
			return id
		}
	}
	return 0
}

// forgetMemory drops the oldest finished jobs beyond jobKeptMemory; a running job is never dropped.
func (j *Jobs) forgetMemory() {
	for len(j.live) > jobKeptMemory {
		oldest := 0
		for id, job := range j.live {
			if !job.Running() && (oldest == 0 || id < oldest) {
				oldest = id
			}
		}
		if oldest == 0 {
			return
		}
		delete(j.live, oldest)
	}
}

// forgetStore is the ids the store must drop to keep its newest jobKept records; a running job stays.
func (j *Jobs) forgetStore() []int {
	ids := make([]int, 0, len(j.before)+len(j.live))
	for id := range j.before {
		ids = append(ids, id)
	}
	for id := range j.live {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	if len(ids) <= jobKept {
		return nil
	}
	var forget []int
	for _, id := range ids[:len(ids)-jobKept] {
		if job, known := j.live[id]; known && job.Running() {
			continue
		}
		if job, known := j.before[id]; known && job.Running() {
			continue
		}
		forget = append(forget, id)
		delete(j.before, id)
	}
	return forget
}

// Get is one job by id: this process's own, or a record another pod left. A foreign record is read again, no more
// often than jobForeignTTL.
func (j *Jobs) Get(ctx context.Context, id int) (pages.Job, bool) {
	j.mu.Lock()
	if job, live := j.live[id]; live {
		found := *job
		j.mu.Unlock()
		return found, true
	}
	j.refresh(ctx)
	job, known := j.before[id]
	j.mu.Unlock()
	return job, known
}

// Recent is every job this registry knows: its own newest first, then the records newest first.
func (j *Jobs) Recent(ctx context.Context) []pages.Job {
	j.mu.Lock()
	j.refresh(ctx)
	found := make([]pages.Job, 0, len(j.live)+len(j.before))
	for _, job := range j.live {
		found = append(found, *job)
	}
	for _, job := range j.before {
		found = append(found, job)
	}
	j.mu.Unlock()
	slices.SortFunc(found, func(a, b pages.Job) int { return b.ID - a.ID })
	return found
}

// Reach records that a job's change was seen at its end, once: a later change may replace it, but this one ran.
func (j *Jobs) Reach(ctx context.Context, id int) {
	j.mu.Lock()
	job, live := j.live[id]
	if !live || job.Reached != "" {
		j.mu.Unlock()
		return
	}
	job.Reached = j.inputs.Now().UTC().Format(time.RFC3339Nano)
	reached := *job
	j.mu.Unlock()
	j.save(ctx, reached, nil)
}

// Drain stops taking jobs and waits for the running ones, for the pod that is stopping; false if some outlast the
// timeout. A job cut short is left running, and the pod that replaces this one calls it interrupted.
func (j *Jobs) Drain(timeout time.Duration) bool {
	j.mu.Lock()
	j.draining = true
	j.mu.Unlock()
	deadline := j.inputs.Now().Add(timeout)
	for {
		if !j.anyRunning() {
			return true
		}
		if !j.inputs.Now().Before(deadline) {
			return false
		}
		j.inputs.Sleep(jobDrainPoll)
	}
}

// anyRunning is whether this process still runs a job.
func (j *Jobs) anyRunning() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, job := range j.live {
		if job.Running() {
			return true
		}
	}
	return false
}

// save writes one job's record and deletes the ones the store no longer keeps. A failure is not the job's failure:
// the record is for the operator reading the page afterwards, never the effect the job performs.
func (j *Jobs) save(ctx context.Context, job pages.Job, forget []int) {
	if j.inputs.Store == nil {
		return
	}
	records := map[int]*string{job.ID: recordOf(job, j.inputs.Redact)}
	for _, id := range forget {
		records[id] = nil
	}
	_ = j.inputs.Store.Save(ctx, records)
}

// audit reports one event to the pod's log, or nowhere when the caller keeps no log.
func (j *Jobs) audit(event JobEvent) {
	if j.inputs.Audit != nil {
		j.inputs.Audit(event)
	}
}

// redact keeps a secret out of a job's lines and records.
func (j *Jobs) redact(text string) string {
	if j.inputs.Redact == nil {
		return text
	}
	return j.inputs.Redact(text)
}

// capLine keeps at most jobLineChars characters of one line, as the console always has.
func capLine(text string) string {
	if len([]rune(text)) <= jobLineChars {
		return text
	}
	return string([]rune(text)[:jobLineChars])
}

// recordOf is one job as its record states it, its output cut from the front until the record fits: the end of a
// job's output is the part that says how it went.
func recordOf(job pages.Job, redact func(string) string) *string {
	lines := make([]string, len(job.Lines))
	for i, line := range job.Lines {
		if redact != nil {
			line = redact(line)
		}
		lines[i] = capLine(line)
	}
	for {
		raw, err := json.Marshal(recordDocumentOf(job, lines))
		if err != nil || len(raw) <= jobRecordBytes || len(lines) <= 1 {
			text := string(raw)
			return &text
		}
		lines = append([]string{earlierOutput}, lines[max(2, len(lines)/4):]...)
	}
}

// recordDocumentOf is one record's fields, in the order the console writes them.
func recordDocumentOf(job pages.Job, lines []string) jobRecordDocument {
	record := jobRecordDocument{ID: job.ID, Action: job.Action, Unit: job.Unit, Identity: job.Identity,
		Started: job.Started.Format(time.RFC3339Nano), State: job.State, Link: job.Link, Owner: job.Owner,
		Resume: job.Resume, Reached: job.Reached, Lines: lines}
	if lines == nil {
		record.Lines = []string{}
	}
	if job.Finished != nil {
		finished := job.Finished.Format(time.RFC3339Nano)
		record.Finished = &finished
	}
	return record
}

// jobRecordDocument is a job's stored form: the keys the console has always written, which a pod running the other
// implementation reads as well.
type jobRecordDocument struct {
	ID       int            `json:"id"`
	Action   string         `json:"action"`
	Unit     string         `json:"unit"`
	Identity string         `json:"identity"`
	Started  string         `json:"started"`
	Finished *string        `json:"finished"`
	State    string         `json:"state"`
	Link     string         `json:"link"`
	Owner    string         `json:"owner"`
	Resume   map[string]any `json:"resume"`
	Reached  string         `json:"reached"`
	Lines    []string       `json:"lines"`
}
