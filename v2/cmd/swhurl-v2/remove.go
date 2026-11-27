package main

import (
	"fmt"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/apps"
)

// removalDocument is the versioned report `remove` writes: the files it wrote and deleted, and what it left to the
// platform's own renderers.
type removalDocument struct {
	Kind        string          `json:"kind"`
	Version     int             `json:"version"`
	Application string          `json:"application"`
	Environment string          `json:"environment"`
	Files       []files.Outcome `json:"files"`
	Removed     []files.Outcome `json:"removed"`
	Notes       []string        `json:"notes"`
}

// remove deletes one v2-owned instance: its records, the files compiled from them and its unit, the namespace's auth
// object, and the app record when this was its app's last environment.
func (c cli) remove(r request) int {
	if c.appTreeRefused(r.out) {
		return 2
	}
	result, err := apps.RemoveIn(r.checkout(), r.app, r.env, c.writer)
	if err != nil {
		return c.wrote(append(result.Written, result.Removed...), err)
	}
	document, err := encodeRemoval(result)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	fmt.Fprintln(c.diagnostic, result.Summary())
	for _, note := range result.Notes {
		fmt.Fprintf(c.diagnostic, "next: %s\n", note)
	}
	return emit(c.out, c.diagnostic, document)
}

// encodeRemoval is the report of one removal.
func encodeRemoval(result apps.Removal) ([]byte, error) {
	return encodeDocument(removalDocument{Kind: "v2-remove", Version: 1, Application: result.Application,
		Environment: result.Environment, Files: nonNilOutcomes(result.Written),
		Removed: nonNilOutcomes(result.Removed), Notes: nonNil(result.Notes)})
}

// nonNil is a report's list: empty rather than absent, so a reader sees "nothing" instead of "unknown".
func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
