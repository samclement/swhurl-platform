// Package ntfytest is the fake for ntfy.Publisher and the contract suite both implementations run.
package ntfytest

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/ntfy"
	"github.com/samclement/swhurl-platform/v2/notify"
)

// Scenario is what the contract runs against both implementations: an address whose server accepts a publication,
// one whose server refuses it, and the publication.
type Scenario struct {
	Accepting, Refusing string
	Publication         notify.Publication
}

// Sent is one publication a topic accepted: its address and the document its server was posted.
type Sent struct {
	Address  string
	Document []byte
}

// Fake publishes to the topics it is given and records what each accepted. An address it refuses fails as the
// adapter does, before anything is sent.
type Fake struct {
	Refusing map[string]bool // addresses whose server answers 403
	Fail     error           // returned instead of publishing, after the call is counted
	Attempts int
	Sent     []Sent
}

var _ ntfy.Publisher = (*Fake)(nil)

// New returns a fake whose servers accept every publication.
func New() *Fake { return &Fake{Refusing: map[string]bool{}} }

// Publish records the document an accepting topic is sent, and fails as the adapter does otherwise.
func (f *Fake) Publish(_ context.Context, address string, publication notify.Publication) error {
	f.Attempts++
	topic, ok := addressTopic(address)
	if !ok {
		return &ntfy.Error{Code: ntfy.RefusedRequest, Subject: "address", Reason: "not_an_https_topic"}
	}
	if f.Fail != nil {
		return f.Fail
	}
	if f.Refusing[address] {
		return &ntfy.Error{Code: ntfy.Failed, Subject: "publish", Reason: "status_403"}
	}
	document, err := publication.Document(topic)
	if err != nil {
		return &ntfy.Error{Code: ntfy.RefusedRequest, Subject: "publication", Reason: "unencodable"}
	}
	f.Sent = append(f.Sent, Sent{address, document})
	return nil
}

// addressTopic is an address's topic, if it is an HTTPS URL with a host and a one-segment path, and nothing else.
func addressTopic(address string) (string, bool) {
	parsed, err := url.Parse(address)
	if err != nil {
		return "", false
	}
	topic := strings.Trim(parsed.EscapedPath(), "/")
	whole := parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.Opaque == "" &&
		parsed.RawQuery == "" && !parsed.ForceQuery && parsed.Fragment == ""
	return topic, whole && topic != "" && !strings.Contains(topic, "/")
}

// Received is the documents an address was sent, in order.
func (f *Fake) Received(address string) [][]byte {
	var documents [][]byte
	for _, sent := range f.Sent {
		if sent.Address == address {
			documents = append(documents, bytes.Clone(sent.Document))
		}
	}
	return documents
}

// Contract is the behaviour every ntfy.Publisher must have: an accepting topic's server is posted the publication's
// document naming that topic, a refusing one is a failure, and an address that is not an HTTPS topic is refused
// before anything is sent. No error names the address or its topic. open returns the publisher and what the
// accepting topic has received.
func Contract(t *testing.T, scenario Scenario, open func(*testing.T, Scenario) (ntfy.Publisher, func() [][]byte)) {
	t.Helper()
	publisher, received := open(t, scenario)
	ctx := context.Background()
	if err := publisher.Publish(ctx, scenario.Accepting, scenario.Publication); err != nil {
		t.Fatalf("publishing to the accepting topic: %v", err)
	}
	want, err := scenario.Publication.Document(topicOf(t, scenario.Accepting))
	if got := received(); err != nil || len(got) != 1 || !bytes.Equal(got[0], want) {
		t.Fatalf("the accepting topic received %q, want %s", got, want)
	}
	err = publisher.Publish(ctx, scenario.Refusing, scenario.Publication)
	if !failed(err, ntfy.Failed) || names(err, scenario.Refusing) {
		t.Fatalf("publishing to the refusing topic: %v", err)
	}
	refusesWhatIsNotATopic(t, publisher, scenario.Publication)
	if got := received(); len(got) != 1 {
		t.Fatalf("the accepting topic received %d publications, want 1", len(got))
	}
}

// refusesWhatIsNotATopic: an address that is not an HTTPS topic is refused, and the refusal does not repeat it.
func refusesWhatIsNotATopic(t *testing.T, publisher ntfy.Publisher, publication notify.Publication) {
	t.Helper()
	for _, address := range []string{"http://ntfy.example/secret-9f2e", "https://ntfy.example/", "https://ntfy.example/secret-9f2e/b",
		"https://ntfy.example/secret-9f2e?auth=x", "https://ntfy.example/secret-9f2e#x", "https://user:pass@ntfy.example/secret-9f2e",
		"ntfy.example/secret-9f2e", "https:secret-9f2e", "https://%zz/secret-9f2e"} {
		if err := publisher.Publish(context.Background(), address, publication); !failed(err, ntfy.RefusedRequest) || names(err, address) {
			t.Fatalf("publishing to %q: %v", address, err)
		}
	}
}

// topicOf is the address's topic, as the scenario states it.
func topicOf(t *testing.T, address string) string {
	t.Helper()
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Trim(parsed.Path, "/")
}

// failed is whether err is the adapter's error with this code.
func failed(err error, code string) bool {
	var failure *ntfy.Error
	return errors.As(err, &failure) && failure.Code == code
}

// names is whether an error repeats the address or its topic.
func names(err error, address string) bool {
	parsed, _ := url.Parse(address)
	topic := ""
	if parsed != nil {
		topic = strings.Trim(parsed.Path, "/")
	}
	return strings.Contains(err.Error(), address) || len(topic) > 3 && strings.Contains(err.Error(), topic)
}
