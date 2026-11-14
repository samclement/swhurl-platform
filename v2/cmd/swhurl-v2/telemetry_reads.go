package main

import (
	"context"

	"github.com/samclement/swhurl-platform/v2/adapters/github"
	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/observe"
	"github.com/samclement/swhurl-platform/v2/orchestration"
	"github.com/samclement/swhurl-platform/v2/telemetry"
)

// The console's reads, each one a span inside the request that made it. They live here, not in the adapters: an
// adapter may not import another, and a span is the console's concern, not the API server's or GitHub's. A span's
// attributes name what was read (a kind, a namespace, a repository, a path), never a token or what came back.

// traced runs one call as a child span named name.
func traced[T any](ctx context.Context, t *consoleTelemetry, name string, attributes []telemetry.Attribute,
	call func(context.Context) (T, error)) (T, error) {
	ctx, span := t.child(ctx, name, attributes...)
	value, err := call(ctx)
	span.end(err)
	return value, err
}

func refAttributes(ref observe.Ref) []telemetry.Attribute {
	attributes := []telemetry.Attribute{{Key: "k8s.kind", Value: ref.Kind}}
	if ref.Namespace != "" {
		attributes = append(attributes, telemetry.Attribute{Key: "k8s.namespace.name", Value: ref.Namespace})
	}
	if ref.Name != "" {
		attributes = append(attributes, telemetry.Attribute{Key: "k8s.object.name", Value: ref.Name})
	}
	return attributes
}

// tracedReader is the cluster reader, with a span per read.
type tracedReader struct {
	inner kube.Reader
	trace *consoleTelemetry
}

// traceReader is r with a span per read, or r itself when nothing is recorded.
func (t *consoleTelemetry) traceReader(r kube.Reader) kube.Reader {
	if t == nil || r == nil {
		return r
	}
	return tracedReader{r, t}
}

func (r tracedReader) Read(ctx context.Context, ref observe.Ref) (observe.Read, error) {
	return traced(ctx, r.trace, "kube.Read", refAttributes(ref), func(ctx context.Context) (observe.Read, error) {
		return r.inner.Read(ctx, ref)
	})
}

func (r tracedReader) List(ctx context.Context, ref observe.Ref, selector string) (observe.List, error) {
	return traced(ctx, r.trace, "kube.List", refAttributes(ref), func(ctx context.Context) (observe.List, error) {
		return r.inner.List(ctx, ref, selector)
	})
}

func (r tracedReader) ListAllNamespaces(ctx context.Context, ref observe.Ref, selector string) (observe.List, error) {
	return traced(ctx, r.trace, "kube.ListAllNamespaces", refAttributes(ref), func(ctx context.Context) (observe.List, error) {
		return r.inner.ListAllNamespaces(ctx, ref, selector)
	})
}

func (r tracedReader) ListNamespace(ctx context.Context, ref observe.Ref) (observe.List, error) {
	return traced(ctx, r.trace, "kube.ListNamespace", refAttributes(ref), func(ctx context.Context) (observe.List, error) {
		return r.inner.ListNamespace(ctx, ref)
	})
}

// tracedCollector is the cluster's collection reads, with a span per read.
type tracedCollector struct {
	inner kube.Collector
	trace *consoleTelemetry
}

func (t *consoleTelemetry) traceCollector(c kube.Collector) kube.Collector {
	if t == nil || c == nil {
		return c
	}
	return tracedCollector{c, t}
}

func (c tracedCollector) CollectAllNamespaces(ctx context.Context, ref observe.Ref) (observe.List, error) {
	return traced(ctx, c.trace, "kube.CollectAllNamespaces", refAttributes(ref), func(ctx context.Context) (observe.List, error) {
		return c.inner.CollectAllNamespaces(ctx, ref)
	})
}

func (c tracedCollector) ListNamespace(ctx context.Context, ref observe.Ref) (observe.List, error) {
	return traced(ctx, c.trace, "kube.ListNamespace", refAttributes(ref), func(ctx context.Context) (observe.List, error) {
		return c.inner.ListNamespace(ctx, ref)
	})
}

func (c tracedCollector) ListEvents(ctx context.Context, involvedKind string) (observe.List, error) {
	attributes := []telemetry.Attribute{{Key: "k8s.kind", Value: "Event"}, {Key: "k8s.involved.kind", Value: involvedKind}}
	return traced(ctx, c.trace, "kube.ListEvents", attributes, func(ctx context.Context) (observe.List, error) {
		return c.inner.ListEvents(ctx, involvedKind)
	})
}

// tracedData is the console's own records, with a span per read or write made inside a request.
type tracedData struct {
	inner kube.Data
	trace *consoleTelemetry
}

func (t *consoleTelemetry) traceData(d kube.Data) kube.Data {
	if t == nil || d == nil {
		return d
	}
	return tracedData{d, t}
}

func dataAttributes(kind, namespace, name string) []telemetry.Attribute {
	return refAttributes(observe.Ref{Kind: kind, Namespace: namespace, Name: name})
}

func (d tracedData) GetConfigMap(ctx context.Context, namespace, name string) (*kube.ConfigMap, error) {
	return traced(ctx, d.trace, "kube.GetConfigMap", dataAttributes("ConfigMap", namespace, name),
		func(ctx context.Context) (*kube.ConfigMap, error) { return d.inner.GetConfigMap(ctx, namespace, name) })
}

func (d tracedData) GetSecret(ctx context.Context, namespace, name string) (*kube.Secret, error) {
	return traced(ctx, d.trace, "kube.GetSecret", dataAttributes("Secret", namespace, name),
		func(ctx context.Context) (*kube.Secret, error) { return d.inner.GetSecret(ctx, namespace, name) })
}

func (d tracedData) ConfigMaps(ctx context.Context, namespace string, selector kube.Selector) ([]kube.ConfigMap, error) {
	return traced(ctx, d.trace, "kube.ConfigMaps", dataAttributes("ConfigMap", namespace, selector.Name),
		func(ctx context.Context) ([]kube.ConfigMap, error) {
			return d.inner.ConfigMaps(ctx, namespace, selector)
		})
}

func (d tracedData) Secrets(ctx context.Context, namespace string, selector kube.Selector) ([]kube.Secret, error) {
	return traced(ctx, d.trace, "kube.Secrets", dataAttributes("Secret", namespace, selector.Name),
		func(ctx context.Context) ([]kube.Secret, error) { return d.inner.Secrets(ctx, namespace, selector) })
}

func (d tracedData) SetConfigMapKey(ctx context.Context, namespace, name, key, value, fieldManager string) error {
	_, err := traced(ctx, d.trace, "kube.SetConfigMapKey", dataAttributes("ConfigMap", namespace, name),
		func(ctx context.Context) (struct{}, error) {
			return struct{}{}, d.inner.SetConfigMapKey(ctx, namespace, name, key, value, fieldManager)
		})
	return err
}

func (d tracedData) SetConfigMapKeys(ctx context.Context, namespace, name string, data map[string]*string, fieldManager string) error {
	_, err := traced(ctx, d.trace, "kube.SetConfigMapKeys", dataAttributes("ConfigMap", namespace, name),
		func(ctx context.Context) (struct{}, error) {
			return struct{}{}, d.inner.SetConfigMapKeys(ctx, namespace, name, data, fieldManager)
		})
	return err
}

// tracedGitHub is the console's GitHub reads, with a span per call.
type tracedGitHub struct {
	inner github.Console
	trace *consoleTelemetry
}

func (t *consoleTelemetry) traceGitHub(g github.Console) github.Console {
	if t == nil || g == nil {
		return g
	}
	return tracedGitHub{g, t}
}

func repositoryAttributes(repository string, path ...string) []telemetry.Attribute {
	attributes := []telemetry.Attribute{{Key: "github.repository", Value: repository}}
	if len(path) > 0 {
		attributes = append(attributes, telemetry.Attribute{Key: "github.path", Value: path[0]})
	}
	return attributes
}

// found is a file's body and whether it was there, carried through traced as one value.
type found struct {
	body []byte
	held bool
}

func (g tracedGitHub) Revision(ctx context.Context, repository, token string) (string, error) {
	return traced(ctx, g.trace, "github.Revision", repositoryAttributes(repository),
		func(ctx context.Context) (string, error) { return g.inner.Revision(ctx, repository, token) })
}

func (g tracedGitHub) Contents(ctx context.Context, repository, token, revision, path string) ([]byte, bool, error) {
	got, err := traced(ctx, g.trace, "github.Contents", repositoryAttributes(repository, path),
		func(ctx context.Context) (found, error) {
			body, held, err := g.inner.Contents(ctx, repository, token, revision, path)
			return found{body, held}, err
		})
	return got.body, got.held, err
}

func (g tracedGitHub) Tarball(ctx context.Context, repository, token, revision string) ([]byte, error) {
	return traced(ctx, g.trace, "github.Tarball", repositoryAttributes(repository),
		func(ctx context.Context) ([]byte, error) { return g.inner.Tarball(ctx, repository, token, revision) })
}

func (g tracedGitHub) CommitFiles(ctx context.Context, repository, token, revision string, paths []string) (map[string][]byte, error) {
	attributes := append(repositoryAttributes(repository), telemetry.Attribute{Key: "github.files", Value: len(paths)})
	return traced(ctx, g.trace, "github.CommitFiles", attributes, func(ctx context.Context) (map[string][]byte, error) {
		return g.inner.CommitFiles(ctx, repository, token, revision, paths)
	})
}

func (g tracedGitHub) AppFile(ctx context.Context, repository, token, path string) ([]byte, bool, error) {
	got, err := traced(ctx, g.trace, "github.AppFile", repositoryAttributes(repository, path),
		func(ctx context.Context) (found, error) {
			body, held, err := g.inner.AppFile(ctx, repository, token, path)
			return found{body, held}, err
		})
	return got.body, got.held, err
}

func (g tracedGitHub) PublicFile(ctx context.Context, repository, revision, path string) ([]byte, bool, error) {
	got, err := traced(ctx, g.trace, "github.PublicFile", repositoryAttributes(repository, path),
		func(ctx context.Context) (found, error) {
			body, held, err := g.inner.PublicFile(ctx, repository, revision, path)
			return found{body, held}, err
		})
	return got.body, got.held, err
}

func (g tracedGitHub) Compare(ctx context.Context, repository, token, from, to string) ([]string, bool, error) {
	type compared struct {
		paths     []string
		contained bool
	}
	got, err := traced(ctx, g.trace, "github.Compare", repositoryAttributes(repository),
		func(ctx context.Context) (compared, error) {
			paths, contained, err := g.inner.Compare(ctx, repository, token, from, to)
			return compared{paths, contained}, err
		})
	return got.paths, got.contained, err
}

func (g tracedGitHub) OpenPulls(ctx context.Context, repository, token string) ([]github.Pull, error) {
	return traced(ctx, g.trace, "github.OpenPulls", repositoryAttributes(repository),
		func(ctx context.Context) ([]github.Pull, error) { return g.inner.OpenPulls(ctx, repository, token) })
}

func (g tracedGitHub) Pull(ctx context.Context, repository, token string, number int) (github.Pull, error) {
	attributes := append(repositoryAttributes(repository), telemetry.Attribute{Key: "github.pull", Value: number})
	return traced(ctx, g.trace, "github.Pull", attributes,
		func(ctx context.Context) (github.Pull, error) { return g.inner.Pull(ctx, repository, token, number) })
}

// tracedSource is the pinned tree's source, with a span per read: the bundle's cache answers without one.
type tracedSource struct {
	inner orchestration.ConsoleSource
	trace *consoleTelemetry
}

func (t *consoleTelemetry) traceSource(s orchestration.ConsoleSource) orchestration.ConsoleSource {
	if t == nil || s == nil {
		return s
	}
	return tracedSource{s, t}
}

func (s tracedSource) Revision(ctx context.Context) (string, error) {
	return traced(ctx, s.trace, "source.Revision", nil, s.inner.Revision)
}

func (s tracedSource) Tree(ctx context.Context, revision string) (map[string][]byte, error) {
	return traced(ctx, s.trace, "source.Tree", []telemetry.Attribute{{Key: "vcs.ref.head.revision", Value: revision}},
		func(ctx context.Context) (map[string][]byte, error) { return s.inner.Tree(ctx, revision) })
}

func (s tracedSource) Files(ctx context.Context, revision string, paths []string) (map[string][]byte, error) {
	attributes := []telemetry.Attribute{{Key: "vcs.ref.head.revision", Value: revision}, {Key: "source.files", Value: len(paths)}}
	return traced(ctx, s.trace, "source.Files", attributes,
		func(ctx context.Context) (map[string][]byte, error) { return s.inner.Files(ctx, revision, paths) })
}
