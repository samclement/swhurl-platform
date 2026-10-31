package main

import (
	"fmt"
	"path/filepath"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/orchestration"
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
	one, status := c.recordsEdit(r.out, r.records, r.app, r.env)
	if status != 0 {
		return status
	}
	inputs, status := c.removeInputs(r, one)
	if status != 0 {
		return status
	}
	result, err := orchestration.Remove(inputs, r.out, c.writer)
	if err != nil {
		return c.wrote(append(result.Written, result.Removed...), err)
	}
	document, err := encodeRemoval(result)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	fmt.Fprintf(c.diagnostic, "%s/%s removed: %d files removed, %d written\n", result.Application,
		result.Environment, len(result.Removed), len(result.Written))
	for _, note := range result.Notes {
		fmt.Fprintf(c.diagnostic, "next: %s\n", note)
	}
	return emit(c.out, c.diagnostic, document)
}

// removeInputs assembles one removal: the places its own files are, the platform's two registrations, and whether the
// app record goes with it.
func (c cli) removeInputs(r request, one recordsEdit) (orchestration.RemovalInputs, int) {
	profile, status := c.read(r.profile, "profile")
	if status != 0 {
		return orchestration.RemovalInputs{}, status
	}
	decoded, err := kernel.DecodeProfile(profile)
	if err != nil {
		return orchestration.RemovalInputs{}, fail(c.diagnostic, err)
	}
	spec, err := kernel.DecodeInstance(one.instance)
	if err != nil {
		return orchestration.RemovalInputs{}, fail(c.diagnostic, err)
	}
	environments, err := appEnvironments(r.records, one.application)
	if err != nil {
		fmt.Fprintf(c.diagnostic, "io: records: cannot read %s\n", filepath.Join(r.records, one.application))
		return orchestration.RemovalInputs{}, 1
	}
	records, appRecord := filepath.Dir(one.instancePath), ""
	if appRecord, err = underOut(r.out, filepath.Join(r.records, one.application, appFile)); err != nil {
		return orchestration.RemovalInputs{}, fail(c.diagnostic, err)
	}
	calls, status := c.read(r.callFile, "calls")
	if status != 0 {
		return orchestration.RemovalInputs{}, status
	}
	inputs := orchestration.RemovalInputs{Instance: one.instance, Release: one.release,
		Calls: [][]byte{calls}, Records: records,
		Compiled:        kernel.InstanceFolder(spec.Application, spec.Environment, decoded),
		Unit:            kernel.UnitPath(spec.Application, spec.Environment, decoded),
		AuthFile:        filepath.Join("platform", "auth", kernel.AuthFileName(kernel.NamespaceOf(spec.Application, spec.Environment))),
		AppRecord:       appRecord,
		LastEnvironment: len(environments) == 1}
	if inputs.UnitRegistration, status = c.registration(r.out, "clusters/home/kustomization.yaml"); status != 0 {
		return orchestration.RemovalInputs{}, status
	}
	if inputs.AuthRegistration, status = c.registration(r.out, "platform/auth/kustomization.yaml"); status != 0 {
		return orchestration.RemovalInputs{}, status
	}
	return inputs, 0
}

// registration reads one kustomization a removal edits, relative to the destination. A tree without it holds no entry
// either, so it is not read.
func (c cli) registration(out, path string) (orchestration.Registration, int) {
	document, status := c.readPresent(filepath.Join(out, filepath.FromSlash(path)), "registration")
	return orchestration.Registration{Path: path, Document: document}, status
}

// encodeRemoval is the report of one removal.
func encodeRemoval(result orchestration.Removal) ([]byte, error) {
	return encodeDocument(removalDocument{Kind: "v2-remove", Version: 1, Application: result.Application,
		Environment: result.Environment, Files: nonNilOutcomes(result.Written),
		Removed: nonNilOutcomes(result.Removed), Notes: nonNil(result.Notes)})
}
