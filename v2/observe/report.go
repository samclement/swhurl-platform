package observe

import (
	"bytes"
	"encoding/json"
	"time"
)

// EncodeReport is one observation as a versioned JSON document: the target it is about, the evidence's
// source and time, the state, every finding in order, the containers that are not ready, the route and its
// certificates, the protection's own answer, what was assessed, and what this slice never looks at. Identical
// reports encode to identical bytes. Assessment lists are never null; live fields retain missing facts.
func EncodeReport(report Report) ([]byte, error) {
	document := reportDocument{
		Kind:        "v2-observation",
		Version:     4,
		Currentness: report.Currentness, Live: report.Live,
		Source:       report.Source,
		ObservedAt:   report.ObservedAt,
		State:        stateDocument(report.State),
		Verdict:      report.Verdict,
		Findings:     make([]findingDocument, 0, len(report.Findings)),
		Problems:     make([]problemDocument, 0, len(report.Problems)),
		Routes:       make([]routeDocument, 0, len(report.Routes)),
		Certificates: make([]certificateDocument, 0, len(report.Certificates)),
		Backup:       backupDocumentOf(report.Backup),
		Assessed:     nonNil(report.Assessed),
		Unassessed:   nonNil(report.Unassessed),
		NotCovered:   nonNil(report.NotCovered),
		Target:       targetDocumentOf(report.Target),
	}
	for _, finding := range report.Findings {
		document.Findings = append(document.Findings, findingDocument{finding.Field, finding.Outcome, finding.Reason})
	}
	for _, problem := range report.Problems {
		document.Problems = append(document.Problems, problemDocument(problem))
	}
	for _, route := range report.Routes {
		document.Routes = append(document.Routes, routeDocument{route.Name, route.Outcome, route.Reason, nonNil(route.Hosts)})
	}
	for _, certificate := range report.Certificates {
		document.Certificates = append(document.Certificates, certificateDocument(certificate))
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), nil
}

// reportDocument is the report's shape on stdout: field order is the document order, and a change to it
// raises Version.
type reportDocument struct {
	Kind         string                `json:"kind"`
	Version      int                   `json:"version"`
	Source       string                `json:"source"`
	ObservedAt   string                `json:"observedAt"`
	Currentness  Currentness           `json:"currentness"`
	Live         LiveFacts             `json:"live"`
	State        stateDocument         `json:"state"`
	Verdict      Verdict               `json:"verdict"`
	Backup       backupDocument        `json:"backup"`
	Findings     []findingDocument     `json:"findings"`
	Problems     []problemDocument     `json:"problems"`
	Routes       []routeDocument       `json:"routes"`
	Certificates []certificateDocument `json:"certificates"`
	Assessed     []string              `json:"assessed"`
	Unassessed   []string              `json:"unassessed"`
	NotCovered   []string              `json:"notCovered"`
	Target       targetDocument        `json:"target"`
}

// stateDocument is the instance's state and the fact that decided it; a healthy state names none.
type stateDocument struct {
	Name   string `json:"name"`
	Field  string `json:"field,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type problemDocument struct {
	Pod       string `json:"pod"`
	Container string `json:"container"`
	Reason    string `json:"reason"`
	Message   string `json:"message,omitempty"`
}

type routeDocument struct {
	Name    string   `json:"name"`
	Outcome string   `json:"outcome"`
	Reason  string   `json:"reason,omitempty"`
	Hosts   []string `json:"hosts"`
}

type certificateDocument struct {
	Name    string `json:"name"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
}

// backupDocument is the protection's answer: its state and reason, the schedule's own facts, and the newest
// run. The threshold is the caller's, in seconds, so a reader of the document needs no clock either.
type backupDocument struct {
	State           string          `json:"state"`
	Reason          string          `json:"reason,omitempty"`
	Schedule        string          `json:"schedule,omitempty"`
	LastScheduledAt string          `json:"lastScheduledAt,omitempty"`
	LastSucceededAt string          `json:"lastSucceededAt,omitempty"`
	MaxAgeSeconds   int64           `json:"maxAgeSeconds,omitempty"`
	NewestRun       *runDocument    `json:"newestRun,omitempty"`
	Archive         archiveDocument `json:"archive"`
}

type runDocument struct {
	Name        string `json:"name"`
	CreatedAt   string `json:"createdAt,omitempty"`
	CompletedAt string `json:"completedAt,omitempty"`
	Outcome     string `json:"outcome"`
}

// archiveDocument is the object store's answer: the newest archive, how old it is, and what was read.
type archiveDocument struct {
	State         string `json:"state"`
	Reason        string `json:"reason,omitempty"`
	Bucket        string `json:"bucket,omitempty"`
	Prefix        string `json:"prefix,omitempty"`
	Newest        string `json:"newest,omitempty"`
	LastModified  string `json:"lastModified,omitempty"`
	AgeSeconds    int64  `json:"ageSeconds,omitempty"`
	MaxAgeSeconds int64  `json:"maxAgeSeconds,omitempty"`
}

func archiveDocumentOf(report ArchiveReport) archiveDocument {
	return archiveDocument{State: report.State, Reason: report.Reason, Bucket: report.Bucket, Prefix: report.Prefix,
		Newest: report.Newest, LastModified: report.LastModified,
		AgeSeconds: int64(report.Age / time.Second), MaxAgeSeconds: int64(report.MaxAge / time.Second)}
}

func backupDocumentOf(report BackupReport) backupDocument {
	document := backupDocument{State: report.State, Reason: report.Reason, Schedule: report.Schedule,
		LastScheduledAt: report.LastScheduledAt, LastSucceededAt: report.LastSucceededAt,
		MaxAgeSeconds: int64(report.MaxAge / time.Second), Archive: archiveDocumentOf(report.Archive)}
	if report.NewestRun != nil {
		document.NewestRun = &runDocument{Name: report.NewestRun.Name, CreatedAt: report.NewestRun.CreatedAt,
			CompletedAt: report.NewestRun.CompletedAt, Outcome: report.NewestRun.Outcome}
	}
	return document
}

type findingDocument struct {
	Field   string `json:"field"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
}

type targetDocument struct {
	Namespace string                `json:"namespace"`
	Unit      refDocument           `json:"unit"`
	Release   refDocument           `json:"release"`
	Workload  refDocument           `json:"workload"`
	Pods      refDocument           `json:"pods"`
	Backup    *backupTargetDocument `json:"backup,omitempty"`
	Route     *routeTargetDocument  `json:"route,omitempty"`
	Selector  string                `json:"selector"`
	Container string                `json:"container"`
	Digest    string                `json:"digest"`
	Replicas  int32                 `json:"replicas"`
	Revision  string                `json:"revision,omitempty"`
}

// backupTargetDocument is what a protection observation read: the CronJob, the collection of its runs, and
// the selector that finds them.
type backupTargetDocument struct {
	CronJob  refDocument `json:"cronJob"`
	Pods     refDocument `json:"pods"`
	Selector string      `json:"selector"`
	Schedule string      `json:"schedule"`
}

// routeTargetDocument is what a route observation read: the Ingress and each certificate it needs.
type routeTargetDocument struct {
	Ingress      refDocument   `json:"ingress"`
	Certificates []refDocument `json:"certificates"`
}

type refDocument struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name,omitempty"`
}

func targetDocumentOf(target Target) targetDocument {
	return targetDocument{
		Namespace: target.Namespace,
		Unit:      refDocumentOf(target.Unit),
		Release:   refDocumentOf(target.Release),
		Workload:  refDocumentOf(target.Workload),
		Pods:      refDocumentOf(target.Pods),
		Backup:    backupTargetDocumentOf(target.Backup),
		Route:     routeTargetDocumentOf(target.Route),
		Selector:  target.Selector,
		Container: target.Container,
		Digest:    target.Digest,
		Replicas:  target.Replicas,
		Revision:  target.Revision,
	}
}

func backupTargetDocumentOf(target *BackupTarget) *backupTargetDocument {
	if target == nil {
		return nil
	}
	return &backupTargetDocument{CronJob: refDocumentOf(target.CronJob), Pods: refDocumentOf(target.Pods),
		Selector: target.Selector, Schedule: target.Schedule}
}

func routeTargetDocumentOf(target *RouteTarget) *routeTargetDocument {
	if target == nil {
		return nil
	}
	document := &routeTargetDocument{Ingress: refDocumentOf(target.Ingress), Certificates: []refDocument{}}
	for _, certificate := range target.Certificates {
		document.Certificates = append(document.Certificates, refDocumentOf(certificate))
	}
	return document
}

func refDocumentOf(ref Ref) refDocument {
	return refDocument{ref.APIVersion, ref.Kind, ref.Namespace, ref.Name}
}

// nonNil keeps a list in the document even when it is empty, so a consumer never has to treat null and
// empty as different.
func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
