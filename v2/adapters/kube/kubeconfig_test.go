package kube_test

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
)

// kubeconfig is a synthetic kubeconfig in the JSON form "kubectl config view --raw -o json" prints:
// the material is base64 of a placeholder, because Connect reads encoding, not certificates.
func kubeconfig(t *testing.T, change func(map[string]any)) []byte {
	t.Helper()
	certificate, key := testCertificate(t)
	document := map[string]any{
		"kind":            "Config",
		"apiVersion":      "v1",
		"current-context": "homelab",
		"clusters": []any{map[string]any{"name": "homelab", "cluster": map[string]any{
			"server": "https://api.swhurl.test:6443", "certificate-authority-data": base64.StdEncoding.EncodeToString(certificate),
		}}},
		"users": []any{map[string]any{"name": "operator", "user": map[string]any{
			"client-certificate-data": base64.StdEncoding.EncodeToString(certificate), "client-key-data": base64.StdEncoding.EncodeToString(key),
		}}},
		"contexts": []any{map[string]any{"name": "homelab", "context": map[string]any{"cluster": "homelab", "user": "operator"}}},
	}
	if change != nil {
		change(document)
	}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func encoder(value string) string { return base64.StdEncoding.EncodeToString([]byte(value)) }

// user is the first user's body, for the tests that change its credential material.
func user(document map[string]any) map[string]any {
	return document["users"].([]any)[0].(map[string]any)["user"].(map[string]any)
}

// Connect reads the current context: one endpoint and the material to reach it.
func TestConnectReadsTheCurrentContext(t *testing.T) {
	connection, err := kube.Connect(kubeconfig(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if connection.Endpoint != "https://api.swhurl.test:6443" || connection.Context != "homelab" {
		t.Fatalf("connection %+v", connection)
	}
	if len(connection.Credentials.CA) == 0 || len(connection.Credentials.Certificate) == 0 || len(connection.Credentials.Key) == 0 {
		t.Fatal("the credential material was not decoded")
	}
	if connection.Token != "" {
		t.Fatalf("a token with no token in the kubeconfig: %q", connection.Token)
	}
	if _, err := connection.Reader(); err != nil {
		t.Fatalf("a reader for usable material: %v", err)
	}
}

// A bearer token is an alternative to a client certificate.
func TestConnectAcceptsABearerToken(t *testing.T) {
	connection, err := kube.Connect(kubeconfig(t, func(document map[string]any) {
		user(document)["client-certificate-data"] = ""
		user(document)["client-key-data"] = ""
		user(document)["token"] = "an-opaque-token"
	}))
	if err != nil {
		t.Fatal(err)
	}
	if connection.Token != "an-opaque-token" || len(connection.Credentials.Certificate) != 0 {
		t.Fatalf("connection %+v", connection)
	}
	if _, err := connection.Reader(); err != nil {
		t.Fatal(err)
	}
}

// What Connect cannot use, it refuses: a request that cannot succeed is not evidence about anything.
func TestConnectRefusesWhatItCannotUse(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(map[string]any)
		raw    string
		reason string
	}{
		{"not JSON", nil, "not json", "single_json_object_required"},
		{"no current context", func(document map[string]any) { delete(document, "current-context") }, "", "current_context_required"},
		{"an unknown context", func(document map[string]any) { document["current-context"] = "other" }, "", "current_context_required"},
		{"an unknown cluster", func(document map[string]any) {
			document["contexts"] = []any{map[string]any{"name": "homelab", "context": map[string]any{"cluster": "other", "user": "operator"}}}
		}, "", "cluster_required"},
		{"an unknown user", func(document map[string]any) {
			document["contexts"] = []any{map[string]any{"name": "homelab", "context": map[string]any{"cluster": "homelab", "user": "other"}}}
		}, "", "user_required"},
		{"no server", func(document map[string]any) {
			document["clusters"] = []any{map[string]any{"name": "homelab", "cluster": map[string]any{}}}
		}, "", "server_required"},
		{"a CA that is not base64", func(document map[string]any) {
			document["clusters"].([]any)[0].(map[string]any)["cluster"].(map[string]any)["certificate-authority-data"] = "not base64!"
		}, "", "invalid_ca"},
		{"a certificate that is not base64", func(document map[string]any) {
			user(document)["client-certificate-data"] = "not base64!"
		}, "", "invalid_client_certificate"},
		{"a key that is not base64", func(document map[string]any) {
			user(document)["client-key-data"] = "not base64!"
		}, "", "invalid_client_key"},
		{"a credential plugin", func(document map[string]any) {
			user(document)["exec"] = map[string]any{"command": "some-helper"}
		}, "", "unsupported_credential"},
		{"an auth provider", func(document map[string]any) {
			user(document)["auth-provider"] = map[string]any{"name": "gcp"}
		}, "", "unsupported_credential"},
		{"no credential", func(document map[string]any) {
			user(document)["client-certificate-data"] = ""
			user(document)["client-key-data"] = ""
		}, "", "credential_required"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := []byte(tt.raw)
			if tt.change != nil {
				data = kubeconfig(t, tt.change)
			}
			_, err := kube.Connect(data)
			if !refused(err, tt.reason) {
				t.Fatalf("got %v, want a refusal with reason %s", err, tt.reason)
			}
		})
	}
}

// The endpoint rule is the reader's, not only Connect's: a kubeconfig naming plain http is refused when
// the reader is built, so a token is never sent in the clear.
func TestConnectRefusesPlainHTTP(t *testing.T) {
	connection, err := kube.Connect(kubeconfig(t, func(document map[string]any) {
		document["clusters"].([]any)[0].(map[string]any)["cluster"].(map[string]any)["server"] = "http://api.swhurl.test:6443"
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Reader(); !refused(err, "https_required") {
		t.Fatalf("plain http: %v", err)
	}
}

// A kubeconfig's error never echoes its content.
func TestConnectErrorsCarryNoMaterial(t *testing.T) {
	_, err := kube.Connect(kubeconfig(t, func(document map[string]any) {
		user(document)["client-key-data"] = "c2VjcmV0LW1hdGVyaWFs"
		user(document)["exec"] = map[string]any{"command": "helper"}
	}))
	if err == nil || strings.Contains(err.Error(), "c2VjcmV0") || strings.Contains(err.Error(), "helper") {
		t.Fatalf("an error echoed the kubeconfig: %v", err)
	}
}
