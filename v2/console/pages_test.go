package console_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/kube/kubetest"
	"github.com/samclement/swhurl-platform/v2/console"
	healthrules "github.com/samclement/swhurl-platform/v2/health/rules"
	"github.com/samclement/swhurl-platform/v2/observe"
	"github.com/samclement/swhurl-platform/v2/pages"
)

// unit is one Flux Kustomization as the cluster lists it, applied at one commit.
func unit(name, ready, message string) []byte {
	return []byte(`{"metadata":{"name":"` + name + `","namespace":"flux-system"},"spec":{},"status":{"lastAppliedRevision":"main@sha1:` +
		strings.Repeat("a", 40) + `","conditions":[{"type":"Ready","status":"` + ready + `","message":"` + message + `"}]}}`)
}

// unresolved is a source whose commit cannot be resolved: no page reads a tree it did not pin.
type unresolved struct{}

func (unresolved) Revision(context.Context) (string, error) {
	return "", errors.New("github: rate limited")
}

func (unresolved) Tree(context.Context, string) (map[string][]byte, error) {
	return nil, errors.New("read a tree it did not pin")
}

func (unresolved) Files(context.Context, string, []string) (map[string][]byte, error) {
	return nil, errors.New("read files it did not pin")
}

// A unit's page is found among the cluster's units by name: an app's unit names its instance, a unit whose
// readiness says nothing shows the revision it applied, one Flux does not apply says so, and a name the cluster does
// not list is not found rather than an error.
func TestUnitPage(t *testing.T) {
	cluster := kubetest.New(kubetest.Answers{})
	cluster.Collections = map[observe.Ref][][]byte{healthrules.PlatformUnits: {
		unit("cluster-stack", "False", "dependency not ready"), unit("app-web-prod", "True", "")}}
	server := console.Server{Collector: cluster}
	page, found, err := server.UnitPage(context.Background(), "app-web-prod")
	if err != nil || !found {
		t.Fatalf("found %v, error %v", found, err)
	}
	if page.App == nil || *page.App != (pages.Instance{App: "web", Env: "prod"}) || page.Refused != "" ||
		!strings.HasPrefix(page.Detail, "applied ") {
		t.Fatalf("page = %+v", page)
	}
	if page, _, _ := server.UnitPage(context.Background(), "cluster-stack"); page.App != nil ||
		page.Detail != "dependency not ready" || page.Refused != pages.RefusedUnits["cluster-stack"] {
		t.Fatalf("platform unit's page = %+v", page)
	}
	if page, found, err := server.UnitPage(context.Background(), "app-gone-prod"); found || err != nil {
		t.Fatalf("an unlisted unit: %+v, found %v, error %v", page, found, err)
	}
}

// A failed read is the page's error in Go's own words, and stops the reads that would follow it: units the cluster
// could not list are unavailable, and an unresolved commit reads neither a tree nor the cluster.
func TestPageReadFailures(t *testing.T) {
	cluster := kubetest.New(kubetest.Answers{Pods: healthrules.PlatformUnits, ListReason: "forbidden"})
	if _, err := (console.Server{Collector: cluster, Reader: cluster}).Platform(context.Background()); err == nil ||
		err.Error() != "Flux units unavailable: forbidden" {
		t.Fatalf("platform error = %v", err)
	}
	if len(cluster.Reads) != 0 {
		t.Fatalf("checks ran after the units failed: %v", cluster.Reads)
	}
	cluster = kubetest.New(kubetest.Answers{})
	server := console.Server{Bundle: &console.Bundle{Source: unresolved{}}, Collector: cluster, Reader: cluster}
	if _, err := server.Overview(context.Background()); err == nil ||
		err.Error() != "Go status unavailable: github: rate limited" {
		t.Fatalf("overview error = %v", err)
	}
	if _, _, err := server.Rows(context.Background()); err == nil || !strings.HasPrefix(err.Error(), "Go status unavailable: ") {
		t.Fatalf("rows error = %v", err)
	}
	if len(cluster.Selectors) != 0 || len(cluster.Reads) != 0 {
		t.Fatalf("the cluster was read without a pinned commit: %v %v", cluster.Selectors, cluster.Reads)
	}
}
