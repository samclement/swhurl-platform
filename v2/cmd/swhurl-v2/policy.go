package main

import (
	"encoding/json"

	"github.com/samclement/swhurl-platform/v2/adapters/helm"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

type policyDocument struct {
	Kind      string                       `json:"kind"`
	Version   int                          `json:"version"`
	Instances []orchestration.PolicyResult `json:"instances"`
}

// policy uses exactly render's whole-tree readers, resolution and team/channel validation, without a writer.
func (c cli) policy(r request) int {
	rendered, status := c.renderTree(r, provisionalRevision)
	if status != 0 {
		return status
	}
	rendered, status = c.withNotify(r, rendered)
	if status != 0 {
		return status
	}
	renderer := c.charts
	if renderer == nil {
		renderer = helm.New()
	}
	results, err := orchestration.CheckPolicy(rendered.instances, r.chart, renderer)
	if err != nil {
		return c.failure(err)
	}
	data, err := json.MarshalIndent(policyDocument{Kind: "v2-policy", Version: 1, Instances: results}, "", "  ")
	if err != nil {
		return c.failure(err)
	}
	if status := emit(c.out, c.diagnostic, data); status != 0 {
		return status
	}
	for _, result := range results {
		if result.RenderFailure != "" || len(result.Findings) > 0 {
			return 1
		}
	}
	return 0
}
