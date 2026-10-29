// Package sops encrypts one document with SOPS to a public age recipient, by running the `sops` command with the
// plaintext on its standard input. It is the one process v2 starts (see the `no-subprocess` rule and
// docs/iteration-2-plan.md#guardrails): the format — age for the data key, AES-GCM for the values, the SOPS
// envelope — has a maintained implementation, and a second one written here would be a hand-built cipher.
//
// It reads no private key. Encryption needs only the recipient, so a minted secret is encrypted to the
// platform's public key and the operator's identity is never in this process. Nothing but the ciphertext leaves
// it: stderr is read to learn that the command failed and never echoed, because a tool's error text is not a
// place to risk a plaintext.
package sops

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
)

const (
	// RefusedEncryption is the code every failure here carries: an encryption that did not happen.
	RefusedEncryption = "refused_encryption"
	// MaxDocument bounds what is handed to the command and what it may return: an instance's Secret is a few
	// hundred bytes.
	MaxDocument = 1 << 20
	// encryptedRegex limits encryption to the values, as every SOPS file in this repository does: a Secret's
	// name and namespace stay readable so a reviewer can see which object a file is.
	encryptedRegex = "^(data|stringData)$"
)

// Encrypter turns one plaintext document into the ciphertext of the same document.
type Encrypter interface {
	Encrypt(document []byte, recipient string) ([]byte, error)
}

// Error names a fixed reason and never carries the document, the recipient or the command's own output.
type Error struct{ Reason string }

func (e *Error) Error() string { return RefusedEncryption + ": " + e.Reason }

// Command runs the sops binary named by Binary, which is looked up in PATH.
type Command struct{ Binary string }

// New is the adapter that runs `sops` from PATH.
func New() *Command { return &Command{Binary: "sops"} }

var _ Encrypter = (*Command)(nil)

// Encrypt runs sops in encrypt mode: the recipient is public, the input is the document on stdin, and the
// output is the ciphertext. A missing binary, a non-zero exit and an oversized answer are refused rather than
// returned as partial output.
func (c *Command) Encrypt(document []byte, recipient string) ([]byte, error) {
	if recipient == "" {
		return nil, refuse("no_recipient")
	}
	if len(document) == 0 || len(document) > MaxDocument {
		return nil, refuse("document_too_large")
	}
	// --config /dev/null: the recipient is this call's argument, not a repository creation rule, so the
	// directory the operator happens to run in must not decide how the document is encrypted.
	command := exec.Command(c.Binary, "--config", "/dev/null", "--encrypt", "--age", recipient,
		"--encrypted-regex", encryptedRegex, "--input-type", "yaml", "--output-type", "yaml", "/dev/stdin")
	command.Stdin = bytes.NewReader(document)
	var discarded bytes.Buffer
	command.Stderr = &discarded
	out, err := command.Output()
	var exit *exec.ExitError
	switch {
	case errors.Is(err, exec.ErrNotFound), errors.As(err, new(*exec.Error)):
		return nil, refuse("sops_not_available")
	case errors.As(err, &exit):
		return nil, refuse(fmt.Sprintf("sops_exit_%d", exit.ExitCode()))
	case err != nil:
		return nil, refuse("sops_not_available")
	case len(out) == 0:
		return nil, refuse("empty_output")
	case len(out) > MaxDocument:
		return nil, refuse("output_too_large")
	}
	return out, nil
}

// refuse is a fixed reason, so a diagnostic says what failed without saying what was encrypted.
func refuse(reason string) error { return &Error{Reason: reason} }
