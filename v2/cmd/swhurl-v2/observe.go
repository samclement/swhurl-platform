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
)

// readTimeout bounds one observation's requests: a server that does not answer leaves every fact
// unavailable, which is what the report then says.
const readTimeout = 20 * time.Second

// observe compiles the instance in memory, derives the target from the compiled documents, reads the
// four kinds from the API server the kubeconfig names, and writes the report. It reads only.
func (c cli) observe(r request) int {
	target, status := c.observationTarget(r)
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
	evidence, err := read(c.diagnostic, reader, target, source)
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

// observationTarget compiles the instance and derives what the observation is about, without reading
// anything but the three named files.
func (c cli) observationTarget(r request) (observe.Target, int) {
	spec, status := c.instance(r.instance)
	if status != 0 {
		return observe.Target{}, status
	}
	release, status := c.release(r.release)
	if status != 0 {
		return observe.Target{}, status
	}
	data, status := c.read(r.profile, "profile")
	if status != 0 {
		return observe.Target{}, status
	}
	profile, err := kernel.DecodeProfile(data)
	if err != nil {
		return observe.Target{}, fail(c.diagnostic, err)
	}
	compiled, err := kernel.Compile(spec, release, profile)
	if err != nil {
		return observe.Target{}, fail(c.diagnostic, err)
	}
	helmRelease, unit, err := compiledDocuments(compiled)
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
	return target, 0
}

// compiledDocuments is the HelmRelease and the unit among a compilation's files: the unit is the path
// the compilation registered, so its naming convention is not repeated here.
func compiledDocuments(compiled kernel.Compilation) (helmRelease, unit []byte, err error) {
	if len(compiled.Registrations) != 1 {
		return nil, nil, errors.New("invalid: compilation: expected one unit registration")
	}
	for _, file := range compiled.Files {
		switch {
		case filepath.Base(file.Path) == "helmrelease.json":
			helmRelease = file.Document
		case file.Path == compiled.Registrations[0]:
			unit = file.Document
		}
	}
	if helmRelease == nil || unit == nil {
		return nil, nil, errors.New("invalid: compilation: the HelmRelease or its unit is missing")
	}
	return helmRelease, unit, nil
}

// read collects the target's evidence. A read this adapter refuses is an error (no evidence was
// attempted); anything the server answers, including a failure, becomes evidence.
func read(diagnostic io.Writer, reader kube.Reader, target observe.Target, source string) (observe.Evidence, error) {
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
	return evidence, nil
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
	return summary
}
