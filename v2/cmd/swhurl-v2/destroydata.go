package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/datadestroy"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// dataCluster is the three ports destroy-data reads and changes the cluster through.
type dataCluster struct {
	reader  kube.Reader
	writer  kube.Writer
	secrets kube.SecretNamer
}

// destroyData deletes a released claim or volume and its host data. The target's shape and the confirmation are
// checked before the kubeconfig is read, so a refusal there reaches no cluster. A refusal is one [ERROR] line and
// exit 2 with nothing changed; a change that failed is one [ERROR] line and exit 1.
func (c cli) destroyData(r request) int {
	target, err := datadestroy.ParseTarget(r.target)
	if err == nil {
		err = datadestroy.Confirm(target, r.confirm)
	}
	if err != nil {
		return c.destroyFailed(err)
	}
	cluster, status := c.dataCluster(r)
	if status != 0 {
		return status
	}
	outcome, err := orchestration.DestroyData(context.Background(), orchestration.DestroyInputs{
		Reader: cluster.reader, Writer: cluster.writer, Secrets: cluster.secrets, Target: target, DryRun: r.dryRun,
		Now: time.Now, Sleep: c.sleeper()})
	if err != nil {
		return c.destroyFailed(err)
	}
	if r.dryRun {
		return emit(c.out, c.diagnostic, []byte(strings.Join(datadestroy.Plan(target, outcome.Volume), "\n")))
	}
	return emit(c.out, c.diagnostic, []byte(datadestroy.Destroyed(target, outcome.Volume)))
}

// destroyFailed prints why nothing, or not everything, was done: the usage line or a refusal is exit 2, anything
// else exit 1.
func (c cli) destroyFailed(err error) int {
	var refusal *datadestroy.Refusal
	switch {
	case errors.Is(err, datadestroy.ErrUsage):
		fmt.Fprintln(c.diagnostic, err)
		return 2
	case errors.As(err, &refusal):
		fmt.Fprintf(c.diagnostic, "[ERROR] %v\n", err)
		return 2
	}
	fmt.Fprintf(c.diagnostic, "[ERROR] %v\n", err)
	return 1
}

// dataCluster is the API server the kubeconfig names; dataClusterOf is what a test supplies instead.
func (c cli) dataCluster(r request) (dataCluster, int) {
	if c.dataClusterOf != nil {
		cluster, err := c.dataClusterOf()
		if err != nil {
			return dataCluster{}, fail(c.diagnostic, err)
		}
		return cluster, 0
	}
	connection, _, status := c.connection(r)
	if status != 0 {
		return dataCluster{}, status
	}
	reader, err := connection.Reader()
	if err != nil {
		return dataCluster{}, fail(c.diagnostic, err)
	}
	writer, err := connection.Writer()
	if err != nil {
		return dataCluster{}, fail(c.diagnostic, err)
	}
	secrets, err := connection.SecretNamer()
	if err != nil {
		return dataCluster{}, fail(c.diagnostic, err)
	}
	return dataCluster{reader: reader, writer: writer, secrets: secrets}, 0
}
