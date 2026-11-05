package kernel

import (
	"errors"
	"os"
	"slices"
	"testing"
)

const namesCatalogue = `{"kind": "platform-names", "version": 1, "names": [
  {"name": "SWHURL_APP", "surface": "env", "meaning": "App name", "availability": "Every instance"},
  {"name": "SWHURL_SERVICE_<APP>_URL", "surface": "env", "meaning": "A callee's address", "availability": "Callers"},
  {"name": "platform.swhurl.com/team", "surface": "label", "meaning": "Owning team", "availability": "Every instance"}
]}`

func names(t *testing.T, data string) NameCatalogue {
	t.Helper()
	catalogue, err := DecodeNameCatalogue([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	return catalogue
}

func TestUnlistedNamesAreReportedOnceInOrderOfUse(t *testing.T) {
	catalogue := names(t, namesCatalogue)
	text := "SWHURL_APP SWHURL_UNLISTED platform.swhurl.com/team platform.swhurl.com/other SWHURL_UNLISTED SWHURL_APP"
	if got, want := catalogue.Unlisted([]byte(text)), []string{"SWHURL_UNLISTED", "platform.swhurl.com/other"}; !slices.Equal(got, want) {
		t.Fatalf("unlisted = %q, want %q", got, want)
	}
	if got := catalogue.Unlisted([]byte("no platform names here, nor SWHURLX or platform.swhurl.org/x")); got != nil {
		t.Fatalf("unlisted = %q, want none", got)
	}
}

// A family lists its members and its prefix alone, which a reader completes at run time, and nothing else.
func TestAFamilyListsItsMembersAndItsPrefix(t *testing.T) {
	catalogue := names(t, namesCatalogue)
	for _, listed := range []string{"SWHURL_SERVICE_ORDERS_URL", "SWHURL_SERVICE_ORDERS_API_URL", "SWHURL_SERVICE_",
		"SWHURL_SERVICE_<APP>_URL"} {
		if got := catalogue.Unlisted([]byte(listed)); got != nil {
			t.Errorf("%s: unlisted = %q, want listed", listed, got)
		}
	}
	for _, unlisted := range []string{"SWHURL_SERVICE__URL", "SWHURL_SERVICE_ORDERS", "SWHURL_SERVICES"} {
		if got := catalogue.Unlisted([]byte(unlisted)); !slices.Equal(got, []string{unlisted}) {
			t.Errorf("%s: unlisted = %q, want it", unlisted, got)
		}
	}
}

func TestAMalformedCatalogueIsRefused(t *testing.T) {
	for name, data := range map[string]string{
		"not JSON":      `{`,
		"unknown field": `{"kind": "platform-names", "version": 1, "names": [], "extra": 1}`,
		"wrong kind":    `{"kind": "platform-stacks", "version": 1, "names": []}`,
		"wrong version": `{"kind": "platform-names", "version": 2, "names": []}`,
		"empty field":   `{"kind": "platform-names", "version": 1, "names": [{"name": "SWHURL_APP", "surface": "env", "meaning": "", "availability": "x"}]}`,
		"entry field":   `{"kind": "platform-names", "version": 1, "names": [{"name": "SWHURL_APP", "surface": "env", "meaning": "m", "availability": "x", "note": "n"}]}`,
		"twice": `{"kind": "platform-names", "version": 1, "names": [{"name": "SWHURL_APP", "surface": "env", "meaning": "m", "availability": "x"},
			{"name": "SWHURL_APP", "surface": "env", "meaning": "m", "availability": "x"}]}`,
		"other case":    `{"kind": "platform-names", "version": 1, "names": [{"NAME": "SWHURL_APP", "surface": "env", "meaning": "m", "availability": "x"}]}`,
		"null":          `{"kind": "platform-names", "version": 1, "names": null}`,
		"wrong type":    `{"kind": "platform-names", "version": 1, "names": [{"name": 1, "surface": "env", "meaning": "m", "availability": "x"}]}`,
		"two parts":     `{"kind": "platform-names", "version": 1, "names": [{"name": "SWHURL_<A>_<B>", "surface": "env", "meaning": "m", "availability": "x"}]}`,
		"unclosed part": `{"kind": "platform-names", "version": 1, "names": [{"name": "SWHURL_<A", "surface": "env", "meaning": "m", "availability": "x"}]}`,
		"no prefix":     `{"kind": "platform-names", "version": 1, "names": [{"name": "<A>_URL", "surface": "env", "meaning": "m", "availability": "x"}]}`,
	} {
		var refusal *Error
		if _, err := DecodeNameCatalogue([]byte(data)); !errors.As(err, &refusal) {
			t.Errorf("%s: err = %v, want a refusal", name, err)
		}
	}
}

// The platform's own catalogue decodes, and lists what every app reads.
func TestThePlatformCatalogueDecodes(t *testing.T) {
	data, err := os.ReadFile("../contract/names.json")
	if err != nil {
		t.Fatal(err)
	}
	catalogue := names(t, string(data))
	read := "SWHURL_APP SWHURL_ENV SWHURL_SERVICE_ORDERS_URL platform.swhurl.com/team platform.swhurl.com/managed"
	if got := catalogue.Unlisted([]byte(read)); got != nil {
		t.Fatalf("unlisted = %q, want none", got)
	}
}
