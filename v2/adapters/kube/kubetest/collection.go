package kubetest

import (
	"context"
	"maps"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/observe"
)

var _ kube.Collector = (*Fake)(nil)

// GetConfigMap is the fake's named get, distinct from the selected list and its permission.
func (f *Fake) GetConfigMap(_ context.Context, namespace, name string) (*kube.ConfigMap, error) {
	f.Calls = append(f.Calls, "get "+name)
	if !namePattern.MatchString(namespace) || !namePattern.MatchString(name) {
		return nil, &kube.Error{Code: kube.RefusedRequest, Subject: "configmap", Reason: "invalid_name"}
	}
	if err := f.failing("get " + name); err != nil {
		return nil, err
	}
	for _, item := range f.ConfigMapData[namespace] {
		if item.Name == name {
			return &kube.ConfigMap{Name: item.Name, Labels: maps.Clone(item.Labels), Data: maps.Clone(item.Data)}, nil
		}
	}
	return nil, nil
}

// CollectAllNamespaces answers the explicitly unfiltered collection. The caller owns the answer's bytes.
func (f *Fake) CollectAllNamespaces(_ context.Context, ref observe.Ref) (observe.List, error) {
	f.Selectors = append(f.Selectors, "")
	f.Reads = append(f.Reads, ref)
	if f.Refuse != nil {
		return observe.List{}, f.Refuse
	}
	if ref.Namespace != "" || ref.Name != "" {
		return observe.List{}, &kube.Error{Code: kube.RefusedRequest, Subject: "resource", Reason: "collection_required"}
	}
	namespaced := ref
	namespaced.Namespace = "default"
	if !readable(namespaced) {
		return observe.List{}, &kube.Error{Code: kube.RefusedRequest, Subject: "resource", Reason: "namespaced_kind_required"}
	}
	if f.Answers.ListReason != "" {
		return observe.List{Ref: ref, Status: observe.Unavailable, Reason: f.Answers.ListReason}, nil
	}
	if body := f.Answers.Collection; body != nil {
		return kube.DecodeCollection(ref, "", body), nil
	}
	return kube.DecodeCollection(ref, "", collection(f.Collections[ref])), nil
}

// ListEvents answers Events for only the requested involved kind.
func (f *Fake) ListEvents(ctx context.Context, kind string) (observe.List, error) {
	if kind != "HelmRelease" && kind != "Kustomization" {
		return observe.List{}, &kube.Error{Code: kube.RefusedRequest, Subject: "events", Reason: "unsupported_involved_kind"}
	}
	ref := observe.Ref{APIVersion: "v1", Kind: "Event"}
	all, err := f.CollectAllNamespaces(ctx, ref)
	f.Selectors[len(f.Selectors)-1] = "involvedObject.kind=" + kind
	if err != nil || all.Status != observe.Present {
		return all, err
	}
	return filterEvents(all, kind), nil
}

// CheckerCollectionContract covers explicit unfiltered reads, core Event field selection and named state GET. open
// returns ports over one fixture containing both lifecycle kinds, and a named ConfigMap with a state key.
func CheckerCollectionContract(t *testing.T, open func(*testing.T) (kube.Collector, kube.Data)) {
	t.Helper()
	reader, data := open(t)
	ctx := context.Background()
	ref := observe.Ref{APIVersion: "v1", Kind: "Event"}
	all, err := reader.CollectAllNamespaces(ctx, ref)
	if err != nil || all.Status != observe.Present || len(all.Items) != 2 {
		t.Fatalf("all events: %+v %v", all, err)
	}
	for _, kind := range []string{"HelmRelease", "Kustomization"} {
		list, err := reader.ListEvents(ctx, kind)
		if err != nil || list.Status != observe.Present || len(list.Items) != 1 {
			t.Fatalf("%s events: %+v %v", kind, list, err)
		}
	}
	if _, err := reader.ListEvents(ctx, "Pod"); err == nil {
		t.Fatal("unsupported event selector accepted")
	}
	checkerNamedGetContract(t, data)
}

func checkerNamedGetContract(t *testing.T, data kube.Data) {
	t.Helper()
	ctx := context.Background()
	cm, err := data.GetConfigMap(ctx, "console", "notification-state")
	if err != nil || cm == nil || cm.Data["state.json"] != "{}" {
		t.Fatalf("named get: %+v %v", cm, err)
	}
	cm.Data["state.json"] = "changed"
	again, err := data.GetConfigMap(ctx, "console", "notification-state")
	if err != nil || again.Data["state.json"] != "{}" {
		t.Fatalf("borrowed get data: %+v %v", again, err)
	}
	if absent, err := data.GetConfigMap(ctx, "console", "absent"); err != nil || absent != nil {
		t.Fatalf("missing get: %+v %v", absent, err)
	}
}
