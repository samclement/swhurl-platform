package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// The bound make flux-reconcile's wait has always had, and how often it reads.
const (
	fluxWaitTimeout = 20 * time.Minute
	fluxWaitPoll    = 2 * time.Second
)

// fluxWait waits for every Flux unit at the source's newest revision, printing each line as it happens: a heading,
// then [OK], [INFO], [WARN] and [BAD] lines. A wait that stopped at [BAD] is exit 1, and so is a failed read, on
// standard error.
func (c cli) fluxWait(r request) int {
	cluster, status := c.unitCluster(r)
	if status != 0 {
		return status
	}
	sleep := c.sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	passed, err := orchestration.FluxWait(context.Background(), orchestration.FluxWaitInputs{Reader: cluster.Reader,
		Timeout: fluxWaitTimeout, Poll: fluxWaitPoll, Now: time.Now, Sleep: sleep,
		Progress: func(line orchestration.FluxWaitLine) {
			if line.Level == orchestration.FluxWaitHeading {
				fmt.Fprintf(c.out, "\n== %s ==\n", line.Text)
				return
			}
			fmt.Fprintf(c.out, "[%s] %s\n", strings.ToUpper(string(line.Level)), line.Text)
		}})
	if err != nil {
		fmt.Fprintf(c.diagnostic, "[ERROR] could not read Flux state: %v\n", err)
		return 1
	}
	if !passed {
		return 1
	}
	return 0
}
