package kernel

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func previewFacts() (HTTPInstance, HTTPWorkload) {
	return HTTPInstance{"hello", "staging", Configuration{1, 128}}, HTTPWorkload{"docker.io/nginxinc/nginx-unprivileged", "sha256:" + strings.Repeat("a", 64), 8080, "/", 101}
}

// No source, commit, media type, platform, selection or release identity is available here.
func TestRenderWithoutReleaseSemantics(t *testing.T) {
	s, w := previewFacts()
	p := profile(t)
	got, err := RenderHTTP(s, w, p)
	if err != nil {
		t.Fatal(err)
	}
	again, err := RenderHTTP(s, w, p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, again) || len(got.Files) != 6 || len(got.Inventory) != 5 || len(got.Registrations) != 1 {
		t.Fatalf("unexpected resources: %+v", got)
	}
	original, _ := json.Marshal(got)
	p.Delivery.DependsOn[0] = "changed"
	got.Files[0].Document[0] = '!'
	got.Files[1].Path = "changed"
	got.Inventory[0].Owner = "changed"
	got.Registrations[0] = "changed"
	unchanged, _ := json.Marshal(again)
	if string(original) != string(unchanged) {
		t.Fatal("calls alias their resource output or profile")
	}
	fresh, err := RenderHTTP(s, w, profile(t))
	if err != nil || !reflect.DeepEqual(fresh, again) {
		t.Fatal("retained mutation across calls")
	}
	s.Configuration.Replicas = 0
	zero, err := RenderHTTP(s, w, profile(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(zero.Files[1].Document), `"replicas":0`) {
		t.Fatal("explicit zero lost")
	}
}

func TestRenderValidationAndOrder(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*HTTPInstance, *HTTPWorkload, *PlatformProfile)
		code   Code
		field  string
	}{
		{"names before workload", func(s *HTTPInstance, w *HTTPWorkload, p *PlatformProfile) { s.Application = "BAD"; w.Image = "BAD" }, Invalid, "application"},
		{"environment", func(s *HTTPInstance, w *HTTPWorkload, p *PlatformProfile) { s.Environment = "dev" }, Unsupported, "environment"},
		{"image before digest", func(s *HTTPInstance, w *HTTPWorkload, p *PlatformProfile) { w.Image = "bad"; w.Digest = "bad" }, Invalid, "artifact.image"},
		{"digest algorithm", func(s *HTTPInstance, w *HTTPWorkload, p *PlatformProfile) { w.Digest = "sha512:bad" }, Unsupported, "artifact.digest"},
		{"digest syntax", func(s *HTTPInstance, w *HTTPWorkload, p *PlatformProfile) { w.Digest = "sha256:bad" }, Invalid, "artifact.digest"},
		{"port before path", func(s *HTTPInstance, w *HTTPWorkload, p *PlatformProfile) { w.Port = 0; w.HealthPath = "bad" }, Invalid, "runtime.port"},
		{"port upper bound", func(s *HTTPInstance, w *HTTPWorkload, p *PlatformProfile) { w.Port = 65536 }, Invalid, "runtime.port"},
		{"path before uid", func(s *HTTPInstance, w *HTTPWorkload, p *PlatformProfile) { w.HealthPath = "//bad"; w.UID = 0 }, Invalid, "runtime.healthPath"},
		{"uid before configuration", func(s *HTTPInstance, w *HTTPWorkload, p *PlatformProfile) { w.UID = 0; s.Configuration.Replicas = -1 }, Invalid, "runtime.uid"},
		{"configuration order", func(s *HTTPInstance, w *HTTPWorkload, p *PlatformProfile) { s.Configuration = Configuration{-1, 0} }, Invalid, "configuration.replicas"},
		{"configuration before profile", func(s *HTTPInstance, w *HTTPWorkload, p *PlatformProfile) {
			s.Configuration.MemoryMiB = 0
			p.SchemaVersion = 2
		}, Invalid, "configuration.memoryMiB"},
		{"profile", func(s *HTTPInstance, w *HTTPWorkload, p *PlatformProfile) { p.Workload.CPURequestMilli = 0 }, Invalid, "workload.cpuRequestMilli"},
		{"quota", func(s *HTTPInstance, w *HTTPWorkload, p *PlatformProfile) { s.Configuration.Replicas = 14 }, OverQuota, "quota.pods"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, w := previewFacts()
			p := profile(t)
			tt.change(&s, &w, &p)
			got, err := RenderHTTP(s, w, p)
			refusal(t, err, tt.code, tt.field)
			if !reflect.DeepEqual(got, ResourceSet{}) {
				t.Fatal("partial resources on refusal")
			}
		})
	}
}

func TestRenderQuotaBoundaries(t *testing.T) {
	for _, tt := range []struct {
		replicas, memory, cpu int32
		field                 string
	}{
		{13, 64, 10, ""}, {14, 64, 10, "quota.pods"}, {7, 256, 10, ""}, {8, 256, 10, "quota.requestsMemoryMiB"},
		{1, 128, 1001, "quota.requestsCpuMilli"}, {0, 1 << 30, 10, ""}, {1<<31 - 1, 1<<31 - 1, 10, "quota.pods"},
	} {
		s, w := previewFacts()
		s.Configuration = Configuration{tt.replicas, tt.memory}
		p := profile(t)
		p.Workload.CPURequestMilli = tt.cpu
		_, err := RenderHTTP(s, w, p)
		if tt.field == "" {
			if err != nil {
				t.Fatal(err)
			}
		} else {
			refusal(t, err, OverQuota, tt.field)
		}
	}
	s, w := previewFacts()
	p := profile(t)
	p.Quota.LimitsMemoryMiB = 255
	_, err := RenderHTTP(s, w, p)
	refusal(t, err, OverQuota, "quota.limitsMemoryMiB")
}

func TestSourceProvenanceChangesOnlyRealInputIdentities(t *testing.T) {
	s, r, p := instance(t, "prod.json"), releaseFile(t, "r41.json"), profile(t)
	before := compile(t, s, r, p)
	r.Source.Repository = "another/repository"
	r.Source.Commit = strings.Repeat("c", 40)
	r.ReleaseID = ""
	id, err := r.ID()
	if err != nil {
		t.Fatal(err)
	}
	s.Release = id
	after := compile(t, s, r, p)
	if before.ID != after.ID || !reflect.DeepEqual(before.Files, after.Files) || !reflect.DeepEqual(before.Inventory, after.Inventory) || !reflect.DeepEqual(before.Registrations, after.Registrations) {
		t.Fatal("provenance changed resources")
	}
	rev, _ := s.Revision()
	pid, _ := p.ID()
	if after.Inputs.Release != id || after.Inputs.Instance != rev || after.Inputs.Profile != pid || before.Inputs.Release == after.Inputs.Release || before.Inputs.Instance == after.Inputs.Instance {
		t.Fatal("real identities were not attached")
	}
}

func TestCompileCompleteRefusalPrecedence(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*InstanceSpec, *ReleaseBundle, *PlatformProfile)
		code   Code
		field  string
	}{
		{"instance before release", func(s *InstanceSpec, r *ReleaseBundle, p *PlatformProfile) {
			s.Release = "bad"
			r.Source.Commit = "bad"
		}, Invalid, "release"},
		{"source before image", func(s *InstanceSpec, r *ReleaseBundle, p *PlatformProfile) {
			r.Source.Commit = "bad"
			r.Artifact.Image = "bad"
		}, Invalid, "source.commit"},
		{"media before runtime", func(s *InstanceSpec, r *ReleaseBundle, p *PlatformProfile) {
			r.Artifact.MediaType = "bad"
			r.Runtime.Port = 0
		}, Unsupported, "artifact.mediaType"},
		{"platform before runtime", func(s *InstanceSpec, r *ReleaseBundle, p *PlatformProfile) {
			r.Artifact.Platform = "bad"
			r.Runtime.Port = 0
		}, Unsupported, "artifact.platform"},
		{"runtime before identity", func(s *InstanceSpec, r *ReleaseBundle, p *PlatformProfile) { r.Runtime.UID = 0; r.ReleaseID = "bad" }, Invalid, "runtime.uid"},
		{"identity before profile", func(s *InstanceSpec, r *ReleaseBundle, p *PlatformProfile) {
			r.Source.Commit = strings.Repeat("c", 40)
			p.Quota.Pods = 0
		}, IdentityMismatch, "releaseId"},
		{"release before profile", func(s *InstanceSpec, r *ReleaseBundle, p *PlatformProfile) { r.Source.Commit = "bad"; p.Quota.Pods = 0 }, Invalid, "source.commit"},
		{"profile before application", func(s *InstanceSpec, r *ReleaseBundle, p *PlatformProfile) {
			r.Application = "other"
			r.ReleaseID = ""
			p.Quota.Pods = 0
		}, Invalid, "quota.pods"},
		{"application before selection", func(s *InstanceSpec, r *ReleaseBundle, p *PlatformProfile) { r.Application = "other"; r.ReleaseID = "" }, WrongApplication, "application"},
		{"selection before quota", func(s *InstanceSpec, r *ReleaseBundle, p *PlatformProfile) {
			s.Release = ReleaseID("rel-sha256:" + strings.Repeat("0", 64))
			s.Configuration.Replicas = 14
		}, WrongRelease, "release"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, r, p := instance(t, "prod.json"), releaseFile(t, "r41.json"), profile(t)
			tt.change(&s, &r, &p)
			c, err := Compile(s, r, p)
			refusal(t, err, tt.code, tt.field)
			if !reflect.DeepEqual(c, Compilation{}) {
				t.Fatal("partial compilation")
			}
		})
	}
}
