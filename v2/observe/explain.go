// Package observe owns the pure projection of observed cluster facts onto one intended target:
// does the evidence show the intended release running? It reads nothing.
//
// The caller supplies the object bytes, the source's refusals, and the identity and time of the
// source; the pure layer never reads a clock, a cluster or a file, and an unread or unavailable
// fact is never reported as absent or as running.
package observe

import (
	"fmt"
	"regexp"
	"strings"
)

// Ref identifies one Kubernetes object as its API names it.
type Ref struct {
	APIVersion, Kind, Namespace, Name string
}

// Target is what v2 intended for one instance: the objects to look at and the facts they must show.
// Every ref, selector and intended value is supplied; deriving them from compiled output is the
// caller's work, not this layer's.
type Target struct {
	Namespace string
	Unit      Ref    // the instance's Flux unit (a Kustomization)
	Release   Ref    // its HelmRelease
	Workload  Ref    // the Deployment the release creates
	Pods      Ref    // the collection to list: v1 Pod in the instance namespace
	Selector  string // the label selector that finds this instance's own pods
	Container string // the container whose image must be the intended one
	Digest    string // the intended artifact, compared by digest: the same content under another name is not a difference
	Replicas  int32
	Revision  string // the source revision Flux should have applied; empty when it was not supplied
	// Backup is a database instance's nightly protection; nil when the instance renders none.
	Backup *BackupTarget
}

// A read's status, and the outcomes a finding can have. The same word means the same thing wherever
// it appears: absent is a successful answer that something is not there, unavailable is a failed read.
const (
	Present     = "present"
	Absent      = "absent"
	Unavailable = "unavailable"
	NotRead     = "not_read" // the caller supplied no answer for this object
	NotAssessed = "not_assessed"
	Different   = "different"

	Reconciled    = "reconciled"
	NotReconciled = "not_reconciled"
	Applied       = "applied"
	NotApplied    = "not_applied"
	Settled       = "settled"
	NotSettled    = "not_settled"
	Running       = "running"
	NotRunning    = "not_running"
	Ready         = "ready"
	NotReady      = "not_ready"
	AsIntended    = "as_intended"
)

// The fixed reasons. A positive outcome carries none; every other outcome names why.
const (
	ReasonNotFound      = "not_found"
	ReasonForbidden     = "forbidden"
	ReasonTimedOut      = "timed_out"
	ReasonTransport     = "transport"
	ReasonStatus        = "status"
	ReasonMalformed     = "malformed"
	ReasonTooLarge      = "response_too_large"
	ReasonSuspended     = "suspended"
	ReasonNoCondition   = "condition_absent"
	ReasonNotTrue       = "condition_not_ready"
	ReasonNotAvailable  = "condition_not_available"
	ReasonStale         = "status_stale"
	ReasonNotSupplied   = "revision_not_supplied"
	ReasonNotAppliedRev = "revision_not_applied"
	ReasonRevisionShape = "unrecognised_revision"
	ReasonUnitGone      = "unit_absent"
	ReasonUnitDown      = "unit_unavailable"
	ReasonWorkloadGone  = "workload_absent"
	ReasonWorkloadDown  = "workload_unavailable"
	ReasonPodsDown      = "pods_unavailable"
	ReasonNoReplicas    = "no_replicas_intended"
	ReasonNoPods        = "no_pods"
	ReasonNotPulled     = "not_pulled"
	ReasonNoContainer   = "container_absent"
	ReasonUnpinnedPod   = "unpinned_pod_image"
	ReasonDigest        = "digest"
	ReasonReplicas      = "available_replicas"
)

// Read is one object read: what the source answered, including that it could not be read.
type Read struct {
	Ref    Ref
	Status string
	Reason string
	Object []byte // present only; never carried by absent or unavailable
}

// List is one label-selected collection read. A successful list with no items is present, not absent.
type List struct {
	Ref      Ref
	Selector string
	Status   string
	Reason   string
	Items    [][]byte
}

// Evidence is what was observed, and the identity and time of the source that observed it. Both
// strings are supplied and copied verbatim: this layer never reads a clock and never names a server.
type Evidence struct {
	Source     string
	ObservedAt string
	Objects    []Read
	Pods       []List
	// Backup is the protection's evidence; nil when the caller made no such read, which is reported as
	// unassessed rather than assumed.
	Backup *BackupEvidence
}

// Finding is one fact: whether it was established, what was found, and why not.
type Finding struct {
	Field, Outcome, Reason string
}

// Verdict is the report's answer about the target as a whole. It is not a boolean: a fact that could
// not be read leaves the answer unknown rather than turning it into success or absence.
type Verdict string

const (
	IsRunning    Verdict = "running"
	IsNotRunning Verdict = "not_running"
	Unknown      Verdict = "unknown"
)

// Report is one observation of one target. Every call owns its slices, and nothing the report
// names as assessed was read from anywhere but the evidence it was given.
type Report struct {
	Target     Target
	Source     string
	ObservedAt string
	Findings   []Finding // fixed order: unit, unit.revision, release, workload, image, ready, replicas
	Verdict    Verdict
	// Backup is the protection's own answer, which the verdict never uses.
	Backup     BackupReport
	Assessed   []string // findings this report established
	Unassessed []string // findings it could not establish, and why is in the finding's reason
	NotCovered []string // facts this slice never looks at, whatever the evidence says
}

// Error names a fixed safe field and category, never a rejected value or a read's content.
type Error struct{ Field, Reason string }

func (e *Error) Error() string           { return fmt.Sprintf("invalid: %s: %s", e.Field, e.Reason) }
func invalid(field, reason string) error { return &Error{field, reason} }

// Explain reports whether the evidence shows the target's intended release running. Invalid or
// contradictory evidence returns no report: an unreadable input is not a semantic gap.
func Explain(target Target, evidence Evidence) (Report, error) {
	if err := target.validate(); err != nil {
		return Report{}, err
	}
	reads, list, err := index(target, evidence)
	if err != nil {
		return Report{}, err
	}
	facts, err := factsOf(target, reads, list)
	if err != nil {
		return Report{}, err
	}
	findings := findingsOf(target, facts)
	backup, err := backupOf(target, evidence)
	if err != nil {
		return Report{}, err
	}
	report := Report{Target: target, Source: evidence.Source, ObservedAt: evidence.ObservedAt,
		Findings: findings, Backup: backup, NotCovered: notCovered(), Assessed: []string{}, Unassessed: []string{}}
	report.Verdict = verdict(findings)
	for _, finding := range findings {
		if finding.Outcome == NotAssessed {
			report.Unassessed = append(report.Unassessed, finding.Field)
		} else {
			report.Assessed = append(report.Assessed, finding.Field)
		}
	}
	return report, nil
}

// findingsOf reports the target's facts in fixed order. Each finding answers one question, so a
// fact that cannot be established leaves the others standing instead of erasing them.
func findingsOf(target Target, facts facts) []Finding {
	return []Finding{
		fluxFinding("unit", facts.unit, Reconciled, NotReconciled),
		revisionFinding(target, facts),
		fluxFinding("release", facts.release, Settled, NotSettled),
		presenceFinding("workload", facts.workload.read),
		imageFinding(target, facts.workload.read, facts.pods),
		readyFinding(facts.workload),
		replicasFinding(target, facts.workload),
	}
}

// fluxFinding reads one Flux object's Ready condition. Flux reports the generation it observed, so a
// condition about an older spec is stale evidence and is not read as ready.
func fluxFinding(field string, f fact, positive, negative string) Finding {
	switch f.read.Status {
	case Present:
	case Absent:
		return Finding{field, Absent, ReasonNotFound}
	case Unavailable:
		return Finding{field, Unavailable, f.read.Reason}
	default:
		return Finding{field, NotAssessed, NotRead}
	}
	if f.flux.suspended {
		return Finding{field, negative, ReasonSuspended}
	}
	return fluxReadiness(field, f.flux.conditions, f.flux.generation, positive, negative, ReasonNotTrue)
}

// fluxReadiness judges a Flux object's Ready condition, which carries the generation it observed, so a
// condition about an older spec is stale evidence rather than readiness.
func fluxReadiness(field string, conditions []condition, generation int64, positive, negative, whenFalse string) Finding {
	condition, found := statusOf(conditions, "Ready")
	switch {
	case !found:
		return Finding{field, negative, ReasonNoCondition}
	case condition.ObservedGeneration != generation:
		return Finding{field, negative, ReasonStale}
	case condition.Status != "True":
		return Finding{field, negative, whenFalse}
	}
	return Finding{field, positive, ""}
}

// revisionFinding compares the unit's applied revision with the intended commit, when one was supplied.
// Flux states a source revision as `<ref>@<algo>:<commit>`, so the commit is the text after the last
// colon; a revision in any other shape is not interpreted.
func revisionFinding(target Target, facts facts) Finding {
	const field = "unit.revision"
	if target.Revision == "" {
		return Finding{field, NotAssessed, ReasonNotSupplied}
	}
	if facts.unit.read.Status != Present {
		return Finding{field, NotAssessed, unitReason(facts.unit.read.Status)}
	}
	applied := facts.unit.flux.lastAppliedRevision
	if applied == "" {
		return Finding{field, NotApplied, ReasonNotAppliedRev}
	}
	commit, named := appliedCommit(applied)
	switch {
	case !named:
		return Finding{field, NotAssessed, ReasonRevisionShape}
	case commit == target.Revision:
		return Finding{field, Applied, ""}
	}
	return Finding{field, Different, ""}
}

// appliedCommit is the commit a Flux source revision names: `<ref>@<algo>:<commit>` yields the text
// after its last colon, and anything without a digest part is not interpreted.
func appliedCommit(revision string) (string, bool) {
	at := strings.LastIndex(revision, "@")
	if at < 0 {
		return "", false
	}
	colon := strings.LastIndex(revision[at:], ":")
	if colon < 0 {
		return "", false
	}
	commit := revision[at+colon+1:]
	return commit, commit != ""
}

// unitReason explains why the unit's own facts could not be read: the object is absent, the read
// failed, or it was never made.
func unitReason(status string) string {
	switch status {
	case Absent:
		return ReasonUnitGone
	case Unavailable:
		return ReasonUnitDown
	}
	return NotRead
}

// presenceFinding reports whether one object is there. Only a successful answer that it is not there
// makes it absent; a failed read stays unavailable.
func presenceFinding(field string, read Read) Finding {
	switch read.Status {
	case Present:
		return Finding{field, Present, ""}
	case Absent:
		return Finding{field, Absent, ReasonNotFound}
	case Unavailable:
		return Finding{field, Unavailable, read.Reason}
	}
	return Finding{field, NotAssessed, NotRead}
}

// imageFinding answers whether the intended digest is running in the target's container. It reads the
// pods, not the Deployment's spec: a rollout in progress keeps the old pods, so the spec can name the
// new image while the old one runs.
func imageFinding(target Target, workload Read, pods podsFact) Finding {
	const field = "image"
	if reason, stopped := gate(workload, ReasonWorkloadGone, ReasonWorkloadDown); stopped {
		return Finding{field, NotAssessed, reason}
	}
	if reason, stopped := listGate(pods.list); stopped {
		return Finding{field, NotAssessed, reason}
	}
	return runningImage(target, pods)
}

// listGate explains why a collection read cannot be used. An empty list is present, so it has no absent
// form here.
func listGate(list List) (string, bool) {
	switch list.Status {
	case Present:
		return "", false
	case Unavailable:
		return ReasonPodsDown, true
	}
	return NotRead, true
}

// runningImage judges the images the pods reported: one pod that has not pulled yet is not the same
// fact as a pod running another digest, and neither is the same as no pod at all.
func runningImage(target Target, pods podsFact) Finding {
	const field = "image"
	switch {
	case !pods.found:
		if target.Replicas == 0 {
			return Finding{field, NotAssessed, ReasonNoReplicas}
		}
		return Finding{field, NotRunning, ReasonNoPods}
	case !pods.complete:
		return Finding{field, Different, ReasonNoContainer}
	case !pods.pinned:
		return Finding{field, Different, ReasonUnpinnedPod}
	case len(pods.images) == 0:
		return Finding{field, NotRunning, ReasonNotPulled}
	}
	for _, image := range pods.images {
		if !strings.HasSuffix(image, "@"+target.Digest) {
			return Finding{field, Different, ReasonDigest}
		}
	}
	return Finding{field, Running, ""}
}

// readyFinding reports the Deployment's Available condition. A Deployment states the generation it has
// observed on its status, not on each condition, so a stale status is refused before the condition is
// read: readiness about an older spec is not readiness of this one.
func readyFinding(f workloadFact) Finding {
	const field = "ready"
	if reason, stopped := gate(f.read, ReasonWorkloadGone, ReasonWorkloadDown); stopped {
		return Finding{field, NotAssessed, reason}
	}
	if f.object.observed != f.object.generation {
		return Finding{field, NotReady, ReasonStale}
	}
	condition, found := statusOf(f.object.conditions, "Available")
	switch {
	case !found:
		return Finding{field, NotReady, ReasonNoCondition}
	case condition.Status != "True":
		return Finding{field, NotReady, ReasonNotAvailable}
	}
	return Finding{field, Ready, ""}
}

// gate explains why a fact that depends on the workload cannot be assessed: the object is absent, its
// read failed, or it was not read. Each is a different reason, and none of them is a positive fact.
func gate(read Read, absent, down string) (string, bool) {
	switch read.Status {
	case Present:
		return "", false
	case Absent:
		return absent, true
	case Unavailable:
		return down, true
	}
	return NotRead, true
}

// replicasFinding compares the available replicas with the intended count. Zero is a value, not an
// absent default.
func replicasFinding(target Target, f workloadFact) Finding {
	const field = "replicas"
	if reason, stopped := gate(f.read, ReasonWorkloadGone, ReasonWorkloadDown); stopped {
		return Finding{field, NotAssessed, reason}
	}
	if f.object.availableReplicas != int64(target.Replicas) {
		return Finding{field, Different, ReasonReplicas}
	}
	return Finding{field, AsIntended, ""}
}

// verdict decides the answer from the findings. A fact that was established as negative decides, even
// if other facts could not be read; otherwise anything unread or unavailable leaves it unknown, and
// only a target whose every fact is positive is running.
func verdict(findings []Finding) Verdict {
	unknown := false
	for _, finding := range findings {
		switch outcomeSign(finding.Outcome) {
		case negative:
			return IsNotRunning
		case unclear:
			unknown = true
		}
	}
	if unknown {
		return Unknown
	}
	return IsRunning
}

// The three signs a finding's outcome can have for the verdict.
const (
	positive = iota
	negative
	unclear
)

// IsPositive reports whether an outcome is one of the seven that make a report running. A caller that
// formats or filters a report needs the same table the verdict uses, not a copy of the words.
func IsPositive(outcome string) bool { return outcomeSign(outcome) == positive }

func outcomeSign(outcome string) int {
	switch outcome {
	case Reconciled, Applied, Settled, Present, Running, Ready, AsIntended:
		return positive
	case NotReconciled, NotApplied, NotSettled, Absent, Different, NotRunning, NotReady:
		return negative
	case Unavailable, NotRead, NotAssessed:
		return unclear
	}
	return unclear
}

// validate checks the target's fields in a fixed order, so the first broken one decides which error is
// returned: the namespace, then each role's reference against the roles before it, then the facts that
// are not references. A target whose roles name the same object twice is refused, because one read would
// otherwise answer two questions.
func (t Target) validate() error {
	if !namePattern().MatchString(t.Namespace) {
		return invalid("namespace", "expected_a_name")
	}
	if err := validateRoles(t); err != nil {
		return err
	}
	return t.validateFacts()
}

// role is one object the target reads, and whether it must sit in the instance's namespace.
type role struct {
	field       string
	ref         Ref
	inNamespace bool
	named       bool
}

// validateRoles checks every reference against each other and against the instance's namespace.
func validateRoles(t Target) error {
	roles := []role{
		{"unit", t.Unit, false, true},
		{"release", t.Release, true, true},
		{"workload", t.Workload, true, true},
		{"pods", t.Pods, true, false},
	}
	if t.Backup != nil {
		roles = append(roles, role{"backup", t.Backup.CronJob, true, true})
	}
	for i, role := range roles {
		for _, other := range roles[:i] {
			if other.ref == role.ref {
				return invalid("target", "repeated_ref")
			}
		}
		if err := validateRef(role.field, role.ref, role.named); err != nil {
			return err
		}
		if role.inNamespace && role.ref.Namespace != t.Namespace {
			return invalid(role.field, "namespace_mismatch")
		}
	}
	return nil
}

// validateFacts checks what the target says must be running.
func (t Target) validateFacts() error {
	if !objectPattern().MatchString(t.Container) {
		return invalid("container", "expected_an_object_name")
	}
	if !selectorPattern().MatchString(t.Selector) {
		return invalid("selector", "expected_a_simple_label_selector")
	}
	if !digestPattern().MatchString(t.Digest) {
		return invalid("digest", "expected_a_sha256_digest")
	}
	if t.Replicas < 0 {
		return invalid("replicas", "expected_a_nonnegative_count")
	}
	if t.Revision != "" && !revisionPattern().MatchString(t.Revision) {
		return invalid("revision", "expected_a_commit")
	}
	if t.Backup == nil {
		return nil
	}
	return validateBackup(*t.Backup, t.Namespace)
}

// validateBackup checks the protection's own facts: its pods are their own collection — the same kind and
// namespace as the instance's pods with another selector — so they are validated and never mistaken for the
// instance's own pod list.
func validateBackup(backup BackupTarget, namespace string) error {
	if err := validateRef("backup.pods", backup.Pods, false); err != nil {
		return err
	}
	if backup.Pods.Namespace != namespace {
		return invalid("backup.pods", "namespace_mismatch")
	}
	if !selectorPattern().MatchString(backup.Selector) {
		return invalid("backup.selector", "expected_a_simple_label_selector")
	}
	if strings.TrimSpace(backup.Schedule) == "" {
		return invalid("backup.schedule", "expected_a_schedule")
	}
	return nil
}

// validateRef checks one role's object reference. The unit may live outside the instance's namespace;
// the release, workload and pod list may not, and the pod list names a collection rather than an object.
func validateRef(field string, ref Ref, named bool) error {
	if !versionPattern().MatchString(ref.APIVersion) {
		return invalid(field, "expected_an_api_version")
	}
	if !kindPattern().MatchString(ref.Kind) {
		return invalid(field, "expected_a_kind")
	}
	if !namePattern().MatchString(ref.Namespace) {
		return invalid(field, "expected_a_namespace")
	}
	if !named {
		if ref.Name != "" {
			return invalid(field, "name_not_used")
		}
		return nil
	}
	if !objectPattern().MatchString(ref.Name) {
		return invalid(field, "expected_an_object_name")
	}
	return nil
}

// notCovered lists the facts this slice never looks at, so "running" cannot be read as parity.
func notCovered() []string {
	return []string{"history", "git_publication", "source_build", "pods", "routes", "storage", "secrets",
		"calls", "events", "team", "quota", "backup_restore", "foreign_ownership"}
}

// The grammars. The pure rule forbids package variables, so they compile per call; they are repeated
// from kernel deliberately, because observe imports no kernel types.
func namePattern() *regexp.Regexp {
	return regexp.MustCompile(`^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$`)
}

func objectPattern() *regexp.Regexp {
	return regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)
}

func versionPattern() *regexp.Regexp {
	return regexp.MustCompile(`^([a-z0-9]([a-z0-9.-]*[a-z0-9])?/)?v[0-9][0-9a-z]*$`)
}

func kindPattern() *regexp.Regexp { return regexp.MustCompile(`^[A-Z][A-Za-z]*$`) }

func digestPattern() *regexp.Regexp { return regexp.MustCompile(`^sha256:[0-9a-f]{64}$`) }

func selectorPattern() *regexp.Regexp {
	term := `[A-Za-z0-9]([A-Za-z0-9._/-]*[A-Za-z0-9])?(=[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?)?`
	return regexp.MustCompile(`^` + term + `(,` + term + `)*$`)
}

func revisionPattern() *regexp.Regexp { return regexp.MustCompile(`^[0-9a-f]{40}$`) }
