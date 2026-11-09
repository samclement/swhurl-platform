package main

import (
	"context"
	"fmt"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// A unit action is the console's reconcile, suspend or resume of one Flux unit. The console keeps the lines as the
// job's output and reads the unit's page afterwards, so every line of progress goes to standard output and only a
// refusal goes to standard error. The action reads and patches the API server: it runs no command, and it changes
// nothing outside the unit and its source.

// unitActions are the three actions, under the names the console's paths use.
var unitActions = map[string]orchestration.FluxAction{
	"reconcile": orchestration.FluxReconcile,
	"suspend":   orchestration.FluxSuspend,
	"resume":    orchestration.FluxResume,
}

// unit performs one action on one unit.
func (c cli) unit(r request) int {
	action, known := unitActions[r.action]
	if !known {
		return fail(c.diagnostic, fmt.Errorf("invalid: action: %q is not reconcile, suspend or resume", r.action))
	}
	cluster, status := c.unitCluster(r)
	if status != 0 {
		return status
	}
	err := orchestration.Flux(context.Background(), orchestration.FluxInputs{Action: action, Unit: r.unit,
		Cluster: cluster, Now: time.Now, Sleep: time.Sleep,
		Progress: func(line string) { fmt.Fprintln(c.out, line) }})
	if err != nil {
		return fail(c.diagnostic, err)
	}
	return 0
}

// unitCluster is the API server this action changes: the one credential the command was given, and nothing else.
// unitClusterOf is what a test supplies instead, so an action needs no network.
func (c cli) unitCluster(r request) (orchestration.Cluster, int) {
	if c.unitClusterOf != nil {
		cluster, err := c.unitClusterOf()
		if err != nil {
			return orchestration.Cluster{}, fail(c.diagnostic, err)
		}
		return cluster, 0
	}
	connection, _, status := c.connection(r)
	if status != 0 {
		return orchestration.Cluster{}, status
	}
	client, err := kube.Client(connection.Credentials)
	if err != nil {
		return orchestration.Cluster{}, fail(c.diagnostic, err)
	}
	// One request's bound; the whole action's ten minutes is the use case's wait, not the HTTP client's.
	client.Timeout = 30 * time.Second
	api, err := kube.NewAPI(connection.Endpoint, client, connection.Token)
	if err != nil {
		return orchestration.Cluster{}, fail(c.diagnostic, err)
	}
	return orchestration.Cluster{Reader: api, Writer: api}, 0
}
