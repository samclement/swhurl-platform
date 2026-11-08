package checkrepo

import (
	"errors"
	"slices"
	"testing"
)

// Substitution follows Flux: every ${NAME} replaced, $${NAME} left as written, ${} and an unclosed ${ not references,
// and the first NAME with no value an error naming it.
func TestSubstitute(t *testing.T) {
	path := Path{Path: "platform/x", Values: map[string]string{"A": "1", "B": "two"}}
	for in, want := range map[string]string{
		"${A}-${B}":         "1-two",
		"$${A} ${A}":        "$${A} 1",
		"$$${A}":            "$$${A}",
		"${} ${A":           "${} ${A",
		"x${A}y\n${B}\n":    "x1y\ntwo\n",
		"no references":     "no references",
		"${A}${A}${B}${B}x": "11twotwox",
	} {
		if got, err := Substitute(path, in); err != nil || got != want {
			t.Errorf("Substitute(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	var finding *Finding
	if _, err := Substitute(path, "${A} ${env:KEY} ${C}"); !errors.As(err, &finding) ||
		finding.Problem != "platform/x: unresolved Flux substitution ${env:KEY} (this unit substitutes; escape a literal reference as $${...})" {
		t.Errorf("unresolved: %v", err)
	}
	if got, err := Substitute(Path{Path: "p"}, "${env:KEY}"); err != nil || got != "${env:KEY}" {
		t.Errorf("a path without values substitutes nothing: %q, %v", got, err)
	}
}

func TestAnchors(t *testing.T) {
	found := anchors("# Top\n## 17. `check-repo` in Go: the decisions (approved 8 November 2026)\n### Café — notes\nnot # a heading\n")
	for _, anchor := range []string{"top", "17-check-repo-in-go-the-decisions-approved-8-november-2026", "café--notes"} {
		if !found[anchor] {
			t.Errorf("no anchor %q in %v", anchor, found)
		}
	}
}

func TestResolve(t *testing.T) {
	for _, c := range []struct {
		folder, target, want string
		inside               bool
	}{{"docs", "../README.md", "README.md", true}, {"docs", "./a/../b.md", "docs/b.md", true}, {"", "./", ".", true},
		{"docs", "../../x", "", false}, {"", "/etc", "", false}} {
		if got, inside := resolve(c.folder, c.target); got != c.want || inside != c.inside {
			t.Errorf("resolve(%q, %q) = %q, %v", c.folder, c.target, got, inside)
		}
	}
}

func TestNeedsReadsTextAndManifestsSeparately(t *testing.T) {
	present := []string{".claude/x.md", "README.md", "apps/a.yaml", "clusters/home/app-x.json", "clusters/home/sub/y.json",
		"docs/a.md", "host/x.sh", "platform/p/a.yml", "v2-apps/instances/a/b/c.json", "v2/contract/names.json", "v2/x.yaml"}
	text, manifests := Needs(present)
	if want := []string{"README.md", "docs/a.md", "v2-apps/instances/a/b/c.json", "v2/contract/names.json"}; !slices.Equal(text, want) {
		t.Errorf("text %q, want %q", text, want)
	}
	if want := []string{"apps/a.yaml", "clusters/home/app-x.json", "platform/p/a.yml"}; !slices.Equal(manifests, want) {
		t.Errorf("manifests %q, want %q", manifests, want)
	}
	if want := []string{"clusters/home/app-x.json"}; !slices.Equal(PathNeeds(present), want) {
		t.Errorf("path needs %q", PathNeeds(present))
	}
}

func TestCompleteNeedsApiVersionKindAndMetadata(t *testing.T) {
	path := Path{Path: "p"}
	whole := Document{Value: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": nil}}
	if err := Complete(path, []Document{whole}); err != nil {
		t.Errorf("a complete document: %v", err)
	}
	for _, partial := range []Document{{}, {Value: map[string]any{"apiVersion": "v1", "kind": "ConfigMap"}}} {
		if err := Complete(path, []Document{whole, partial}); err == nil {
			t.Errorf("%v passed", partial)
		}
	}
}
