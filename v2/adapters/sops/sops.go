// Package sops encrypts one document with SOPS to a public age recipient, by running the `sops` command with the
// plaintext on its standard input, and decrypts one tracked file locally for make check-secrets. It is one of the
// approved subprocess adapters (see the `no-subprocess` rule and docs/iteration-2-plan.md#guardrails): the format —
// age for the data key, AES-GCM for the values, the SOPS envelope — has a maintained implementation, and a second one
// written here would be a hand-built cipher.
//
// Encrypt needs no private key: a minted secret is encrypted to the platform's public key, so the operator's
// identity is never in this process. Decrypt is handed the path of the operator's key file and passes it only to
// `sops`, in the child's environment, which holds nothing else; this process never opens the key. The console
// wires only the encrypter. stderr is read to learn that the command failed and never echoed, because a tool's
// error text is not a place to risk a plaintext.
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
	out, reason := run(command)
	if reason != "" {
		return nil, refuse(reason)
	}
	return out, nil
}

// RefusedDecryption is the code every decryption failure carries: a decryption that did not happen.
const RefusedDecryption = "refused_decryption"

// Decrypter turns one encrypted file into its plaintext, with the age identity in a key file.
type Decrypter interface {
	Decrypt(path, keyFile string) ([]byte, error)
}

// DecryptError names a fixed reason and never carries the file's content or the command's own output.
type DecryptError struct{ Reason string }

func (e *DecryptError) Error() string { return RefusedDecryption + ": " + e.Reason }

var _ Decrypter = (*Command)(nil)

// Decrypt runs sops in decrypt mode on the file at path, with SOPS_AGE_KEY_FILE set to keyFile as the child's whole
// environment, so no other key the operator's shell or home holds is tried. The plaintext is returned to the caller,
// which must not show it. A missing binary, a non-zero exit (a wrong key, a file that is not SOPS), an empty answer
// and an oversized one are refused rather than returned as partial output.
func (c *Command) Decrypt(path, keyFile string) ([]byte, error) {
	if path == "" || keyFile == "" {
		return nil, &DecryptError{Reason: "no_file_or_key"}
	}
	// --config /dev/null: no creation rule of the directory the operator runs in applies; decryption reads the file's
	// own metadata.
	command := exec.Command(c.Binary, "--config", "/dev/null", "decrypt", "--input-type", "yaml", "--output-type", "yaml", path)
	command.Env = []string{"SOPS_AGE_KEY_FILE=" + keyFile}
	out, reason := run(command)
	if reason != "" {
		return nil, &DecryptError{Reason: reason}
	}
	return out, nil
}

// run runs one sops command with its stderr discarded and its output bounded: the output, or the fixed reason it
// failed.
func run(command *exec.Cmd) ([]byte, string) {
	var discarded bytes.Buffer
	command.Stderr = &discarded
	out, err := command.Output()
	var exit *exec.ExitError
	switch {
	case errors.Is(err, exec.ErrNotFound), errors.As(err, new(*exec.Error)):
		return nil, "sops_not_available"
	case errors.As(err, &exit):
		return nil, fmt.Sprintf("sops_exit_%d", exit.ExitCode())
	case err != nil:
		return nil, "sops_not_available"
	case len(out) == 0:
		return nil, "empty_output"
	case len(out) > MaxDocument:
		return nil, "output_too_large"
	}
	return out, ""
}

// refuse is a fixed reason, so a diagnostic says what failed without saying what was encrypted.
func refuse(reason string) error { return &Error{Reason: reason} }
