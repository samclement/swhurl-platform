package apptemplatetest

import (
	"encoding/json"
	"fmt"
	"strings"
)

// podSpec is what the test reads of a pod, or of a Deployment's pod template.
type podSpec struct {
	AutomountServiceAccountToken *bool `json:"automountServiceAccountToken"`
	SecurityContext              struct {
		RunAsNonRoot *bool `json:"runAsNonRoot"`
	} `json:"securityContext"`
	Containers []container `json:"containers"`
}

// container is what the test reads of the app's container.
type container struct {
	Name  string `json:"name"`
	Image string `json:"image"`
	Env   []struct {
		Name  string  `json:"name"`
		Value *string `json:"value"`
	} `json:"env"`
	ReadinessProbe *probe `json:"readinessProbe"`
	LivenessProbe  *probe `json:"livenessProbe"`
	StartupProbe   *probe `json:"startupProbe"`
}

// probe is the HTTP path a probe asks, if it asks one.
type probe struct {
	HTTPGet *struct {
		Path string `json:"path"`
	} `json:"httpGet"`
}

// deployment is what the test reads of the web app's workload.
type deployment struct {
	Spec struct {
		Replicas *int `json:"replicas"`
		Strategy struct {
			Type string `json:"type"`
		} `json:"strategy"`
		Template struct {
			Spec podSpec `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

// app is the container named Container, or none.
func (s podSpec) app() container {
	for _, one := range s.Containers {
		if one.Name == Container {
			return one
		}
	}
	return container{}
}

// variable is one environment variable's literal value, and whether the container states it.
func (c container) variable(name string) (string, bool) {
	for _, variable := range c.Env {
		if variable.Name == name && variable.Value != nil {
			return *variable.Value, true
		}
	}
	return "", false
}

// Deployed is the verdicts on the web app's Deployment, in the order they are said: its pod's security defaults,
// the image pinned by digest, the database path, and one replica replaced stop-first.
func Deployed(document []byte) (security Line, pinned []Line) {
	var read deployment
	if json.Unmarshal(document, &read) != nil {
		read = deployment{}
	}
	pod := read.Spec.Template.Spec
	security = check(isTrue(pod.SecurityContext.RunAsNonRoot) && pod.AutomountServiceAccountToken != nil &&
		!*pod.AutomountServiceAccountToken, "web pod runs non-root without a service-account token",
		"web pod security defaults missing")
	app := pod.app()
	path, stated := app.variable("DATABASE_PATH")
	shown := "unset"
	if stated {
		shown = fmt.Sprintf("%q", path)
	}
	replicas := "unset"
	if read.Spec.Replicas != nil {
		replicas = fmt.Sprint(*read.Spec.Replicas)
	}
	return security, []Line{
		check(strings.Contains(app.Image, "@sha256:"), "prod image pinned by digest", "prod image not digest-pinned"),
		check(path == Database, "DATABASE_PATH points into the volume", "DATABASE_PATH is "+shown),
		check(replicas == "1" && read.Spec.Strategy.Type == "Recreate", "one replica, stop-first updates (single writer)",
			fmt.Sprintf("replicas %s, strategy %q", replicas, read.Spec.Strategy.Type)),
	}
}

func isTrue(value *bool) bool { return value != nil && *value }

// pod is what the test reads of one of the web app's pods.
type pod struct {
	Metadata struct {
		Name              string `json:"name"`
		DeletionTimestamp string `json:"deletionTimestamp"`
	} `json:"metadata"`
	Spec   podSpec `json:"spec"`
	Status struct {
		Conditions []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"conditions"`
	} `json:"status"`
}

// ProbedReady is the verdict on the web app's pods: one, not being deleted, is Ready, and its container's readiness,
// liveness and startup probes all ask the health path its release declares.
func ProbedReady(pods [][]byte, healthPath string) Line {
	ok := "web pod Ready on its " + healthPath + " probes"
	why := "no pod"
	for _, document := range pods {
		var read pod
		if json.Unmarshal(document, &read) != nil || read.Metadata.DeletionTimestamp != "" {
			continue
		}
		if why = read.unprobed(healthPath); why == "" {
			return Line{OK, ok}
		}
	}
	return Line{Bad, "web pod not Ready on its " + healthPath + " probes: " + why}
}

// unprobed is why a pod is not Ready on the health path, or nothing when it is.
func (p pod) unprobed(healthPath string) string {
	app := p.Spec.app()
	for _, one := range []struct {
		name  string
		probe *probe
	}{{"readiness", app.ReadinessProbe}, {"liveness", app.LivenessProbe}, {"startup", app.StartupProbe}} {
		if one.probe == nil || one.probe.HTTPGet == nil || one.probe.HTTPGet.Path != healthPath {
			return p.Metadata.Name + " has no " + one.name + " probe on " + healthPath
		}
	}
	for _, condition := range p.Status.Conditions {
		if condition.Type == "Ready" && condition.Status == "True" {
			return ""
		}
	}
	return p.Metadata.Name + " is not Ready"
}
