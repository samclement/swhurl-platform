package copier_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/copier"
	"github.com/samclement/swhurl-platform/v2/adapters/copier/copiertest"
)

// requireCopier skips where Copier is not installed, as the chart tests skip without Helm: the contract renders a
// local template through the real binary.
func requireCopier(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("copier"); err != nil {
		if os.Getenv("REQUIRE_COPIER") != "" {
			t.Fatal("REQUIRE_COPIER is set but Copier is missing")
		}
		t.Skip("Copier is not installed")
	}
}

// templateFixture copies testdata/template into a Git repository tagged v1, which is what --vcs-ref names.
func templateFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files, err := os.ReadDir("testdata/template")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		body, err := os.ReadFile(filepath.Join("testdata/template", file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, file.Name()), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"init", "--quiet", dir},
		{"-C", dir, "add", "--all"},
		{"-C", dir, "-c", "user.name=fixture", "-c", "user.email=fixture@example.test", "commit", "--quiet", "-m", "v1"},
		{"-C", dir, "tag", "v1"},
	} {
		if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	return dir
}

func TestContract(t *testing.T) {
	requireCopier(t)
	template := copiertest.Scenario()
	template.Template = templateFixture(t)
	copiertest.Contract(t, func(*testing.T, copier.Request) copier.Renderer { return copier.New("copier@9.18.2") }, template)
}

func TestQuestionsReadsTheTemplatesChoiceQuestions(t *testing.T) {
	document, err := os.ReadFile("testdata/copier.yml")
	if err != nil {
		t.Fatal(err)
	}
	questions, err := copier.Questions(document)
	if err != nil {
		t.Fatalf("questions: %v", err)
	}
	if len(questions) != 4 {
		t.Fatalf("questions = %#v", questions)
	}
	kind := questions[0]
	if kind.Name != "kind" || kind.Help != "web or worker" || kind.Default != "web" ||
		len(kind.Choices) != 2 || kind.Choices[0] != "web" || kind.Choices[1] != "worker" {
		t.Fatalf("kind = %#v", kind)
	}
	if database := questions[1]; database.Name != "database" || database.Choices[1] != "sqlite" || database.Default != "none" {
		t.Fatalf("database = %#v", database)
	}
}

func TestQuestionsRefusesADocumentThatIsNotAMapping(t *testing.T) {
	for _, document := range []string{"", "a list\n", "- one\n- two\n", "{unclosed\n"} {
		if _, err := copier.Questions([]byte(document)); err == nil {
			t.Errorf("%q: read as questions", document)
		}
	}
}

func TestQuestionsOffersNoFreeTextAndNoPlatformField(t *testing.T) {
	document := "app_name:\n  type: str\ndatabase:\n  type: str\n  help: free text\n"
	questions, err := copier.Questions([]byte(document))
	if err != nil || len(questions) != 0 {
		t.Fatalf("questions = %#v, %v", questions, err)
	}
}

func TestRenderRefusesARequestThatNamesNoTemplateRevisionOrDestination(t *testing.T) {
	renderer := copier.New("copier@9.18.2")
	for name, request := range map[string]copier.Request{
		"no template": {Revision: "v1", Dest: t.TempDir()},
		"no revision": {Template: "https://github.com/x/y.git", Dest: t.TempDir()},
		"no dest":     {Template: "https://github.com/x/y.git", Revision: "v1"},
		"bad answer":  {Template: "https://github.com/x/y.git", Revision: "v1", Dest: t.TempDir(), Answers: map[string]string{"--data": "x"}},
	} {
		if err := renderer.Render(context.Background(), request); err == nil {
			t.Errorf("%s: rendered", name)
		}
	}
}
