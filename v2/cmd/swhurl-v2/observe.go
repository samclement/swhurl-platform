package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/observe"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// readTimeout bounds one observation's requests: a server that does not answer leaves every fact
// unavailable, which is what the report then says.
const readTimeout = 20 * time.Second

// defaultMaxAge is how old a database instance's newest successful backup run may be: a daily job with
// room for one missed run. It is v1's BACKUP_MAX_AGE_HOURS default, and the operator can state another.
const defaultMaxAge = 26 * time.Hour

// observe compiles the instance in memory, derives the target from the compiled documents, reads the kinds
// it names from the API server the kubeconfig names, and writes the report. It reads only.
func (c cli) observe(r request) int {
	target, status := c.observationTarget(r)
	if status != 0 {
		return status
	}
	maxAge, status := c.maxAge(r)
	if status != 0 {
		return status
	}
	kubeconfig, status := c.read(r.kubeconfig, "kubeconfig")
	if status != 0 {
		return status
	}
	connection, err := kube.Connect(kubeconfig)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	reader, err := connection.Reader()
	if err != nil {
		return fail(c.diagnostic, err)
	}
	// The evidence's source is the context that answered, unless the operator names it.
	source := connection.Context
	if r.source != "" {
		source = r.source
	}
	evidence, err := read(c.diagnostic, reader, target, source, maxAge)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	report, err := observe.Explain(target, evidence)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	document, err := observe.EncodeReport(report)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	fmt.Fprintln(c.diagnostic, summarize(report))
	return emit(c.out, c.diagnostic, document)
}

// observationTarget compiles the instance with the same use case compile runs and derives what the
// observation is about, without reading anything but the named files.
func (c cli) observationTarget(r request) (observe.Target, int) {
	inputs, status := c.inputs(r)
	if status != 0 {
		return observe.Target{}, status
	}
	compiled, err := orchestration.Compile(inputs)
	if err != nil {
		return observe.Target{}, fail(c.diagnostic, err)
	}
	helmRelease, unit, backup, err := compiledDocuments(compiled)
	if err != nil {
		return observe.Target{}, fail(c.diagnostic, err)
	}
	unitRef, err := observe.UnitRef(unit)
	if err != nil {
		return observe.Target{}, fail(c.diagnostic, err)
	}
	if unitRef.Kind != "Kustomization" {
		return observe.Target{}, fail(c.diagnostic, fmt.Errorf("unsupported: unit: only a Flux Kustomization is read"))
	}
	target, err := observe.TargetOf(helmRelease, unitRef, r.revision)
	if err != nil {
		return observe.Target{}, fail(c.diagnostic, err)
	}
	if backup != nil {
		protection, err := observe.BackupTargetOf(backup)
		if err != nil {
			return observe.Target{}, fail(c.diagnostic, err)
		}
		target.Backup = &protection
	}
	return target, 0
}

// maxAge is the threshold the backup answer is judged by: the operator's, or the default.
func (c cli) maxAge(r request) (time.Duration, int) {
	if r.maxAge == "" {
		return defaultMaxAge, 0
	}
	stated, err := time.ParseDuration(r.maxAge)
	if err != nil || stated <= 0 {
		fmt.Fprint(c.diagnostic, usage)
		return 0, 2
	}
	return stated, 0
}

// compiledDocuments is the HelmRelease, the unit and — for a database instance — the backup CronJob among a
// compilation's files: the unit is the path the compilation registered, so its naming convention is not
// repeated here.
func compiledDocuments(compiled kernel.Compilation) (helmRelease, unit, backup []byte, err error) {
	if len(compiled.Registrations) != 1 {
		return nil, nil, nil, errors.New("invalid: compilation: expected one unit registration")
	}
	for _, file := range compiled.Files {
		switch {
		case filepath.Base(file.Path) == "helmrelease.json":
			helmRelease = file.Document
		case file.Path == compiled.Registrations[0]:
			unit = file.Document
		case filepath.Base(file.Path) == "backup-cronjob.json":
			backup = file.Document
		}
	}
	if helmRelease == nil || unit == nil {
		return nil, nil, nil, errors.New("invalid: compilation: the HelmRelease or its unit is missing")
	}
	return helmRelease, unit, backup, nil
}

// read collects the target's evidence. A read this adapter refuses is an error (no evidence was
// attempted); anything the server answers, including a failure, becomes evidence.
func read(diagnostic io.Writer, reader kube.Reader, target observe.Target, source string,
	maxAge time.Duration) (observe.Evidence, error) {
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()
	evidence := observe.Evidence{Source: source, ObservedAt: time.Now().UTC().Format(time.RFC3339)}
	for _, ref := range []observe.Ref{target.Unit, target.Release, target.Workload} {
		read, err := reader.Read(ctx, ref)
		if err != nil {
			return observe.Evidence{}, err
		}
		evidence.Objects = append(evidence.Objects, read)
	}
	pods, err := reader.List(ctx, target.Pods, target.Selector)
	if err != nil {
		return observe.Evidence{}, err
	}
	evidence.Pods = []observe.List{pods}
	if target.Backup == nil {
		return evidence, nil
	}
	// A database instance's protection: its job, the pods its runs create, and — through the newest pod's
	// owner reference — the run itself. A Job carries no label the job's selector could find, so its name
	// comes from the pod the job created, never from a naming convention.
	cronJob, err := reader.Read(ctx, target.Backup.CronJob)
	if err != nil {
		return observe.Evidence{}, err
	}
	protection := &observe.BackupEvidence{CronJob: cronJob, MaxAge: maxAge}
	if protection.Pods, err = reader.List(ctx, target.Backup.Pods, target.Backup.Selector); err != nil {
		return observe.Evidence{}, err
	}
	if protection.Pods.Status == observe.Present {
		if protection.Run, err = runEvidence(ctx, reader, target, protection.Pods); err != nil {
			return observe.Evidence{}, err
		}
	}
	evidence.Backup = protection
	return evidence, nil
}

// runEvidence reads the newest run's Job, if the job's pods name one.
func runEvidence(ctx context.Context, reader kube.Reader, target observe.Target, pods observe.List) (*observe.Read, error) {
	name, found, err := observe.NewestRun(pods.Items)
	if err != nil || !found {
		return nil, err
	}
	run, err := kube.RefOf("batch", "Job", target.Namespace, name)
	if err != nil {
		return nil, err
	}
	read, err := reader.Read(ctx, run)
	if err != nil {
		return nil, err
	}
	return &read, nil
}

// summarize is the one human line for stderr: the verdict, and every fact that is not its positive
// outcome. The document on stdout is the report; this is a convenience, and never a substitute.
func summarize(report observe.Report) string {
	summary := "verdict " + string(report.Verdict)
	var reasons []string
	for _, finding := range report.Findings {
		if !observe.IsPositive(finding.Outcome) {
			reason := finding.Field + " " + finding.Outcome
			if finding.Reason != "" {
				reason += "/" + finding.Reason
			}
			reasons = append(reasons, reason)
		}
	}
	if len(reasons) > 0 {
		summary += ": " + strings.Join(reasons, "; ")
	}
	if report.Backup.State != observe.BackupNotApplicable {
		summary += "; backup " + report.Backup.State
		if report.Backup.Reason != "" {
			summary += "/" + report.Backup.Reason
		}
	}
	return summary
}
