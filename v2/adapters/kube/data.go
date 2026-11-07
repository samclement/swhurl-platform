package kube

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"

	"github.com/samclement/swhurl-platform/v2/observe"
)

// Data reads ConfigMaps and Secrets in one namespace, and sets one ConfigMap key: the state a service keeps
// for itself. It is a port of its own, so Reader still never reads a Secret and an observing use case cannot reach
// one. GetConfigMap and GetSecret are named GETs; the selected reads are lists. A failed read is never absence.
type Data interface {
	GetConfigMap(ctx context.Context, namespace, name string) (*ConfigMap, error)
	GetSecret(ctx context.Context, namespace, name string) (*Secret, error)
	ConfigMaps(ctx context.Context, namespace string, selector Selector) ([]ConfigMap, error)
	Secrets(ctx context.Context, namespace string, selector Selector) ([]Secret, error)
	// SetConfigMapKey sets one key of a ConfigMap with a JSON merge patch under the field manager, leaving every
	// other key as it is.
	SetConfigMapKey(ctx context.Context, namespace, name, key, value, fieldManager string) error
}

// Selector picks the objects a list reads: one object by Name (a field selector on metadata.name), or those whose
// labels match Labels (a label selector). Exactly one is given.
type Selector struct{ Name, Labels string }

// ConfigMap is a listed ConfigMap's name, labels and data.
type ConfigMap struct {
	Name   string
	Labels map[string]string
	Data   map[string]string
}

// Secret is a listed Secret's name, labels and decoded data.
type Secret struct {
	Name   string
	Labels map[string]string
	Data   map[string][]byte
}

var _ Data = (*API)(nil)

// maxDataBytes bounds ConfigMap/Secret lists and the ConfigMap a patch returns: an object holds up to 1 MiB
// of data, which JSON escaping and metadata can grow.
const maxDataBytes = 4 << 20

// keyPattern is the shape a ConfigMap key can have.
var keyPattern = regexp.MustCompile(`^[-._a-zA-Z0-9]{1,253}$`)

// ConfigMaps lists the ConfigMaps the selector picks.
func (a *API) ConfigMaps(ctx context.Context, namespace string, selector Selector) ([]ConfigMap, error) {
	items, err := a.listData(ctx, "configmaps", namespace, selector)
	if err != nil {
		return nil, err
	}
	found := make([]ConfigMap, 0, len(items))
	for _, item := range items {
		found = append(found, ConfigMap{Name: item.Metadata.Name, Labels: item.Metadata.Labels, Data: item.Data})
	}
	return found, nil
}

// Secrets lists the Secrets the selector picks, with their data decoded.
func (a *API) Secrets(ctx context.Context, namespace string, selector Selector) ([]Secret, error) {
	items, err := a.listData(ctx, "secrets", namespace, selector)
	if err != nil {
		return nil, err
	}
	found := make([]Secret, 0, len(items))
	for _, item := range items {
		secret, err := decodedSecret(item, "secrets")
		if err != nil {
			return nil, err
		}
		found = append(found, secret)
	}
	return found, nil
}

// decodedSecret is an item's data decoded from base64; one value that is not is a malformed read of the whole.
func decodedSecret(item dataItem, subject string) (Secret, error) {
	data := make(map[string][]byte, len(item.Data))
	for key, encoded := range item.Data {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return Secret{}, &Error{Failed, subject, observe.ReasonMalformed}
		}
		data[key] = decoded
	}
	return Secret{Name: item.Metadata.Name, Labels: item.Metadata.Labels, Data: data}, nil
}

// SetConfigMapKey patches one key of one ConfigMap.
func (a *API) SetConfigMapKey(ctx context.Context, namespace, name, key, value, fieldManager string) error {
	if !namePattern.MatchString(namespace) || !namePattern.MatchString(name) || !keyPattern.MatchString(key) {
		return &Error{RefusedRequest, "configmap", "invalid_name"}
	}
	if fieldManager == "" || len(fieldManager) > 128 {
		return &Error{RefusedRequest, "configmap", "field_manager_required"}
	}
	patch, err := json.Marshal(map[string]map[string]string{"data": {key: value}})
	if err != nil {
		return &Error{RefusedRequest, "configmap", "unencodable"}
	}
	path := "/api/v1/namespaces/" + url.PathEscape(namespace) + "/configmaps/" + url.PathEscape(name)
	_, failure := a.exchangeBounded(ctx, http.MethodPatch, path, url.Values{"fieldManager": {fieldManager}}, patch,
		"application/merge-patch+json", maxDataBytes)
	if failure != nil {
		return &Error{Failed, "configmap", failure.Reason}
	}
	return nil
}

// dataItem is one listed ConfigMap or Secret: its identity, labels and data as the API states them.
type dataItem struct {
	Metadata struct {
		Name      string            `json:"name"`
		Namespace string            `json:"namespace"`
		Labels    map[string]string `json:"labels"`
	} `json:"metadata"`
	Data map[string]string `json:"data"`
}

// listData reads one complete list of a core kind in one namespace. A partial or malformed list, or an item from
// another namespace, is a failed read.
func (a *API) listData(ctx context.Context, collection, namespace string, selector Selector) ([]dataItem, error) {
	query, err := selector.query()
	if err != nil {
		return nil, err
	}
	if !namePattern.MatchString(namespace) {
		return nil, &Error{RefusedRequest, collection, "invalid_namespace"}
	}
	answer := a.getBounded(ctx, "/api/v1/namespaces/"+url.PathEscape(namespace)+"/"+collection, query, maxDataBytes)
	if answer.status != http.StatusOK {
		return nil, &Error{Failed, collection, answer.reason()}
	}
	items, reason := dataItems(answer.body, namespace)
	if reason != "" {
		return nil, &Error{Failed, collection, reason}
	}
	return items, nil
}

// query is the selector as the list's query, refusing a selector that picks nothing or picks twice.
func (s Selector) query() (url.Values, error) {
	switch {
	case (s.Name == "") == (s.Labels == ""):
		return nil, &Error{RefusedRequest, "selector", "one_of_name_or_labels_required"}
	case s.Name != "" && !namePattern.MatchString(s.Name):
		return nil, &Error{RefusedRequest, "selector", "invalid_name"}
	case s.Name != "":
		return url.Values{"fieldSelector": {"metadata.name=" + s.Name}}, nil
	}
	return url.Values{"labelSelector": {s.Labels}}, nil
}

// dataItems is a list document's items, or why it is not a complete list of this namespace's objects.
func dataItems(body []byte, namespace string) ([]dataItem, string) {
	var doc struct {
		Metadata struct {
			Continue  string `json:"continue"`
			Remaining *int64 `json:"remainingItemCount"`
		} `json:"metadata"`
		Items *[]dataItem `json:"items"`
	}
	if json.Unmarshal(bytes.TrimSpace(body), &doc) != nil || doc.Items == nil {
		return nil, observe.ReasonMalformed
	}
	if doc.Metadata.Continue != "" || doc.Metadata.Remaining != nil && *doc.Metadata.Remaining > 0 {
		return nil, observe.ReasonIncomplete
	}
	seen := map[string]bool{}
	for _, item := range *doc.Items {
		if item.Metadata.Name == "" || item.Metadata.Namespace != namespace || seen[item.Metadata.Name] {
			return nil, observe.ReasonMalformed
		}
		seen[item.Metadata.Name] = true
	}
	return *doc.Items, ""
}

// GetConfigMap reads one named object, requiring get, NOT list permission. ConfigMaps with Selector{Name:...}
// remains a field-selected LIST for its existing callers; a resourceNames get/patch-only Role cannot use it.
// Only a successful 404 is absence (nil); forbidden, malformed and oversized answers are errors.
func (a *API) GetConfigMap(ctx context.Context, namespace, name string) (*ConfigMap, error) {
	if !namePattern.MatchString(namespace) || !namePattern.MatchString(name) {
		return nil, &Error{RefusedRequest, "configmap", "invalid_name"}
	}
	path := "/api/v1/namespaces/" + url.PathEscape(namespace) + "/configmaps/" + url.PathEscape(name)
	answer := a.getBounded(ctx, path, nil, maxDataBytes)
	if answer.status == http.StatusNotFound {
		return nil, nil
	}
	if answer.status != http.StatusOK {
		return nil, &Error{Failed, "configmap", answer.reason()}
	}
	var item dataItem
	ref := observe.Ref{APIVersion: "v1", Kind: "ConfigMap", Namespace: namespace, Name: name}
	if !identifies(ref, answer.body) || json.Unmarshal(answer.body, &item) != nil {
		return nil, &Error{Failed, "configmap", observe.ReasonMalformed}
	}
	return &ConfigMap{Name: item.Metadata.Name, Labels: item.Metadata.Labels, Data: item.Data}, nil
}

// GetSecret reads one named Secret, requiring get, NOT list permission: a Role naming the Secret in resourceNames
// with get alone allows it. Only a successful 404 is absence (nil); forbidden, malformed and oversized answers, and a
// value that is not base64, are errors. No error carries the answer's bytes.
func (a *API) GetSecret(ctx context.Context, namespace, name string) (*Secret, error) {
	if !namePattern.MatchString(namespace) || !namePattern.MatchString(name) {
		return nil, &Error{RefusedRequest, "secret", "invalid_name"}
	}
	path := "/api/v1/namespaces/" + url.PathEscape(namespace) + "/secrets/" + url.PathEscape(name)
	answer := a.getBounded(ctx, path, nil, maxDataBytes)
	if answer.status == http.StatusNotFound {
		return nil, nil
	}
	if answer.status != http.StatusOK {
		return nil, &Error{Failed, "secret", answer.reason()}
	}
	var item dataItem
	ref := observe.Ref{APIVersion: "v1", Kind: "Secret", Namespace: namespace, Name: name}
	if !identifies(ref, answer.body) || json.Unmarshal(answer.body, &item) != nil {
		return nil, &Error{Failed, "secret", observe.ReasonMalformed}
	}
	secret, err := decodedSecret(item, "secret")
	if err != nil {
		return nil, err
	}
	return &secret, nil
}
