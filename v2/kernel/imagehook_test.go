package kernel

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
)

const hookHostFixture = "flux-webhook.example.test"

// hookRelease is a staging release record publishing image from repository.
func hookRelease(application, image, repository string) []byte {
	return []byte(`{"schemaVersion": 1, "application": "` + application + `",
  "source": {"repository": "` + repository + `", "commit": "` + strings.Repeat("b", 40) + `"},
  "artifact": {"image": "` + image + `", "digest": "sha256:` + strings.Repeat("b", 64) + `",
    "mediaType": "application/vnd.oci.image.manifest.v1+json", "platform": "linux/amd64"},
  "runtime": {"kind": "web", "port": 8080, "healthPath": "/healthz", "uid": 65532}}`)
}

// The URL is the path Flux derives for the app-images Receiver in flux-system from the token.
func TestImageHookURLIsThePathFluxDerives(t *testing.T) {
	digest := sha256.Sum256([]byte("fixture-token" + "app-images" + "flux-system"))
	want := "https://" + hookHostFixture + "/hook/" + hex.EncodeToString(digest[:])
	if got := ImageHookURL("fixture-token", hookHostFixture); got != want {
		t.Fatalf("url = %s, want %s", got, want)
	}
	if ImageHookURL("rotated", hookHostFixture) == want {
		t.Fatal("a rotated token must move the URL")
	}
	events := ImageHookEvents()
	events[0] = "changed"
	if !reflect.DeepEqual(ImageHookEvents(), []string{"package", "registry_package"}) {
		t.Fatal("the events are the caller's own copy")
	}
}

// Only the app's own platform image qualifies; another image is left out, and a record that cannot say is a problem.
func TestImageHookAppsChoosesTheOwnPlatformImage(t *testing.T) {
	staging := map[string][]byte{
		"notes":     hookRelease("notes", "ghcr.io/samclement/notes", "samclement/notes"),
		"renamed":   hookRelease("renamed", "ghcr.io/samclement/renamed", "samclement/notes-source"),
		"pinned":    hookRelease("pinned", "ghcr.io/other/pinned", "samclement/pinned"),
		"third":     hookRelease("third", "docker.io/samclement/third", "samclement/third"),
		"elsewhere": hookRelease("elsewhere", "ghcr.io/samclement/notes", "samclement/notes"),
		"broken":    []byte(`{"schemaVersion": 1`),
		"moved":     hookRelease("notes", "ghcr.io/samclement/moved", "samclement/moved"),
	}
	got := ImageHookApps(staging, "SamClement")
	want := []ImageHookApp{
		{Application: "broken", Problem: got[0].Problem},
		{Application: "moved", Problem: "the staging release record names notes"},
		{Application: "notes", Repository: "samclement/notes"},
		{Application: "renamed", Repository: "samclement/notes-source"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("apps = %#v", got)
	}
	if !strings.HasPrefix(got[0].Problem, "the staging release record cannot be read: ") {
		t.Fatalf("problem = %q", got[0].Problem)
	}
	if apps := ImageHookApps(map[string][]byte{}, "samclement"); apps == nil || len(apps) != 0 {
		t.Fatalf("no record is no app, not nil: %#v", apps)
	}
}

// A correct webhook is left alone; a rotated, disabled or wrong-event one to the host is changed; none is created.
func TestPlanImageHook(t *testing.T) {
	want := ImageHookURL("fixture-token", hookHostFixture)
	good := ListedHook{ID: 7, Active: true, Events: []string{"registry_package", "package"}, URL: want}
	other := ListedHook{ID: 1, Active: true, Events: []string{"push"}, URL: "https://elsewhere.example.test/x"}
	changed := func(change func(*ListedHook)) ListedHook {
		hook := good
		change(&hook)
		return hook
	}
	cases := []struct {
		name   string
		listed []ListedHook
		plan   ImageHookPlan
	}{
		{"none", nil, ImageHookPlan{State: ImageHookMissing}},
		{"another host only", []ListedHook{other}, ImageHookPlan{State: ImageHookMissing}},
		{"in place", []ListedHook{other, good}, ImageHookPlan{State: ImageHookInPlace}},
		{"rotated token", []ListedHook{changed(func(h *ListedHook) { h.URL = ImageHookURL("old", hookHostFixture) })},
			ImageHookPlan{State: ImageHookStale, ID: 7}},
		{"disabled", []ListedHook{changed(func(h *ListedHook) { h.Active = false })}, ImageHookPlan{State: ImageHookStale, ID: 7}},
		{"wrong events", []ListedHook{changed(func(h *ListedHook) { h.Events = []string{"push"} })},
			ImageHookPlan{State: ImageHookStale, ID: 7}},
		{"an extra event", []ListedHook{changed(func(h *ListedHook) { h.Events = append(h.Events, "push") })},
			ImageHookPlan{State: ImageHookStale, ID: 7}},
		{"the host in another case", []ListedHook{changed(func(h *ListedHook) {
			h.URL = strings.Replace(want, hookHostFixture, strings.ToUpper(hookHostFixture), 1)
		})}, ImageHookPlan{State: ImageHookStale, ID: 7}},
		{"a correct one beside a stale one", []ListedHook{changed(func(h *ListedHook) { h.ID, h.Active = 3, false }), good},
			ImageHookPlan{State: ImageHookInPlace}},
		{"the first stale one", []ListedHook{changed(func(h *ListedHook) { h.ID, h.Active = 3, false }),
			changed(func(h *ListedHook) { h.ID, h.Events = 4, nil })}, ImageHookPlan{State: ImageHookStale, ID: 3}},
		{"not a URL", []ListedHook{{ID: 5, Active: true, URL: "://"}}, ImageHookPlan{State: ImageHookMissing}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := PlanImageHook(tt.listed, want); got != tt.plan {
				t.Fatalf("plan = %#v, want %#v", got, tt.plan)
			}
		})
	}
}
