package otlp

import (
	"net/http"
	"testing"
	"time"
)

// An export is limited to 10 seconds whatever client it is given, and the given client is left as it was.
func TestExportsAreLimitedToTenSeconds(t *testing.T) {
	given := &http.Client{Timeout: time.Minute}
	for _, client := range []*http.Client{nil, given} {
		collector, err := New(client, "http://collector:4318")
		if err != nil || collector.client.Timeout != 10*time.Second {
			t.Fatalf("%v %v", collector, err)
		}
	}
	if given.Timeout != time.Minute || given.CheckRedirect != nil {
		t.Fatal("the given client was changed")
	}
}
