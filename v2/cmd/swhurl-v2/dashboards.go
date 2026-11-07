package main

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/clickstack"
	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// dashboardsTimeout bounds one pass inside the CronJob's 50 seconds.
const dashboardsTimeout = 45 * time.Second

// dashboardsNamed requires exactly one source of apps, the cluster or a records tree, and exactly one credential.
func (r *request) dashboardsNamed() []string {
	if (r.kubeconfig != "") == r.inCluster || (r.records != "") == r.cluster {
		return []string{""}
	}
	return []string{}
}

// clickstackDashboards runs one dashboard pass and logs each outcome as a JSON line. It exits 1 when the pass
// stopped, after logging how far it got; no line holds a credential.
func (c cli) clickstackDashboards(r request) int {
	logger := &jsonLog{out: c.out, now: time.Now, name: "clickstack-dashboards"}
	options := orchestration.DashboardOptions{DryRun: r.dryRun}
	if r.records != "" {
		records, status := c.dashboardRecords(r.records)
		if status != 0 {
			return status
		}
		options.FromRecords, options.Records = true, records
	}
	connection, _, status := c.connection(r)
	if status != 0 {
		return status
	}
	ports, err := dashboardPorts(connection)
	if err != nil {
		logger.write("ERROR", "Kubernetes connection failed")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), dashboardsTimeout)
	defer cancel()
	report, err := orchestration.SyncDashboards(ctx, ports, options)
	for _, line := range report.Lines {
		logger.write("INFO", line.Text)
	}
	if err != nil {
		logger.write("ERROR", err.Error())
		return 1
	}
	logger.write("INFO", "ClickStack dashboard pass completed, dry_run="+map[bool]string{false: "false", true: "true"}[r.dryRun])
	return 0
}

// dashboardPorts is the pass's reads, its named Secrets and record, and ClickStack reached through the API server's
// exec.
func dashboardPorts(connection kube.Connection) (orchestration.DashboardPorts, error) {
	reader, err := connection.Reader()
	if err != nil {
		return orchestration.DashboardPorts{}, err
	}
	data, err := connection.Data()
	if err != nil {
		return orchestration.DashboardPorts{}, err
	}
	executor, err := connection.Executor()
	if err != nil {
		return orchestration.DashboardPorts{}, err
	}
	mongo, app := orchestration.ClickStackRunners(reader, executor)
	return orchestration.DashboardPorts{Reader: reader, Data: data, ClickStack: clickstack.New(mongo, app)}, nil
}

// dashboardRecords is every instance folder of the records tree with its release record. A tree that cannot be
// read, or a release record that cannot, is a failure, never an app left out.
func (c cli) dashboardRecords(root string) ([]orchestration.RecordedInstance, int) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fail(c.diagnostic, err)
	}
	records := []orchestration.RecordedInstance{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		environments, err := appEnvironments(root, entry.Name())
		if err != nil {
			return nil, fail(c.diagnostic, err)
		}
		for _, environment := range environments {
			what := entry.Name() + "/" + environment + " release"
			release, status := c.read(filepath.Join(root, entry.Name(), environment, releaseFile), what)
			if status != 0 {
				return nil, status
			}
			records = append(records, orchestration.RecordedInstance{Application: entry.Name(), Environment: environment, Release: release})
		}
	}
	return records, 0
}
