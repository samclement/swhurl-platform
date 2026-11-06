package kube_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/kube/kubetest"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// issuerServer answers the two recorded token-verifier documents by the paths the API server serves them at.
func issuerServer(t *testing.T) *apiServer {
	t.Helper()
	return &apiServer{documents: map[string][]byte{
		".well-known/openid-configuration": document(t, "openid-configuration.json"),
		"v1/jwks":                          document(t, "jwks.json"),
	}}
}

func issuerOf(t *testing.T, server *apiServer) kube.Issuer {
	t.Helper()
	api, err := kube.NewAPI(endpoint, &http.Client{Transport: server}, "")
	if err != nil {
		t.Fatal(err)
	}
	return api
}

func TestIssuerContract(t *testing.T) {
	want := kube.SigningKeys{Issuer: "https://kubernetes.default.svc.cluster.local", Keys: document(t, "jwks.json")}
	kubetest.IssuerContract(t, want, func(t *testing.T, _ kube.SigningKeys) kube.Issuer {
		return issuerOf(t, issuerServer(t))
	})
}

// The issuer and keys are two GETs of the documents the API server publishes for token verifiers, at fixed paths.
func TestSigningKeysAreTheTwoPublishedDocuments(t *testing.T) {
	server := issuerServer(t)
	if _, err := issuerOf(t, server).SigningKeys(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(server.requests) != 2 || server.requests[0].URL.Path != "/.well-known/openid-configuration" ||
		server.requests[1].URL.Path != "/openid/v1/jwks" || server.requests[0].Method != http.MethodGet {
		t.Fatalf("%v", server.requests)
	}
	// The API server answers 406 to a key set asked for as application/json: it serves it as its own type.
	if accept := server.requests[1].Header.Get("Accept"); accept != "application/jwk-set+json" {
		t.Fatalf("the key set was asked for as %q", accept)
	}
}

// A document not served, or not the document it should be, is a failed read: never an issuer or key set of nothing.
func TestSigningKeysFailures(t *testing.T) {
	for name, test := range map[string]struct {
		server  *apiServer
		subject string
		reason  string
	}{
		"forbidden":    {&apiServer{status: http.StatusForbidden}, "issuer", observe.ReasonForbidden},
		"no issuer":    {&apiServer{body: []byte(`{"keys":[]}`)}, "issuer", observe.ReasonMalformed},
		"keys missing": {&apiServer{documents: map[string][]byte{".well-known/openid-configuration": []byte(`{"issuer":"https://k"}`)}}, "signing_keys", observe.ReasonStatus},
		"no key array": {&apiServer{documents: map[string][]byte{".well-known/openid-configuration": []byte(`{"issuer":"https://k"}`),
			"v1/jwks": []byte(`{"keys":null}`)}}, "signing_keys", observe.ReasonMalformed},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := issuerOf(t, test.server).SigningKeys(context.Background())
			var failure *kube.Error
			if !errors.As(err, &failure) || failure.Code != kube.Failed || failure.Subject != test.subject || failure.Reason != test.reason {
				t.Fatalf("%v", err)
			}
		})
	}
}
