// Package legacy translates one predecessor instance's records into the facts a v2 preview can use, and
// names every fact it cannot carry. It reads nothing: the caller supplies the documents and their
// sources, and nothing here becomes a second authority for a legacy record.
package legacy

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The three refusal categories: a malformed document, a layout this preview does not support, and a fact
// the preview needs that neither the records nor the committed output establish.
const (
	Invalid     = "invalid"
	Unsupported = "unsupported"
	Missing     = "missing"
)

// Error names a category, the document and field it is about, and a fixed reason. It never carries a
// record's value.
type Error struct{ Code, Field, Reason string }

func (e *Error) Error() string { return fmt.Sprintf("%s: %s: %s", e.Code, e.Field, e.Reason) }

func refuse(code, field, reason string) error { return &Error{code, field, reason} }

// Records are one instance's legacy documents as JSON: the app record, the instance's settings and
// release, and the HelmRelease the predecessor itself committed. The committed document is evidence of
// the values that only rendering produced.
type Records struct {
	App, Instance, Release, Committed []byte
}

// Workload is the HTTP workload the preview renders from. The tag is carried for the comparison, which
// names it as a change; the preview itself pins the digest.
type Workload struct {
	Image, Tag, Digest string
	Kind               string
	Port               int32
	HealthPath         string
	UID                int32
}

// Instance is the environment-owned configuration the preview renders from.
type Instance struct {
	Exposure  string
	Replicas  int32
	MemoryMiB int32
	Team      string
}

// Gap is one legacy fact the preview cannot carry: where it is, what it is about, and who must resolve
// it. Where is a document and field name, never a value.
type Gap struct {
	Where, Fact, Owner string
}

// Translation is the scoped result: the facts the preview uses, where each came from, and every legacy
// fact it cannot carry. It is not a migration plan and grants nothing.
type Translation struct {
	Application, Environment string
	Workload                 Workload
	Instance                 Instance
	Sources                  map[string]string // fact name → the document it came from
	Gaps                     []Gap
}

// Translate reads the records and the predecessor's committed output. It refuses a layout it does not
// support or a fact the preview needs and no document establishes; everything else it cannot carry is a
// gap, so a legacy capability is never silently dropped.
func Translate(records Records) (Translation, error) {
	committed, err := openCommitted(records.Committed)
	if err != nil {
		return Translation{}, err
	}
	app, err := open("app.yaml", records.App)
	if err != nil {
		return Translation{}, err
	}
	instance, err := open("instance.yaml", records.Instance)
	if err != nil {
		return Translation{}, err
	}
	release, err := open("release.yaml", records.Release)
	if err != nil {
		return Translation{}, err
	}
	translation := Translation{
		Application: committed.application,
		Environment: committed.environment,
		Sources:     map[string]string{},
	}
	if err := translation.workload(release); err != nil {
		return Translation{}, err
	}
	if err := translation.settings(instance, committed); err != nil {
		return Translation{}, err
	}
	if team, err := app.text("team"); err != nil {
		return Translation{}, err
	} else if team != "" {
		translation.Instance.Team = team
		translation.Sources["instance.team"] = "app.yaml"
	}
	translation.Gaps = append(translation.Gaps, gaps(app, instance, release, translation.Instance)...)
	translation.Gaps = append(translation.Gaps, Gap{Where: "release.yaml: image", Fact: "image.provenance",
		Owner: "release/build"})
	sort.Slice(translation.Gaps, func(i, j int) bool {
		return translation.Gaps[i].Where+" "+translation.Gaps[i].Fact < translation.Gaps[j].Where+" "+translation.Gaps[j].Fact
	})
	return translation, nil
}

// workload takes the facts the HTTP preview renders. Only a web release has them, and the preview is
// HTTP-only, so any other kind is refused rather than remodelled.
func (t *Translation) workload(release *record) error {
	workload, leftovers, err := workloadOf(release)
	if err != nil {
		return err
	}
	t.Workload = workload
	for _, fact := range []string{"image", "kind", "digest", "port", "healthPath", "uid"} {
		t.Sources["workload."+fact] = "release.yaml"
	}
	for _, field := range leftovers {
		t.Gaps = append(t.Gaps, Gap{Where: "release.yaml: image." + field, Fact: "release.yaml.image." + field,
			Owner: ownerOf(field)})
	}
	return nil
}

// workloadOf reads the workload facts, and the image fields this preview does not carry, so they can be
// named as gaps rather than dropped.
func workloadOf(release *record) (Workload, []string, error) {
	image, err := release.object("image")
	if err != nil {
		return Workload{}, nil, err
	}
	workload := Workload{}
	for _, field := range []struct {
		name string
		into *string
	}{{"repository", &workload.Image}, {"tag", &workload.Tag}, {"digest", &workload.Digest}} {
		if *field.into, err = image.text(field.name); err != nil {
			return Workload{}, nil, err
		}
	}
	for _, field := range []struct {
		name string
		into *string
	}{{"kind", &workload.Kind}, {"healthPath", &workload.HealthPath}} {
		if *field.into, err = release.text(field.name); err != nil {
			return Workload{}, nil, err
		}
	}
	for _, field := range []struct {
		name string
		into *int32
	}{{"port", &workload.Port}, {"uid", &workload.UID}} {
		if *field.into, err = release.integer(field.name); err != nil {
			return Workload{}, nil, err
		}
	}
	switch {
	case workload.Kind != "web":
		return Workload{}, nil, refuse(Unsupported, "release.yaml: kind", "only_http_preview")
	case workload.Digest == "":
		return Workload{}, nil, refuse(Missing, "release.yaml: image.digest", "a_digest_is_required")
	}
	return workload, image.unused(), nil
}

// settings takes the environment's configuration. A replica count or a memory limit that the record
// omits is taken from the committed output only when that output states it: an absent value is never
// replaced by a guess.
func (t *Translation) settings(instance *record, committed effective) error {
	exposure, err := instance.text("exposure")
	if err != nil {
		return err
	}
	if exposure == "" {
		exposure = "private"
	}
	t.Instance.Exposure = exposure
	t.Sources["instance.exposure"] = "instance.yaml"
	replicas, stated, err := instance.optionalInteger("replicas")
	if err != nil {
		return err
	}
	switch {
	case stated:
		t.Sources["instance.replicas"] = "instance.yaml"
	case committed.replicas != nil:
		replicas, t.Sources["instance.replicas"] = *committed.replicas, "committed HelmRelease"
	default:
		return refuse(Missing, "instance.yaml: replicas", "no_identified_default")
	}
	t.Instance.Replicas = replicas
	memory, ok, err := memoryMiB(committed.memoryLimit)
	if err != nil {
		return err
	}
	if !ok {
		return refuse(Missing, "committed HelmRelease: values.controllers.main.containers.main.resources.limits.memory", "no_identified_value")
	}
	t.Instance.MemoryMiB = memory
	t.Sources["instance.memory"] = "committed HelmRelease"
	return nil
}

// gaps names every legacy fact the preview cannot carry: a field the records state that the preview does
// not use, and a field the records contain that this translator does not know. The record's own field
// names and document names are safe to report; values are not.
func gaps(app, instance, release *record, carried Instance) []Gap {
	var found []Gap
	add := func(document string, record *record, owner func(string) string) {
		for _, field := range record.unused() {
			found = append(found, Gap{Where: document + ": " + field, Fact: document + "." + field, Owner: owner(field)})
		}
	}
	if carried.Team != "" {
		found = append(found, Gap{Where: "app.yaml: team", Fact: "app.team", Owner: "platform (teams)"})
	}
	if carried.Exposure != "private" {
		found = append(found, Gap{Where: "instance.yaml: exposure", Fact: "instance.exposure", Owner: "platform (exposure)"})
	}
	add("app.yaml", app, ownerOf)
	add("instance.yaml", instance, ownerOf)
	add("release.yaml", release, ownerOf)
	return found
}

// ownerOf names who must resolve one legacy fact. An unknown field goes to the bridge itself, because a
// translator that lags a record's schema is the bridge's problem.
func ownerOf(field string) string {
	switch field {
	case "database", "secrets":
		return "resource-generation (storage, secrets)"
	case "host", "issuer":
		return "platform (exposure)"
	case "calls", "allows", "subscriptions", "egress":
		return "relationship resolution"
	case "alerts":
		return "platform (alerts)"
	case "telemetry":
		return "platform (telemetry)"
	case "capacity", "allocations":
		return "platform (quota, allocations)"
	case "schedule", "command", "stack":
		return "release/build (app kinds, telemetry)"
	case "publishes", "offers":
		return "platform (events, offers)"
	case "description", "apiPath", "resources", "version":
		return "platform (rendering)"
	}
	return "transition bridge"
}

// effective is what the predecessor's committed HelmRelease states, for the values the records omit.
type effective struct {
	application, environment string
	replicas                 *int32
	memoryLimit              string
}

// openCommitted reads the predecessor's own output: the deployment's identity and the values only
// rendering stated. Its absence is refused, because there is nothing to compare a preview with.
func openCommitted(data []byte) (effective, error) {
	var document struct {
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Spec struct {
			Values struct {
				Controllers map[string]struct {
					Replicas   *int32 `json:"replicas"`
					Containers map[string]struct {
						Resources struct {
							Limits struct {
								Memory string `json:"memory"`
							} `json:"limits"`
						} `json:"resources"`
					} `json:"containers"`
				} `json:"controllers"`
			} `json:"values"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return effective{}, refuse(Invalid, "helmrelease.yaml", "single_json_object_required")
	}
	name, namespace := document.Metadata.Name, document.Metadata.Namespace
	if name == "" || !strings.HasPrefix(namespace, name+"-") {
		return effective{}, refuse(Invalid, "helmrelease.yaml: metadata", "application_and_environment_required")
	}
	result := effective{application: name, environment: strings.TrimPrefix(namespace, name+"-")}
	controller, found := document.Spec.Values.Controllers["main"]
	if !found {
		return effective{}, refuse(Invalid, "helmrelease.yaml: values.controllers", "main_controller_required")
	}
	result.replicas = controller.Replicas
	if container, found := controller.Containers["main"]; found {
		result.memoryLimit = container.Resources.Limits.Memory
	}
	return result, nil
}

// memoryMiB accepts the quantity spellings v1 renders for memory: whole Mi or Gi.
func memoryMiB(quantity string) (int32, bool, error) {
	if quantity == "" {
		return 0, false, nil
	}
	multiplier, text := int64(1), quantity
	switch {
	case strings.HasSuffix(quantity, "Gi"):
		multiplier, text = 1024, strings.TrimSuffix(quantity, "Gi")
	case strings.HasSuffix(quantity, "Mi"):
		text = strings.TrimSuffix(quantity, "Mi")
	default:
		return 0, false, refuse(Unsupported, "committed HelmRelease: memory", "unsupported_quantity")
	}
	if text == "" || strings.Trim(text, "0123456789") != "" {
		return 0, false, refuse(Unsupported, "committed HelmRelease: memory", "unsupported_quantity")
	}
	var value int64
	if _, err := fmt.Sscanf(text, "%d", &value); err != nil || value < 1 || value*multiplier > 1<<20 {
		return 0, false, refuse(Invalid, "committed HelmRelease: memory", "expected_a_positive_quantity")
	}
	return int32(value * multiplier), true, nil
}
