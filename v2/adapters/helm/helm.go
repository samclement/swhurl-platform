// Package helm acquires explicit pinned charts and renders explicit local charts as separate operations.
// Rendering never acquires dependencies; neither operation talks to a cluster.
// YAML is a dependency format only here: callers receive facts or complete JSON objects, not parser values.
package helm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"
)

const (
	RefusedRender = "refused_render"
	MaxValues     = 1 << 20
	MaxOutput     = 16 << 20
	MaxStderr     = 64 << 10
	RenderTimeout = 30 * time.Second
)

// Request supplies the chart directory, release identity and JSON values, without modifying the chart.
// Chart must be an existing local directory, never a repository name, URL or archive.
type Request struct {
	Chart, Release, Namespace string
	ChartName, ChartVersion   string
	Values                    json.RawMessage
}

// Renderer returns all rendered objects in document order, or no objects on any failure.
// Objects own their bytes; no namespace or other missing property is invented. No kind is filtered.
type Renderer interface {
	Render(Request) ([]json.RawMessage, error)
}

// Error carries a fixed reason only, never chart, values, output, paths or tool/parser diagnostics.
type Error struct{ Reason string }

func (e *Error) Error() string   { return RefusedRender + ": " + e.Reason }
func refuse(reason string) error { return &Error{Reason: reason} }

// Command invokes Helm from PATH. Binary is injectable for boundary tests, not an argv runner.
type Command struct {
	Binary  string
	timeout time.Duration // tests may shorten, never extend, the fixed production bound
}

func New() *Command { return &Command{Binary: "helm"} }

var _ Renderer = (*Command)(nil)

func (c *Command) Render(request Request) ([]json.RawMessage, error) {
	if err := validate(request); err != nil {
		return nil, err
	}
	normalized, err := normalizedValues(request.Values)
	if err != nil {
		return nil, err
	}
	chart, err := filepath.Abs(request.Chart)
	if err != nil {
		return nil, refuse("invalid_chart")
	}
	dir, err := os.MkdirTemp("", "swhurl-helm-*")
	if err != nil {
		return nil, refuse("temporary_values_failed")
	}
	defer os.RemoveAll(dir)
	values := filepath.Join(dir, "values.json")
	if err := os.WriteFile(values, normalized, 0o600); err != nil {
		return nil, refuse("temporary_values_failed")
	}
	return c.render(chart, values, request)
}

func (c *Command) render(chart, values string, request Request) ([]json.RawMessage, error) {
	timeout := RenderTimeout
	if c.timeout > 0 && c.timeout < timeout {
		timeout = c.timeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, c.Binary, "template", request.Release, chart,
		"--namespace", request.Namespace, "--values", values, "--include-crds")
	// WaitDelay bounds inherited pipes even if a failing executable leaves children behind.
	command.WaitDelay = time.Second
	stdout := &bounded{limit: MaxOutput, keep: true}
	stderr := &bounded{limit: MaxStderr}
	command.Stdout, command.Stderr = stdout, stderr
	err := command.Run()
	switch {
	case stdout.exceeded:
		return nil, refuse("output_too_large")
	case stderr.exceeded:
		return nil, refuse("stderr_too_large")
	case ctx.Err() != nil:
		return nil, refuse("timeout")
	case err != nil:
		return nil, refuse("helm_failed")
	}
	return objects(stdout.buffer.Bytes())
}

func validate(request Request) error {
	if len(request.Values) == 0 || len(request.Values) > MaxValues {
		return refuse("invalid_values")
	}
	if !validValues(request.Values) {
		return refuse("invalid_values")
	}
	return validateLocation(request)
}

func validateLocation(request Request) error {
	if !regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`).MatchString(request.Release) || len(request.Release) > 53 {
		return refuse("invalid_release")
	}
	if !regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`).MatchString(request.Namespace) || len(request.Namespace) > 63 {
		return refuse("invalid_namespace")
	}
	if request.Chart == "" {
		return refuse("invalid_chart")
	}
	return chartMatches(request)
}

// bounded retains at most limit bytes. Stderr is counted but discarded, not retained for diagnostics.
type bounded struct {
	buffer         bytes.Buffer
	limit, count   int
	keep, exceeded bool
}

func (b *bounded) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.count {
		b.exceeded = true
		return 0, io.ErrShortWrite
	}
	b.count += len(p)
	if b.keep {
		return b.buffer.Write(p)
	}
	return len(p), nil
}
