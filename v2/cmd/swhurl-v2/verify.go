package main

import (
	"context"
	"time"

	"github.com/samclement/swhurl-platform/v2/observe"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// verify runs the platform checks that only read the cluster, with the credential status takes, and prints their
// report: the sections and [OK]/[WARN]/[BAD] lines, or the v2-platform-checks document with --json. A bad entry
// makes the exit status 1, after the report; an unusable credential or a request the adapter refuses is 2.
func (c cli) verify(r request) int {
	connection, source, status := c.connection(r)
	if status != 0 {
		return status
	}
	reader, err := connection.Reader()
	if err != nil {
		return fail(c.diagnostic, err)
	}
	if r.source != "" {
		source = r.source
	}
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()
	report, err := orchestration.Platform(ctx, orchestration.PlatformInputs{Reader: reader, Source: source, Now: time.Now})
	if err != nil {
		return fail(c.diagnostic, err)
	}
	document := []byte(observe.PlatformText(report))
	if r.json {
		if document, err = observe.EncodePlatformReport(report); err != nil {
			return fail(c.diagnostic, err)
		}
	}
	if status := emit(c.out, c.diagnostic, document); status != 0 || report.Passed() {
		return status
	}
	return 1
}
