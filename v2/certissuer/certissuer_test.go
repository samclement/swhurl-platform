package certissuer

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func issuer(kind, name string) map[string]any {
	return map[string]any{"kind": kind, "metadata": map[string]any{"name": name}}
}

func TestOnlyLetsEncryptClusterIssuersMayBeNamed(t *testing.T) {
	got := Issuers([]map[string]any{
		issuer("ClusterIssuer", "letsencrypt-staging"), issuer("ClusterIssuer", "selfsigned"),
		issuer("Issuer", "letsencrypt-namespaced"), issuer("ClusterIssuer", "letsencrypt-prod"),
		issuer("ClusterIssuer", "letsencrypt-prod"), {"kind": "ClusterIssuer"}, nil,
	})
	if want := []string{"letsencrypt-prod", "letsencrypt-staging"}; !slices.Equal(got, want) {
		t.Fatalf("issuers %q, want %q", got, want)
	}
	err := Allowed(got, "selfsigned")
	if err == nil || err.Error() != "unknown issuer 'selfsigned'; expected one of letsencrypt-prod, letsencrypt-staging" {
		t.Fatalf("selfsigned: %v", err)
	}
	if err := Allowed(got, "letsencrypt-staging"); err != nil {
		t.Fatal(err)
	}
}

func TestDataIsOneConfigMapWithData(t *testing.T) {
	data := map[string]any{Key: "letsencrypt-prod"}
	if got, err := Data([]map[string]any{{"kind": "ConfigMap", "data": data}}); err != nil || got[Key] != "letsencrypt-prod" {
		t.Fatalf("%v %v", got, err)
	}
	for name, documents := range map[string][]map[string]any{
		"none":      nil,
		"a Secret":  {{"kind": "Secret", "data": data}},
		"no data":   {{"kind": "ConfigMap"}},
		"two":       {{"kind": "ConfigMap", "data": data}, {"kind": "ConfigMap", "data": data}},
		"data list": {{"kind": "ConfigMap", "data": []any{}}},
	} {
		var refusal *Refusal
		if _, err := Data(documents); !errors.As(err, &refusal) || refusal.Reason != "expected a ConfigMap with a data section" {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := Current(map[string]any{}); err == nil || err.Error() != "Missing key 'CERT_ISSUER' in "+Path {
		t.Fatalf("missing key: %v", err)
	}
}

func TestSetLineReplacesTheOneLineAndItsComment(t *testing.T) {
	text := "data:\n  CERT_ISSUER: letsencrypt-prod   # a note\n  BASE_DOMAIN: example.test\n"
	got, err := SetLine([]byte(text), "letsencrypt-staging")
	if want := "data:\n  CERT_ISSUER: letsencrypt-staging\n  BASE_DOMAIN: example.test\n"; err != nil || string(got) != want {
		t.Fatalf("%q %v", got, err)
	}
	for name, text := range map[string]string{
		"none":        "data:\n  BASE_DOMAIN: x\n",
		"unindented":  "CERT_ISSUER: x\n",
		"two":         "data:\n  CERT_ISSUER: x\n  CERT_ISSUER: y\n",
		"in a scalar": "data:\n  CERT_ISSUER: x\n  BASE_DOMAIN: |\n    CERT_ISSUER: nested\n",
	} {
		if _, err := SetLine([]byte(text), "letsencrypt-staging"); err == nil || err.Error() != "expected exactly one 'CERT_ISSUER:' line" {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestConfirmAcceptsOnlyTheOneValueChanging(t *testing.T) {
	before := map[string]any{Key: "letsencrypt-prod", "BASE_DOMAIN": "example.test"}
	if err := Confirm(before, map[string]any{Key: "letsencrypt-staging", "BASE_DOMAIN": "example.test"}, "letsencrypt-staging"); err != nil {
		t.Fatal(err)
	}
	for name, after := range map[string]map[string]any{
		"another value":   {Key: "letsencrypt-staging", "BASE_DOMAIN": "other.test"},
		"a key lost":      {Key: "letsencrypt-staging"},
		"a key added":     {Key: "letsencrypt-staging", "BASE_DOMAIN": "example.test", "X": "y"},
		"the wrong value": {Key: "letsencrypt-prod", "BASE_DOMAIN": "example.test"},
	} {
		if err := Confirm(before, after, "letsencrypt-staging"); err == nil || !strings.Contains(err.Error(), "would change other settings") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestEveryOutcomeEndsWithTheReminder(t *testing.T) {
	want := map[Outcome]string{
		AlreadySet:  "[INFO] CERT_ISSUER already set to letsencrypt-prod",
		WouldUpdate: "[INFO] CERT_ISSUER would update to letsencrypt-prod in " + Path,
		Updated:     "[INFO] CERT_ISSUER updated to letsencrypt-prod in " + Path,
	}
	for outcome, first := range want {
		if got := Messages(outcome, "letsencrypt-prod"); !slices.Equal(got, []string{first, Reminder}) {
			t.Errorf("%d: %q", outcome, got)
		}
	}
}
