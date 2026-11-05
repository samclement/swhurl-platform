package ntfy

import (
	"net/http"
	"testing"
	"time"
)

// A publication is limited to 10 seconds whatever client it is given, and the given client is left as it was.
func TestPublishingIsLimitedToTenSeconds(t *testing.T) {
	given := &http.Client{Timeout: time.Minute}
	for _, client := range []*http.Client{nil, given} {
		if limit := New(client).(*Topics).client.Timeout; limit != 10*time.Second {
			t.Fatalf("the limit is %s", limit)
		}
	}
	if given.Timeout != time.Minute || given.CheckRedirect != nil {
		t.Fatal("the given client was changed")
	}
}
