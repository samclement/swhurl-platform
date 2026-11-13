package orchestration

import (
	"path"
	"strings"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/manifests"
	"github.com/samclement/swhurl-platform/v2/certissuer"
	"github.com/samclement/swhurl-platform/v2/checkrepo"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// CertIssuerInputs are what platform-certs reads from a checkout: every file under certissuer.IssuersDir, and the
// settings file's text, which is nil when the file is not there.
type CertIssuerInputs struct {
	Issuers  []files.TreeFile
	Settings []byte
}

// SetCertIssuer sets CERT_ISSUER in the settings file under root to issuer, which must be a letsencrypt-*
// ClusterIssuer the issuer files define. The edit is decided on the text and confirmed on the data decoded again;
// a dry run decides and confirms it and writes nothing, and an issuer already set writes nothing. A refusal is a
// *certissuer.Refusal and nothing is written; a failed write is the writer's error.
func SetCertIssuer(inputs CertIssuerInputs, issuer string, dryRun bool, root string, decoder manifests.Decoder,
	writer files.Writer) (certissuer.Outcome, error) {
	issuers, err := allowedIssuers(inputs.Issuers, decoder)
	if err != nil {
		return 0, err
	}
	if err := certissuer.Allowed(issuers, issuer); err != nil {
		return 0, err
	}
	if inputs.Settings == nil {
		return 0, &certissuer.Refusal{Reason: "Missing settings file: " + certissuer.Path}
	}
	edited, err := editSettings(inputs.Settings, issuer, decoder)
	switch {
	case err != nil:
		return 0, err
	case edited == nil:
		return certissuer.AlreadySet, nil
	case dryRun:
		return certissuer.WouldUpdate, nil
	}
	if _, err := writer.Write(root, []kernel.File{{Path: certissuer.Path, Document: edited}}); err != nil {
		return 0, err
	}
	return certissuer.Updated, nil
}

// allowedIssuers are the letsencrypt-* ClusterIssuers the YAML files among the issuer folder's files define.
func allowedIssuers(tree []files.TreeFile, decoder manifests.Decoder) ([]string, error) {
	var documents []map[string]any
	for _, file := range tree {
		if !strings.HasSuffix(file.Path, ".yaml") {
			continue
		}
		decoded, err := decoder.Documents(file.Body)
		if err != nil {
			return nil, &certissuer.Refusal{Reason: path.Join(certissuer.IssuersDir, file.Path) + ": not YAML"}
		}
		documents = append(documents, documentValues(decoded)...)
	}
	return certissuer.Issuers(documents), nil
}

// editSettings is the settings text with CERT_ISSUER set to issuer and confirmed on the data decoded again, or nil
// when issuer is already set.
func editSettings(text []byte, issuer string, decoder manifests.Decoder) ([]byte, error) {
	before, err := decodeSettings(decoder, text)
	if err != nil {
		return nil, err
	}
	current, err := certissuer.Current(before)
	if err != nil || current == issuer {
		return nil, err
	}
	edited, err := certissuer.SetLine(text, issuer)
	if err != nil {
		return nil, err
	}
	after, err := decodeSettings(decoder, edited)
	if err == nil {
		err = certissuer.Confirm(before, after, issuer)
	}
	if err != nil {
		return nil, &certissuer.Refusal{Reason: "editing " + certissuer.Key + " would change other settings; refusing to write"}
	}
	return edited, nil
}

// decodeSettings is the settings text's data, refused when it is not YAML or not one ConfigMap with data.
func decodeSettings(decoder manifests.Decoder, text []byte) (map[string]any, error) {
	documents, err := decoder.Documents(text)
	if err != nil {
		return nil, &certissuer.Refusal{Reason: "expected a ConfigMap with a data section"}
	}
	return certissuer.Data(documentValues(documents))
}

func documentValues(documents []checkrepo.Document) []map[string]any {
	out := make([]map[string]any, 0, len(documents))
	for _, document := range documents {
		out = append(out, document.Value)
	}
	return out
}
