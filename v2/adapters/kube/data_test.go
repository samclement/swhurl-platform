package kube_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/kube/kubetest"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// dataServer answers the TokenReview, ConfigMap and Secret requests of the notify service from the recorded
// documents, as the API server does: a list picks by field or label selector, a merge patch changes one ConfigMap's
// data, and a review vouches only for the recorded token and audience. A request it would refuse — a patch of
// another type or without a field manager — is answered as the API server answers it, so a wrong request fails the
// contract instead of passing it.
type dataServer struct {
	t        *testing.T
	items    map[string][]map[string]any // the namespace's items, by collection
	requests []*http.Request
	bodies   []string
	answer   []byte // when set, every list is answered with these bytes
}

func newDataServer(t *testing.T) *dataServer {
	t.Helper()
	server := &dataServer{t: t, items: map[string][]map[string]any{}}
	for collection, fixture := range map[string]string{"configmaps": "configmaps.json", "secrets": "secrets.json"} {
		var list struct{ Items []map[string]any }
		if err := json.Unmarshal(document(t, fixture), &list); err != nil {
			t.Fatal(err)
		}
		server.items[collection] = list.Items
	}
	return server
}

func (s *dataServer) RoundTrip(request *http.Request) (*http.Response, error) {
	s.requests = append(s.requests, request)
	var body []byte
	if request.Body != nil {
		read, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		body = read
	}
	s.bodies = append(s.bodies, string(body))
	status, answer := s.respond(request, body)
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(answer)),
		Header: http.Header{}, Request: request}, nil
}

func (s *dataServer) respond(request *http.Request, body []byte) (int, []byte) {
	segments := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
	switch {
	case request.Method == http.MethodPost && request.URL.Path == "/apis/authentication.k8s.io/v1/tokenreviews":
		return s.review(request, body)
	case len(segments) == 5 && segments[3] == "notify" && request.Method == http.MethodGet && s.answer != nil:
		return http.StatusOK, s.answer
	case len(segments) == 5 && segments[3] == "notify" && request.Method == http.MethodGet:
		return http.StatusOK, s.list(segments[4], request.URL.Query())
	case len(segments) == 6 && segments[3] == "notify" && segments[4] == "configmaps" && request.Method == http.MethodPatch:
		return s.patch(segments[5], request, body)
	case len(segments) == 6 && segments[3] == "notify" && segments[4] == "secrets" && request.Method == http.MethodGet:
		return s.get("secrets", "Secret", segments[5])
	}
	// The service's role is the notify namespace's: anything else is forbidden.
	return http.StatusForbidden, nil
}

// review answers the recorded review for the recorded token and audience, and the unauthenticated one otherwise.
func (s *dataServer) review(request *http.Request, body []byte) (int, []byte) {
	var asked, recorded struct {
		APIVersion, Kind string
		Spec             struct {
			Token     string   `json:"token"`
			Audiences []string `json:"audiences"`
		} `json:"spec"`
	}
	vouched := document(s.t, "tokenreview.json")
	if request.Header.Get("Content-Type") != "application/json" || json.Unmarshal(body, &asked) != nil ||
		asked.APIVersion != "authentication.k8s.io/v1" || asked.Kind != "TokenReview" {
		return http.StatusBadRequest, nil
	}
	if err := json.Unmarshal(vouched, &recorded); err != nil {
		s.t.Fatal(err)
	}
	if asked.Spec.Token == recorded.Spec.Token && reflect.DeepEqual(asked.Spec.Audiences, recorded.Spec.Audiences) {
		return http.StatusCreated, vouched
	}
	return http.StatusCreated, document(s.t, "tokenreview-unauthenticated.json")
}

// get answers one named item as the API server does, with its apiVersion and kind, or not found.
func (s *dataServer) get(collection, kind, name string) (int, []byte) {
	for _, item := range s.items[collection] {
		if item["metadata"].(map[string]any)["name"] == name {
			encoded, err := json.Marshal(map[string]any{"apiVersion": "v1", "kind": kind, "metadata": item["metadata"], "data": item["data"]})
			if err != nil {
				s.t.Fatal(err)
			}
			return http.StatusOK, encoded
		}
	}
	return http.StatusNotFound, nil
}

// list answers the collection's items the field or label selector picks.
func (s *dataServer) list(collection string, query url.Values) []byte {
	picked := []map[string]any{}
	for _, item := range s.items[collection] {
		metadata := item["metadata"].(map[string]any)
		labels, _ := metadata["labels"].(map[string]any)
		name, byName := strings.CutPrefix(query.Get("fieldSelector"), "metadata.name=")
		if byName && metadata["name"] == name || query.Has("labelSelector") && labelled(query.Get("labelSelector"), labels) {
			picked = append(picked, item)
		}
	}
	encoded, err := json.Marshal(map[string]any{"kind": "List", "apiVersion": "v1", "metadata": map[string]any{}, "items": picked})
	if err != nil {
		s.t.Fatal(err)
	}
	return encoded
}

func labelled(selector string, labels map[string]any) bool {
	for _, term := range strings.Split(selector, ",") {
		key, value, _ := strings.Cut(term, "=")
		if labels[key] != value {
			return false
		}
	}
	return true
}

// patch applies a JSON merge patch of data to one ConfigMap, as a field manager's change.
func (s *dataServer) patch(name string, request *http.Request, body []byte) (int, []byte) {
	if request.Header.Get("Content-Type") != "application/merge-patch+json" {
		return http.StatusUnsupportedMediaType, nil
	}
	var change struct{ Data map[string]string }
	if request.URL.Query().Get("fieldManager") == "" || json.Unmarshal(body, &change) != nil {
		return http.StatusBadRequest, nil
	}
	for _, item := range s.items["configmaps"] {
		if item["metadata"].(map[string]any)["name"] == name {
			data, _ := item["data"].(map[string]any)
			for key, value := range change.Data {
				data[key] = value
			}
			encoded, _ := json.Marshal(item)
			return http.StatusOK, encoded
		}
	}
	return http.StatusNotFound, nil
}

// api is the real adapter over the server, with a bearer token.
func (s *dataServer) api() *kube.API {
	s.t.Helper()
	reader, err := kube.New(endpoint, &http.Client{Transport: s}, "service-token")
	if err != nil {
		s.t.Fatal(err)
	}
	return reader.(*kube.API)
}

// dataAnswers is the data contract's scenario from the recorded lists: the outbox ConfigMap, the ntfy channel
// Secret its transport label selects, and the webhook's Secret, which that label does not.
func dataAnswers(t *testing.T) kubetest.DataAnswers {
	t.Helper()
	return kubetest.DataAnswers{
		Namespace: "notify",
		Labels:    "platform.swhurl.com/notify-transport=ntfy",
		ConfigMap: kube.ConfigMap{Name: "notify-outbox", Labels: map[string]string{
			"kustomize.toolkit.fluxcd.io/name": "platform-notify", "kustomize.toolkit.fluxcd.io/namespace": "flux-system"},
			Data: map[string]string{"state.json": `{"version":1,"seen":{},"pending":[]}`}},
		Secret: kube.Secret{Name: "channel-payments", Labels: map[string]string{
			"kustomize.toolkit.fluxcd.io/name": "platform-notify", "kustomize.toolkit.fluxcd.io/namespace": "flux-system",
			"platform.swhurl.com/notify-transport": "ntfy"},
			Data: map[string][]byte{"address": []byte("https://ntfy.example/private-topic-0123")}},
		Other: "notify-alerts",
	}
}

func TestReviewContract(t *testing.T) {
	answers := kubetest.ReviewAnswers{Token: "checker-token", Audience: "notify",
		Username: "system:serviceaccount:console:notification-check"}
	kubetest.ReviewContract(t, answers, func(t *testing.T, _ kubetest.ReviewAnswers) kube.Reviewer {
		return newDataServer(t).api()
	})
}

func TestDataContract(t *testing.T) {
	kubetest.DataContract(t, dataAnswers(t), func(t *testing.T, _ kubetest.DataAnswers) kube.Data {
		return newDataServer(t).api()
	})
}

// A review is a POST of a TokenReview naming the token and the one audience, with the service's own bearer token.
func TestReviewRequestIsStated(t *testing.T) {
	server := newDataServer(t)
	if _, err := server.api().Review(context.Background(), "checker-token", "notify"); err != nil {
		t.Fatal(err)
	}
	request := server.requests[0]
	if request.Method != http.MethodPost || request.URL.Path != "/apis/authentication.k8s.io/v1/tokenreviews" ||
		request.URL.RawQuery != "" || request.Header.Get("Authorization") != "Bearer service-token" {
		t.Fatalf("%s %s %q", request.Method, request.URL, request.Header.Get("Authorization"))
	}
	want := `{"apiVersion":"authentication.k8s.io/v1","kind":"TokenReview","spec":{"audiences":["notify"],"token":"checker-token"}}`
	if server.bodies[0] != want {
		t.Fatalf("review body %s", server.bodies[0])
	}
}

// A list is a GET of the namespace's collection with exactly one selector, and a key is set by a JSON merge patch
// of that key alone under the field manager.
func TestDataRequestsAreStated(t *testing.T) {
	server := newDataServer(t)
	api := server.api()
	ctx := context.Background()
	if _, err := api.Secrets(ctx, "notify", kube.Selector{Name: "notify-alerts"}); err != nil {
		t.Fatal(err)
	}
	if _, err := api.Secrets(ctx, "notify", kube.Selector{Labels: "platform.swhurl.com/notify-transport=ntfy"}); err != nil {
		t.Fatal(err)
	}
	if err := api.SetConfigMapKey(ctx, "notify", "notify-outbox", "state.json", `{"version":1}`, "swhurl-notify"); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{
		"GET /api/v1/namespaces/notify/secrets?fieldSelector=metadata.name%3Dnotify-alerts",
		"GET /api/v1/namespaces/notify/secrets?labelSelector=platform.swhurl.com%2Fnotify-transport%3Dntfy",
		"PATCH /api/v1/namespaces/notify/configmaps/notify-outbox?fieldManager=swhurl-notify",
	} {
		if got := server.requests[i].Method + " " + server.requests[i].URL.RequestURI(); got != want {
			t.Fatalf("request %d: %s, want %s", i, got, want)
		}
	}
	patch := server.requests[2]
	if patch.Header.Get("Content-Type") != "application/merge-patch+json" || server.bodies[2] != `{"data":{"state.json":"{\"version\":1}"}}` {
		t.Fatalf("patch %q %s", patch.Header.Get("Content-Type"), server.bodies[2])
	}
}

// Several keys are one merge patch: a value sets its key and a nil deletes one, so a writer forgets a record without
// rewriting the keys another pod owns. A key that is not a name is refused before any request.
func TestSeveralConfigMapKeysAreOnePatch(t *testing.T) {
	server := newDataServer(t)
	api := server.api()
	value := "kept"
	if err := api.SetConfigMapKeys(context.Background(), "notify", "notify-outbox", map[string]*string{
		"job-2": &value, "job-1": nil}, "console-jobs"); err != nil {
		t.Fatal(err)
	}
	if len(server.requests) != 1 || server.requests[0].Method != http.MethodPatch {
		t.Fatalf("requests %d", len(server.requests))
	}
	if server.bodies[0] != `{"data":{"job-1":null,"job-2":"kept"}}` {
		t.Fatalf("patch %s", server.bodies[0])
	}
	refused := api.SetConfigMapKeys(context.Background(), "notify", "notify-outbox", map[string]*string{"bad key": &value}, "console-jobs")
	var failure *kube.Error
	if !errors.As(refused, &failure) || failure.Reason != "invalid_name" || len(server.requests) != 1 {
		t.Fatalf("an invalid key: %v after %d requests", refused, len(server.requests))
	}
}

// A list that is not this namespace's complete list, or a Secret whose data is not base64, is a failed read rather
// than fewer objects; so is a status the server refuses with. No error repeats what the server answered.
func TestDataFailuresAreReads(t *testing.T) {
	item := `{"metadata":{"name":"channel-payments","namespace":"notify"},"data":{"address":"aHR0cHM6Ly9udGZ5LmV4YW1wbGUvc2VjcmV0"}}`
	for answer, reason := range map[string]string{
		`{"items":[` + item + `],"metadata":{"continue":"next"}}`:      observe.ReasonIncomplete,
		`{"items":[` + item + `],"metadata":{"remainingItemCount":1}}`: observe.ReasonIncomplete,
		`{"metadata":{}}`: observe.ReasonMalformed,
		`{"items":[` + strings.Replace(item, `"notify"`, `"other"`, 1) + `]}`:                         observe.ReasonMalformed,
		`{"items":[` + item + `,` + item + `]}`:                                                       observe.ReasonMalformed,
		`{"items":[` + strings.Replace(item, "aHR0cHM6Ly9udGZ5LmV4YW1wbGUvc2VjcmV0", "%%%", 1) + `]}`: observe.ReasonMalformed,
	} {
		server := newDataServer(t)
		server.answer = []byte(answer)
		secrets, err := server.api().Secrets(context.Background(), "notify", kube.Selector{Name: "channel-payments"})
		var failure *kube.Error
		if !errors.As(err, &failure) || failure.Code != kube.Failed || failure.Reason != reason || secrets != nil {
			t.Fatalf("%s: %v, %v", answer, secrets, err)
		}
		if strings.Contains(err.Error(), "ntfy") {
			t.Fatalf("the error repeats the answer: %v", err)
		}
	}
	server := newDataServer(t)
	if _, err := server.api().ConfigMaps(context.Background(), "elsewhere", kube.Selector{Name: "a"}); err == nil ||
		err.Error() != "failed: configmaps: forbidden" {
		t.Fatalf("a refused list: %v", err)
	}
}

// A review the server cannot answer is an error that repeats neither the token nor the answer.
func TestReviewFailureNamesNoToken(t *testing.T) {
	server := &writing{status: http.StatusInternalServerError}
	api, err := kube.New(endpoint, &http.Client{Transport: server}, "service-token")
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.(kube.Reviewer).Review(context.Background(), "checker-token", "notify")
	if err == nil || err.Error() != "failed: tokenreview: status" {
		t.Fatalf("a failed review: %v", err)
	}
	server.status = http.StatusCreated
	if _, err := api.(kube.Reviewer).Review(context.Background(), "checker-token", "notify"); err == nil ||
		err.Error() != "failed: tokenreview: malformed" {
		t.Fatalf("a review without a status: %v", err)
	}
}

// Reader still never reads a Secret: there is no reference to one, a Secret named by hand is refused before any
// request, and so is a list of them. Only the Data port reads Secrets.
func TestReaderCannotReadASecret(t *testing.T) {
	if _, err := kube.RefOf("", "Secret", "notify", "notify-alerts"); err == nil {
		t.Fatal("a Secret has a readable reference")
	}
	server := newDataServer(t)
	reader := kube.Reader(server.api())
	secret := observe.Ref{APIVersion: "v1", Kind: "Secret", Namespace: "notify", Name: "notify-alerts"}
	if _, err := reader.Read(context.Background(), secret); !refused(err, "unsupported_kind") {
		t.Fatalf("reading a Secret: %v", err)
	}
	secret.Name = ""
	if _, err := reader.List(context.Background(), secret, "platform.swhurl.com/notify-transport=ntfy"); !refused(err, "unsupported_kind") {
		t.Fatalf("listing Secrets: %v", err)
	}
	secret.Namespace = ""
	if _, err := reader.ListAllNamespaces(context.Background(), secret, "a=b"); !refused(err, "unsupported_kind") {
		t.Fatalf("listing Secrets everywhere: %v", err)
	}
	if len(server.requests) != 0 {
		t.Fatalf("%d requests were made for a Secret", len(server.requests))
	}
}

// An endpoint with a path, as a proxy to the API server has, prefixes every request: a review and a change go
// where a read goes.
func TestRequestsKeepTheEndpointsPath(t *testing.T) {
	server := newDataServer(t)
	api, err := kube.New("https://proxy.swhurl.test/k8s/clusters/home/", &http.Client{Transport: server}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = api.(kube.Reviewer).Review(context.Background(), "checker-token", "notify")
	_ = api.(kube.Data).SetConfigMapKey(context.Background(), "notify", "notify-outbox", "state.json", "{}", "notify")
	_, _ = api.(kube.Data).ConfigMaps(context.Background(), "notify", kube.Selector{Name: "notify-outbox"})
	for i, want := range []string{
		"/k8s/clusters/home/apis/authentication.k8s.io/v1/tokenreviews",
		"/k8s/clusters/home/api/v1/namespaces/notify/configmaps/notify-outbox",
		"/k8s/clusters/home/api/v1/namespaces/notify/configmaps",
	} {
		if got := server.requests[i].URL.Path; got != want {
			t.Fatalf("request %d went to %s, want %s", i, got, want)
		}
	}
}

// The larger ConfigMap answer bound is local to Data, not a relaxation of the existing writer's bound.
func TestConfigMapPatchAndWriterResponseBounds(t *testing.T) {
	job, _ := kube.WritableRefOf("batch", "Job", "notify", "restore")
	pod, _ := kube.RefOf("", "Pod", "notify", "notify")
	for _, operation := range []struct {
		name  string
		limit int
		run   func(*kube.API) error
	}{
		{"configmap key", 4 << 20, func(api *kube.API) error {
			return api.SetConfigMapKey(context.Background(), "notify", "notify-outbox", "state.json", "{}", "notify")
		}},
		{"patch", 1 << 20, func(api *kube.API) error { return api.Patch(context.Background(), job, []byte(`{}`)) }},
		{"create", 1 << 20, func(api *kube.API) error { return api.Create(context.Background(), job, []byte(`{}`)) }},
		{"delete", 1 << 20, func(api *kube.API) error { return api.Delete(context.Background(), job) }},
		{"logs", 1 << 20, func(api *kube.API) error { _, err := api.Logs(context.Background(), pod, 10); return err }},
	} {
		t.Run(operation.name, func(t *testing.T) {
			for _, oversized := range []bool{false, true} {
				size := operation.limit
				if oversized {
					size++
				}
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					_, _ = io.WriteString(w, `"`+strings.Repeat("x", size-2)+`"`)
				}))
				api, err := kube.NewAPI(server.URL, server.Client(), "")
				if err != nil {
					t.Fatal(err)
				}
				err = operation.run(api)
				server.Close()
				var failure *kube.Error
				if oversized {
					if !errors.As(err, &failure) || failure.Reason != observe.ReasonTooLarge {
						t.Fatalf("%d-byte answer: %v", size, err)
					}
				} else if err != nil {
					t.Fatalf("%d-byte answer: %v", size, err)
				}
			}
		})
	}
}

// A Secret by name is a GET of that one object, never a list: the dashboard job's Roles allow get alone. Every
// answer other than the object or a 404 is a failure, and a value that is not base64 is a malformed read; no error
// carries the answer.
func TestSecretGetIsNamedAndFailsClosed(t *testing.T) {
	server := newDataServer(t)
	secret, err := server.api().GetSecret(context.Background(), "notify", "notify-alerts")
	if err != nil || secret == nil || len(server.requests) != 1 {
		t.Fatalf("named get: %v", err)
	}
	if request := server.requests[0]; request.Method != http.MethodGet ||
		request.URL.Path != "/api/v1/namespaces/notify/secrets/notify-alerts" || request.URL.RawQuery != "" {
		t.Fatalf("%s %s", request.Method, request.URL)
	}
	for status, body := range map[int]string{
		http.StatusForbidden:  `{"kind":"Status","message":"private-answer"}`,
		http.StatusOK:         `{"apiVersion":"v1","kind":"Secret","metadata":{"name":"notify-alerts","namespace":"notify"},"data":{"secret":"not-base64!"}}`,
		http.StatusFound:      ``,
		http.StatusBadGateway: `private-answer`,
	} {
		answered := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
		}))
		api, _ := kube.NewAPI(answered.URL, answered.Client(), "")
		found, err := api.GetSecret(context.Background(), "notify", "notify-alerts")
		answered.Close()
		if err == nil || found != nil || strings.Contains(err.Error(), "private-answer") || strings.Contains(err.Error(), "not-base64") {
			t.Errorf("status %d: %+v, %v", status, found, err)
		}
	}
	other := `{"apiVersion":"v1","kind":"Secret","metadata":{"name":"other","namespace":"notify"},"data":{}}`
	answered := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, other) }))
	defer answered.Close()
	api, _ := kube.NewAPI(answered.URL, answered.Client(), "")
	if found, err := api.GetSecret(context.Background(), "notify", "notify-alerts"); err == nil || found != nil {
		t.Fatalf("another object answered as the named one: %+v, %v", found, err)
	}
}
