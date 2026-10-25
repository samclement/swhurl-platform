// Package shadow owns the bounded, pure transition comparison policy, not release authority.
package shadow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ResourceChoices explain only exact planned resource changes, not publication authority.
type ResourceChoices struct{ CPURequestMilli, MemoryMiB int32 }

type Finding struct{ Field, Outcome, Reason string }

// HTTPComparison reports only selected HTTP resource groups. It establishes no whole-instance parity.
// Every call owns its non-null slices; findings never contain arbitrary input values.
type HTTPComparison struct {
	Findings        []Finding
	AssessedScope   []string
	UnassessedScope []string
}

// Error names a fixed safe field and category, never rejected values or keys.
type Error struct{ Field, Reason string }

func (e *Error) Error() string           { return fmt.Sprintf("invalid: %s: %s", e.Field, e.Reason) }
func invalid(field, reason string) error { return &Error{field, reason} }

// CompareHTTP compares actual one-controller/container app-template 5.2.1 HelmRelease JSON.
// Invalid/ambiguous documents return no comparison. Other template shapes are out of coverage.
func CompareHTTP(before, after []byte, choices ResourceChoices) (HTTPComparison, error) {
	if choices.CPURequestMilli < 1 || choices.MemoryMiB < 1 {
		return HTTPComparison{}, invalid("choices", "positive_integers_required")
	}
	a, err := project(before)
	if err != nil {
		return HTTPComparison{}, err
	}
	b, err := project(after)
	if err != nil {
		return HTTPComparison{}, err
	}
	result := HTTPComparison{[]Finding{}, []string{}, []string{}}
	for _, field := range comparedFields() {
		finding := compareField(field, a, b, choices)
		result.Findings = append(result.Findings, finding)
		if finding.Outcome == "not_assessed" {
			result.UnassessedScope = append(result.UnassessedScope, field)
		} else {
			result.AssessedScope = append(result.AssessedScope, field)
		}
	}
	result.UnassessedScope = append(result.UnassessedScope,
		"release.source_type_platform", "routes", "storage", "secrets", "calls", "additional_env_config_mounts_containers",
		"namespace.team", "networkPolicy", "quota", "flux.units_registrations", "foreignOwnership")
	return result, nil
}

func comparedFields() []string {
	return []string{"metadata", "chart", "image", "service.http", "probes", "replicas", "env.identity",
		"pod.securityContext", "pod.automountServiceAccountToken", "container.securityContext", "resources.cpuRequest", "resources.memory"}
}

type projection struct {
	fields  map[string]any
	covered bool
	// excluded names findings this document cannot assess: an overridden pod subtree, a
	// disabled controller, or a spelling outside this policy's grammar.
	excluded map[string]bool
}

func compareField(field string, a, b projection, choices ResourceChoices) Finding {
	switch field {
	case "metadata", "chart":
		return exact(field, a.fields[field], b.fields[field])
	}
	if !a.covered || !b.covered || a.excluded[field] || b.excluded[field] {
		return Finding{field, "not_assessed", "outside_http_shape"}
	}
	return workloadField(field, a.fields[field], b.fields[field], choices)
}

// workloadField compares one covered finding of the fixed HTTP shape.
func workloadField(field string, x, y any, choices ResourceChoices) Finding {
	switch field {
	case "image":
		return compareImage(x, y)
	case "replicas":
		if x == nil || y == nil {
			return Finding{field, "not_assessed", "unresolved_replicas"}
		}
	case "resources.cpuRequest":
		return compareCPU(x, y, choices.CPURequestMilli)
	case "resources.memory":
		return compareMemory(x, y, choices.MemoryMiB)
	}
	return exact(field, x, y)
}

func exact(field string, a, b any) Finding {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	if bytes.Equal(x, y) {
		return Finding{field, "equal", "same_selected_fields"}
	}
	return Finding{field, "different", "selected_fields_changed"}
}

func compareImage(a, b any) Finding {
	x, y := a.([]string), b.([]string)
	if x[1] == "" || y[1] == "" {
		return Finding{"image", "not_assessed", "missing_digest"}
	}
	// The pure rule forbids package variables, so these compile per call. The digest grammar is
	// repeated from kernel deliberately: shadow imports no kernel types.
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(x[1]) || !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(y[1]) {
		return Finding{"image", "not_assessed", "unsupported_digest"}
	}
	// A tag beside the same digest is the named digest-only change, in either direction.
	if x[0] == y[0] && x[1] == y[1] && x[2] != y[2] {
		return Finding{"image", "intentional", "tag_beside_digest"}
	}
	return exact("image", a, b)
}

func compareCPU(a, b any, choice int32) Finding {
	if a == nil || b == nil {
		return Finding{"resources.cpuRequest", "not_assessed", "missing_quantity"}
	}
	x, okX := quantity(a, "cpu")
	y, okY := quantity(b, "cpu")
	if !okX || !okY {
		return Finding{"resources.cpuRequest", "not_assessed", "unsupported_quantity"}
	}
	if x == y {
		return Finding{"resources.cpuRequest", "equal", "same_quantity"}
	}
	if y == int64(choice) {
		return Finding{"resources.cpuRequest", "intentional", "chosen_cpu_request"}
	}
	return Finding{"resources.cpuRequest", "different", "unexplained_cpu_request"}
}

func compareMemory(a, b any, choice int32) Finding {
	x, y := a.([]any), b.([]any)
	if x[0] == nil || x[1] == nil || y[0] == nil || y[1] == nil {
		return Finding{"resources.memory", "not_assessed", "missing_quantity"}
	}
	xr, xl, okX := memoryPair(x)
	yr, yl, okY := memoryPair(y)
	if !okX || !okY {
		return Finding{"resources.memory", "not_assessed", "unsupported_quantity"}
	}
	if xr == yr && xl == yl {
		return Finding{"resources.memory", "equal", "same_quantity"}
	}
	if explainedMemory(xr, xl, yr, yl, int64(choice)) {
		return Finding{"resources.memory", "intentional", "chosen_single_memory"}
	}
	return Finding{"resources.memory", "different", "unexplained_memory"}
}

// explainedMemory is exactly the one-memory-value change: the prior limit is the choice, the prior
// request does not exceed it, and both new values are that choice. It is not a blanket memory ignore.
func explainedMemory(xr, xl, yr, yl, m int64) bool {
	return xl == m && xr <= m && yr == m && yl == m
}

func memoryPair(v []any) (int64, int64, bool) {
	request, okR := quantity(v[0], "memory")
	limit, okL := quantity(v[1], "memory")
	return request, limit, okR && okL
}

// quantity normalises only whole cores/milliCPU or Mi/Gi, using checked integers, never floats.
func quantity(value any, kind string) (int64, bool) {
	text, ok := value.(string)
	if !ok {
		return 0, false
	}
	multiplier := int64(1)
	pattern := `^[0-9]+$`
	if kind == "cpu" {
		if strings.HasSuffix(text, "m") {
			text = strings.TrimSuffix(text, "m")
		} else {
			multiplier = 1000
		}
	} else {
		switch {
		case strings.HasSuffix(text, "Mi"):
			text = strings.TrimSuffix(text, "Mi")
		case strings.HasSuffix(text, "Gi"):
			text = strings.TrimSuffix(text, "Gi")
			multiplier = 1024
		default:
			return 0, false
		}
	}
	if !regexp.MustCompile(pattern).MatchString(text) {
		return 0, false
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil || n > (1<<63-1)/multiplier {
		return 0, false
	}
	return n * multiplier, true
}

// decode rejects ambiguity before projection, including duplicate keys in excluded subtrees.
func decode(data []byte) (map[string]any, error) {
	if !utf8.Valid(data) || !json.Valid(data) {
		return nil, invalid("document", "single_utf8_json_required")
	}
	if !validEscapes(data) {
		return nil, invalid("document", "invalid_unicode_escape")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	value, err := unique(d)
	if err != nil {
		return nil, err
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, invalid("document", "object_required")
	}
	return root, nil
}

// encoding/json replaces unpaired UTF-16 escapes with U+FFFD; refuse that lossy interpretation.
// Called only after json.Valid, so each escape's syntax and bounds are already established.
func validEscapes(data []byte) bool {
	for i := 0; i < len(data); {
		if data[i] != '\\' {
			i++
			continue
		}
		if data[i+1] != 'u' {
			i += 2
			continue
		}
		code, _ := strconv.ParseUint(string(data[i+2:i+6]), 16, 16)
		i += 6
		switch code & 0xfc00 {
		case 0xdc00:
			return false
		case 0xd800:
			if !lowSurrogateEscape(data[i:]) {
				return false
			}
			i += 6
		}
	}
	return true
}

func lowSurrogateEscape(data []byte) bool {
	if len(data) < 6 || string(data[:2]) != `\u` {
		return false
	}
	low, err := strconv.ParseUint(string(data[2:6]), 16, 16)
	return err == nil && low&0xfc00 == 0xdc00
}

func unique(d *json.Decoder) (any, error) {
	token, err := d.Token()
	if err != nil {
		return nil, invalid("document", "invalid_json")
	}
	switch token {
	case json.Delim('{'):
		object := map[string]any{}
		for d.More() {
			key, _ := d.Token()
			name := key.(string)
			if _, found := object[name]; found {
				return nil, invalid("document", "duplicate_key")
			}
			value, err := unique(d)
			if err != nil {
				return nil, err
			}
			object[name] = value
		}
		_, err = d.Token()
		return object, err
	case json.Delim('['):
		array := []any{}
		for d.More() {
			value, err := unique(d)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		_, err = d.Token()
		return array, err
	}
	if token == nil {
		return json.RawMessage("null"), nil
	}
	return token, nil
}

// reader checks fixed selected paths without reflecting unknown keys in diagnostics.
type reader struct{ err error }

func (r *reader) object(value any, field string, optional bool) map[string]any {
	if value == nil && optional {
		return map[string]any{}
	}
	m, ok := value.(map[string]any)
	if !ok && r.err == nil {
		r.err = invalid(field, "object_required")
	}
	return m
}
func (r *reader) scalar(value any, field, kind string, optional bool) any {
	if value == nil && optional {
		return nil
	}
	ok := false
	switch kind {
	case "string":
		_, ok = value.(string)
	case "bool":
		_, ok = value.(bool)
	case "integer":
		if n, number := value.(json.Number); number {
			i, err := strconv.ParseInt(string(n), 10, 32)
			ok = err == nil && i >= 0
		}
	}
	if !ok && r.err == nil {
		r.err = invalid(field, "incorrect_type_or_range")
	}
	return value
}
func (r *reader) text(value any, field string, optional bool) string {
	v := r.scalar(value, field, "string", optional)
	s, _ := v.(string)
	return s
}

func project(data []byte) (projection, error) {
	root, err := decode(data)
	if err != nil {
		return projection{}, err
	}
	r := &reader{}
	spec := r.object(root["spec"], "spec", false)
	meta := r.object(root["metadata"], "metadata", false)
	chart := r.object(spec["chart"], "chart", false)
	chartSpec := r.object(chart["spec"], "chart.spec", false)
	source := r.object(chartSpec["sourceRef"], "chart.sourceRef", false)
	fields := map[string]any{
		"metadata": []string{r.text(meta["name"], "metadata.name", false), r.text(meta["namespace"], "metadata.namespace", false)},
		"chart":    chartSpec,
	}
	for _, key := range []string{"chart", "version"} {
		r.text(chartSpec[key], "chart", false)
	}
	for _, key := range []string{"kind", "name", "namespace"} {
		r.text(source[key], "chart.sourceRef", false)
	}
	api := r.text(root["apiVersion"], "apiVersion", false)
	kind := r.text(root["kind"], "kind", false)
	// Absent values (a valuesFrom-style HelmRelease) and a chart this policy does not pin are
	// shapes outside coverage, not malformed documents. A null or a wrong type is still an error.
	values := r.object(spec["values"], "values", true)
	controllers := r.object(values["controllers"], "controllers", true)
	if r.err != nil {
		return projection{}, r.err
	}
	if !httpEnvelope(api, kind, chartSpec, controllers) {
		return projection{fields: fields}, nil
	}
	main := r.object(controllers["main"], "controllers.main", false)
	containers, comparable := controllerOf(r, main)
	if r.err != nil {
		return projection{}, r.err
	}
	if !comparable {
		return projection{fields: fields}, nil
	}
	c := r.object(containers["main"], "container.main", false)
	excluded := outsideShape(r, values, main)
	projectWorkload(r, fields, values, main, c)
	fields["probes"], excluded["probes"] = projectProbes(r, c["probes"])
	if r.err != nil {
		return projection{}, r.err
	}
	return projection{fields: fields, covered: true, excluded: excluded}, nil
}

// controllerOf returns the one main container's set when it is the HTTP shape this policy covers.
// A disabled controller renders no workload, so nothing about it is comparable.
func controllerOf(r *reader, main map[string]any) (map[string]any, bool) {
	containers := r.object(main["containers"], "containers", false)
	enabled := r.scalar(main["enabled"], "controllers.main.enabled", "bool", true)
	if r.err != nil {
		return nil, false
	}
	if value, ok := enabled.(bool); ok && !value {
		return nil, false
	}
	controllerType := r.text(main["type"], "controller.type", true)
	if r.err != nil {
		return nil, false
	}
	return containers, httpController(containers, controllerType)
}

// outsideShape marks findings whose selected subtrees are not the effective ones. app-template
// merges controllers.<name>.pod over defaultPodOptions (strategy "overwrite" by default), so any
// per-controller pod override or explicit strategy makes the pod-level comparison unsound.
func outsideShape(r *reader, values, main map[string]any) map[string]bool {
	excluded := map[string]bool{}
	override := values["defaultPodOptionsStrategy"] != nil
	if pod := main["pod"]; pod != nil {
		override = override || len(r.object(pod, "controllers.main.pod", false)) > 0
	}
	if override {
		excluded["pod.securityContext"] = true
		excluded["pod.automountServiceAccountToken"] = true
	}
	return excluded
}

func httpController(containers map[string]any, kind string) bool {
	return len(containers) == 1 && containers["main"] != nil && (kind == "" || kind == "deployment")
}

func httpEnvelope(api, kind string, chart, controllers map[string]any) bool {
	return api == "helm.toolkit.fluxcd.io/v2" && kind == "HelmRelease" && chart["chart"] == "app-template" && chart["version"] == "5.2.1" && len(controllers) == 1 && controllers["main"] != nil
}

func projectWorkload(r *reader, fields map[string]any, values, main, c map[string]any) {
	image := r.object(c["image"], "image", false)
	fields["image"] = []string{r.text(image["repository"], "image.repository", false), r.text(image["digest"], "image.digest", true), r.text(image["tag"], "image.tag", true)}
	replicas := main["replicas"]
	if _, template := replicas.(string); template {
		replicas = nil
	}
	fields["replicas"] = r.scalar(replicas, "replicas", "integer", true)
	env := r.object(c["env"], "env", true)
	fields["env.identity"] = []any{r.scalar(env["SWHURL_APP"], "env.identity", "string", true), r.scalar(env["SWHURL_ENV"], "env.identity", "string", true)}
	pod := r.object(values["defaultPodOptions"], "pod", true)
	fields["pod.securityContext"] = security(r, pod["securityContext"], "pod.securityContext")
	fields["pod.automountServiceAccountToken"] = r.scalar(pod["automountServiceAccountToken"], "pod.automountServiceAccountToken", "bool", true)
	fields["container.securityContext"] = security(r, c["securityContext"], "container.securityContext")
	service := r.object(values["service"], "service", false)
	sm := r.object(service["main"], "service.main", false)
	ports := r.object(sm["ports"], "service.ports", false)
	http := r.object(ports["http"], "service.http", false)
	fields["service.http"] = []any{r.scalar(sm["controller"], "service.controller", "string", false), r.scalar(http["port"], "service.http.port", "integer", false)}
	resources := r.object(c["resources"], "resources", true)
	requests := r.object(resources["requests"], "resources.requests", true)
	limits := r.object(resources["limits"], "resources.limits", true)
	fields["resources.cpuRequest"] = r.scalar(requests["cpu"], "resources.cpuRequest", "string", true)
	fields["resources.memory"] = []any{r.scalar(requests["memory"], "resources.memory", "string", true), r.scalar(limits["memory"], "resources.memory", "string", true)}
}

// Entire security subtrees survive projection, including settings unknown to this policy.
func security(r *reader, value any, field string) any {
	if value == nil {
		return nil
	}
	m := r.object(value, field, false)
	for _, key := range []string{"runAsNonRoot", "allowPrivilegeEscalation", "readOnlyRootFilesystem", "privileged"} {
		r.scalar(m[key], field, "bool", true)
	}
	for _, key := range []string{"runAsUser", "runAsGroup", "fsGroup"} {
		r.scalar(m[key], field, "integer", true)
	}
	securityDetails(r, m, field)
	return value
}

// Known structured security settings are type checked; the original subtree is still compared whole.
func securityDetails(r *reader, m map[string]any, field string) {
	for _, key := range []string{"seccompProfile", "appArmorProfile"} {
		if m[key] == nil {
			continue
		}
		profile := r.object(m[key], field, false)
		r.scalar(profile["type"], field, "string", false)
		r.scalar(profile["localhostProfile"], field, "string", true)
	}
	if m["capabilities"] != nil {
		caps := r.object(m["capabilities"], field, false)
		for _, key := range []string{"add", "drop"} {
			r.list(caps[key], field, "string")
		}
	}
	r.list(m["supplementalGroups"], field, "integer")
	if m["seLinuxOptions"] != nil {
		options := r.object(m["seLinuxOptions"], field, false)
		for _, key := range []string{"user", "role", "type", "level"} {
			r.scalar(options[key], field, "string", true)
		}
	}
}

func (r *reader) list(value any, field, kind string) {
	if value == nil {
		return
	}
	values, ok := value.([]any)
	if !ok && r.err == nil {
		r.err = invalid(field, "array_required")
	}
	for _, value := range values {
		r.scalar(value, field, kind, false)
	}
}

// projectProbes returns the probes subtree and whether a spelling outside this policy's grammar
// makes it unassessable: Kubernetes also accepts a named port.
func projectProbes(r *reader, value any) (any, bool) {
	if value == nil {
		return nil, true
	}
	namedPort := false
	probes := r.object(value, "probes", false)
	for _, key := range []string{"readiness", "liveness", "startup"} {
		if probes[key] == nil {
			continue
		}
		p := r.object(probes[key], "probes", false)
		r.scalar(p["enabled"], "probes", "bool", true)
		r.scalar(p["custom"], "probes", "bool", true)
		spec := r.object(p["spec"], "probes.spec", true)
		for _, key := range []string{"periodSeconds", "failureThreshold", "initialDelaySeconds", "timeoutSeconds", "successThreshold"} {
			r.scalar(spec[key], "probes.spec", "integer", true)
		}
		if spec["httpGet"] != nil {
			http := r.object(spec["httpGet"], "probes.httpGet", false)
			r.scalar(http["path"], "probes.httpGet.path", "string", false)
			if _, named := http["port"].(string); named {
				namedPort = true
				continue
			}
			r.scalar(http["port"], "probes.httpGet.port", "integer", false)
		}
	}
	return value, namedPort
}
