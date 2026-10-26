// Package kube reads the few Kubernetes objects v2 observes, through the API server.
//
// It starts no process. Each kind's API group and version is stated in this package and checked in its
// tests against an API document for that kind, never discovered at run time. A request either returns
// the object's bytes or reports that the object is absent (a successful 404) or that it could not be
// read (forbidden, timeout, transport failure, an answer that is not the object the reference names, a
// redirect); a failed read is never absence.
//
// Credentials belong to the caller: TLS material (a CA bundle, a client certificate) to the client,
// and a bearer token, when one is used, to New. Nothing here resolves a kubeconfig or a service
// account token, and nothing here writes.
package kube

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/samclement/swhurl-platform/v2/observe"
)

// Reader is the port observation depends on. Neither method turns a failed request into absence.
type Reader interface {
	Read(ctx context.Context, ref observe.Ref) (observe.Read, error)
	List(ctx context.Context, ref observe.Ref, selector string) (observe.List, error)
}

// maxBytes bounds one response body, so a wrong endpoint or a wrong kind fails as a read instead of
// consuming memory without limit.
const maxBytes = 1 << 20

// The two error codes: a request this adapter refuses to make (no evidence was attempted), and
// credential material it cannot use.
const (
	RefusedRequest = "refused_request"
	// Failed is a change the server refused or could not answer: the writer's counterpart to unavailable
	// evidence.
	Failed         = "failed"
	BadCredentials = "credentials"
)

// Error names a code, what it is about and a fixed reason. It never carries a response body, a
// token or an object's content.
type Error struct{ Code, Subject, Reason string }

func (e *Error) Error() string { return fmt.Sprintf("%s: %s: %s", e.Code, e.Subject, e.Reason) }

// Credentials is the TLS material for one API server. An empty CA uses the system roots; a client
// certificate is the alternative to a bearer token.
type Credentials struct {
	CA          []byte
	Certificate []byte
	Key         []byte
}

// Client builds the TLS client these credentials describe, refusing material it cannot use.
func Client(credentials Credentials) (*http.Client, error) {
	configuration := &tls.Config{MinVersion: tls.VersionTLS12}
	if len(credentials.CA) > 0 {
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(credentials.CA) {
			return nil, &Error{BadCredentials, "ca", "no certificate found"}
		}
		configuration.RootCAs = roots
	}
	if len(credentials.Certificate) > 0 || len(credentials.Key) > 0 {
		pair, err := tls.X509KeyPair(credentials.Certificate, credentials.Key)
		if err != nil {
			return nil, &Error{BadCredentials, "client_certificate", "unusable key pair"}
		}
		configuration.Certificates = []tls.Certificate{pair}
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: configuration}}, nil
}

// New reads through one API server endpoint with the given client, which carries the TLS material.
// A non-empty token is sent with every request as a bearer token. The endpoint must be https, so a
// token is never sent in the clear; an endpoint without one is refused rather than downgraded.
func New(endpoint string, client *http.Client, token string) (Reader, error) {
	base, err := url.Parse(endpoint)
	if err != nil || base.Host == "" {
		return nil, &Error{RefusedRequest, "endpoint", "not_a_url"}
	}
	if base.Scheme != "https" {
		return nil, &Error{RefusedRequest, "endpoint", "https_required"}
	}
	if client == nil {
		return nil, &Error{RefusedRequest, "client", "required"}
	}
	// A redirect is neither evidence about the object nor a place to send the token: return the 3xx as
	// the answer instead of following it, so a response can only come from the endpoint asked.
	directed := *client
	directed.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &API{base: base, client: &directed, token: token}, nil
}

// API is the Reader over one API server.
type API struct {
	base   *url.URL
	client *http.Client
	token  string
}

// Read reads one object.
func (a *API) Read(ctx context.Context, ref observe.Ref) (observe.Read, error) {
	if ref.Name == "" {
		return observe.Read{}, &Error{RefusedRequest, "resource", "name_required"}
	}
	resource, err := resourceOf(ref)
	if err != nil {
		return observe.Read{}, err
	}
	answer := a.get(ctx, resource.objectPath(ref), nil)
	if answer.status == http.StatusOK {
		if !identifies(ref, answer.body) {
			return failedRead(ref, observe.ReasonMalformed), nil
		}
		return observe.Read{Ref: ref, Status: observe.Present, Object: answer.body}, nil
	}
	if answer.status == http.StatusNotFound {
		return observe.Read{Ref: ref, Status: observe.Absent, Reason: observe.ReasonNotFound}, nil
	}
	return failedRead(ref, answer.reason()), nil
}

// List reads one label-selected collection. An empty successful list is present with no items; a
// collection read never reports absence, because absence is a fact about one object.
func (a *API) List(ctx context.Context, ref observe.Ref, selector string) (observe.List, error) {
	if selector == "" {
		return observe.List{}, &Error{RefusedRequest, "selector", "required"}
	}
	resource, err := resourceOf(ref)
	if err != nil {
		return observe.List{}, err
	}
	answer := a.get(ctx, resource.listPath(ref), url.Values{"labelSelector": {selector}})
	list := observe.List{Ref: ref, Selector: selector}
	if answer.status != http.StatusOK {
		list.Status, list.Reason = observe.Unavailable, answer.reason()
		return list, nil
	}
	items, ok := itemsOf(answer.body)
	if !ok {
		list.Status, list.Reason = observe.Unavailable, observe.ReasonMalformed
		return list, nil
	}
	list.Status, list.Items = observe.Present, items
	return list, nil
}

// failedRead is evidence that the request did not produce the object.
func failedRead(ref observe.Ref, reason string) observe.Read {
	return observe.Read{Ref: ref, Status: observe.Unavailable, Reason: reason}
}

// answer is what the server said, or why nothing usable was heard.
type answer struct {
	status int // 0 when no response arrived
	body   []byte
	why    string // set when the request failed before an answer
}

// reason names why an answer carried no usable object.
func (a answer) reason() string {
	if a.why != "" {
		return a.why
	}
	switch a.status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return observe.ReasonForbidden
	}
	return observe.ReasonStatus
}

// get performs one GET. Only the server's answer, or the failure to get one, is reported: this
// adapter makes no decision about what the answer means.
func (a *API) get(ctx context.Context, path string, query url.Values) answer {
	address := *a.base
	address.Path = strings.TrimSuffix(address.Path, "/") + path
	address.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address.String(), nil)
	if err != nil {
		return answer{why: observe.ReasonTransport}
	}
	request.Header.Set("Accept", "application/json")
	if a.token != "" {
		request.Header.Set("Authorization", "Bearer "+a.token)
	}
	response, err := a.client.Do(request)
	if err != nil {
		return answer{why: failureReason(err)}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return answer{why: failureReason(err)}
	}
	if len(body) > maxBytes {
		return answer{why: observe.ReasonTooLarge}
	}
	return answer{status: response.StatusCode, body: body}
}

// failureReason names a request that never reached an answer. A cancelled context is a failure of the
// read, not evidence about the object.
func failureReason(err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || os.IsTimeout(err) {
		return observe.ReasonTimedOut
	}
	return observe.ReasonTransport
}

// identifies reports whether a document is the object the reference names. A 200 that is another
// object, another kind, or no object at all (a proxy page, a Status document, `null`) is not evidence
// about this one.
func identifies(ref observe.Ref, body []byte) bool {
	var document struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
	}
	if json.Unmarshal(body, &document) != nil {
		return false
	}
	return document.APIVersion == ref.APIVersion && document.Kind == ref.Kind &&
		document.Metadata.Name == ref.Name && document.Metadata.Namespace == ref.Namespace
}

// itemsOf reads a list document's items, as owned copies. A body without an items array is not a list,
// so it is refused rather than read as an empty one.
func itemsOf(body []byte) ([][]byte, bool) {
	var document struct {
		Items *[]json.RawMessage `json:"items"`
	}
	if json.Unmarshal(body, &document) != nil || document.Items == nil {
		return nil, false
	}
	items := make([][]byte, 0, len(*document.Items))
	for _, item := range *document.Items {
		items = append(items, bytes.Clone(item))
	}
	return items, true
}

// resource is where one supported kind lives: its API version, its collection, and whether it lives in
// a namespace. Every kind is stated, never discovered.
type resource struct {
	apiVersion string
	collection string
	namespaced bool
}

// pathKey is the pair a kind is stated by: its group (empty for the core API) and its kind.
type pathKey struct{ group, kind string }

// kinds is the whole table this adapter reads. A kind outside it is a refused request, not a discovery.
func kinds() map[pathKey]resource {
	return map[pathKey]resource{
		{"", "Namespace"}:                                {"v1", "namespaces", false},
		{"", "Pod"}:                                      {"v1", "pods", true},
		{"", "ResourceQuota"}:                            {"v1", "resourcequotas", true},
		{"apps", "Deployment"}:                           {"apps/v1", "deployments", true},
		{"batch", "CronJob"}:                             {"batch/v1", "cronjobs", true},
		{"batch", "Job"}:                                 {"batch/v1", "jobs", true},
		{"networking.k8s.io", "NetworkPolicy"}:           {"networking.k8s.io/v1", "networkpolicies", true},
		{"networking.k8s.io", "Ingress"}:                 {"networking.k8s.io/v1", "ingresses", true},
		{"helm.toolkit.fluxcd.io", "HelmRelease"}:        {"helm.toolkit.fluxcd.io/v2", "helmreleases", true},
		{"kustomize.toolkit.fluxcd.io", "Kustomization"}: {"kustomize.toolkit.fluxcd.io/v1", "kustomizations", true},
	}
}

// RefOf is the reference to one object a caller knows by group, kind, namespace and name, as the compiler
// states those. It refuses a kind this adapter does not read, so a caller cannot guess a version.
func RefOf(group, kind, namespace, name string) (observe.Ref, error) {
	found, ok := kinds()[pathKey{group, kind}]
	if !ok {
		return observe.Ref{}, &Error{RefusedRequest, "resource", "unsupported_kind"}
	}
	return observe.Ref{APIVersion: found.apiVersion, Kind: kind, Namespace: namespace, Name: name}, nil
}

// resourceOf states the API group, version, collection and scope of one kind. An unsupported kind is a
// refused request, not a discovery.
func resourceOf(ref observe.Ref) (resource, error) {
	group := ""
	if before, _, hasGroup := strings.Cut(ref.APIVersion, "/"); hasGroup {
		group = before
	}
	found, ok := kinds()[pathKey{group, ref.Kind}]
	if !ok || found.apiVersion != ref.APIVersion {
		return resource{}, &Error{RefusedRequest, "resource", "unsupported_kind"}
	}
	if found.namespaced && !namePattern.MatchString(ref.Namespace) {
		return resource{}, &Error{RefusedRequest, "resource", "invalid_namespace"}
	}
	if !found.namespaced && ref.Namespace != "" {
		return resource{}, &Error{RefusedRequest, "resource", "namespace_not_used"}
	}
	if ref.Name != "" && !namePattern.MatchString(ref.Name) {
		return resource{}, &Error{RefusedRequest, "resource", "invalid_name"}
	}
	return found, nil
}

// namespacedPath is where one kind's collection lives: cluster-scoped kinds sit under the prefix, the
// others under their namespace.
func (r resource) namespacedPath(ref observe.Ref) string {
	if !r.namespaced {
		return r.prefix() + "/" + r.collection
	}
	return r.prefix() + "/namespaces/" + url.PathEscape(ref.Namespace) + "/" + r.collection
}

// prefix is where one kind's resources start: the core API is /api, a group is /apis.
func (r resource) prefix() string {
	if strings.Contains(r.apiVersion, "/") {
		return "/apis/" + r.apiVersion
	}
	return "/api/" + r.apiVersion
}

// namePattern is the shape an API object's name and namespace can have, so a reference cannot reshape
// the request path (`..`, a slash, an empty segment).
var namePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)

func (r resource) objectPath(ref observe.Ref) string {
	return r.namespacedPath(ref) + "/" + url.PathEscape(ref.Name)
}

func (r resource) listPath(ref observe.Ref) string { return r.namespacedPath(ref) }
