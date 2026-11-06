package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/samclement/swhurl-platform/v2/observe"
)

// Issuer reads who signs the cluster's ServiceAccount tokens: the issuer its OIDC discovery document names and the
// public keys it publishes. It is a port of its own beside Reader, so only a use case that compares the platform's
// token facts with the cluster's can ask.
type Issuer interface {
	SigningKeys(ctx context.Context) (SigningKeys, error)
}

// SigningKeys is the cluster's token issuer and its JWKS document, as the API server serves it.
type SigningKeys struct {
	Issuer string
	Keys   []byte
}

var _ Issuer = (*API)(nil)

// The two documents the API server publishes for token verifiers: where it is fixed, not discovered.
const (
	discoveryPath = "/.well-known/openid-configuration"
	keysPath      = "/openid/v1/jwks"
	// keysType is the key set's own media type: the API server answers 406 to a request for application/json.
	keysType = "application/jwk-set+json"
)

// SigningKeys reads the discovery document's issuer and the key set. Either document not served, or not the
// document it should be (no issuer, no keys array), is a failed read, never an empty key set.
func (a *API) SigningKeys(ctx context.Context) (SigningKeys, error) {
	discovery := a.get(ctx, discoveryPath, nil)
	if discovery.status != http.StatusOK {
		return SigningKeys{}, &Error{Failed, "issuer", discovery.reason()}
	}
	var document struct {
		Issuer string `json:"issuer"`
	}
	if json.Unmarshal(discovery.body, &document) != nil || document.Issuer == "" {
		return SigningKeys{}, &Error{Failed, "issuer", observe.ReasonMalformed}
	}
	keys := a.getAccepting(ctx, keysPath, nil, maxBytes, keysType)
	if keys.status != http.StatusOK {
		return SigningKeys{}, &Error{Failed, "signing_keys", keys.reason()}
	}
	var set struct {
		Keys *[]json.RawMessage `json:"keys"`
	}
	if json.Unmarshal(keys.body, &set) != nil || set.Keys == nil {
		return SigningKeys{}, &Error{Failed, "signing_keys", observe.ReasonMalformed}
	}
	return SigningKeys{Issuer: document.Issuer, Keys: bytes.Clone(keys.body)}, nil
}
