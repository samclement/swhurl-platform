package kube_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/kube/kubetest"
	"github.com/samclement/swhurl-platform/v2/observe"
)

func collectionPorts(t *testing.T, fake bool) (kube.Collector, kube.Data) {
	t.Helper()
	raw := document(t, "lifecycle-events.json")
	var list struct{ Items []json.RawMessage }
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	if fake {
		items := [][]byte{}
		for _, item := range list.Items {
			items = append(items, item)
		}
		f := kubetest.New(kubetest.Answers{})
		f.Collections = map[observe.Ref][][]byte{{APIVersion: "v1", Kind: "Event"}: items}
		f.ConfigMapData = map[string][]kube.ConfigMap{"console": {{Name: "notification-state", Data: map[string]string{"state.json": "{}"}}}}
		return f, f
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/events":
			if r.Method != "GET" || r.URL.Query().Has("labelSelector") {
				t.Error("events used labels or wrong verb")
			}
			items := []json.RawMessage{}
			selector := r.URL.Query().Get("fieldSelector")
			for _, item := range list.Items {
				var e struct{ InvolvedObject struct{ Kind string } }
				_ = json.Unmarshal(item, &e)
				if selector == "" || selector == "involvedObject.kind="+e.InvolvedObject.Kind {
					items = append(items, item)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
		case "/api/v1/namespaces/console/configmaps/notification-state":
			if r.URL.RawQuery != "" || r.Method != "GET" {
				t.Error("state read was not named GET")
			}
			fmt.Fprint(w, `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"notification-state","namespace":"console"},"data":{"state.json":"{}"}}`)
		case "/api/v1/namespaces/console/configmaps/absent":
			w.WriteHeader(404)
		default:
			w.WriteHeader(403) // lists are NOT permitted by the existing Role
		}
	}))
	t.Cleanup(server.Close)
	api, err := kube.NewAPI(server.URL, server.Client(), "")
	if err != nil {
		t.Fatal(err)
	}
	return api, api
}
func TestCheckerCollectionContract(t *testing.T) {
	for _, fake := range []bool{false, true} {
		t.Run(fmt.Sprint(fake), func(t *testing.T) {
			kubetest.CheckerCollectionContract(t, func(t *testing.T) (kube.Collector, kube.Data) { return collectionPorts(t, fake) })
		})
	}
}
func TestNamedGetAndEventFailures(t *testing.T) {
	for _, status := range []int{200, 403, 500, 302} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			fmt.Fprint(w, `{"metadata":{"name":"wrong"}}`)
		}))
		api, _ := kube.NewAPI(server.URL, server.Client(), "")
		if _, err := api.GetConfigMap(context.Background(), "console", "notification-state"); err == nil {
			t.Errorf("%d state read treated as absence", status)
		}
		list, err := api.ListEvents(context.Background(), "HelmRelease")
		if err != nil || list.Status != observe.Unavailable {
			t.Errorf("%d events not unavailable: %+v %v", status, list, err)
		}
		server.Close()
	}
}
func TestEventCollectionBoundsAndCompleteness(t *testing.T) {
	for _, body := range []string{`{"metadata":{"continue":"more"},"items":[]}`, `{"items":null}`, strings.Repeat(" ", 1<<20) + `{"items":[]}`} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		api, _ := kube.NewAPI(server.URL, server.Client(), "")
		list, err := api.ListEvents(context.Background(), "HelmRelease")
		if err != nil || list.Status != observe.Unavailable {
			t.Errorf("incomplete/unbounded events: %+v %v", list, err)
		}
		server.Close()
	}
}
