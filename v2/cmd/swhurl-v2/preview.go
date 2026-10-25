package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/legacy"
	"github.com/samclement/swhurl-platform/v2/shadow"
)

// previewNote is what the document says about itself: a preview of known workload facts is not a
// migration plan, a publication or a parity claim.
const previewNote = "a preview of the workload facts both sides state; not a migration plan, a publication or a parity claim"

// preview renders one predecessor instance through v2's renderer and compares the result with what the
// predecessor committed. Nothing is applied, and nothing is read but the five named files.
func (c cli) preview(r request) int {
	documents, status := c.previewRecords(r)
	if status != 0 {
		return status
	}
	translation, err := legacy.Translate(documents)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	profile, status := c.profile(r.profile)
	if status != 0 {
		return status
	}
	subject := previewSubject(translation)
	rendered, err := kernel.RenderHTTP(subject.instance, subject.workload, profile)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	after, ok := renderedHelmRelease(rendered)
	if !ok {
		return fail(c.diagnostic, fmt.Errorf("invalid: compilation: the HelmRelease is missing"))
	}
	comparison, err := shadow.CompareHTTP(documents.Committed, after,
		shadow.ResourceChoices{CPURequestMilli: profile.Workload.CPURequestMilli, MemoryMiB: translation.Instance.MemoryMiB})
	if err != nil {
		return fail(c.diagnostic, err)
	}
	document, err := encodePreview(translation, comparison)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	fmt.Fprintln(c.diagnostic, previewSummary(comparison))
	return emit(c.out, c.diagnostic, document)
}

// previewRecords reads the five documents a preview needs: the three legacy records, the predecessor's
// committed output and the platform profile.
func (c cli) previewRecords(r request) (legacy.Records, int) {
	var documents legacy.Records
	for _, file := range []struct {
		path string
		what string
		into *[]byte
	}{
		{r.app, "app", &documents.App},
		{r.settings, "settings", &documents.Instance},
		{r.release, "release", &documents.Release},
		{r.committed, "committed", &documents.Committed},
	} {
		data, status := c.read(file.path, file.what)
		if status != 0 {
			return legacy.Records{}, status
		}
		*file.into = data
	}
	return documents, 0
}

// subject is the translation as the renderer's inputs: the workload facts and the environment facts.
type subject struct {
	workload kernel.HTTPWorkload
	instance kernel.HTTPInstance
}

// previewSubject maps the translated facts onto the renderer's inputs. The renderer validates them, so a
// legacy value the compiled output cannot carry is refused there rather than here.
func previewSubject(translation legacy.Translation) subject {
	return subject{
		workload: kernel.HTTPWorkload{Image: translation.Workload.Image, Digest: translation.Workload.Digest,
			Port: translation.Workload.Port, HealthPath: translation.Workload.HealthPath, UID: translation.Workload.UID},
		instance: kernel.HTTPInstance{Application: translation.Application, Environment: translation.Environment,
			Configuration: kernel.Configuration{Replicas: translation.Instance.Replicas, MemoryMiB: translation.Instance.MemoryMiB}},
	}
}

// renderedHelmRelease is the HelmRelease among the rendered files: the document a comparison reads.
func renderedHelmRelease(rendered kernel.ResourceSet) ([]byte, bool) {
	for _, file := range rendered.Files {
		if filepath.Base(file.Path) == "helmrelease.json" {
			return file.Document, true
		}
	}
	return nil, false
}

// previewDocument is the report's shape: field order is the document order, and a change raises Version.
type previewDocument struct {
	Kind        string         `json:"kind"`
	Version     int            `json:"version"`
	Application string         `json:"application"`
	Environment string         `json:"environment"`
	Preview     previewFacts   `json:"preview"`
	Gaps        []gapDocument  `json:"gaps"`
	Comparison  comparedFields `json:"comparison"`
	Note        string         `json:"note"`
}

type previewFacts struct {
	Workload workloadDocument  `json:"workload"`
	Instance instanceDocument  `json:"instance"`
	Sources  map[string]string `json:"sources"`
}

type workloadDocument struct {
	Image      string `json:"image"`
	Tag        string `json:"tag,omitempty"`
	Digest     string `json:"digest"`
	Kind       string `json:"kind"`
	Port       int32  `json:"port"`
	HealthPath string `json:"healthPath"`
	UID        int32  `json:"uid"`
}

type instanceDocument struct {
	Exposure  string `json:"exposure"`
	Replicas  int32  `json:"replicas"`
	MemoryMiB int32  `json:"memoryMiB"`
	Team      string `json:"team,omitempty"`
}

type gapDocument struct {
	Where string `json:"where"`
	Fact  string `json:"fact"`
	Owner string `json:"owner"`
}

type comparedFields struct {
	Findings   []findingDocument `json:"findings"`
	Assessed   []string          `json:"assessed"`
	Unassessed []string          `json:"unassessed"`
}

type findingDocument struct {
	Field   string `json:"field"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`
}

// encodePreview writes the report. Lists are present even when empty, and the source map is sorted by the
// encoder, so two identical previews encode identically.
func encodePreview(translation legacy.Translation, comparison shadow.HTTPComparison) ([]byte, error) {
	document := previewDocument{
		Kind: "v2-preview", Version: 1,
		Application: translation.Application, Environment: translation.Environment,
		Preview: previewFacts{
			Workload: workloadDocument{translation.Workload.Image, translation.Workload.Tag, translation.Workload.Digest,
				translation.Workload.Kind, translation.Workload.Port, translation.Workload.HealthPath, translation.Workload.UID},
			Instance: instanceDocument{translation.Instance.Exposure, translation.Instance.Replicas,
				translation.Instance.MemoryMiB, translation.Instance.Team},
			Sources: translation.Sources,
		},
		Gaps:       make([]gapDocument, 0, len(translation.Gaps)),
		Comparison: comparedFields{Findings: make([]findingDocument, 0, len(comparison.Findings)), Assessed: nonNil(comparison.AssessedScope), Unassessed: nonNil(comparison.UnassessedScope)},
		Note:       previewNote,
	}
	for _, gap := range translation.Gaps {
		document.Gaps = append(document.Gaps, gapDocument{gap.Where, gap.Fact, gap.Owner})
	}
	for _, finding := range comparison.Findings {
		document.Comparison.Findings = append(document.Comparison.Findings, findingDocument{finding.Field, finding.Outcome, finding.Reason})
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), nil
}

// previewSummary is the one human line for stderr: the comparison outcomes that need an explanation or
// are a difference, which is what the operator reads first.
func previewSummary(comparison shadow.HTTPComparison) string {
	var named []string
	for _, finding := range comparison.Findings {
		if finding.Outcome == "different" || finding.Outcome == "not_assessed" {
			name := finding.Field + " " + finding.Outcome
			if finding.Reason != "" {
				name += "/" + finding.Reason
			}
			named = append(named, name)
		}
	}
	if len(named) == 0 {
		return "preview: every assessed field agrees"
	}
	return "preview: " + strings.Join(named, "; ")
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
