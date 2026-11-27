package console_test

import (
	"testing"

	"github.com/samclement/swhurl-platform/v2/console"
)

// Every registered secret is replaced, a secret inside a longer one is replaced with it whole, and blank values are
// not secrets.
func TestTheRedactorReplacesEverySecret(t *testing.T) {
	var redactor console.Redactor
	for _, secret := range []string{"tok", "  ", "", "tok-123", " https://hooks.example/abc \n", "tok"} {
		redactor.Add(secret)
	}
	for _, test := range []struct{ text, want string }{
		{"no secret here", "no secret here"},
		{"fetched with tok-123", "fetched with <redacted>"},
		{"tok and tok-123", "<redacted> and <redacted>"},
		{"posting to https://hooks.example/abc now", "posting to <redacted> now"},
		{"a  b", "a  b"},
	} {
		if got := redactor.Redact(test.text); got != test.want {
			t.Errorf("%q: %q, want %q", test.text, got, test.want)
		}
	}
}
