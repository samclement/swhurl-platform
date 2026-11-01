package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/samclement/swhurl-platform/v2/kernel"
)

// declaration decodes the declaration label an image build printed and applies the creation rules an instance of it
// would face, with no image, no registry and no write. The stack templates' check uses it: a rendered app's label is
// the same document its image will carry, so what the platform would create from it can be proved before a build.

// declarationNote says what the command read and what it did not: the label is a claim about an image this run never
// fetched, so the report is about the declaration rather than about a delivery.
const declarationNote = "the declaration alone: no image, no registry and no write; a create reads the same label from " +
	"the image the tag names"

// declarationDocument is the report: what the label states, and the identities a create from it would write.
type declarationDocument struct {
	Kind            string                  `json:"kind"`
	Version         int                     `json:"version"`
	Application     string                  `json:"application"`
	SchemaVersion   int32                   `json:"schemaVersion"`
	Runtime         kernel.Runtime          `json:"runtime"`
	Telemetry       bool                    `json:"telemetry,omitempty"`
	Database        string                  `json:"database,omitempty"`
	DatabaseSizeMiB int32                   `json:"databaseSizeMiB,omitempty"`
	Publishes       []string                `json:"publishes,omitempty"`
	MemoryMiB       int32                   `json:"memoryMiB"`
	Description     string                  `json:"description,omitempty"`
	APIPath         string                  `json:"apiPath,omitempty"`
	Documents       []declarationDocumentOf `json:"documents"`
	Release         kernel.ReleaseID        `json:"release"`
	Instance        kernel.InstanceRevision `json:"instance"`
	Note            string                  `json:"note"`
}

// declarationDocumentOf is one offered document as the report names it: the role the label states, the fixed name it
// takes in the instance's offers folder, and how many bytes it is.
type declarationDocumentOf struct {
	Role  string `json:"role"`
	File  string `json:"file"`
	Bytes int    `json:"bytes"`
}

// declaration reads one label, applies the create rules, and reports what the image states.
func (c cli) declaration(r request) int {
	data, status := c.read(r.label, "declaration label")
	if status != 0 {
		return status
	}
	decoded, err := kernel.DecodeDeclaration(labelJSON(data))
	if err != nil {
		return c.failure(err)
	}
	spec, _, err := declarationInstance(r, decoded)
	if err != nil {
		return c.failure(err)
	}
	document, err := encodeDeclaration(r.app, decoded, spec)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	fmt.Fprintf(c.diagnostic, "declaration: %s states %s, %d MiB, %d document(s)\n", r.app, decoded.Runtime.Kind,
		decoded.MemoryMiB, len(decoded.Documents))
	return emit(c.out, c.diagnostic, document)
}

// labelJSON is the JSON inside the one line an image build passes to the build as a config label: `<key>=<json>`, or
// the JSON on its own, so a file holding either is read.
func labelJSON(data []byte) []byte {
	line := bytes.TrimSpace(data)
	return bytes.TrimPrefix(line, []byte(kernel.DeclarationLabel+"="))
}

// declarationInstance is what a create from this declaration would decide: its release's identity is computed from
// placeholder artifact facts, because the label says nothing about the image it travels in, and its instance is the
// kernel's creation rule applied to the app, team and exposure the caller states.
func declarationInstance(r request, decoded kernel.Declaration) (kernel.InstanceSpec, kernel.AppSpec, error) {
	release, err := decoded.Release(r.app, "check-templates/app", strings.Repeat("0", 40),
		"ghcr.io/check-templates/app", "sha256:"+strings.Repeat("0", 64), kernel.ManifestMediaType)
	if err != nil {
		return kernel.InstanceSpec{}, kernel.AppSpec{}, err
	}
	creation := kernel.Creation{Application: r.app, Team: r.team, Release: release, Declaration: decoded,
		Exposure: r.exposure}
	return creation.Instance()
}

// encodeDeclaration is the report of one decoded label.
func encodeDeclaration(application string, decoded kernel.Declaration, spec kernel.InstanceSpec) ([]byte, error) {
	revision, err := spec.Revision()
	if err != nil {
		return nil, err
	}
	documents := make([]declarationDocumentOf, 0, len(decoded.Documents))
	for _, document := range decoded.OfferedFiles() {
		role := roleOf(document.Path)
		documents = append(documents, declarationDocumentOf{Role: role, File: document.Path,
			Bytes: len(document.Document)})
	}
	document := declarationDocument{Kind: "v2-declaration", Version: 1, Application: application,
		SchemaVersion: decoded.SchemaVersion, Runtime: decoded.Runtime, Telemetry: decoded.Telemetry,
		Database: decoded.Database, DatabaseSizeMiB: decoded.DatabaseSizeMiB, Publishes: decoded.Publishes,
		MemoryMiB: decoded.MemoryMiB, Description: decoded.Description, APIPath: decoded.APIPath,
		Documents: documents, Release: spec.Release, Instance: revision, Note: declarationNote}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), nil
}

// roleOf is the role a declared document's fixed file name stands for.
func roleOf(name string) string {
	for role, file := range kernel.DocumentRoles {
		if file == name {
			return role
		}
	}
	return ""
}
