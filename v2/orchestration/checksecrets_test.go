package orchestration

import (
	"errors"
	"slices"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/git"
	"github.com/samclement/swhurl-platform/v2/adapters/git/gittest"
	"github.com/samclement/swhurl-platform/v2/adapters/manifests"
	"github.com/samclement/swhurl-platform/v2/adapters/sops/sopstest"
)

// Each tracked Secret file is decrypted under the checkout's root with the key file given, and a file that does not
// decrypt or parse is a finding rather than a stop.
func TestCheckSecretsDecryptsEachTrackedFile(t *testing.T) {
	tracked := []string{".sops.yaml", "README.md", "apps/a/secret.sops.yaml", "apps/b/secret.sops.yaml", "apps/c/secret.sops.yml"}
	decrypter := &sopstest.Decrypter{Files: map[string][]byte{
		"/repo/apps/a/secret.sops.yaml": []byte("apiVersion: v1\nkind: Secret\nmetadata: {namespace: a, name: s}\nstringData:\n  K: value\n---\n"),
		"/repo/apps/c/secret.sops.yml":  []byte("stringData: [unclosed\n"),
	}}
	report, err := CheckSecrets("/repo", gittest.New("/repo", git.Checkout{Tracked: tracked}), decrypter, manifests.YAML{}, "/keys/age.agekey")
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range report.Lines[:4] {
		lines = append(lines, string(line.Level)+" "+line.Text)
	}
	want := []string{
		"OK apps/a/secret.sops.yaml: a/s (1 stringData)",
		"ERROR apps/b/secret.sops.yaml: cannot decrypt",
		"ERROR apps/c/secret.sops.yml: cannot read the decrypted document",
		"ERROR CLICKSTACK_INGESTION_KEY must be in exactly platform/clickstack/secret.sops.yaml and platform/otel/secret.sops.yaml; found in none",
	}
	if !slices.Equal(lines, want) {
		t.Fatalf("lines = %q", lines)
	}
	if !slices.Equal(decrypter.KeyFiles, []string{"/keys/age.agekey", "/keys/age.agekey", "/keys/age.agekey"}) {
		t.Fatalf("key files = %q", decrypter.KeyFiles)
	}
}

func TestCheckSecretsStopsOnAGitRefusal(t *testing.T) {
	refusal := &git.Error{Reason: git.NotARepository}
	reader := gittest.New("/repo", git.Checkout{})
	reader.Err = refusal
	if _, err := CheckSecrets("/repo", reader, &sopstest.Decrypter{}, manifests.YAML{}, "k"); !errors.Is(err, refusal) {
		t.Fatalf("err = %v", err)
	}
}
