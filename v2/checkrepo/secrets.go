package checkrepo

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// encryptedName is the file name of an encrypted Secret, at any depth of a unit's folder (*.sops.y*ml).
var encryptedName = regexp.MustCompile(`\.sops\.y[^/]*ml$`)

// encryptedRegex is the only part of a Secret SOPS may encrypt here: its values.
const encryptedRegex = "^(data|stringData)$"

// requiredSettings are the platform settings every substituting unit relies on.
var requiredSettings = []string{"BASE_DOMAIN", "CERT_ISSUER"}

// checkSecrets is every Git-managed Secret under the manifest roots encrypted with SOPS age, and every unit that
// decrypts holding at least one encrypted Secret file in its folder. The backup writer's Secret in an app's backup
// file is the one exception: its values are Flux substitutions, not secrets.
func checkSecrets(r Repository, units []unit) Result {
	result := Result{Check: sopsSecrets}
	count := 0
	for _, file := range sortedManifests(r) {
		found, problems := fileSecrets(file, r.Manifests[file])
		count += found
		result.Problems = append(result.Problems, problems...)
	}
	decrypting := 0
	for _, u := range units {
		if !u.decrypts {
			continue
		}
		decrypting++
		if !holdsEncrypted(r.Present, u.path) {
			result.Problems = append(result.Problems, u.name+" decrypts SOPS but its path has no *.sops.yaml Secret")
		}
	}
	if len(result.Problems) == 0 {
		result.Okay = fmt.Sprintf("%d Git-managed Secrets use SOPS age encryption, and each of the %d units that "+
			"decrypt has one", count, decrypting)
	}
	return result
}

// sortedManifests is the manifest files under the manifest roots, in order.
func sortedManifests(r Repository) []string {
	var files []string
	for file := range r.Manifests {
		if isManifest(file) {
			files = append(files, file)
		}
	}
	slices.Sort(files)
	return files
}

// fileSecrets checks one manifest file: how many Secrets it holds, and every problem with them.
func fileSecrets(file string, decoded Decoded) (int, []string) {
	if problem := kubernetesDocuments(file, decoded); problem != "" {
		return 0, []string{problem}
	}
	var problems []string
	count := 0
	for _, document := range decoded.Documents {
		if document.Kind != "Secret" {
			continue
		}
		count++
		if problem := secretProblem(file, document.Value); problem != "" {
			problems = append(problems, file+": "+problem)
		}
	}
	if isEncryptedFile(file) && count == 0 {
		problems = append(problems, file+": expected an encrypted Secret")
	}
	return count, problems
}

// kubernetesDocuments says why a file is not Kubernetes YAML: not YAML at all, empty, or holding a document that is
// not a mapping.
func kubernetesDocuments(file string, decoded Decoded) string {
	if decoded.Err != "" {
		return file + ": invalid YAML: " + decoded.Err
	}
	if len(decoded.Documents) == 0 || slices.ContainsFunc(decoded.Documents, func(d Document) bool { return d.Value == nil }) {
		return file + ": expected Kubernetes YAML documents"
	}
	return ""
}

// secretProblem is what is wrong with one Secret in file, or "".
func secretProblem(file string, secret map[string]any) string {
	switch {
	case baseName(file) == backupFile && substitutedOnly(secret):
		return ""
	case !isEncryptedFile(file):
		return "Git-managed Secret must be a .sops.yaml or .sops.yml file"
	}
	sops, _ := secret["sops"].(map[string]any)
	switch {
	case !truthy(sops["age"]) || !truthy(sops["mac"]):
		return "missing SOPS age metadata"
	case sops["encrypted_regex"] != encryptedRegex:
		return "unexpected SOPS encrypted_regex"
	}
	return payloadProblem(secret)
}

// payloadProblem is whether the Secret's data and stringData are present, non-empty and every value ciphertext.
func payloadProblem(secret map[string]any) string {
	var payloads []map[string]any
	for _, key := range []string{"data", "stringData"} {
		value, stated := secret[key]
		if !stated {
			continue
		}
		payload, isMapping := value.(map[string]any)
		if !isMapping || len(payload) == 0 {
			return "Secret needs nonempty encrypted data or stringData"
		}
		payloads = append(payloads, payload)
	}
	if len(payloads) == 0 {
		return "Secret needs nonempty encrypted data or stringData"
	}
	for _, payload := range payloads {
		for _, value := range payload {
			if text, isText := value.(string); !isText || !strings.HasPrefix(text, "ENC[") {
				return "Secret contains an unencrypted value"
			}
		}
	}
	return ""
}

// substitutedOnly is a Secret whose every value is a reference to the backup jobs' upload-only key, nothing else.
func substitutedOnly(secret map[string]any) bool {
	data, _ := secret["stringData"].(map[string]any)
	if truthy(secret["data"]) || len(data) == 0 {
		return false
	}
	for _, value := range data {
		text, _ := value.(string)
		if !slices.ContainsFunc(backupVariables, func(name string) bool { return text == "${"+name+"}" }) {
			return false
		}
	}
	return true
}

func isEncryptedFile(file string) bool {
	return strings.HasSuffix(file, ".sops.yaml") || strings.HasSuffix(file, ".sops.yml")
}

// holdsEncrypted is whether a unit's folder, as its spec.path names it, holds a *.sops.y*ml file at any depth.
func holdsEncrypted(present []string, unitPath string) bool {
	folder, inside := resolve("", unitPath)
	if !inside {
		return false
	}
	return slices.ContainsFunc(present, func(file string) bool {
		return encryptedName.MatchString(baseName(file)) && (folder == "." || under(file, folder))
	})
}

// truthy is a value YAML gave that is set: not null, false, zero or empty.
func truthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case int:
		return v != 0
	case float64:
		return v != 0
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	}
	return true
}

// checkSettings is the platform settings: a platform-settings ConfigMap whose data is strings and sets each required
// setting. The settings are returned only when they can be substituted.
func checkSettings(r Repository) (map[string]string, Result) {
	result := Result{Check: platformSettings}
	settings, problem := readSettings(r)
	if problem != "" {
		result.Problems = []string{problem}
		return nil, result
	}
	for _, key := range requiredSettings {
		if settings[key] == "" {
			result.Problems = append(result.Problems, key+" missing from platform-settings")
		}
	}
	if len(result.Problems) == 0 {
		result.Okay = "platform-settings is strings and sets " + strings.Join(requiredSettings, " and ")
	}
	return settings, result
}

// readSettings is the settings ConfigMap's data, or why it is not one.
func readSettings(r Repository) (map[string]string, string) {
	decoded, held := r.Manifests[settingsFile]
	if !held {
		return nil, settingsFile + ": missing"
	}
	if problem := kubernetesDocuments(settingsFile, decoded); problem != "" {
		return nil, problem
	}
	first := decoded.Documents[0]
	if first.Kind != "ConfigMap" || first.Name != "platform-settings" {
		return nil, settingsFile + ": expected platform-settings ConfigMap"
	}
	data, isMapping := first.Value["data"].(map[string]any)
	if !isMapping {
		return nil, settingsFile + ": settings data must contain strings"
	}
	settings := map[string]string{}
	for key, value := range data {
		text, isText := value.(string)
		if !isText {
			return nil, settingsFile + ": settings data must contain strings"
		}
		settings[key] = text
	}
	return settings, ""
}
