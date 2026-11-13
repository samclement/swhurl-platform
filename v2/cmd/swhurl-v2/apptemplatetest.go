package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/samclement/swhurl-platform/v2/adapters/route"
	"github.com/samclement/swhurl-platform/v2/apptemplatetest"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// appTemplateTest deploys the fixture's rendered instances, checks them and removes them, printing each line as it
// is decided. A dry run prints the plan and reads nothing. Leftovers are one [ERROR] line and exit 1 with nothing
// changed; a failed check or step is exit 1 after the report. An interrupt stops the step under way and the cleanup
// still runs.
func (c cli) appTemplateTest(r request) int {
	if r.dryRun {
		return emit(c.out, c.diagnostic, []byte(apptemplatetest.Plan))
	}
	rendered, status := c.appTemplateRendered()
	if status != 0 {
		return status
	}
	cluster, status := c.dataCluster(r)
	if status != 0 {
		return status
	}
	prober := c.prober
	if prober == nil {
		prober = route.New()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	written := true
	lines, err := orchestration.AppTemplateTest(ctx, orchestration.AppTemplateTestInputs{
		Cluster: orchestration.Cluster{Reader: cluster.reader, Writer: cluster.writer}, Route: prober, Rendered: rendered,
		Now: c.appRepoNow(), Sleep: c.sleeper(),
		Say: func(line apptemplatetest.Line) {
			if _, err := fmt.Fprintln(c.out, apptemplatetest.Format(line)); err != nil {
				written = false
			}
		}})
	var preflight *apptemplatetest.Preflight
	if errors.As(err, &preflight) {
		fmt.Fprintf(c.diagnostic, "[ERROR] %v\n", err)
		return 1
	}
	if err != nil {
		return fail(c.diagnostic, err)
	}
	if !written {
		fmt.Fprintln(c.diagnostic, "io: output: cannot write result")
		return 1
	}
	if apptemplatetest.Passes(lines) {
		return 0
	}
	return 1
}

// appTemplateRendered reads what the test takes from the fixture's render, from this checkout: each app's unit, the
// web app's Ingress and its release record.
func (c cli) appTemplateRendered() (apptemplatetest.Rendered, int) {
	units := map[string][]byte{}
	for _, app := range apptemplatetest.Apps() {
		unit, status := c.read(apptemplatetest.UnitFile(app), "fixture unit "+apptemplatetest.Unit(app))
		if status != 0 {
			return apptemplatetest.Rendered{}, status
		}
		units[app] = unit
	}
	ingress, status := c.read(apptemplatetest.IngressFile, "fixture ingress")
	if status != 0 {
		return apptemplatetest.Rendered{}, status
	}
	release, status := c.read(apptemplatetest.ReleaseFile, "fixture release")
	if status != 0 {
		return apptemplatetest.Rendered{}, status
	}
	rendered, err := apptemplatetest.Decode(units, ingress, release)
	if err != nil {
		return apptemplatetest.Rendered{}, fail(c.diagnostic, err)
	}
	return rendered, 0
}
