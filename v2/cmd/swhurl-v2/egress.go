package main

import (
	connectivityrules "github.com/samclement/swhurl-platform/v2/connectivity/rules"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// allocation reads an allocation document the platform wrote, for the commands that take one.
func (c cli) allocation(path, what string) (kernel.EgressDocument, int) {
	data, status := c.read(path, what)
	if status != 0 {
		return kernel.EgressDocument{}, status
	}
	document, err := connectivityrules.DecodeEgressDocument(data)
	if err != nil {
		return kernel.EgressDocument{}, fail(c.diagnostic, err)
	}
	return document, 0
}
