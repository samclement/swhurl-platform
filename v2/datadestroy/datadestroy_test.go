package datadestroy

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestTargetShapes(t *testing.T) {
	for text, want := range map[string]Target{
		"pvc/ns/c": {Kind: Claim, Namespace: "ns", Name: "c", Text: "pvc/ns/c"},
		"pv/v":     {Kind: Volume, Name: "v", Text: "pv/v"},
	} {
		if got, err := ParseTarget(text); err != nil || got != want {
			t.Fatalf("%s: %+v, %v", text, got, err)
		}
	}
	for text, message := range map[string]string{
		"pvc/c":           "Use pvc/<namespace>/<name>",
		"pv/ns/v":         "Use pv/<name>",
		"pvc//c":          "Use pvc/<namespace>/<name>",
		"pv/":             "Use pv/<name>",
		"a/b/c/d":         "Invalid target: a/b/c/d",
		"pvc/a/b/c":       "Invalid target: pvc/a/b/c",
		"kustomization/a": Usage,
		"":                Usage,
	} {
		_, err := ParseTarget(text)
		if err == nil || err.Error() != message {
			t.Fatalf("%q: %v, want %q", text, err, message)
		}
		var refusal *Refusal
		if errors.Is(err, ErrUsage) == errors.As(err, &refusal) {
			t.Fatalf("%q: a usage line or a refusal, not both or neither: %v", text, err)
		}
	}
}

func TestConfirmIsTheTargetExactly(t *testing.T) {
	target, _ := ParseTarget("pvc/apps/data")
	if err := Confirm(target, "pvc/apps/data"); err != nil {
		t.Fatal(err)
	}
	for _, confirm := range []string{"", "pvc/apps/other", "pvc/apps/data ", "PVC/apps/data"} {
		err := Confirm(target, confirm)
		var refusal *Refusal
		if !errors.As(err, &refusal) || err.Error() !=
			"destroy-data permanently deletes pvc/apps/data and its data. Re-run with CONFIRM=pvc/apps/data" {
			t.Fatalf("%q: %v", confirm, err)
		}
	}
}

func pod(claim, phase string) []byte {
	return []byte(`{"status":{"phase":"` + phase + `"},"spec":{"volumes":[{"name":"d","persistentVolumeClaim":{"claimName":"` +
		claim + `"}},{"name":"e","emptyDir":{}}]}}`)
}

// Running, pending and terminating pods hold the claim; finished ones and other claims' pods do not.
func TestMountedByActivePod(t *testing.T) {
	for _, phase := range []string{"Running", "Pending", ""} {
		if mounted, err := MountedByActivePod([][]byte{pod("other", "Running"), pod("data", phase)}, "data"); err != nil || !mounted {
			t.Fatalf("%q: %v, %v", phase, mounted, err)
		}
	}
	for _, phase := range []string{"Succeeded", "Failed"} {
		if mounted, err := MountedByActivePod([][]byte{pod("data", phase), pod("other", "Running")}, "data"); err != nil || mounted {
			t.Fatalf("%q: %v, %v", phase, mounted, err)
		}
	}
	if _, err := MountedByActivePod([][]byte{[]byte("not json")}, "data"); err == nil {
		t.Fatal("an unreadable pod is not a pod that does not mount the claim")
	}
}

func TestClaimFacts(t *testing.T) {
	facts, err := ClaimFactsOf([]byte(`{"metadata":{"annotations":{"meta.helm.sh/release-name":"app"},
		"labels":{"kustomize.toolkit.fluxcd.io/name":"app-x"}},"spec":{"volumeName":"pv-1"}}`))
	if want := (ClaimFacts{Volume: "pv-1", Release: "app", Unit: "app-x", UnitNamespace: "flux-system"}); err != nil || facts != want {
		t.Fatalf("%+v, %v", facts, err)
	}
	facts, err = ClaimFactsOf([]byte(`{"metadata":{"labels":{"kustomize.toolkit.fluxcd.io/name":"u",
		"kustomize.toolkit.fluxcd.io/namespace":"elsewhere"}},"spec":{}}`))
	if want := (ClaimFacts{Unit: "u", UnitNamespace: "elsewhere"}); err != nil || facts != want {
		t.Fatalf("%+v, %v", facts, err)
	}
	if HelmSelector("app") != "owner=helm,name=app" {
		t.Fatal(HelmSelector("app"))
	}
}

func TestOnlyAReleasedVolume(t *testing.T) {
	target, _ := ParseTarget("pv/pv-9")
	if err := VolumeReleased(target, []byte(`{"status":{"phase":"Released"}}`)); err != nil {
		t.Fatal(err)
	}
	err := VolumeReleased(target, []byte(`{"status":{"phase":"Bound"}}`))
	if err == nil || err.Error() != "PV pv-9 is not Released; destroy its claim with pvc/<namespace>/<name> instead" {
		t.Fatal(err)
	}
}

// The reclaim policy changes before the claim goes, and the volume is waited for last; a claim that never bound has
// no volume, and a volume has no claim.
func TestChangesAndTheirLines(t *testing.T) {
	claim, _ := ParseTarget("pvc/apps/data")
	volume, _ := ParseTarget("pv/pv-9")
	for _, c := range []struct {
		target Target
		volume string
		want   []Change
		lines  []string
		done   string
	}{
		{claim, "pv-1", []Change{SetReclaimDelete, DeleteClaim, AwaitVolumeGone}, []string{
			"Plan (destroy-data pvc/apps/data): all checks passed",
			"  would set PV pv-1's reclaim policy to Delete",
			"  would delete PVC apps/data and wait for it to go",
			"  would wait for PV pv-1 and its data to go"},
			"[OK] Destroyed pvc/apps/data (PV pv-1 and its data)"},
		{claim, "", []Change{DeleteClaim}, []string{
			"Plan (destroy-data pvc/apps/data): all checks passed",
			"  would delete PVC apps/data and wait for it to go"},
			"[OK] Destroyed pvc/apps/data"},
		{volume, "pv-9", []Change{SetReclaimDelete, AwaitVolumeGone}, []string{
			"Plan (destroy-data pv/pv-9): all checks passed",
			"  would set PV pv-9's reclaim policy to Delete",
			"  would wait for PV pv-9 and its data to go"},
			"[OK] Destroyed pv/pv-9 (PV pv-9 and its data)"},
	} {
		if got := Changes(c.target, c.volume); !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%s: %v", c.target.Text, got)
		}
		if got := Plan(c.target, c.volume); !reflect.DeepEqual(got, c.lines) {
			t.Fatalf("%s:\n%s", c.target.Text, strings.Join(got, "\n"))
		}
		if got := Destroyed(c.target, c.volume); got != c.done {
			t.Fatalf("%s: %s", c.target.Text, got)
		}
	}
}
