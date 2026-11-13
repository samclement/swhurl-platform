package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/samclement/swhurl-platform/v2/observe"
)

// SecretNamer lists which Secrets exist, never what they hold: the question Helm's record of a release answers. It is
// a port of its own, beside Reader (which never reads a Secret) and Data (which reads a service's own values), so a
// use case that only needs to know a release still exists holds no reader of any value.
type SecretNamer interface {
	// SecretNames lists the names of the Secrets a label selector picks in one namespace. A failed or partial list
	// is an error, never fewer names.
	SecretNames(ctx context.Context, namespace, labels string) ([]string, error)
}

var _ SecretNamer = (*API)(nil)

// metadataOnly asks the API server for each object's metadata alone, so no Secret's data is sent.
const metadataOnly = "application/json;as=PartialObjectMetadataList;g=meta.k8s.io;v=v1"

// SecretNames lists the names the selector picks. The answer must be a metadata list: any other document is
// malformed, so a server that ignored the media type cannot hand this adapter a Secret's values to pass on.
func (a *API) SecretNames(ctx context.Context, namespace, labels string) ([]string, error) {
	if labels == "" {
		return nil, &Error{RefusedRequest, "selector", "required"}
	}
	if !namePattern.MatchString(namespace) {
		return nil, &Error{RefusedRequest, "secrets", "invalid_namespace"}
	}
	answer := a.getAccepting(ctx, "/api/v1/namespaces/"+url.PathEscape(namespace)+"/secrets",
		url.Values{"labelSelector": {labels}}, maxBytes, metadataOnly)
	if answer.status != http.StatusOK {
		return nil, &Error{Failed, "secrets", answer.reason()}
	}
	var kind struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(bytes.TrimSpace(answer.body), &kind) != nil || kind.Kind != "PartialObjectMetadataList" {
		return nil, &Error{Failed, "secrets", observe.ReasonMalformed}
	}
	items, reason := dataItems(answer.body, namespace)
	if reason != "" {
		return nil, &Error{Failed, "secrets", reason}
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Metadata.Name)
	}
	return names, nil
}

// SecretNamer lists Secrets' names on the same API server.
func (c Connection) SecretNamer() (SecretNamer, error) {
	api, err := c.api()
	if err != nil {
		return nil, err
	}
	return api, nil
}
