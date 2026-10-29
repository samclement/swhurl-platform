package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/legacy"
	"github.com/samclement/swhurl-platform/v2/shadow"
)

// notCovered names the facts a shadow report never establishes, whatever the snapshot holds.
func notCovered() []string {
	return []string{"routes", "storage", "secrets", "calls", "events", "team", "alerts", "telemetry",
		"quota", "release.provenance"}
}

// shadowNote is what the document says about itself: an analysis of one snapshot, not an approval.
const shadowNote = "an analysis of one snapshot; not a migration plan, a publication or a parity claim"

// snapshot is one pinned revision as the exporter wrote it: every instance's records and the identities
// of the objects its committed output declares, plus units no record explains.
type snapshot struct {
	Version   int                `json:"version"`
	Revision  string             `json:"revision"`
	Instances []snapshotInstance `json:"instances"`
	Residual  []snapshotUnit     `json:"residual"`
}

type snapshotInstance struct {
	Application string `json:"application"`
	Environment string `json:"environment"`
	// Format is which record shape the export read: "v2" for an instance v2 owns, whose records are its JSON
	// pair, and "v1" (or absent, in an older snapshot) for a predecessor's YAML records.
	Format  string                     `json:"format"`
	Records map[string]json.RawMessage `json:"records"`
	Missing []string                   `json:"missing"`
	Objects []identity                 `json:"objects"`
}

type snapshotUnit struct {
	Unit   string   `json:"unit"`
	Object identity `json:"object"`
}

// identity is one Kubernetes object as a committed manifest declares it.
type identity struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
}

// shadow reads a snapshot, previews every instance through the renderer, and reports each one's
// comparison with its own committed output and every ownership collision in the snapshot. Nothing is
// applied, and no file is read but the two named ones.
func (c cli) shadow(r request) int {
	data, status := c.read(r.snapshot, "snapshot")
	if status != 0 {
		return status
	}
	pinned, err := decodeSnapshot(data)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	profile, status := c.profile(r.profile)
	if status != 0 {
		return status
	}
	report := shadowReport(pinned, profile)
	if r.kubeconfig != "" {
		if status := c.readLive(r.kubeconfig, &report); status != 0 {
			return status
		}
	}
	document, err := encodeShadow(report)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	fmt.Fprintln(c.diagnostic, report.summary())
	return emit(c.out, c.diagnostic, document)
}

// decodeSnapshot refuses a snapshot this command cannot analyse: a document whose version it does not
// know, a revision that is not a commit, or an instance with no identity.
func decodeSnapshot(data []byte) (snapshot, error) {
	var pinned snapshot
	if err := json.Unmarshal(data, &pinned); err != nil {
		return snapshot{}, fmt.Errorf("invalid: snapshot: single_json_object_required")
	}
	if pinned.Version != 1 {
		return snapshot{}, fmt.Errorf("unsupported: snapshot: only_version_1")
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(pinned.Revision) {
		return snapshot{}, fmt.Errorf("invalid: snapshot: revision_required")
	}
	for _, instance := range pinned.Instances {
		if instance.Application == "" || instance.Environment == "" {
			return snapshot{}, fmt.Errorf("invalid: snapshot: instance_identity_required")
		}
	}
	return pinned, nil
}

// analysed is one instance's result: what could be compared, or why nothing could, the claims its
// preview would make, and — when a kubeconfig was given — what the live cluster holds for them.
type analysed struct {
	Application, Environment string
	Available                bool
	Reason                   string
	Gaps                     []string
	Comparison               *shadow.HTTPComparison
	Claims                   []kernel.Claim
	Conflicts                []kernel.Conflict
	Live                     []liveRead
}

// liveRead is what one live read established about an object a preview would create: present, absent, or
// unavailable with a reason. Absence is only ever a successful answer that the object is not there.
type liveRead struct {
	Object string
	Status string
	Reason string
}

// report is one shadow analysis: the snapshot's identity, each instance's result, and its coverage.
type report struct {
	Revision    string
	Profile     kernel.ProfileID
	Instances   []analysed
	Residual    []string
	Declared    int
	Unavailable int
}

// shadowReport analyses every instance and the snapshot's ownership as a whole. The instance being
// compared is not exempt: its preview collides with its own declared objects, and that collision is
// reported like any other.
func shadowReport(pinned snapshot, profile kernel.PlatformProfile) report {
	report := report{Revision: pinned.Revision, Declared: declaredObjects(pinned)}
	if id, err := profile.ID(); err == nil {
		report.Profile = id
	}
	for _, unit := range pinned.Residual {
		report.Residual = append(report.Residual, unit.Unit+" "+objectText(unit.Object))
	}
	claims := make([]kernel.Claim, 0, report.Declared)
	for _, instance := range pinned.Instances {
		owner := instance.Application + "/" + instance.Environment
		for _, object := range instance.Objects {
			claims = append(claims, kernel.Claim{Object: objectOf(object), Owner: owner})
		}
		result := analyse(instance, profile, owner)
		if result.Available {
			claims = append(claims, result.Claims...)
		} else {
			report.Unavailable++
		}
		report.Instances = append(report.Instances, result)
	}
	for _, unit := range pinned.Residual {
		claims = append(claims, kernel.Claim{Object: objectOf(unit.Object), Owner: "residual:" + unit.Unit})
	}
	conflicts := kernel.Ownership(claims)
	for i := range report.Instances {
		report.Instances[i].Conflicts = conflictsOf(conflicts, report.Instances[i].Application+"/"+report.Instances[i].Environment)
	}
	return report
}

// analyse previews one instance. A translation or rendering refusal makes that instance unavailable with
// its reason; it never removes the others.
func analyse(instance snapshotInstance, profile kernel.PlatformProfile, owner string) analysed {
	result := analysed{Application: instance.Application, Environment: instance.Environment}
	translation, err := legacy.Translate(legacy.Records{App: instance.Records["app"], Instance: instance.Records["instance"],
		Release: instance.Records["release"], Committed: instance.Records["helmrelease"]})
	if err != nil {
		result.Reason = err.Error()
		return result
	}
	for _, gap := range translation.Gaps {
		result.Gaps = append(result.Gaps, gap.Fact)
	}
	subject := previewSubject(translation)
	rendered, err := kernel.RenderInstance(subject.instance, subject.workload, profile)
	if err != nil {
		result.Reason = err.Error()
		return result
	}
	after, ok := renderedHelmRelease(rendered)
	if !ok {
		result.Reason = "invalid: compilation: the HelmRelease is missing"
		return result
	}
	comparison, err := shadow.CompareHTTP(instance.Records["helmrelease"], after,
		shadow.ResourceChoices{CPURequestMilli: profile.Workload.CPURequestMilli, MemoryMiB: translation.Instance.MemoryMiB})
	if err != nil {
		result.Reason = err.Error()
		return result
	}
	result.Available, result.Comparison = true, &comparison
	for _, claim := range rendered.Inventory {
		result.Claims = append(result.Claims, kernel.Claim{Object: claim.Object, Owner: owner})
	}
	return result
}

// readLive reads every object a preview would create, from the API server the kubeconfig names. A failed
// read stays unavailable: it is never reported as absence, and it never changes a comparison.
func (c cli) readLive(kubeconfig string, report *report) int {
	data, status := c.read(kubeconfig, "kubeconfig")
	if status != 0 {
		return status
	}
	connection, err := kube.Connect(data)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	reader, err := connection.Reader()
	if err != nil {
		return fail(c.diagnostic, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()
	for i := range report.Instances {
		instance := &report.Instances[i]
		if !instance.Available {
			continue
		}
		for _, claim := range instance.Claims {
			ref, err := kube.RefOf(claim.Object.Group, claim.Object.Kind, claim.Object.Namespace, claim.Object.Name)
			if err != nil {
				instance.Live = append(instance.Live, liveRead{objectTextOf(claim.Object), "not_checked", "unsupported_kind"})
				continue
			}
			read, err := reader.Read(ctx, ref)
			if err != nil {
				return fail(c.diagnostic, err)
			}
			instance.Live = append(instance.Live, liveRead{objectTextOf(claim.Object), read.Status, read.Reason})
		}
	}
	return 0
}

// conflictsOf is the collisions an instance is part of.
func conflictsOf(conflicts []kernel.Conflict, owner string) []kernel.Conflict {
	var mine []kernel.Conflict
	for _, conflict := range conflicts {
		for _, claimant := range conflict.Owners {
			if claimant == owner {
				mine = append(mine, conflict)
				break
			}
		}
	}
	return mine
}

// declaredObjects is how many objects the snapshot's manifests declare.
func declaredObjects(pinned snapshot) int {
	count := len(pinned.Residual)
	for _, instance := range pinned.Instances {
		count += len(instance.Objects)
	}
	return count
}

// objectOf maps a manifest identity onto the compiler's object identity.
func objectOf(object identity) kernel.Object {
	group, _, cut := strings.Cut(object.APIVersion, "/")
	if !cut || group == "v1" {
		group = ""
	}
	return kernel.Object{Group: group, Kind: object.Kind, Namespace: object.Namespace, Name: object.Name}
}

// objectText names an object as Kubernetes tools do.
func objectText(object identity) string {
	kind := object.Kind
	if group, _, cut := strings.Cut(object.APIVersion, "/"); cut && group != "v1" {
		kind += "." + group
	}
	if object.Namespace == "" {
		return kind + " " + object.Name
	}
	return kind + " " + object.Namespace + "/" + object.Name
}

// summary is the one human line for stderr: coverage first, then the instances that need a look.
func (r report) summary() string {
	text := fmt.Sprintf("shadow: %d declared object(s), %d compared, %d unavailable", r.Declared,
		len(r.Instances)-r.Unavailable, r.Unavailable)
	var noted []string
	for _, instance := range r.Instances {
		name := instance.Application + "/" + instance.Environment
		if !instance.Available {
			noted = append(noted, name+" unavailable")
			continue
		}
		for _, finding := range instance.Comparison.Findings {
			if finding.Outcome == "different" {
				noted = append(noted, name+" "+finding.Field)
			}
		}
		if len(instance.Conflicts) > 0 {
			noted = append(noted, fmt.Sprintf("%s %d ownership collision(s)", name, len(instance.Conflicts)))
		}
	}
	if len(noted) == 0 {
		return text + ": no differences and no collisions"
	}
	return text + ": " + strings.Join(noted, "; ")
}

// shadowDocument is the report's shape: field order is the document order, and a change raises Version.
type shadowDocument struct {
	Kind      string                   `json:"kind"`
	Version   int                      `json:"version"`
	Revision  string                   `json:"revision"`
	Profile   kernel.ProfileID         `json:"profile"`
	Instances []shadowInstanceDocument `json:"instances"`
	Residual  []string                 `json:"residual"`
	Coverage  coverageDocument         `json:"coverage"`
	Note      string                   `json:"note"`
}

type shadowInstanceDocument struct {
	Application string             `json:"application"`
	Environment string             `json:"environment"`
	Available   bool               `json:"available"`
	Reason      string             `json:"reason,omitempty"`
	Gaps        []string           `json:"gaps"`
	Comparison  comparedFields     `json:"comparison"`
	Ownership   []conflictDocument `json:"ownership"`
	Live        []liveDocument     `json:"live"`
}

// liveDocument is one live answer about an object the preview would create. Present and absent are the
// two successful answers; unavailable carries the reason its read failed.
type liveDocument struct {
	Object string `json:"object"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// conflictDocument is one object more than one claim names. Owners are distinct, in claim order; Claims
// counts them all, so one owner with two claims is the instance colliding with its own committed output,
// which is expected, and two owners are a real conflict.
type conflictDocument struct {
	Object string   `json:"object"`
	Owners []string `json:"owners"`
	Claims int      `json:"claims"`
}

type coverageDocument struct {
	Declared    int      `json:"declaredObjects"`
	Compared    int      `json:"compared"`
	Unavailable int      `json:"unavailable"`
	NotCovered  []string `json:"notCovered"`
}

// encodeShadow writes the report. Every list is present even when empty, so a consumer never treats null
// and empty as different.
func encodeShadow(report report) ([]byte, error) {
	document := shadowDocument{
		Kind: "v2-shadow", Version: 1, Revision: report.Revision, Profile: report.Profile,
		Instances: make([]shadowInstanceDocument, 0, len(report.Instances)), Residual: nonNil(report.Residual),
		Coverage: coverageDocument{Declared: report.Declared, Compared: len(report.Instances) - report.Unavailable,
			Unavailable: report.Unavailable, NotCovered: notCovered()},
		Note: shadowNote,
	}
	for _, instance := range report.Instances {
		entry := shadowInstanceDocument{Application: instance.Application, Environment: instance.Environment,
			Available: instance.Available, Reason: instance.Reason, Gaps: nonNil(instance.Gaps),
			Comparison: comparedFields{Findings: []findingDocument{}, Assessed: []string{}, Unassessed: []string{}},
			Ownership:  make([]conflictDocument, 0, len(instance.Conflicts)),
			Live:       make([]liveDocument, 0, len(instance.Live))}
		if instance.Comparison != nil {
			entry.Comparison = comparedFields{
				Findings:   findingsOf(instance.Comparison.Findings),
				Assessed:   nonNil(instance.Comparison.AssessedScope),
				Unassessed: nonNil(instance.Comparison.UnassessedScope),
			}
		}
		for _, live := range instance.Live {
			entry.Live = append(entry.Live, liveDocument{live.Object, live.Status, live.Reason})
		}
		for _, conflict := range instance.Conflicts {
			entry.Ownership = append(entry.Ownership, conflictDocument{objectTextOf(conflict.Object),
				distinct(conflict.Owners), len(conflict.Owners)})
		}
		document.Instances = append(document.Instances, entry)
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), nil
}

// findingsOf is the comparison's findings in the document's shape.
func findingsOf(findings []shadow.Finding) []findingDocument {
	converted := make([]findingDocument, 0, len(findings))
	for _, finding := range findings {
		converted = append(converted, findingDocument{finding.Field, finding.Outcome, finding.Reason})
	}
	return converted
}

// distinct keeps each owner once, in the order it first claimed the object.
func distinct(owners []string) []string {
	seen := map[string]bool{}
	kept := make([]string, 0, len(owners))
	for _, owner := range owners {
		if !seen[owner] {
			seen[owner] = true
			kept = append(kept, owner)
		}
	}
	return kept
}

// objectTextOf names a compiled object as the inventory does.
func objectTextOf(object kernel.Object) string {
	kind := strings.Join([]string{object.Kind, object.Group}, ".")
	if object.Group == "" {
		kind = object.Kind
	}
	if object.Namespace == "" {
		return kind + " " + object.Name
	}
	return kind + " " + object.Namespace + "/" + object.Name
}
