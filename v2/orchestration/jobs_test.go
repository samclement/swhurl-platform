package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/pages"
)

// jobRecords is a job store in memory: the records as written, which pods are alive, and every save it received.
type jobRecords struct {
	mu      sync.Mutex
	records map[int]string
	alive   map[string]bool
	saves   []map[int]*string
	loads   int
	loadErr error
	saveErr error
}

func newJobRecords() *jobRecords {
	return &jobRecords{records: map[int]string{}, alive: map[string]bool{}}
}

func (s *jobRecords) Load(context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads++
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	found := make([]string, 0, len(s.records))
	for _, raw := range s.records {
		found = append(found, raw)
	}
	return found, nil
}

func (s *jobRecords) Save(_ context.Context, records map[int]*string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves = append(s.saves, maps.Clone(records))
	if s.saveErr != nil {
		return s.saveErr
	}
	for id, raw := range records {
		if raw == nil {
			delete(s.records, id)
			continue
		}
		s.records[id] = *raw
	}
	return nil
}

func (s *jobRecords) Alive(_ context.Context, owner string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.alive[owner], nil
}

// saved is every save the store received, oldest first.
func (s *jobRecords) saved() []map[int]*string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.saves)
}

// holds is whether the store still holds one job's record.
func (s *jobRecords) holds(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, known := s.records[id]
	return known
}

// kept is the record the store holds for one job, decoded.
func (s *jobRecords) kept(t *testing.T, id int) pages.Job {
	t.Helper()
	s.mu.Lock()
	raw, known := s.records[id]
	s.mu.Unlock()
	if !known {
		t.Fatalf("no record for job %d", id)
	}
	job, ok := decodeJob(raw)
	if !ok {
		t.Fatalf("record %q is not a job", raw)
	}
	return job
}

// auditLog is the registry's audit events, read while a job may still be writing them.
type auditLog struct {
	mu     sync.Mutex
	events []JobEvent
}

// add keeps one event.
func (a *auditLog) add(event JobEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, event)
}

// all is the events so far.
func (a *auditLog) all() []JobEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.events)
}

// jobInputs is a registry with a fixed clock, a real pause and its audit events collected.
func jobInputs(store *jobRecords) (JobsInputs, *auditLog) {
	audit := &auditLog{}
	now := time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC)
	return JobsInputs{Store: store, Owner: "console-pod", Now: func() time.Time { return now },
		Sleep: time.Sleep, Audit: audit.add}, audit
}

// await is one job at the state a test waits for.
func await(t *testing.T, jobs *Jobs, id int, state string) pages.Job {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if job, known := jobs.Get(context.Background(), id); known && job.State == state {
			return job
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("job %d never reached %s", id, state)
	return pages.Job{}
}

// A job runs once per target, keeps the lines its work reports, its link, and its record.
func TestJobsRunOneJobPerTargetAndRecordItsLines(t *testing.T) {
	store := newJobRecords()
	inputs, audit := jobInputs(store)
	jobs := NewJobs(context.Background(), inputs)
	release := make(chan struct{})
	work := func(_ context.Context, line func(string)) (string, error) {
		line("first")
		<-release
		line("second")
		return "https://github.com/owner/repo/pull/42", nil
	}
	started, err := jobs.Start(context.Background(), "reconcile", "gw", "sam@swhurl.com", nil, work)
	if err != nil || started.State != "running" || started.ID != 1 || !started.Live {
		t.Fatalf("started %+v, %v", started, err)
	}
	if _, err := jobs.Start(context.Background(), "suspend", "gw", "sam@swhurl.com", nil, work); err == nil ||
		err.Error() != "a job for gw is already running" {
		t.Fatalf("a second job for the same target: %v", err)
	}
	close(release)
	ended := await(t, jobs, 1, "succeeded")
	if strings.Join(ended.Lines, ",") != "first,second" || ended.Link != "https://github.com/owner/repo/pull/42" {
		t.Fatalf("job %+v", ended)
	}
	if kept := store.kept(t, 1); kept.State != "succeeded" || len(kept.Lines) != 2 || kept.Owner != "console-pod" {
		t.Fatalf("record %+v", kept)
	}
	lines := audit.all()
	if len(lines) != 2 || lines[0].State != "started" || lines[1].State != "succeeded" ||
		lines[1].ID != 1 || lines[1].Action != "reconcile" || lines[1].Target != "gw" ||
		lines[1].Link != "https://github.com/owner/repo/pull/42" {
		t.Fatalf("audit %+v", lines)
	}
}

// A failed job's error is its last line, and the audit says it failed.
func TestAJobsErrorIsItsLastLine(t *testing.T) {
	store := newJobRecords()
	inputs, audit := jobInputs(store)
	jobs := NewJobs(context.Background(), inputs)
	work := func(_ context.Context, line func(string)) (string, error) {
		line("requesting")
		return "", errors.New("Kustomization gw reconciliation failed: health check failed")
	}
	if _, err := jobs.Start(context.Background(), "resume", "gw", "sam@swhurl.com", nil, work); err != nil {
		t.Fatal(err)
	}
	failed := await(t, jobs, 1, "failed")
	if failed.Lines[len(failed.Lines)-1] != "[ERROR] Kustomization gw reconciliation failed: health check failed" {
		t.Fatalf("lines %q", failed.Lines)
	}
	lines := audit.all()
	if last := lines[len(lines)-1]; last.State != "failed" || last.ID != 1 {
		t.Fatalf("audit %+v", lines)
	}
}

// A record keeps the newest jobKept jobs and deletes the ones beyond it: the oldest leaves the store when the new
// job's own record is written, in the same change.
func TestJobsKeepTheNewestRecordsOnly(t *testing.T) {
	store := newJobRecords()
	for id := 1; id <= jobKept; id++ {
		store.records[id] = *recordOf(pages.Job{ID: id, Action: "suspend", Unit: "gw", Identity: "x",
			Started: time.Now(), State: "succeeded"}, nil)
	}
	inputs, _ := jobInputs(store)
	jobs := NewJobs(context.Background(), inputs)
	started, err := jobs.Start(context.Background(), "reconcile", "gw", "sam@swhurl.com", nil,
		func(context.Context, func(string)) (string, error) { return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	await(t, jobs, started.ID, "succeeded")
	saves := store.saved()
	deleted := slices.ContainsFunc(saves, func(save map[int]*string) bool {
		raw, deleting := save[1]
		return deleting && raw == nil
	})
	if !deleted || store.holds(1) {
		t.Fatalf("the oldest record was not deleted: %v", saves)
	}
	if !store.holds(started.ID) || !store.holds(jobKept) {
		t.Fatalf("the newest records were not kept: %v", saves)
	}
}

// A record is cut from the front until it fits, and every line is capped: the end says how the job went.
func TestAJobsRecordIsCutFromTheFront(t *testing.T) {
	lines := make([]string, 50)
	for i := range lines {
		lines[i] = strings.Repeat(string(rune('a'+i%26)), 2_000)
	}
	lines[len(lines)-1] = "the end"
	raw := recordOf(pages.Job{ID: 1, Action: "reconcile", Unit: "gw", Identity: "x", Started: time.Now(),
		State: "succeeded", Lines: lines}, nil)
	if len(*raw) > jobRecordBytes {
		t.Fatalf("record is %d bytes", len(*raw))
	}
	job, ok := decodeJob(*raw)
	if !ok {
		t.Fatal("the record does not decode")
	}
	if job.Lines[0] != earlierOutput || job.Lines[len(job.Lines)-1] != "the end" {
		t.Fatalf("lines %d..%q", len(job.Lines), job.Lines[0])
	}
	for _, line := range job.Lines {
		if len([]rune(line)) > jobLineChars {
			t.Fatalf("a line of %d characters", len([]rune(line)))
		}
	}
	var keys map[string]any
	if err := json.Unmarshal([]byte(*raw), &keys); err != nil {
		t.Fatal(err)
	}
	found := slices.Sorted(maps.Keys(keys))
	want := []string{"action", "finished", "id", "identity", "lines", "link", "owner", "reached", "resume", "started", "state", "unit"}
	if !slices.Equal(found, want) {
		t.Fatalf("keys %v, want %v", found, want)
	}
}

// A job whose pod is gone is interrupted when the next process reads the record; a job whose pod is alive is not.
func TestJobsSettleAJobWhosePodIsGone(t *testing.T) {
	store := newJobRecords()
	started := time.Date(2026, time.November, 9, 11, 0, 0, 0, time.UTC)
	for _, job := range []pages.Job{
		{ID: 1, Action: "reconcile", Unit: "gw", Identity: "x", Started: started, State: "running", Owner: "gone"},
		{ID: 2, Action: "suspend", Unit: "gw2", Identity: "x", Started: started, State: "running", Owner: "alive"},
	} {
		store.records[job.ID] = *recordOf(job, nil)
	}
	store.alive["alive"] = true
	inputs, audit := jobInputs(store)
	jobs := NewJobs(context.Background(), inputs)
	gone, _ := jobs.Get(context.Background(), 1)
	if gone.State != "interrupted" || !strings.Contains(gone.Lines[len(gone.Lines)-1], "interrupted") {
		t.Fatalf("job 1: %+v", gone)
	}
	if kept := store.kept(t, 1); kept.State != "interrupted" {
		t.Fatalf("the record still says %s", kept.State)
	}
	if running, _ := jobs.Get(context.Background(), 2); running.State != "running" {
		t.Fatalf("a job whose pod is alive is %s", running.State)
	}
	lines := audit.all()
	if len(lines) != 1 || lines[0].State != "interrupted" || lines[0].ID != 1 {
		t.Fatalf("audit %+v", lines)
	}
}

// A record another pod wrote is read as it is, and cannot be reached by this one; a job of our own can be, once.
func TestJobsReadAnotherPodsRecords(t *testing.T) {
	store := newJobRecords()
	store.records[7] = *recordOf(pages.Job{ID: 7, Action: "reconcile", Unit: "gw", Identity: "x",
		Started: time.Now(), State: "running", Owner: "other-pod"}, nil)
	store.alive["other-pod"] = true
	inputs, _ := jobInputs(store)
	jobs := NewJobs(context.Background(), inputs)
	foreign, known := jobs.Get(context.Background(), 7)
	if !known || foreign.Live {
		t.Fatalf("another pod's job: %+v %v", foreign, known)
	}
	if _, err := jobs.Start(context.Background(), "suspend", "gw", "sam@swhurl.com", nil,
		func(context.Context, func(string)) (string, error) { return "", nil }); err == nil {
		t.Fatal("a job for another pod's running target was started")
	}
	jobs.Reach(context.Background(), 7)
	if after, _ := jobs.Get(context.Background(), 7); after.Reached != "" {
		t.Fatalf("another pod's job was reached: %q", after.Reached)
	}
	mine, err := jobs.Start(context.Background(), "suspend", "gw2", "sam@swhurl.com", nil,
		func(context.Context, func(string)) (string, error) { return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	jobs.Reach(context.Background(), mine.ID)
	reached := await(t, jobs, mine.ID, "succeeded").Reached
	if reached == "" || store.kept(t, mine.ID).Reached != reached {
		t.Fatalf("reached %q", reached)
	}
	jobs.Reach(context.Background(), mine.ID)
	if twice := await(t, jobs, mine.ID, "succeeded").Reached; twice != reached {
		t.Fatalf("reached twice: %q then %q", reached, twice)
	}
}

// A job outlives the request that started it: cancelling the caller's context does not cancel the work.
func TestAJobOutlivesTheRequestThatStartedIt(t *testing.T) {
	inputs, _ := jobInputs(newJobRecords())
	jobs := NewJobs(context.Background(), inputs)
	request, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	started, err := jobs.Start(request, "reconcile", "gw", "sam@swhurl.com", nil,
		func(ctx context.Context, _ func(string)) (string, error) {
			<-release
			return "", ctx.Err()
		})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	close(release)
	if ended := await(t, jobs, started.ID, "succeeded"); ended.State != "succeeded" {
		t.Fatalf("job %+v", ended)
	}
}

// Another pod's records are read again only while one this registry remembers is running, and a record it never
// loaded is not adopted: its id may be this pod's next.
func TestJobsReadAgainOnlyARunningRecordTheyRemember(t *testing.T) {
	store := newJobRecords()
	store.records[7] = *recordOf(pages.Job{ID: 7, Action: "reconcile", Unit: "gw", Identity: "x",
		Started: time.Now(), State: "succeeded", Owner: "other-pod"}, nil)
	inputs, _ := jobInputs(store)
	jobs := NewJobs(context.Background(), inputs)
	store.mu.Lock()
	store.records[8] = *recordOf(pages.Job{ID: 8, Action: "reconcile", Unit: "gw2", Identity: "x",
		Started: time.Now(), State: "running", Owner: "other-pod"}, nil)
	store.mu.Unlock()
	if _, known := jobs.Get(context.Background(), 8); known {
		t.Fatal("a record this registry never loaded was adopted")
	}
	if store.loads != 1 {
		t.Fatalf("the store was read %d times with no remembered job running", store.loads)
	}

	store = newJobRecords()
	store.alive["other-pod"] = true
	store.records[7] = *recordOf(pages.Job{ID: 7, Action: "reconcile", Unit: "gw", Identity: "x",
		Started: time.Now(), State: "running", Owner: "other-pod"}, nil)
	inputs, _ = jobInputs(store)
	jobs = NewJobs(context.Background(), inputs)
	store.mu.Lock()
	store.records[7] = *recordOf(pages.Job{ID: 7, Action: "reconcile", Unit: "gw", Identity: "x",
		Started: time.Now(), State: "running", Owner: "other-pod", Lines: []string{"grew"}}, nil)
	store.mu.Unlock()
	if grown, _ := jobs.Get(context.Background(), 7); len(grown.Lines) != 1 {
		t.Fatalf("a remembered running record was not read again: %+v", grown)
	}
}

// Draining stops new work and waits for what is running; a job left running is the next pod's to interrupt.
func TestJobsDrainWaitsAndRefusesNewWork(t *testing.T) {
	store := newJobRecords()
	inputs, _ := jobInputs(store)
	inputs.Now = func() time.Time { return time.Now() }
	jobs := NewJobs(context.Background(), inputs)
	release := make(chan struct{})
	if _, err := jobs.Start(context.Background(), "reconcile", "gw", "sam@swhurl.com", nil,
		func(context.Context, func(string)) (string, error) {
			<-release
			return "", nil
		}); err != nil {
		t.Fatal(err)
	}
	if jobs.Drain(20 * time.Millisecond) {
		t.Fatal("draining finished with a job still running")
	}
	if _, err := jobs.Start(context.Background(), "suspend", "gw2", "sam@swhurl.com", nil,
		func(context.Context, func(string)) (string, error) { return "", nil }); err == nil ||
		!strings.Contains(err.Error(), "restarting") {
		t.Fatalf("a new job while draining: %v", err)
	}
	close(release)
	if !jobs.Drain(time.Second) {
		t.Fatal("draining did not finish after the job did")
	}
}

// A store that cannot be read, written or asked about its pods never fails a job.
func TestJobsSurviveAnUnusableStore(t *testing.T) {
	store := newJobRecords()
	store.loadErr, store.saveErr = errors.New("no configmap"), errors.New("no configmap")
	inputs, _ := jobInputs(store)
	inputs.Owner = "gone"
	jobs := NewJobs(context.Background(), inputs)
	if len(jobs.Recent(context.Background())) != 0 {
		t.Fatal("an unreadable store is not no records")
	}
	if _, err := jobs.Start(context.Background(), "reconcile", "gw", "sam@swhurl.com", nil,
		func(_ context.Context, line func(string)) (string, error) {
			line("done")
			return "", nil
		}); err != nil {
		t.Fatal(err)
	}
	if ended := await(t, jobs, 1, "succeeded"); ended.Lines[0] != "done" {
		t.Fatalf("job %+v", ended)
	}
}

// Every line a job reports is redacted on the way in, so a secret is in neither the page nor the record.
func TestJobsRedactEveryLine(t *testing.T) {
	store := newJobRecords()
	inputs, _ := jobInputs(store)
	inputs.Redact = func(text string) string { return strings.ReplaceAll(text, "tok-123", "<redacted>") }
	jobs := NewJobs(context.Background(), inputs)
	if _, err := jobs.Start(context.Background(), "reconcile", "gw", "sam@swhurl.com", nil,
		func(_ context.Context, line func(string)) (string, error) {
			line("fetched with tok-123")
			return "", nil
		}); err != nil {
		t.Fatal(err)
	}
	ended := await(t, jobs, 1, "succeeded")
	if ended.Lines[0] != "fetched with <redacted>" || strings.Contains(store.kept(t, 1).Lines[0], "tok-123") {
		t.Fatalf("lines %q", ended.Lines)
	}
}

// A job's lines are capped as it reports them, not only when its record is written.
func TestJobsCapEveryLine(t *testing.T) {
	store := newJobRecords()
	inputs, _ := jobInputs(store)
	jobs := NewJobs(context.Background(), inputs)
	long := strings.Repeat("x", jobLineChars+50)
	if _, err := jobs.Start(context.Background(), "reconcile", "gw", "sam@swhurl.com", nil,
		func(_ context.Context, line func(string)) (string, error) {
			line(long)
			return "", nil
		}); err != nil {
		t.Fatal(err)
	}
	if ended := await(t, jobs, 1, "succeeded"); len([]rune(ended.Lines[0])) != jobLineChars {
		t.Fatalf("a line of %d characters", len([]rune(ended.Lines[0])))
	}
}
