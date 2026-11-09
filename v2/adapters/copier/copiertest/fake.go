// Package copiertest is the fake for copier.Renderer and the contract suite both implementations run.
package copiertest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/copier"
)

// Scenario is the request the contract renders: a template that writes one file with the app_name answer, and the
// revision it holds. A template fixture for the real adapter is a Git repository tagged with it.
func Scenario() copier.Request {
	return copier.Request{Template: "fixture-template", Revision: "v1", Answers: map[string]string{"app_name": "notes"}}
}

// Fake answers one render by writing the file the fixture template writes, and refuses a revision it does not hold.
type Fake struct {
	Requests []copier.Request
	Fail     error
}

var _ copier.Renderer = (*Fake)(nil)

// New returns a fake that holds the scenario's revision.
func New() *Fake { return &Fake{} }

func (f *Fake) Render(_ context.Context, request copier.Request) error {
	f.Requests = append(f.Requests, request)
	if f.Fail != nil {
		return f.Fail
	}
	if request.Template == "" || request.Revision == "" || request.Revision == "no-such-revision" || request.Dest == "" {
		return &copier.Error{Reason: "copier_failed", Detail: "the fixture holds no such revision"}
	}
	body := request.Answers["app_name"] + "\n"
	if err := os.MkdirAll(request.Dest, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(request.Dest, "rendered.txt"), []byte(body), 0o644)
}

// Contract is the behaviour every copier.Renderer must have: the template at its pinned revision rendered into the
// destination with the answers, and a revision the template does not hold refused. open returns the renderer and the
// template fixture it can render.
func Contract(t *testing.T, open func(*testing.T, copier.Request) copier.Renderer, template copier.Request) {
	t.Helper()
	renderer := open(t, template)
	rendered := template
	rendered.Dest = t.TempDir()
	if err := renderer.Render(context.Background(), rendered); err != nil {
		t.Fatalf("render: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(rendered.Dest, "rendered.txt"))
	if err != nil || strings.TrimSpace(string(body)) != template.Answers["app_name"] {
		t.Fatalf("rendered file = %q, %v", body, err)
	}
	missing := rendered
	missing.Dest = t.TempDir()
	missing.Revision = "no-such-revision"
	if err := renderer.Render(context.Background(), missing); err == nil {
		t.Error("a revision the template does not hold was rendered")
	}
	if err := renderer.Render(context.Background(), copier.Request{Template: template.Template}); err == nil {
		t.Error("a render with no revision was made")
	}
}
