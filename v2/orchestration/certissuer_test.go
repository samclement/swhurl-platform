package orchestration

import (
	"errors"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/files/filestest"
	"github.com/samclement/swhurl-platform/v2/adapters/manifests"
	"github.com/samclement/swhurl-platform/v2/certissuer"
)

const settingsText = `# Cluster settings (fixture comment must survive)
apiVersion: v1
kind: ConfigMap
metadata:
  name: platform-settings
  namespace: flux-system
data:
  CERT_ISSUER: letsencrypt-prod   # trailing note is replaced with the value line
  BASE_DOMAIN: homelab.swhurl.com
`

// issuerFiles are the three issuers Git defines, one of them self-signed, and the folder's kustomization.
var issuerFiles = []files.TreeFile{
	{Path: "clusterissuer-letsencrypt-prod.yaml", Body: []byte("kind: ClusterIssuer\nmetadata: {name: letsencrypt-prod}\n")},
	{Path: "clusterissuer-letsencrypt-staging.yaml", Body: []byte("kind: ClusterIssuer\nmetadata: {name: letsencrypt-staging}\n")},
	{Path: "clusterissuer-selfsigned.yaml", Body: []byte("kind: ClusterIssuer\nmetadata: {name: selfsigned}\n")},
	{Path: "kustomization.yaml", Body: []byte("kind: Kustomization\nresources: [a.yaml]\n")},
	{Path: "README.md", Body: []byte("kind: [not yaml\n")},
}

func setIssuer(t *testing.T, text, issuer string, dryRun bool) (certissuer.Outcome, *filestest.Fake, error) {
	t.Helper()
	writer := filestest.New()
	inputs := CertIssuerInputs{Issuers: issuerFiles}
	if text != "<absent>" {
		inputs.Settings = []byte(text)
	}
	outcome, err := SetCertIssuer(inputs, issuer, dryRun, "/repo", manifests.YAML{}, writer)
	return outcome, writer, err
}

func TestSetCertIssuerChangesOnlyTheOneLine(t *testing.T) {
	outcome, writer, err := setIssuer(t, settingsText, "letsencrypt-staging", false)
	if err != nil || outcome != certissuer.Updated {
		t.Fatalf("%v %v", outcome, err)
	}
	want := strings.Replace(settingsText, "  CERT_ISSUER: letsencrypt-prod   # trailing note is replaced with the value line",
		"  CERT_ISSUER: letsencrypt-staging", 1)
	if got := string(writer.Contents[certissuer.Path]); got != want || len(writer.Contents) != 1 {
		t.Fatalf("wrote %d files:\n%s", len(writer.Contents), got)
	}
}

func TestSetCertIssuerAlreadySetAndDryRunWriteNothing(t *testing.T) {
	for name, run := range map[string]struct {
		issuer string
		dryRun bool
		want   certissuer.Outcome
	}{
		"already set": {"letsencrypt-prod", false, certissuer.AlreadySet},
		"dry run":     {"letsencrypt-staging", true, certissuer.WouldUpdate},
	} {
		outcome, writer, err := setIssuer(t, settingsText, run.issuer, run.dryRun)
		if err != nil || outcome != run.want || writer.Writes != 0 {
			t.Errorf("%s: %v %v, wrote %d", name, outcome, err, writer.Writes)
		}
	}
}

func TestSetCertIssuerRefusalsWriteNothing(t *testing.T) {
	for name, c := range map[string]struct{ issuer, text, reason string }{
		"untrusted issuer": {"selfsigned", settingsText, "unknown issuer 'selfsigned'; expected one of letsencrypt-prod, letsencrypt-staging"},
		"typo":             {"letsencrypt-prd", settingsText, "unknown issuer 'letsencrypt-prd'"},
		"no file":          {"letsencrypt-staging", "<absent>", "Missing settings file: " + certissuer.Path},
		"missing key": {"letsencrypt-staging", strings.Replace(settingsText,
			"  CERT_ISSUER: letsencrypt-prod   # trailing note is replaced with the value line\n", "", 1),
			"Missing key 'CERT_ISSUER' in " + certissuer.Path},
		"duplicate line":  {"letsencrypt-staging", settingsText + "#  CERT_ISSUER: x\n  CERT_ISSUER: y\n", "expected exactly one 'CERT_ISSUER:' line"},
		"not a ConfigMap": {"letsencrypt-staging", "kind: Secret\ndata:\n  CERT_ISSUER: a\n", "expected a ConfigMap with a data section"},
		"not YAML":        {"letsencrypt-staging", "kind: [ConfigMap\n", "expected a ConfigMap with a data section"},
		"two documents":   {"letsencrypt-staging", settingsText + "---\n" + settingsText, "expected a ConfigMap with a data section"},
		"collateral edit": {"letsencrypt-staging", strings.Replace(settingsText, "BASE_DOMAIN: homelab.swhurl.com",
			"BASE_DOMAIN: |\n    CERT_ISSUER: nested", 1), "expected exactly one 'CERT_ISSUER:' line"},
		"an alias of the value": {"letsencrypt-staging", strings.NewReplacer("letsencrypt-prod   #", "&issuer letsencrypt-prod   #",
			"BASE_DOMAIN: homelab.swhurl.com", "BASE_DOMAIN: *issuer").Replace(settingsText),
			"editing CERT_ISSUER would change other settings; refusing to write"},
	} {
		_, writer, err := setIssuer(t, c.text, c.issuer, false)
		var refusal *certissuer.Refusal
		if !errors.As(err, &refusal) || !strings.HasPrefix(refusal.Reason, c.reason) || writer.Writes != 0 {
			t.Errorf("%s: %v, wrote %d", name, err, writer.Writes)
		}
	}
}

func TestSetCertIssuerRefusesAnIssuerFileThatIsNotYAML(t *testing.T) {
	inputs := CertIssuerInputs{Issuers: []files.TreeFile{{Path: "broken.yaml", Body: []byte("kind: [x\n")}}, Settings: []byte(settingsText)}
	_, err := SetCertIssuer(inputs, "letsencrypt-staging", false, "/repo", manifests.YAML{}, filestest.New())
	if err == nil || err.Error() != "infra/issuers/broken.yaml: not YAML" {
		t.Fatal(err)
	}
}
