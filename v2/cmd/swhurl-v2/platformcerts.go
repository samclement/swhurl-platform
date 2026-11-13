package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/manifests"
	"github.com/samclement/swhurl-platform/v2/certissuer"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// platformCerts sets CERT_ISSUER in the checkout at --root to --issuer and prints the operator's lines. A refusal or
// a failed read or write is one [ERROR] line on stderr and exit 1, and nothing is written.
func (c cli) platformCerts(r request) int {
	inputs, err := certIssuerInputs(r.root)
	if err != nil {
		fmt.Fprintf(c.diagnostic, "[ERROR] %v\n", err)
		return 1
	}
	outcome, err := orchestration.SetCertIssuer(inputs, r.issuer, r.dryRun, r.root, manifests.YAML{}, c.writer)
	if err != nil {
		fmt.Fprintf(c.diagnostic, "[ERROR] %v\n", err)
		return 1
	}
	return emit(c.out, c.diagnostic, []byte(strings.Join(certissuer.Messages(outcome, r.issuer), "\n")))
}

// certIssuerInputs reads the issuer files and the settings file under root; an absent issuer folder holds no issuer
// and an absent settings file is nil.
func certIssuerInputs(root string) (orchestration.CertIssuerInputs, error) {
	var inputs orchestration.CertIssuerInputs
	folder := filepath.Join(root, filepath.FromSlash(certissuer.IssuersDir))
	if _, err := os.Stat(folder); err == nil {
		tree, err := files.Snapshot(folder)
		if err != nil {
			return inputs, fmt.Errorf("%s: %w", certissuer.IssuersDir, err)
		}
		inputs.Issuers = tree
	}
	text, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(certissuer.Path)))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return inputs, fmt.Errorf("%s: cannot read the file", certissuer.Path)
	default:
		inputs.Settings = text
		if inputs.Settings == nil {
			inputs.Settings = []byte{}
		}
	}
	return inputs, nil
}
