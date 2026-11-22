package observe_test

import (
	"encoding/json"
	"testing"

	"github.com/samclement/swhurl-platform/v2/observe"
)

// State is supplied by apps, not rejudged by Explain or the encoder. In particular a healthy rollback must
// retain its distinct state and cannot become promotion-ready merely because the workload is healthy.
func TestReportPreservesAssignedState(t *testing.T) {
	for _, name := range []string{observe.StateHealthy, observe.StateRolledBack, observe.StateStalled, observe.StateUpdating,
		observe.StateFailing, observe.StateSuspended, observe.StateStopped, observe.StateUnknown} {
		t.Run(name, func(t *testing.T) {
			report := explain(t, healthy(t))
			report.State = observe.State{Name: name, Field: "deployment", Reason: "delivery verdict"}
			encoded, err := observe.EncodeReport(report)
			if err != nil {
				t.Fatal(err)
			}
			var document struct {
				Version int
				State   observe.State
			}
			if err := json.Unmarshal(encoded, &document); err != nil {
				t.Fatal(err)
			}
			if document.Version != 6 || document.State != report.State {
				t.Fatalf("%+v", document)
			}
		})
	}
}

// containerPod is a pod whose main container runs the intended image and has the given ready flag and state.
func containerPod(t *testing.T, ready bool, state map[string]any) []byte {
	t.Helper()
	image := "docker.io/nginxinc/nginx-unprivileged@" + digest
	return encode(t, map[string]any{
		"metadata": map[string]any{"name": "hello-main-7c8f9d6b5-x2k4l"},
		"spec":     map[string]any{"containers": []any{map[string]any{"name": "main", "image": image}}},
		"status": map[string]any{"containerStatuses": []any{map[string]any{"name": "main",
			"imageID": "docker-pullable://" + image, "ready": ready, "state": state}}},
	})
}
