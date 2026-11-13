package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// A unit action is the console's reconcile, suspend or resume of one Flux unit, and make suspend's and make resume's
// of a unit or one HelmRelease (--target). The console keeps the lines as the job's output and reads the unit's page
// afterwards, so every line of progress goes to standard output and only a refusal goes to standard error. The action
// reads and patches the API server: it runs no command, and it changes nothing outside the unit, its source and the
// release it names.

// unitActions are the three actions, under the names the console's paths use.
var unitActions = map[string]orchestration.FluxAction{
	"reconcile": orchestration.FluxReconcile,
	"suspend":   orchestration.FluxSuspend,
	"resume":    orchestration.FluxResume,
}

// unit performs one action on one unit, or on what --target names: make suspend's unit or HelmRelease.
func (c cli) unit(r request) int {
	action, known := unitActions[r.action]
	if !known {
		return fail(c.diagnostic, fmt.Errorf("invalid: action: %q is not reconcile, suspend or resume", r.action))
	}
	unit, release := r.unit, (*orchestration.FluxRelease)(nil)
	if r.target != "" {
		var err error
		if unit, release, err = unitTargetOf(r.target); err != nil {
			return fail(c.diagnostic, err)
		}
	}
	cluster, status := c.unitCluster(r)
	if status != 0 {
		return status
	}
	err := orchestration.Flux(context.Background(), orchestration.FluxInputs{Action: action, Unit: unit,
		Release: release, Cluster: cluster, Now: time.Now, Sleep: time.Sleep,
		Progress: func(line string) { fmt.Fprintln(c.out, line) }})
	if err != nil {
		return fail(c.diagnostic, err)
	}
	return 0
}

// unitTargetOf parses make suspend's TARGET; Kubernetes name validation stays in the adapter before its request.
func unitTargetOf(text string) (string, *orchestration.FluxRelease, error) {
	parts := strings.Split(text, "/")
	switch {
	case parts[0] == "kustomization" && len(parts) == 2 && parts[1] != "":
		return parts[1], nil, nil
	case parts[0] == "kustomization":
		return "", nil, errors.New("invalid: target: use kustomization/<name>")
	case parts[0] == "helmrelease" && len(parts) == 3 && parts[1] != "" && parts[2] != "":
		return "", &orchestration.FluxRelease{Namespace: parts[1], Name: parts[2]}, nil
	case parts[0] == "helmrelease":
		return "", nil, errors.New("invalid: target: use helmrelease/<namespace>/<name>")
	}
	return "", nil, errors.New("invalid: target: use kustomization/<name> or helmrelease/<namespace>/<name>")
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
