package kube

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/samclement/swhurl-platform/v2/observe"
)

// Writer changes one object at a time and reads one pod's log. It is a separate interface from Reader so that
// a use case which only observes cannot reach it: this is the first effect v2 performs on a cluster.
type Writer interface {
	// Patch applies a JSON merge patch to one object: the shape the platform uses to suspend a release or
	// set a replica count.
	Patch(ctx context.Context, ref observe.Ref, patch []byte) error
	// Create creates one object from its document.
	Create(ctx context.Context, ref observe.Ref, document []byte) error
	// Delete removes one object. An object that is already gone is not an error.
	Delete(ctx context.Context, ref observe.Ref) error
	// Logs reads the last lines of one pod's log: the evidence a restore job leaves about what it did.
	Logs(ctx context.Context, ref observe.Ref, tailLines int64) (string, error)
}

var _ Writer = (*API)(nil)

// writableKinds is the table of objects v2 may create or delete. A Secret is writable here and still never
// readable through Reader: `kinds` refuses it, so this table is not an invitation to read one.
func writableKinds() map[pathKey]resource {
	return map[pathKey]resource{
		{"", "Secret"}: {"v1", "secrets", true},
	}
}

// WritableRefOf is the reference to an object v2 may create or delete, from the stated table. It refuses a
// kind outside that table, so a caller cannot invent one.
func WritableRefOf(group, kind, namespace, name string) (observe.Ref, error) {
	found, ok := writableKinds()[pathKey{group, kind}]
	if !ok {
		return observe.Ref{}, &Error{RefusedRequest, "resource", "unsupported_kind"}
	}
	if !namePattern.MatchString(namespace) || !namePattern.MatchString(name) {
		return observe.Ref{}, &Error{RefusedRequest, "resource", "invalid_name"}
	}
	return observe.Ref{APIVersion: found.apiVersion, Kind: kind, Namespace: namespace, Name: name}, nil
}

// Patch applies a merge patch to one object.
func (a *API) Patch(ctx context.Context, ref observe.Ref, patch []byte) error {
	return a.write(ctx, http.MethodPatch, ref, patch, "application/merge-patch+json")
}

// Create creates one object: a create posts to its collection.
func (a *API) Create(ctx context.Context, ref observe.Ref, document []byte) error {
	return a.write(ctx, http.MethodPost, ref, document, "application/json")
}

// Delete removes one object.
func (a *API) Delete(ctx context.Context, ref observe.Ref) error {
	return a.write(ctx, http.MethodDelete, ref, nil, "")
}

// Logs reads a pod's log tail.
func (a *API) Logs(ctx context.Context, ref observe.Ref, tailLines int64) (string, error) {
	if ref.Kind != "Pod" || ref.Name == "" {
		return "", &Error{RefusedRequest, "resource", "pod_required"}
	}
	resource, err := resourceOf(ref)
	if err != nil {
		return "", err
	}
	body, failure := a.exchange(ctx, http.MethodGet, resource.objectPath(ref)+"/log",
		url.Values{"tailLines": {strconv.FormatInt(tailLines, 10)}}, nil, "text/plain")
	if failure != nil {
		return "", failure
	}
	return string(body), nil
}

// write sends one change and maps its answer: a 2xx is done, an already-absent delete is done, and anything
// else is a fixed refusal that names no content.
func (a *API) write(ctx context.Context, method string, ref observe.Ref, body []byte, contentType string) error {
	resource, err := a.writableResourceOf(ref, method)
	if err != nil {
		return err
	}
	path := resource.objectPath(ref)
	if method == http.MethodPost {
		path = resource.listPath(ref)
	}
	_, failure := a.exchange(ctx, method, path, nil, body, contentType)
	if failure != nil {
		if method == http.MethodDelete && failure.Reason == observe.ReasonNotFound {
			return nil
		}
		return failure
	}
	return nil
}

// writableResourceOf is the resource a write names: a kind the reader knows, or one only the writer may
// create; and a name whenever the write is not a create.
func (a *API) writableResourceOf(ref observe.Ref, method string) (resource, error) {
	resource, err := resourceOf(ref)
	if err != nil {
		writable, ok := writableKinds()[pathKey{groupOf(ref.APIVersion), ref.Kind}]
		if !ok {
			return resource, err
		}
		resource = writable
	}
	if method != http.MethodPost && ref.Name == "" {
		return resource, &Error{RefusedRequest, "resource", "name_required"}
	}
	return resource, nil
}

// exchange performs one request and returns the answer's body, or a fixed failure. Status meaning is the
// writer's, not a reader's: a write has no absence.
func (a *API) exchange(ctx context.Context, method, path string, query url.Values, body []byte,
	contentType string) ([]byte, *Error) {
	address := *a.base
	address.Path = path
	address.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, method, address.String(), bytes.NewReader(body))
	if err != nil {
		return nil, &Error{RefusedRequest, "resource", "invalid_path"}
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if a.token != "" {
		request.Header.Set("Authorization", "Bearer "+a.token)
	}
	response, err := a.client.Do(request)
	if err != nil {
		return nil, &Error{Failed, "write", failureReason(err)}
	}
	defer response.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, &Error{Failed, "write", failureReason(err)}
	}
	if len(answer) > maxBytes {
		return nil, &Error{Failed, "write", observe.ReasonTooLarge}
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return answer, nil
	}
	return nil, &Error{Failed, "write", statusReason(response.StatusCode)}
}

// statusReason names a write the server refused.
func statusReason(status int) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return observe.ReasonForbidden
	case http.StatusNotFound:
		return observe.ReasonNotFound
	}
	return observe.ReasonStatus
}

// groupOf is an API version's group, empty for the core API.
func groupOf(apiVersion string) string {
	group, _, hasGroup := strings.Cut(apiVersion, "/")
	if !hasGroup {
		return ""
	}
	return group
}
