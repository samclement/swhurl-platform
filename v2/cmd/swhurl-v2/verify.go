package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/github"
	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/s3"
	"github.com/samclement/swhurl-platform/v2/observe"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// verifyTimeout bounds the platform scope's reads: more than one observation's, because it also lists the registry
// and the object store once per app and database.
const verifyTimeout = 2 * time.Minute

// verify runs the platform checks — the cluster scope's six, or with --scope platform every one — with the credential
// status takes, and prints their report: the sections and [OK]/[WARN]/[BAD] lines, or the v2-platform-checks document
// with --json. A bad entry makes the exit status 1, after the report; an unusable credential or a request an adapter
// refuses is 2.
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
	inputs := orchestration.PlatformInputs{Reader: reader, Source: source, Now: time.Now}
	var report observe.PlatformReport
	if r.scope == "platform" {
		report, status = c.verifyPlatform(r, connection, inputs)
	} else {
		report, status = c.verifyCluster(inputs)
	}
	if status != 0 {
		return status
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

func (c cli) verifyCluster(inputs orchestration.PlatformInputs) (observe.PlatformReport, int) {
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()
	report, err := orchestration.Platform(ctx, inputs)
	if err != nil {
		return report, fail(c.diagnostic, err)
	}
	return report, 0
}

// verifyPlatform runs every check: the connection's other ports, the anonymous registry, the object store with the
// operator's credentials when they are set, GitHub with explicit operator/console credentials, the checkout
// at --root, and the tag and host heartbeat facts the caller supplied.
func (c cli) verifyPlatform(r request, connection kube.Connection, cluster orchestration.PlatformInputs) (observe.PlatformReport, int) {
	inputs := orchestration.PlatformAllInputs{PlatformInputs: cluster, Registries: c.registries, ConsoleTag: r.consoleTag,
		Heartbeat: c.heartbeatFacts(r.heartbeat)}
	inputs.GitHub, inputs.GitHubToken = c.github, strings.TrimSpace(os.Getenv("GH_TOKEN"))
	if inputs.GitHub == nil {
		inputs.GitHub = github.New(nil)
	}
	var status int
	if inputs.MaxAge, status = c.backupMaxAge(); status != 0 {
		return observe.PlatformReport{}, status
	}
	if inputs.Checkout, status = c.readCheckout(r.root); status != 0 {
		return observe.PlatformReport{}, status
	}
	var err error
	if inputs.Data, err = connection.Data(); err != nil {
		return observe.PlatformReport{}, fail(c.diagnostic, err)
	}
	if inputs.Issuer, err = connection.Issuer(); err != nil {
		return observe.PlatformReport{}, fail(c.diagnostic, err)
	}
	if inputs.Executor, err = connection.Executor(); err != nil {
		return observe.PlatformReport{}, fail(c.diagnostic, err)
	}
	if _, supplied := awsCredentials(); supplied {
		inputs.Objects = func(bucket string) (s3.Reader, error) { return c.archives(bucket, "") }
	}
	ctx, cancel := context.WithTimeout(context.Background(), verifyTimeout)
	defer cancel()
	report, err := orchestration.PlatformAll(ctx, inputs)
	if err != nil {
		return report, fail(c.diagnostic, err)
	}
	return report, 0
}

// heartbeatFacts are the host heartbeat units as the caller read them: no --heartbeat is facts not supplied, and a
// file this run cannot read is facts it could not read, which the check reports. Neither stops the other checks.
func (c cli) heartbeatFacts(path string) observe.HeartbeatEvidence {
	if path == "" {
		return observe.HeartbeatEvidence{}
	}
	data, err := c.readFile(path)
	if err != nil {
		return observe.HeartbeatEvidence{Supplied: true}
	}
	return observe.HeartbeatEvidence{Supplied: true, Text: string(data)}
}

// backupMaxAge is how old the newest complete backup may be: BACKUP_MAX_AGE_HOURS, or 26 hours.
func (c cli) backupMaxAge() (time.Duration, int) {
	stated := os.Getenv("BACKUP_MAX_AGE_HOURS")
	if stated == "" {
		return defaultMaxAge, 0
	}
	hours, err := strconv.ParseFloat(stated, 64)
	if err != nil || math.IsNaN(hours) || hours <= 0 || hours >= float64(math.MaxInt64)/float64(time.Hour) {
		fmt.Fprintln(c.diagnostic, "invalid: BACKUP_MAX_AGE_HOURS: expected a positive, finite number of hours within the duration range")
		return 0, 2
	}
	return time.Duration(hours * float64(time.Hour)), 0
}
