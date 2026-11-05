package main

import (
	"fmt"

	"github.com/samclement/swhurl-platform/v2/adapters/helm"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

func (c cli) acquireChart(r request) int {
	profile, status := c.profile(r.profile)
	if status != 0 {
		return status
	}
	repositories, status := c.read(r.repositories, "Helm repositories")
	if status != 0 {
		return status
	}
	acquirer := c.acquirer
	if acquirer == nil {
		acquirer = helm.New()
	}
	path, err := orchestration.AcquireChart(profile, repositories, r.cache, acquirer)
	if err != nil {
		fmt.Fprintln(c.diagnostic, err)
		return 1
	}
	return emit(c.out, c.diagnostic, []byte(path+"\n"))
}
