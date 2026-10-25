package kernel

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"slices"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden compilations in testdata/compiled")

func profile(t *testing.T) PlatformProfile {
	t.Helper()
	p, err := DecodeProfile(fixture(t, "profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func releaseFile(t *testing.T, name string) ReleaseBundle {
	t.Helper()
	r, err := DecodeRelease(fixture(t, name))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func compile(t *testing.T, spec InstanceSpec, release ReleaseBundle, p PlatformProfile) Compilation {
	t.Helper()
	c, err := Compile(spec, release, p)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// files maps each compiled file's path to its document.
func files(c Compilation) map[string]string {
	out := map[string]string{}
	for _, file := range c.Files {
		out[file.Path] = string(file.Document)
	}
	return out
}

// changed is the paths whose documents differ between two compilations (or exist in one only).
func changed(a, b Compilation) []string {
	before, after := files(a), files(b)
	var out []string
	for path, doc := range after {
		if before[path] != doc {
			out = append(out, path)
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			out = append(out, path)
		}
	}
	slices.Sort(out)
	return out
}

func TestProfileIdentity(t *testing.T) {
	var golden struct{ ProfileCanonical, ProfileID string }
	if err := json.Unmarshal(fixture(t, "profile-identity.json"), &golden); err != nil {
		t.Fatal(err)
	}
	p := profile(t)
	if got := string(profileCanonical(p)); got != golden.ProfileCanonical {
		t.Fatalf("profile canonical bytes: %s", got)
	}
	if id, err := p.ID(); err != nil || string(id) != golden.ProfileID {
		t.Fatalf("profile ID: %s %v", id, err)
	}
	base, _ := p.ID()
	for _, change := range []func(*PlatformProfile){
		func(p *PlatformProfile) { p.Name = "other" },
		func(p *PlatformProfile) { p.Chart.Name = "other" },
		func(p *PlatformProfile) { p.Chart.Version = "5.2.2" },
		func(p *PlatformProfile) { p.Chart.Repository = "other" },
		func(p *PlatformProfile) { p.Chart.RepositoryNamespace = "other" },
		func(p *PlatformProfile) { p.Delivery.Source = "other" },
		func(p *PlatformProfile) { p.Delivery.OutputRoot = "other" },
		func(p *PlatformProfile) { p.Delivery.DependsOn = []string{"infra-base", "other"} },
		func(p *PlatformProfile) { p.Delivery.DependsOn = nil },
		func(p *PlatformProfile) { p.Workload.CPURequestMilli++ },
		func(p *PlatformProfile) { p.Workload.StartupSeconds += 5 },
		func(p *PlatformProfile) { p.Workload.StartupPeriodSeconds, p.Workload.StartupSeconds = 10, 120 },
		func(p *PlatformProfile) { p.Quota.Pods++ },
		func(p *PlatformProfile) { p.Quota.RequestsCPUMilli++ },
		func(p *PlatformProfile) { p.Quota.RequestsMemoryMiB++ },
		func(p *PlatformProfile) { p.Quota.LimitsMemoryMiB++ },
	} {
		changed := profile(t)
		change(&changed)
		if id, err := changed.ID(); err != nil || id == base {
			t.Fatalf("a changed field must change the profile ID: %v", err)
		}
	}
}

func TestProfileDecoding(t *testing.T) {
	text := string(fixture(t, "profile.json"))
	for _, tt := range []struct {
		name, text string
		want       Code
		field      string
	}{
		{"unsupported version", strings.Replace(text, `"schemaVersion": 1`, `"schemaVersion": 2`, 1), Unsupported, "schemaVersion"},
		{"unknown field", strings.Replace(text, `"name": "home-k3s"`, `"name": "home-k3s", "region": "do-not-echo"`, 1), Unsupported, "profile"},
		{"unknown nested field", strings.Replace(text, `"pods": 14`, `"pods": 14, "services": 3`, 1), Unsupported, "quota"},
		{"missing field", strings.Replace(text, `"cpuRequestMilli": 10,`, ``, 1), Invalid, "workload.cpuRequestMilli"},
		{"null list", strings.Replace(text, `["infra-base"]`, `null`, 1), Invalid, "delivery.dependsOn"},
		{"list of numbers", strings.Replace(text, `["infra-base"]`, `[1]`, 1), Invalid, "profile"},
		{"repeated dependency", strings.Replace(text, `["infra-base"]`, `["infra-base", "infra-base"]`, 1), Invalid, "delivery.dependsOn"},
		{"bad version", strings.Replace(text, `"5.2.1"`, `"v5.2.1"`, 1), Invalid, "chart.version"},
		{"escaping root", strings.Replace(text, `"v2-apps"`, `"../v2-apps"`, 1), Invalid, "delivery.outputRoot"},
		{"absolute root", strings.Replace(text, `"v2-apps"`, `"/v2-apps"`, 1), Invalid, "delivery.outputRoot"},
		{"zero startup period", strings.Replace(text, `"startupPeriodSeconds": 5`, `"startupPeriodSeconds": 0`, 1), Invalid, "workload.startupPeriodSeconds"},
		{"root segment longer than a DNS label", strings.Replace(text, `"v2-apps"`, `"`+strings.Repeat("a", 64)+`"`, 1), Invalid, "delivery.outputRoot"},
		{"startup not a multiple", strings.Replace(text, `"startupSeconds": 120`, `"startupSeconds": 121`, 1), Invalid, "workload.startupSeconds"},
		{"zero quota", strings.Replace(text, `"pods": 14`, `"pods": 0`, 1), Invalid, "quota.pods"},
		{"bad name", strings.Replace(text, `"home-k3s"`, `"Home"`, 1), Invalid, "name"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeProfile([]byte(tt.text))
			refusal(t, err, tt.want, tt.field)
			if strings.Contains(err.Error(), "do-not-echo") {
				t.Fatal("diagnostic dumped an input value")
			}
		})
	}
	empty := strings.Replace(text, `["infra-base"]`, `[]`, 1)
	if p, err := DecodeProfile([]byte(empty)); err != nil || len(p.Delivery.DependsOn) != 0 {
		t.Fatalf("no dependencies is allowed: %v", err)
	}
}

// TestGoldenCompilations: both fixture instances, with the releases they select, compile to the
// committed documents. Run with -update to rewrite them after an intended change.
func TestGoldenCompilations(t *testing.T) {
	p := profile(t)
	for _, tt := range []struct{ instance, release, golden string }{
		{"staging.json", "r42.json", "compiled/staging.json"},
		{"prod.json", "r41.json", "compiled/prod.json"},
	} {
		t.Run(tt.instance, func(t *testing.T) {
			got := EncodeCompilation(compile(t, instance(t, tt.instance), releaseFile(t, tt.release), p))
			path := "../testdata/" + tt.golden
			if *updateGolden {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			if want := fixture(t, tt.golden); !bytes.Equal(got, want) {
				t.Fatalf("compilation differs from %s (run go test ./kernel -run TestGoldenCompilations -update if intended):\n%s", tt.golden, got)
			}
			again := EncodeCompilation(compile(t, instance(t, tt.instance), releaseFile(t, tt.release), p))
			if !bytes.Equal(got, again) {
				t.Fatal("the same inputs must give the same bytes")
			}
		})
	}
}

func TestCompiledResources(t *testing.T) {
	c := compile(t, instance(t, "prod.json"), releaseFile(t, "r41.json"), profile(t))
	f := files(c)
	for path, want := range map[string][]string{
		"v2-apps/instances/demo-orders/prod/namespace.json": {`"name":"demo-orders-prod"`, `"platform.swhurl.com/exposure":"private"`},
		"v2-apps/instances/demo-orders/prod/helmrelease.json": {
			`"image":{"digest":"sha256:aaaa`, `"repository":"ghcr.io/samclement/demo-orders"}`,
			`"env":{"SWHURL_APP":"demo-orders","SWHURL_ENV":"prod"}`, `"replicas":1`,
			`"limits":{"memory":"256Mi"},"requests":{"cpu":"10m","memory":"256Mi"}`,
			`"runAsUser":65532`, `"automountServiceAccountToken":false`, `"failureThreshold":24`,
			`"sourceRef":{"kind":"HelmRepository","name":"bjw-s","namespace":"flux-system"}`, `"version":"5.2.1"`},
		"v2-apps/instances/demo-orders/prod/resourcequota.json": {`"hard":{"limits.memory":"4Gi","pods":"14","requests.cpu":"2","requests.memory":"2Gi"}`},
		"v2-apps/instances/demo-orders/prod/networkpolicy.json": {
			`"ingress":[]`, `"podSelector":{"matchLabels":{"app.kubernetes.io/instance":"demo-orders"}}`, `"policyTypes":["Ingress"]`},
		"v2-apps/instances/demo-orders/prod/kustomization.yaml": {`"resources":["namespace.json","helmrelease.json","resourcequota.json","networkpolicy.json"]`},
		"v2-apps/units/app-demo-orders-prod.json": {`"path":"./v2-apps/instances/demo-orders/prod"`, `"dependsOn":[{"name":"infra-base"}]`,
			`"prune":true`, `"wait":true`, `"timeout":"3m"`},
	} {
		for _, fragment := range want {
			if !strings.Contains(f[path], fragment) {
				t.Errorf("%s lacks %s:\n%s", path, fragment, f[path])
			}
		}
	}
	for _, removed := range []string{`"tag"`, `"SWHURL_NOTIFY_URL"`, `"SWHURL_AUTH_`, `"tokens"`, `"platform.swhurl.com/team"`,
		`"ingress":{`, `"platform.swhurl.com/render"`, `"annotations"`, `"HTTP_PROXY"`, `"HTTPS_PROXY"`} {
		for path, doc := range f {
			if strings.Contains(doc, removed) {
				t.Errorf("%s has %q, which phase 2 does not generate", path, removed)
			}
		}
	}
	if len(f) != 6 || !slices.Equal(c.Registrations, []string{"v2-apps/units/app-demo-orders-prod.json"}) {
		t.Fatalf("files %d, registrations %v", len(f), c.Registrations)
	}
}

// TestCompilationChanges: each input changes only the files it should, and the ID with them.
func TestCompilationChanges(t *testing.T) {
	spec, r41, r42, p := instance(t, "prod.json"), releaseFile(t, "r41.json"), releaseFile(t, "r42.json"), profile(t)
	base := compile(t, spec, r41, p)
	folder := "v2-apps/instances/demo-orders/prod/"
	revision, _ := spec.Revision()
	selected, err := SelectRelease(spec, r42, revision)
	if err != nil {
		t.Fatal(err)
	}
	stopped := spec
	stopped.Configuration.Replicas = 0
	cpu, quota, root := p, p, p
	cpu.Workload.CPURequestMilli = 20
	quota.Quota.Pods = 20
	root.Delivery.OutputRoot = "elsewhere"
	for _, tt := range []struct {
		name string
		got  Compilation
		want []string
	}{
		{"select R42", compile(t, selected, r42, p), []string{folder + "helmrelease.json"}},
		{"stop", compile(t, stopped, r41, p), []string{folder + "helmrelease.json"}},
		{"CPU request", compile(t, spec, r41, cpu), []string{folder + "helmrelease.json"}},
		{"quota", compile(t, spec, r41, quota), []string{folder + "resourcequota.json"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := changed(base, tt.got); !slices.Equal(got, tt.want) {
				t.Errorf("changed %v, want %v", got, tt.want)
			}
			if tt.got.ID == base.ID {
				t.Error("a changed output must change the compilation ID")
			}
		})
	}
	moved := compile(t, spec, r41, root)
	if len(changed(base, moved)) != 12 || !strings.HasPrefix(moved.Registrations[0], "elsewhere/units/") {
		t.Errorf("a new output root moves every file: %v", changed(base, moved))
	}
	// The ID is the output's: another name for the same platform profile compiles to the same files.
	renamed := p
	renamed.Name = "renamed"
	if again := compile(t, spec, r41, renamed); again.ID != base.ID || again.Inputs.Profile == base.Inputs.Profile {
		t.Error("the ID follows the output; the inputs record the profile")
	}
	// The compilation does not alias the profile.
	p.Delivery.DependsOn[0] = "changed"
	if !bytes.Equal(EncodeCompilation(base), EncodeCompilation(compile(t, spec, r41, profile(t)))) {
		t.Error("changing the profile after compiling changed the compilation")
	}
}

func TestCompileRefusals(t *testing.T) {
	staging, prod, r41, r42, p := instance(t, "staging.json"), instance(t, "prod.json"), releaseFile(t, "r41.json"), releaseFile(t, "r42.json"), profile(t)
	other := r42
	other.Application, other.ReleaseID = "other", ""
	broken := p
	broken.Quota.Pods = 0
	invalid := prod
	invalid.Configuration.MemoryMiB = 0
	for _, tt := range []struct {
		name    string
		spec    InstanceSpec
		release ReleaseBundle
		profile PlatformProfile
		want    Code
	}{
		{"not the selected release", staging, r41, p, WrongRelease},
		{"another application's release", staging, other, p, WrongApplication},
		{"invalid instance first", invalid, other, p, Invalid},
		{"invalid profile", prod, r41, broken, Invalid},
		{"invalid profile before another application's release", staging, other, broken, Invalid},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Compile(tt.spec, tt.release, tt.profile)
			code(t, err, tt.want)
			if c.ID != "" || c.Files != nil {
				t.Fatal("a refusal returned a compilation")
			}
		})
	}
}

// TestInventoryAndSelectorMatchTheFiles: the inventory is exactly the objects the files declare,
// and the NetworkPolicy selects the label the chart gives pods, the Helm release name, which Flux
// takes from the HelmRelease's name. The three are written separately, so this ties them together.
func TestInventoryAndSelectorMatchTheFiles(t *testing.T) {
	c := compile(t, instance(t, "prod.json"), releaseFile(t, "r41.json"), profile(t))
	var declared []Object
	var release, selected string
	for _, file := range c.Files {
		if strings.HasSuffix(file.Path, "/kustomization.yaml") {
			continue
		}
		var doc struct {
			APIVersion, Kind string
			Metadata         struct{ Name, Namespace string }
			Spec             struct {
				PodSelector struct{ MatchLabels map[string]string }
			}
		}
		if err := json.Unmarshal(file.Document, &doc); err != nil {
			t.Fatal(err)
		}
		group, _, versioned := strings.Cut(doc.APIVersion, "/")
		if !versioned {
			group = ""
		}
		declared = append(declared, Object{group, doc.Kind, doc.Metadata.Namespace, doc.Metadata.Name})
		switch doc.Kind {
		case "HelmRelease":
			release = doc.Metadata.Name
		case "NetworkPolicy":
			selected = doc.Spec.PodSelector.MatchLabels["app.kubernetes.io/instance"]
			if len(doc.Spec.PodSelector.MatchLabels) != 1 {
				t.Errorf("the policy selects by more than the instance label: %v", doc.Spec.PodSelector.MatchLabels)
			}
		}
	}
	var claimed []Object
	for _, claim := range c.Inventory {
		claimed = append(claimed, claim.Object)
	}
	if !slices.Equal(declared, claimed) {
		t.Errorf("inventory %v, files declare %v", claimed, declared)
	}
	if release == "" || selected != release {
		t.Errorf("the NetworkPolicy selects instance %q, the HelmRelease is %q", selected, release)
	}
}

func TestQuotaFit(t *testing.T) {
	r41, p := releaseFile(t, "r41.json"), profile(t)
	spec := func(replicas, memory int32) InstanceSpec {
		s := instance(t, "prod.json")
		s.Configuration.Replicas, s.Configuration.MemoryMiB = replicas, memory
		return s
	}
	cpu, limits := p, p
	cpu.Workload.CPURequestMilli = 1001
	limits.Quota.LimitsMemoryMiB = 511
	for _, tt := range []struct {
		name    string
		spec    InstanceSpec
		profile PlatformProfile
		field   string
	}{
		{"13 replicas and a surge pod fill 14", spec(13, 64), p, ""},
		{"14 replicas need 15 pods", spec(14, 64), p, "quota.pods"},
		{"8 pods of 256Mi fill 2Gi", spec(7, 256), p, ""},
		{"9 pods of 256Mi do not", spec(8, 256), p, "quota.requestsMemoryMiB"},
		{"two pods of 1001m exceed 2 CPUs", spec(1, 256), cpu, "quota.requestsCpuMilli"},
		{"a limit quota below the requests", spec(1, 256), limits, "quota.limitsMemoryMiB"},
		{"stopped needs nothing", spec(0, 1<<30), p, ""},
		{"no overflow", spec(1<<31-1, 1<<31-1), p, "quota.pods"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Compile(tt.spec, r41, tt.profile)
			if tt.field == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			refusal(t, err, OverQuota, tt.field)
		})
	}
	other := spec(14, 64)
	other.Release = ReleaseID("rel-sha256:" + strings.Repeat("0", 64))
	_, err := Compile(other, r41, p)
	code(t, err, WrongRelease) // the release is checked before the quota
}

func TestNoAppReachesTheUnitsFolder(t *testing.T) {
	r := releaseFile(t, "r41.json")
	r.Application, r.ReleaseID = "units", ""
	id, err := r.ID()
	if err != nil {
		t.Fatal(err)
	}
	s := instance(t, "prod.json")
	s.Application, s.Release = "units", id
	c := compile(t, s, r, profile(t))
	for _, file := range c.Files {
		if strings.HasPrefix(file.Path, "v2-apps/units/") && file.Path != "v2-apps/units/app-units-prod.json" {
			t.Errorf("an app's file is in the units folder: %s", file.Path)
		}
	}
}

func TestOwnership(t *testing.T) {
	p := profile(t)
	staging := compile(t, instance(t, "staging.json"), releaseFile(t, "r42.json"), p)
	prod := compile(t, instance(t, "prod.json"), releaseFile(t, "r41.json"), p)
	if err := CheckOwnership([]Compilation{staging, prod}, nil); err != nil {
		t.Fatalf("two environments own different objects: %v", err)
	}
	err := CheckOwnership([]Compilation{staging, prod, staging}, nil)
	refusal(t, err, OwnershipConflict, "Namespace demo-orders-staging")
	if !strings.Contains(err.Error(), "claimed by demo-orders/staging and demo-orders/staging") {
		t.Errorf("names both owners: %v", err)
	}
	foreign := []Object{{"helm.toolkit.fluxcd.io", "HelmRelease", "demo-orders-prod", "demo-orders"}}
	refusal(t, CheckOwnership([]Compilation{staging, prod}, foreign), OwnershipConflict,
		"HelmRelease.helm.toolkit.fluxcd.io demo-orders-prod/demo-orders")
	unrelated := []Object{{"apps", "Deployment", "demo-orders-prod", "demo-orders"}}
	if err := CheckOwnership([]Compilation{prod}, unrelated); err != nil {
		t.Fatalf("an object the chart renders is not claimed directly: %v", err)
	}
	if got := len(prod.Inventory); got != 5 {
		t.Fatalf("inventory has %d objects, want the five compiled cluster objects", got)
	}
}
