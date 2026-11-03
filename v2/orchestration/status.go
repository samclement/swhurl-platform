package orchestration

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// StatusInputs is one status request: the instance's records and the documents its compilation takes, the commit
// those records were read at, and where and when the evidence is read.
type StatusInputs struct {
	Inputs
	// Revision is the commit the records were read at: the revision Flux should have applied.
	Revision string
	// RecordsRoot and SharedPaths are repository-relative, not temporary extraction paths.
	RecordsRoot string
	SharedPaths []string
	Changes     *observe.Changes
	Reader      kube.Reader
	// Source names the evidence's source, and Now is the clock its time is read from: both are the caller's.
	Source string
	Now    func() time.Time
	// MaxAge is how old a database instance's newest successful backup run may be.
	MaxAge time.Duration
	// Archive lists the instance's own prefix in the object store, given its namespace; nil when the caller does
	// not read the archive, which the report then says.
	Archive func(ctx context.Context, namespace string) (observe.ArchiveList, error)
}

// Status compares the cluster with what the records say should run: it compiles the instance, derives the target
// from the compiled documents, reads what the target names, and reports the observation with its state. It reads
// only. A read the cluster answers with a failure is evidence; a request the adapter refuses to make, a record the
// compiler refuses and a failed archive listing are errors, and no report is made.
func Status(ctx context.Context, inputs StatusInputs) (observe.Report, error) {
	target, err := statusScopedTarget(inputs)
	if err != nil {
		return observe.Report{}, err
	}
	evidence, err := readEvidence(ctx, inputs.Reader, target, inputs.MaxAge)
	if err != nil {
		return observe.Report{}, err
	}
	return statusReport(ctx, inputs, target, evidence, inputs.Now().UTC().Format(time.RFC3339))
}

// compiledInstance is the documents among a compilation's files that the target is derived from. The backup and
// the route are nil for an instance that renders none.
type compiledInstance struct {
	helmRelease, unit, backup, ingress []byte
}

// statusTarget compiles the instance and derives what should be running from the compiled documents, reading
// nothing but the documents it was given.
func statusTarget(inputs Inputs, revision string) (observe.Target, error) {
	compiled, err := Compile(inputs)
	if err != nil {
		return observe.Target{}, err
	}
	return targetFromCompilation(compiled, revision)
}

func targetFromCompilation(compiled kernel.Compilation, revision string) (observe.Target, error) {
	documents, err := compiledDocuments(compiled)
	if err != nil {
		return observe.Target{}, err
	}
	unit, err := observe.UnitRef(documents.unit)
	if err != nil {
		return observe.Target{}, err
	}
	if unit.Kind != "Kustomization" {
		return observe.Target{}, errors.New("unsupported: unit: only a Flux Kustomization is read")
	}
	target, err := observe.TargetOf(documents.helmRelease, unit, revision)
	if err != nil {
		return observe.Target{}, err
	}
	if documents.backup != nil {
		protection, err := observe.BackupTargetOf(documents.backup)
		if err != nil {
			return observe.Target{}, err
		}
		target.Backup = &protection
	}
	if documents.ingress != nil {
		route, err := observe.RouteTargetOf(documents.ingress)
		if err != nil {
			return observe.Target{}, err
		}
		target.Route = &route
	}
	return target, nil
}

// compiledDocuments finds the target's documents among a compilation's files: the unit is the path the
// compilation registered, so its naming convention is not repeated here.
func compiledDocuments(compiled kernel.Compilation) (compiledInstance, error) {
	if len(compiled.Registrations) != 1 {
		return compiledInstance{}, errors.New("invalid: compilation: expected one unit registration")
	}
	var documents compiledInstance
	for _, file := range compiled.Files {
		switch {
		case filepath.Base(file.Path) == "helmrelease.json":
			documents.helmRelease = file.Document
		case file.Path == compiled.Registrations[0]:
			documents.unit = file.Document
		case filepath.Base(file.Path) == "backup-cronjob.json":
			documents.backup = file.Document
		case filepath.Base(file.Path) == "ingress.json":
			documents.ingress = file.Document
		}
	}
	if documents.helmRelease == nil || documents.unit == nil {
		return compiledInstance{}, errors.New("invalid: compilation: the HelmRelease or its unit is missing")
	}
	return documents, nil
}

// readEvidence reads every object the target names. A request the adapter refuses is an error (no evidence was
// attempted); anything the server answers, a failure included, becomes evidence.
func readEvidence(ctx context.Context, reader kube.Reader, target observe.Target, maxAge time.Duration) (observe.Evidence, error) {
	refs := []observe.Ref{target.Unit, target.Release, target.Workload}
	if target.Route != nil {
		refs = append(append(refs, target.Route.Ingress), target.Route.Certificates...)
	}
	var evidence observe.Evidence
	for _, ref := range refs {
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
	evidence.Backup, err = backupEvidence(ctx, reader, target, maxAge)
	return evidence, err
}

// backupEvidence reads a database instance's schedule and selected run pods. The shared statusReport
// assembly reads the newest pod's owner Job, never a guessed name or an assumed Job label.
func backupEvidence(ctx context.Context, reader kube.Reader, target observe.Target, maxAge time.Duration) (*observe.BackupEvidence, error) {
	cronJob, err := reader.Read(ctx, target.Backup.CronJob)
	if err != nil {
		return nil, err
	}
	protection := &observe.BackupEvidence{CronJob: cronJob, MaxAge: maxAge}
	if protection.Pods, err = reader.List(ctx, target.Backup.Pods, target.Backup.Selector); err != nil {
		return nil, err
	}

	return protection, nil
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
