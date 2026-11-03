package kubetest

import (
	"context"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// CollectionContract tests namespace scope and complete-list semantics against both implementations.
// The factory serves the raw document or an unavailable reason, without opening a network listener.
func CollectionContract(t *testing.T, open func(*testing.T, []byte, string) kube.Reader) {
	t.Helper()
	ref := observe.Ref{APIVersion: "v1", Kind: "Pod"}
	item := `{"apiVersion":"v1","kind":"Pod","metadata":{"namespace":"hello-staging","name":"main"}}`
	for _, test := range []struct {
		name, body, reason, status, wantReason string
		count                                  int
	}{
		{"complete", `{"items":[` + item + `]}`, "", observe.Present, "", 1},
		{"empty", `{"items":[]}`, "", observe.Present, "", 0},
		{"failed", "", observe.ReasonForbidden, observe.Unavailable, observe.ReasonForbidden, 0},
		{"oversized", "", observe.ReasonTooLarge, observe.Unavailable, observe.ReasonTooLarge, 0},
		{"pagination", `{"metadata":{"continue":"next"},"items":[` + item + `]}`, "", observe.Unavailable, observe.ReasonIncomplete, 0},
		{"remaining", `{"metadata":{"remainingItemCount":1},"items":[]}`, "", observe.Unavailable, observe.ReasonIncomplete, 0},
		{"missing items", `{}`, "", observe.Unavailable, observe.ReasonMalformed, 0},
		{"null items", `{"items":null}`, "", observe.Unavailable, observe.ReasonMalformed, 0},
		{"wrong identity", `{"items":[{"kind":"Job","metadata":{"name":"main","namespace":"hello-staging"}}]}`, "", observe.Unavailable, observe.ReasonMalformed, 0},
		{"duplicate identity", `{"items":[` + item + `,` + item + `]}`, "", observe.Unavailable, observe.ReasonMalformed, 0},
		{"no namespace", `{"items":[{"apiVersion":"v1","kind":"Pod","metadata":{"name":"main"}}]}`, "", observe.Unavailable, observe.ReasonMalformed, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := open(t, []byte(test.body), test.reason)
			list, err := reader.ListAllNamespaces(context.Background(), ref, "app.kubernetes.io/instance")
			if err != nil || list.Status != test.status || list.Reason != test.wantReason || len(list.Items) != test.count {
				t.Fatalf("%+v, %v", list, err)
			}
			scoped := ref
			scoped.Namespace = "hello-staging"
			list, err = reader.List(context.Background(), scoped, "app.kubernetes.io/instance")
			if err != nil || list.Status != test.status || list.Reason != test.wantReason || len(list.Items) != test.count {
				t.Fatalf("scoped: %+v, %v", list, err)
			}
		})
	}
	collectionRefusals(t, open(t, []byte(`{"items":[]}`), ""))
}

func collectionRefusals(t *testing.T, reader kube.Reader) {
	t.Helper()
	ref := observe.Ref{APIVersion: "v1", Kind: "Pod"}
	if _, err := reader.List(context.Background(), ref, "app.kubernetes.io/instance"); err == nil {
		t.Fatal("List accepted empty namespace")
	}
	if _, err := reader.Read(context.Background(), ref); err == nil {
		t.Fatal("Read accepted empty namespace/name")
	}
	if _, err := reader.ListAllNamespaces(context.Background(), ref, ""); err == nil {
		t.Fatal("empty selector accepted")
	}
	ref.Namespace = "hello-staging"
	if _, err := reader.ListAllNamespaces(context.Background(), ref, "app.kubernetes.io/instance"); err == nil {
		t.Fatal("all namespaces accepted namespace")
	}
	ref.Namespace, ref.Name = "", "main"
	if _, err := reader.ListAllNamespaces(context.Background(), ref, "app.kubernetes.io/instance"); err == nil {
		t.Fatal("all namespaces accepted name")
	}
}
