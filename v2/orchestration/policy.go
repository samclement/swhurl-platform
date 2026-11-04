package orchestration

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/samclement/swhurl-platform/v2/adapters/helm"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/policy"
)

// PolicyResult distinguishes a failed chart render from Kubernetes policy violations for one instance.
type PolicyResult struct {
	Application   string           `json:"application"`
	Environment   string           `json:"environment"`
	RenderFailure string           `json:"renderFailure,omitempty"`
	Findings      []policy.Finding `json:"findings"`
}
type policyCompilation struct {
	request  helm.Request
	source   []json.RawMessage
	platform policy.Platform
	result   PolicyResult
}

// CheckPolicy compiles/validates every input before rendering anything, then judges every instance even if
// one render fails. It writes nothing and never acquires charts. The caller decides output and exit status.
func CheckPolicy(inputs []Inputs, chart string, renderer helm.Renderer) ([]PolicyResult, error) {
	prepared, err := policyCompilations(inputs, chart)
	if err != nil {
		return nil, err
	}
	result := make([]PolicyResult, 0, len(prepared))
	for _, one := range prepared {
		rendered, err := renderer.Render(one.request)
		if err != nil {
			one.result.RenderFailure = renderRefusal(err)
		} else {
			one.result.Findings, err = policy.Check(one.source, one.platform, rendered)
			if err != nil {
				return nil, err
			}
		}
		result = append(result, one.result)
	}
	return result, nil
}
func policyCompilations(inputs []Inputs, chart string) ([]policyCompilation, error) {
	if len(inputs) == 0 {
		return nil, errors.New("invalid_policy_inputs: no_instances")
	}
	result := make([]policyCompilation, 0, len(inputs))
	for _, input := range inputs {
		prepared, err := preparePolicy(input, chart)
		if err != nil {
			return nil, err
		}
		result = append(result, prepared)
	}
	slices.SortFunc(result, func(a, b policyCompilation) int {
		return strings.Compare(a.result.Application+"/"+a.result.Environment, b.result.Application+"/"+b.result.Environment)
	})
	return result, nil
}
func preparePolicy(inputs Inputs, chart string) (policyCompilation, error) {
	compiled, err := Compile(inputs)
	if err != nil {
		return policyCompilation{}, err
	}
	spec, err := kernel.DecodeInstance(inputs.Instance)
	if err != nil {
		return policyCompilation{}, err
	}
	profile, err := kernel.DecodeProfile(inputs.Profile)
	if err != nil {
		return policyCompilation{}, err
	}
	prepared := policyCompilation{platform: policyPlatform(profile), result: PolicyResult{
		Application: spec.Application, Environment: spec.Environment, Findings: []policy.Finding{}}}
	for _, file := range compiled.Files {
		prepared.source = append(prepared.source, file.Document)
	}
	prepared.request, err = compiledChart(compiled, profile, chart)
	return prepared, err
}
func compiledChart(compiled kernel.Compilation, profile kernel.PlatformProfile, chart string) (helm.Request, error) {
	for _, file := range compiled.Files {
		var release struct {
			Kind     string
			Metadata struct{ Name, Namespace string }
			Spec     struct {
				Chart struct {
					Spec struct{ Chart, Version string }
				}
				Values json.RawMessage
			}
		}
		if json.Unmarshal(file.Document, &release) != nil {
			return helm.Request{}, errors.New("invalid_policy_compilation")
		}
		if release.Kind != "HelmRelease" {
			continue
		}
		if release.Spec.Chart.Spec.Chart != profile.Chart.Name || release.Spec.Chart.Spec.Version != profile.Chart.Version {
			return helm.Request{}, errors.New("invalid_policy_chart")
		}
		return helm.Request{Chart: chart, ChartName: profile.Chart.Name, ChartVersion: profile.Chart.Version,
			Release: release.Metadata.Name, Namespace: release.Metadata.Namespace, Values: release.Spec.Values}, nil
	}
	return helm.Request{}, errors.New("invalid_policy_compilation")
}
func policyPlatform(profile kernel.PlatformProfile) policy.Platform {
	q := profile.Quota
	return policy.Platform{Pods: int64(q.Pods), RequestsCPUMilli: int64(q.RequestsCPUMilli),
		RequestsMemoryMiB: int64(q.RequestsMemoryMiB), LimitsMemoryMiB: int64(q.LimitsMemoryMiB),
		StorageClass: profile.Storage.Class, CookieDomain: profile.Routing.BaseDomain, Middleware: profile.Routing.Middleware}
}

// Only the adapter's fixed-domain refusal may cross this boundary, never generic subprocess diagnostics.
func renderRefusal(err error) string {
	var refusal *helm.Error
	if errors.As(err, &refusal) {
		return refusal.Error()
	}
	return helm.RefusedRender
}
