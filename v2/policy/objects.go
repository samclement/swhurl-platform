// Package policy judges actual rendered Kubernetes objects against the compiled instance and platform facts.
// It performs no effects. Record validation and connection resolution remain the compiler's responsibilities.
package policy

import (
	"bytes"
	"encoding/json"
	"errors"
)

type metadata struct {
	Name, Namespace     string
	Labels, Annotations map[string]string
}
type object struct {
	APIVersion, Kind string
	Metadata         metadata
	Spec             json.RawMessage
}

func (o object) ref() string { return o.Kind + "/" + o.Metadata.Name }

type security struct {
	RunAsNonRoot             *bool
	RunAsUser                *int64
	AllowPrivilegeEscalation *bool
	Privileged               bool
	Capabilities             struct{ Drop, Add []string }
}
type resources struct{ Requests, Limits map[string]json.RawMessage }
type reference struct {
	Name     string
	Optional bool
}
type envSource struct {
	SecretRef, ConfigMapRef *reference
	Prefix                  string
}
type environment struct {
	Name      string
	Value     *string
	ValueFrom json.RawMessage
}
type mount struct {
	Name, MountPath      string
	SubPath, SubPathExpr string
	ReadOnly             bool
}
type token struct {
	Audience, Path    string
	ExpirationSeconds int64
}
type projection struct{ ServiceAccountToken *token }
type volume struct {
	Name                  string
	HostPath              json.RawMessage
	PersistentVolumeClaim *struct {
		ClaimName string
		ReadOnly  bool
	}
	ConfigMap *reference
	Projected struct{ Sources []projection }
}
type container struct {
	Name, Image     string
	RestartPolicy   string
	SecurityContext security
	Resources       resources
	Env             []environment
	EnvFrom         []envSource
	VolumeMounts    []mount
	Ports           []struct {
		Name          string
		ContainerPort int64
	}
	ReadinessProbe, LivenessProbe, StartupProbe probe
}
type pod struct {
	AutomountServiceAccountToken  *bool
	HostNetwork, HostPID, HostIPC bool
	SecurityContext               security
	Containers, InitContainers    []container
	Volumes                       []volume
}
type template struct {
	Metadata metadata
	Spec     pod
}
type workloadSpec struct {
	Replicas    *int64
	Parallelism *int64
	Strategy    struct {
		Type          string
		RollingUpdate struct{ MaxSurge json.RawMessage }
	}
	Template          template
	ConcurrencyPolicy string
	JobTemplate       struct {
		Spec struct {
			Parallelism *int64
			Template    template
		}
	}
}
type workload struct {
	object
	spec   workloadSpec
	pod    pod
	labels map[string]string
}

type chartContainer struct {
	Image     struct{ Repository, Digest string }
	Env       map[string]json.RawMessage
	EnvFrom   []envSource
	Resources resources
	Probes    map[string]struct {
		Enabled bool
		Spec    probe
	}
}
type persistence struct {
	Type, Name, ExistingClaim, StorageClass, AccessMode, Size string
	Sources                                                   []projection
	GlobalMounts                                              []struct {
		Path     string
		ReadOnly bool
	}
}
type release struct {
	Metadata metadata
	Spec     struct {
		Chart struct {
			Spec struct{ Chart, Version string }
		}
		Values struct {
			DefaultPodOptions pod
			Service           map[string]struct {
				Ports map[string]struct{ Port int64 }
			}
			Controllers map[string]struct {
				Replicas   int64
				Containers map[string]chartContainer
			}
			Persistence map[string]persistence
		}
	}
}
type facts struct {
	release               release
	environment, exposure string
	backup                metadata
	main                  chartContainer
	source                []object
	platform              Platform
}

func decodeObjects(raw []json.RawMessage) ([]object, error) {
	result := make([]object, 0, len(raw))
	for _, document := range raw {
		var one object
		if json.Unmarshal(document, &one) != nil || one.Kind == "" {
			return nil, errors.New("invalid_policy_object")
		}
		result = append(result, one)
	}
	return result, nil
}

func compilationFacts(source []json.RawMessage, platform Platform) (facts, error) {
	result := facts{platform: platform}
	for _, raw := range source {
		var one object
		if json.Unmarshal(raw, &one) != nil {
			return facts{}, errors.New("invalid_policy_compilation")
		}
		if err := result.sourceFact(one, raw); err != nil {
			return facts{}, err
		}
	}
	result.main = result.release.Spec.Values.Controllers["main"].Containers["main"]
	if result.release.Metadata.Name == "" || result.release.Metadata.Namespace == "" || result.main.Image.Digest == "" {
		return facts{}, errors.New("invalid_policy_compilation")
	}
	return result, nil
}

func (f *facts) sourceFact(one object, raw []byte) error {
	switch one.Kind {
	case "HelmRelease":
		if json.Unmarshal(raw, &f.release) != nil {
			return errors.New("invalid_policy_compilation")
		}
	case "Namespace":
		f.environment = one.Metadata.Labels["platform.swhurl.com/environment"]
		f.exposure = one.Metadata.Labels["platform.swhurl.com/exposure"]
	case "CronJob":
		f.backup = one.Metadata
	}
	if one.Kind != "HelmRelease" && one.Kind != "Kustomization" {
		f.source = append(f.source, one)
	}
	return nil
}

func podOf(one object) (workload, bool, error) {
	w := workload{object: one}
	if one.Kind == "Pod" {
		err := json.Unmarshal(one.Spec, &w.pod)
		w.labels = one.Metadata.Labels
		return w, true, err
	}
	switch one.Kind {
	case "Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob":
	default:
		return w, false, nil
	}
	if err := json.Unmarshal(one.Spec, &w.spec); err != nil {
		return w, true, err
	}
	chosen := w.spec.Template
	if one.Kind == "CronJob" {
		chosen = w.spec.JobTemplate.Spec.Template
	}
	w.pod, w.labels = chosen.Spec, chosen.Metadata.Labels
	return w, true, nil
}

// sameJSON compares dependency object structure without rounding JSON numbers or depending on key order.
func sameJSON(a, b any) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return bytes.Equal(canonical(left), canonical(right))
}
func canonical(raw []byte) []byte {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return nil
	}
	result, _ := json.Marshal(value)
	return result
}
