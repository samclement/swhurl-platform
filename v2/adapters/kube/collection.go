package kube

import (
	"context"
	"net/http"
	"net/url"

	"github.com/samclement/swhurl-platform/v2/observe"
)

// Collector reads the checker's complete unfiltered collections and field-filtered lifecycle Events.
// Reader remains the selected observation port used by other use cases.
type Collector interface {
	CollectAllNamespaces(ctx context.Context, ref observe.Ref) (observe.List, error)
	ListNamespace(ctx context.Context, ref observe.Ref) (observe.List, error)
	ListEvents(ctx context.Context, involvedKind string) (observe.List, error)
}

var _ Collector = (*API)(nil)

// CollectAllNamespaces explicitly reads every object of a namespaced kind, without a selector. Existing selected
// list methods still refuse an empty selector, so an accidentally omitted selector cannot broaden their read.
func (a *API) CollectAllNamespaces(ctx context.Context, ref observe.Ref) (observe.List, error) {
	resource, err := collectionResource(ref)
	if err != nil {
		return observe.List{}, err
	}
	return a.collectionQuery(ctx, ref, resource.prefix()+"/"+resource.collection, nil)
}
func collectionResource(ref observe.Ref) (resource, error) {
	if ref.Namespace != "" || ref.Name != "" {
		return resource{}, &Error{RefusedRequest, "resource", "collection_required"}
	}
	r, err := kindOf(ref)
	if err != nil {
		return resource{}, err
	}
	if !r.namespaced {
		return resource{}, &Error{RefusedRequest, "resource", "namespaced_kind_required"}
	}
	return r, nil
}

// ListEvents reads only core lifecycle Events for the named involved kind, across namespaces. This is a FIELD
// selector, not a label selector. Other kinds are refused: the checker never needs the much larger Pod/Job stream.
func (a *API) ListEvents(ctx context.Context, involvedKind string) (observe.List, error) {
	if involvedKind != "HelmRelease" && involvedKind != "Kustomization" {
		return observe.List{}, &Error{RefusedRequest, "events", "unsupported_involved_kind"}
	}
	ref := observe.Ref{APIVersion: "v1", Kind: "Event"}
	return a.collectionQuery(ctx, ref, "/api/v1/events", url.Values{"fieldSelector": {"involvedObject.kind=" + involvedKind}})
}
func (a *API) collectionQuery(ctx context.Context, ref observe.Ref, path string, query url.Values) (observe.List, error) {
	answer := a.get(ctx, path, query)
	if answer.status != http.StatusOK {
		return observe.List{Ref: ref, Status: observe.Unavailable, Reason: answer.reason()}, nil
	}
	return DecodeCollection(ref, "", answer.body), nil
}
