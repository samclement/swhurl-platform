package kernel

import (
	"encoding/json"
	"regexp"
	"slices"
)

// ProfileID is a platform profile's content identity: prof-sha256 over its canonical encoding.
type ProfileID string

// PlatformProfile is what the platform owns and every compiled instance shares: the pinned
// deployment primitive, where Flux finds the files, and workload and quota defaults.
type PlatformProfile struct {
	SchemaVersion int32    `json:"schemaVersion"`
	Name          string   `json:"name"`
	Chart         Chart    `json:"chart"`
	Delivery      Delivery `json:"delivery"`
	Workload      Workload `json:"workload"`
	Quota         Quota    `json:"quota"`
}

type Chart struct {
	Name                string `json:"name"`
	Version             string `json:"version"`
	Repository          string `json:"repository"`
	RepositoryNamespace string `json:"repositoryNamespace"`
}

// Delivery is how Flux finds the files: its GitRepository, the root under it, the units every
// instance waits for. DependsOn is copied whenever a profile is used, so nothing aliases it.
type Delivery struct {
	Source     string   `json:"source"`
	OutputRoot string   `json:"outputRoot"`
	DependsOn  []string `json:"dependsOn"`
}

type Workload struct {
	CPURequestMilli      int32 `json:"cpuRequestMilli"`
	StartupSeconds       int32 `json:"startupSeconds"`
	StartupPeriodSeconds int32 `json:"startupPeriodSeconds"`
}

type Quota struct {
	Pods              int32 `json:"pods"`
	RequestsCPUMilli  int32 `json:"requestsCpuMilli"`
	RequestsMemoryMiB int32 `json:"requestsMemoryMiB"`
	LimitsMemoryMiB   int32 `json:"limitsMemoryMiB"`
}

var (
	labelPattern   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	rootPattern    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(/[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)
)

func DecodeProfile(data []byte) (PlatformProfile, error) {
	if err := document(data); err != nil {
		return PlatformProfile{}, err
	}
	root, err := fields(data, "profile", []string{"schemaVersion", "name", "chart", "delivery", "workload", "quota"})
	if err != nil {
		return PlatformProfile{}, err
	}
	for _, part := range []struct {
		key      string
		required []string
	}{
		{"chart", []string{"name", "version", "repository", "repositoryNamespace"}},
		{"delivery", []string{"source", "outputRoot", "dependsOn"}},
		{"workload", []string{"cpuRequestMilli", "startupSeconds", "startupPeriodSeconds"}},
		{"quota", []string{"pods", "requestsCpuMilli", "requestsMemoryMiB", "limitsMemoryMiB"}},
	} {
		if _, err := fields(root[part.key], part.key, part.required); err != nil {
			return PlatformProfile{}, err
		}
	}
	var profile PlatformProfile
	if err := json.Unmarshal(data, &profile); err != nil {
		return PlatformProfile{}, refuse(Invalid, "profile", "incorrect field type or integer range")
	}
	if err := profile.validate(); err != nil {
		return PlatformProfile{}, err
	}
	return profile, nil
}

// validate checks the fields in the order of the profile's field table.
func (p PlatformProfile) validate() error {
	if p.SchemaVersion != 1 {
		return refuse(Unsupported, "schemaVersion", "only version 1 is supported")
	}
	if !namePattern.MatchString(p.Name) {
		return refuse(Invalid, "name", "expected an app-name-style profile name")
	}
	if err := p.Chart.validate(); err != nil {
		return err
	}
	if err := p.Delivery.validate(); err != nil {
		return err
	}
	if err := p.Workload.validate(); err != nil {
		return err
	}
	return p.Quota.validate()
}

func (c Chart) validate() error {
	for _, field := range []struct{ name, value string }{
		{"chart.name", c.Name}, {"chart.repository", c.Repository}, {"chart.repositoryNamespace", c.RepositoryNamespace},
	} {
		if !labelPattern.MatchString(field.value) {
			return refuse(Invalid, field.name, "expected a DNS label")
		}
	}
	if !versionPattern.MatchString(c.Version) {
		return refuse(Invalid, "chart.version", "expected MAJOR.MINOR.PATCH")
	}
	return nil
}

func (f Delivery) validate() error {
	if !labelPattern.MatchString(f.Source) {
		return refuse(Invalid, "delivery.source", "expected a DNS label")
	}
	if !rootPattern.MatchString(f.OutputRoot) {
		return refuse(Invalid, "delivery.outputRoot", "expected a relative path of DNS-label segments")
	}
	for i, unit := range f.DependsOn {
		if !labelPattern.MatchString(unit) || slices.Contains(f.DependsOn[:i], unit) {
			return refuse(Invalid, "delivery.dependsOn", "expected distinct DNS labels")
		}
	}
	return nil
}

func (w Workload) validate() error {
	if w.CPURequestMilli < 1 {
		return refuse(Invalid, "workload.cpuRequestMilli", "expected a positive integer")
	}
	if w.StartupPeriodSeconds < 1 {
		return refuse(Invalid, "workload.startupPeriodSeconds", "expected a positive integer")
	}
	if w.StartupSeconds < w.StartupPeriodSeconds || w.StartupSeconds%w.StartupPeriodSeconds != 0 {
		return refuse(Invalid, "workload.startupSeconds", "expected a positive multiple of startupPeriodSeconds")
	}
	return nil
}

func (q Quota) validate() error {
	for _, field := range []struct {
		name  string
		value int32
	}{
		{"quota.pods", q.Pods}, {"quota.requestsCpuMilli", q.RequestsCPUMilli},
		{"quota.requestsMemoryMiB", q.RequestsMemoryMiB}, {"quota.limitsMemoryMiB", q.LimitsMemoryMiB},
	} {
		if field.value < 1 {
			return refuse(Invalid, field.name, "expected a positive integer")
		}
	}
	return nil
}

func profileCanonical(p PlatformProfile) []byte {
	dependsOn := slices.Clone(p.Delivery.DependsOn)
	if dependsOn == nil {
		dependsOn = []string{}
	}
	return canonical([]any{"swhurl.profile.v1", p.SchemaVersion, p.Name,
		p.Chart.Name, p.Chart.Version, p.Chart.Repository, p.Chart.RepositoryNamespace,
		p.Delivery.Source, p.Delivery.OutputRoot, dependsOn,
		p.Workload.CPURequestMilli, p.Workload.StartupSeconds, p.Workload.StartupPeriodSeconds,
		p.Quota.Pods, p.Quota.RequestsCPUMilli, p.Quota.RequestsMemoryMiB, p.Quota.LimitsMemoryMiB})
}

func (p PlatformProfile) ID() (ProfileID, error) {
	if err := p.validate(); err != nil {
		return "", err
	}
	return ProfileID(hash("prof-sha256:", profileCanonical(p))), nil
}
