package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/clickstack"
	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// The wait for HyperDX's API after an install, and the bound on the whole run.
const (
	bootstrapWait     = 5 * time.Minute
	bootstrapInterval = 5 * time.Second
	bootstrapTimeout  = bootstrapWait + 2*time.Minute
)

// clickstackBootstrap makes HyperDX's admin account and team ingestion key the ones in Git and prints what it found
// and did as [OK], [INFO] and [WARN] lines, then [BAD] and exit 1 where it stopped. No line holds a credential.
func (c cli) clickstackBootstrap(r request) int {
	connection, _, status := c.connection(r)
	if status != 0 {
		return status
	}
	ports, err := bootstrapPorts(connection)
	if err != nil {
		fmt.Fprintln(c.diagnostic, "error: Kubernetes connection failed")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), bootstrapTimeout)
	defer cancel()
	_, _ = c.out.Write([]byte("\n== ClickStack bootstrap ==\n"))
	report, err := orchestration.BootstrapClickStack(ctx, ports, bootstrapOptions(r))
	var out strings.Builder
	for _, line := range report.Lines {
		fmt.Fprintf(&out, "[%s] %s\n", strings.ToUpper(string(line.Level)), line.Text)
	}
	if err != nil {
		fmt.Fprintf(&out, "[BAD] %s\n", err.Error())
	}
	_, _ = c.out.Write([]byte(out.String()))
	if err != nil {
		return 1
	}
	return 0
}

// bootstrapOptions is the command line's dry run and the fixed wait for HyperDX's API.
func bootstrapOptions(r request) orchestration.BootstrapOptions {
	return orchestration.BootstrapOptions{DryRun: r.dryRun, Wait: bootstrapWait, Interval: bootstrapInterval}
}

// bootstrapPorts is the bootstrap's named Secrets, and HyperDX reached through the API server's exec.
func bootstrapPorts(connection kube.Connection) (orchestration.BootstrapPorts, error) {
	reader, err := connection.Reader()
	if err != nil {
		return orchestration.BootstrapPorts{}, err
	}
	data, err := connection.Data()
	if err != nil {
		return orchestration.BootstrapPorts{}, err
	}
	executor, err := connection.Executor()
	if err != nil {
		return orchestration.BootstrapPorts{}, err
	}
	mongo, app := orchestration.ClickStackRunners(reader, executor)
	return orchestration.BootstrapPorts{Data: data, Installer: clickstack.NewInstaller(mongo, app)}, nil
}
