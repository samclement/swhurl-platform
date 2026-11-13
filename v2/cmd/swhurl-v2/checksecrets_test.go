package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/git"
	"github.com/samclement/swhurl-platform/v2/adapters/git/gittest"
	"github.com/samclement/swhurl-platform/v2/adapters/sops/sopstest"
)

// checkSecretsPlatform is the plaintext of every file the comparisons read, each copy equal to its twin.
var checkSecretsPlatform = map[string]string{
	"platform/alerts/secret-platform.sops.yaml":           "stringData: {address: 'https://ntfy.sh/private-topic?tpl=1'}",
	"platform/clickstack/secret.sops.yaml":                "stringData: {CLICKSTACK_INGESTION_KEY: private-ingest}",
	"platform/console/notification-secret.sops.yaml":      "stringData: {NTFY_PLATFORM_URL: 'https://ntfy.sh/private-topic'}",
	"platform/console/secret.sops.yaml":                   "stringData: {IMAGE_WEBHOOK_TOKEN: private-hook, GITHUB_TOKEN: private-ghp}",
	"platform/flux-webhook/image-secret.sops.yaml":        "stringData: {token: private-hook}",
	"platform/image-automation/dispatch-secret.sops.yaml": "stringData: {token: private-ghp}",
	"platform/otel/secret.sops.yaml":                      "stringData: {CLICKSTACK_INGESTION_KEY: private-ingest}",
}

// runCheckSecrets checks a checkout that tracks files, decrypting each to its plaintext with a fake, and returns the
// exit status, both streams and the key files the fake was handed.
func runCheckSecrets(t *testing.T, files map[string]string, args ...string) (int, string, string, []string) {
	t.Helper()
	root := t.TempDir()
	key := filepath.Join(root, "age.agekey")
	if err := os.WriteFile(key, []byte("AGE-SECRET-KEY-FAKE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var tracked []string
	decrypter := &sopstest.Decrypter{Files: map[string][]byte{}}
	for path, text := range files {
		tracked = append(tracked, path)
		decrypter.Files[filepath.Join(root, path)] = []byte("apiVersion: v1\nkind: Secret\nmetadata: {namespace: ns, name: " +
			filepath.Base(filepath.Dir(path)) + "}\n" + text + "\n")
	}
	var out, diagnostic bytes.Buffer
	if len(args) == 0 {
		args = []string{"--root", root, "--age-key-file", key}
	}
	status := runWith(append([]string{"check-secrets"}, args...), &out, &diagnostic,
		cli{git: gittest.New(root, git.Checkout{Tracked: tracked}), decrypter: decrypter})
	return status, out.String(), diagnostic.String(), decrypter.KeyFiles
}

func TestCheckSecretsPassesWithoutPrintingAValue(t *testing.T) {
	status, out, diagnostic, keys := runCheckSecrets(t, checkSecretsPlatform)
	if status != 0 || diagnostic != "" || !strings.HasSuffix(out, "\n0 error(s), 0 warning(s).\n") ||
		!strings.Contains(out, "[OK] platform/console/secret.sops.yaml: ns/console (2 stringData)\n") {
		t.Fatalf("exit %d\n%s%s", status, out, diagnostic)
	}
	if strings.Contains(out, "private") || len(keys) != len(checkSecretsPlatform) || !filepath.IsAbs(keys[0]) {
		t.Fatalf("a value is printed, or the key file is not handed to sops whole: %q\n%s", keys, out)
	}
}

func TestCheckSecretsFindingsExitOne(t *testing.T) {
	files := map[string]string{"apps/a/secret.sops.yaml": "stringData: {K: REPLACE_ME, E: ''}\ndata: {D: " +
		"'TUdZNFptRmtOV0l0WkRsallpMDBOamxtTFdFeE5qVXROekE0TmpjM01qZzVOVEJs'}"}
	for path, text := range checkSecretsPlatform {
		files[path] = text
	}
	files["platform/otel/secret.sops.yaml"] = "stringData: {CLICKSTACK_INGESTION_KEY: private-other}"
	status, out, _, _ := runCheckSecrets(t, files)
	for _, want := range []string{
		"[OK] apps/a/secret.sops.yaml: ns/a (1 data, 2 stringData)\n" +
			"  [WARN] apps/a/secret.sops.yaml: data.D: decodes to base64 text; probably encoded twice\n" +
			"  [ERROR] apps/a/secret.sops.yaml: stringData.E: empty\n" +
			"  [ERROR] apps/a/secret.sops.yaml: stringData.K: still the REPLACE_ME placeholder; set it with sops\n",
		"[ERROR] CLICKSTACK_INGESTION_KEY differs between platform/clickstack/secret.sops.yaml and platform/otel/secret.sops.yaml\n",
		"\n3 error(s), 1 warning(s).\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("exit %d, lacks %q:\n%s", status, want, out)
		}
	}
	if status != 1 || strings.Contains(out, "private") {
		t.Fatalf("exit %d\n%s", status, out)
	}
}

func TestCheckSecretsRefusesWithoutAKeyOrACheckout(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "age.agekey")
	status, out, diagnostic, keys := runCheckSecrets(t, checkSecretsPlatform, "--age-key-file", missing)
	if status != 2 || out != "" || diagnostic != "[ERROR] age key not found at "+missing+"; set SOPS_AGE_KEY_FILE\n" || keys != nil {
		t.Fatalf("exit %d, %q, %q, %q", status, out, diagnostic, keys)
	}
	key := filepath.Join(t.TempDir(), "age.agekey")
	if err := os.WriteFile(key, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	status, out, diagnostic, _ = runCheckSecrets(t, checkSecretsPlatform, "--root", t.TempDir(), "--age-key-file", key)
	if status != 2 || out != "" || !strings.HasPrefix(diagnostic, "invalid: check-secrets: ") {
		t.Fatalf("a folder that is not the checkout: exit %d, %q, %q", status, out, diagnostic)
	}
	if status, _, _, _ := runCheckSecrets(t, checkSecretsPlatform, "--root", "."); status != 2 {
		t.Fatalf("no --age-key-file: exit %d", status)
	}
}
