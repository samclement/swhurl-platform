package orchestration

import (
	"path/filepath"

	"github.com/samclement/swhurl-platform/v2/adapters/git"
	"github.com/samclement/swhurl-platform/v2/adapters/manifests"
	"github.com/samclement/swhurl-platform/v2/adapters/sops"
	"github.com/samclement/swhurl-platform/v2/secretcheck"
)

// CheckSecrets runs check-secrets' decisions over one checkout: Git says which Secret files it tracks, decrypter
// decrypts each with the age identity in keyFile, decoder parses the plaintext, and secretcheck.Check judges them all.
// The plaintext stays in this call: only the report, which holds no value, leaves it. A file that does not decrypt or
// parse is the report's finding; a refused Git read stops it with that error.
func CheckSecrets(root string, reader git.Reader, decrypter sops.Decrypter, decoder manifests.Decoder, keyFile string) (secretcheck.Report, error) {
	checkout, err := reader.Read(root)
	if err != nil {
		return secretcheck.Report{}, err
	}
	var files []secretcheck.File
	for _, path := range secretcheck.Tracked(checkout.Tracked) {
		file := secretcheck.File{Path: path}
		plaintext, err := decrypter.Decrypt(filepath.Join(root, filepath.FromSlash(path)), keyFile)
		if err != nil {
			file.Failure = secretcheck.Undecryptable
			files = append(files, file)
			continue
		}
		documents, err := decoder.Documents(plaintext)
		if err != nil {
			file.Failure = secretcheck.Unparsable
		}
		for _, document := range documents {
			file.Documents = append(file.Documents, document.Value)
		}
		files = append(files, file)
	}
	return secretcheck.Check(files), nil
}
