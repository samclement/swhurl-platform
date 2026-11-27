package console

import (
	"slices"
	"strings"
	"sync"
)

// redacted replaces a secret in a job's lines and in its record, as the Python console wrote it. A secret reaches a
// job's output only by accident — a token in a URL, an address in a command's words — and the record outlives the pod.
const redacted = "<redacted>"

// Redactor keeps secrets out of every line a job reports and every record it keeps. The console's tokens are added
// when it starts, and a channel's address when its form is read; a job's own context is never a reason to print one.
// It is safe for concurrent use: jobs run in parallel.
type Redactor struct {
	mu      sync.Mutex
	secrets []string // the longest first, so one secret inside another is replaced whole
}

// Add registers one secret. An empty value is not a secret, and a repeated one is already kept.
func (r *Redactor) Add(secret string) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if slices.Contains(r.secrets, secret) {
		return
	}
	r.secrets = append(r.secrets, secret)
	slices.SortStableFunc(r.secrets, func(a, b string) int { return len(b) - len(a) })
}

// Redact is one text with every registered secret replaced.
func (r *Redactor) Redact(text string) string {
	r.mu.Lock()
	secrets := slices.Clone(r.secrets)
	r.mu.Unlock()
	for _, secret := range secrets {
		text = strings.ReplaceAll(text, secret, redacted)
	}
	return text
}
