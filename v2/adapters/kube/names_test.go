package kube_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// namesServer answers every request with one status and body, and keeps the requests.
type namesServer struct {
	status   int
	body     string
	requests []*http.Request
}

func (s *namesServer) RoundTrip(request *http.Request) (*http.Response, error) {
	s.requests = append(s.requests, request)
	return &http.Response{StatusCode: s.status, Body: io.NopCloser(bytes.NewReader([]byte(s.body))),
		Header: http.Header{}, Request: request}, nil
}

func (s *namesServer) api(t *testing.T) *kube.API {
	t.Helper()
	reader, err := kube.New(endpoint, &http.Client{Transport: s}, "operator-token")
	if err != nil {
		t.Fatal(err)
	}
	return reader.(*kube.API)
}

const helmRecords = `{"kind":"PartialObjectMetadataList","apiVersion":"meta.k8s.io/v1","metadata":{},"items":[
	{"metadata":{"name":"sh.helm.release.v1.app.v1","namespace":"apps","labels":{"owner":"helm","name":"app"}}},
	{"metadata":{"name":"sh.helm.release.v1.app.v2","namespace":"apps","labels":{"owner":"helm","name":"app"}}}]}`

// The list asks for metadata alone, by label, in one namespace, and answers the names.
func TestSecretNamesAskForMetadataOnly(t *testing.T) {
	server := &namesServer{status: http.StatusOK, body: helmRecords}
	names, err := server.api(t).SecretNames(context.Background(), "apps", "owner=helm,name=app")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"sh.helm.release.v1.app.v1", "sh.helm.release.v1.app.v2"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("names %v", names)
	}
	request := server.requests[0]
	if got := request.Method + " " + request.URL.RequestURI(); got !=
		"GET /api/v1/namespaces/apps/secrets?labelSelector=owner%3Dhelm%2Cname%3Dapp" {
		t.Fatalf("request %s", got)
	}
	if accept := request.Header.Get("Accept"); accept != "application/json;as=PartialObjectMetadataList;g=meta.k8s.io;v=v1" {
		t.Fatalf("accept %q", accept)
	}
	server.body = `{"kind":"PartialObjectMetadataList","metadata":{},"items":[]}`
	if names, err := server.api(t).SecretNames(context.Background(), "apps", "owner=helm,name=gone"); err != nil || len(names) != 0 {
		t.Fatalf("an empty list: %v, %v", names, err)
	}
}

// Anything but a complete metadata list of this namespace is a failure, never fewer names; a request without a
// selector or with an invalid namespace is refused before it is sent. No error repeats the answer.
func TestSecretNamesFailuresAreNotAbsence(t *testing.T) {
	item := `{"metadata":{"name":"sh.helm.release.v1.app.v1","namespace":"apps"},"data":{"release":"c2VjcmV0"}}`
	for _, answer := range []struct {
		status       int
		body, reason string
	}{
		{http.StatusOK, `{"kind":"SecretList","items":[` + item + `]}`, observe.ReasonMalformed},
		{http.StatusOK, `{"kind":"PartialObjectMetadataList","items":[` + strings.Replace(item, `"apps"`, `"other"`, 1) + `]}`, observe.ReasonMalformed},
		{http.StatusOK, `{"kind":"PartialObjectMetadataList","items":[` + item + `],"metadata":{"continue":"next"}}`, observe.ReasonIncomplete},
		{http.StatusOK, `not json`, observe.ReasonMalformed},
		{http.StatusForbidden, ``, observe.ReasonForbidden},
		{http.StatusNotAcceptable, ``, observe.ReasonStatus},
	} {
		server := &namesServer{status: answer.status, body: answer.body}
		names, err := server.api(t).SecretNames(context.Background(), "apps", "owner=helm,name=app")
		var failure *kube.Error
		if !errors.As(err, &failure) || failure.Code != kube.Failed || failure.Reason != answer.reason || names != nil {
			t.Fatalf("%d %s: %v, %v", answer.status, answer.body, names, err)
		}
		if strings.Contains(err.Error(), "c2VjcmV0") || strings.Contains(err.Error(), "sh.helm") {
			t.Fatalf("the error repeats the answer: %v", err)
		}
	}
	for namespace, labels := range map[string]string{"apps": "", "Not/A/Namespace": "owner=helm"} {
		server := &namesServer{status: http.StatusOK, body: helmRecords}
		var failure *kube.Error
		if _, err := server.api(t).SecretNames(context.Background(), namespace, labels); !errors.As(err, &failure) ||
			failure.Code != kube.RefusedRequest || len(server.requests) != 0 {
			t.Fatalf("%q %q: %v after %d requests", namespace, labels, err, len(server.requests))
		}
	}
}
