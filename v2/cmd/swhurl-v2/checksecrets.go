package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/samclement/swhurl-platform/v2/adapters/git"
	"github.com/samclement/swhurl-platform/v2/adapters/manifests"
	"github.com/samclement/swhurl-platform/v2/adapters/sops"
	"github.com/samclement/swhurl-platform/v2/orchestration"
	"github.com/samclement/swhurl-platform/v2/secretcheck"
)

// checkSecrets decrypts every Secret file the checkout at --root tracks with the age identity in --age-key-file and
// prints one line per document and value finding, then the comparisons and the counts. No value is printed. An error
// exits 1; a key file that is not there, or a checkout that cannot be read, exits 2.
func (c cli) checkSecrets(r request) int {
	if info, err := os.Stat(r.ageKey); err != nil || info.IsDir() {
		fmt.Fprintf(c.diagnostic, "[ERROR] age key not found at %s; set SOPS_AGE_KEY_FILE\n", r.ageKey)
		return 2
	}
	root, err := filepath.Abs(r.root)
	keyFile, keyErr := filepath.Abs(r.ageKey)
	if err != nil || keyErr != nil {
		fmt.Fprintf(c.diagnostic, "invalid: check-secrets: cannot resolve %s or %s\n", r.root, r.ageKey)
		return 2
	}
	reader, decrypter := c.git, c.decrypter
	if reader == nil {
		reader = git.New()
	}
	if decrypter == nil {
		decrypter = sops.New()
	}
	report, err := orchestration.CheckSecrets(root, reader, decrypter, manifests.YAML{}, keyFile)
	if err != nil {
		fmt.Fprintf(c.diagnostic, "invalid: check-secrets: %v\n", err)
		return 2
	}
	if status := emit(c.out, c.diagnostic, checkSecretsText(report)); status != 0 {
		return status
	}
	if report.Errors > 0 {
		return 1
	}
	return 0
}

// checkSecretsText is the operator's text: a value's finding is indented under its document's line.
func checkSecretsText(report secretcheck.Report) []byte {
	var out strings.Builder
	for _, line := range report.Lines {
		if line.Value {
			out.WriteString("  ")
		}
		fmt.Fprintf(&out, "[%s] %s\n", line.Level, line.Text)
	}
	fmt.Fprintf(&out, "\n%d error(s), %d warning(s).\n", report.Errors, report.Warnings)
	return []byte(out.String())
}
