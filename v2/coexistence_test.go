package v2_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Fail closed if the repository starts discovering the prototype as deployed input.
// These assertions intentionally guard the current v1 discovery/build contracts.
func TestCoexistence(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("..", path))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	paths := regexp.MustCompile(`(?m)^\s+path:\s+(\./[^\s]+)\s*$`)
	files, err := filepath.Glob("../clusters/home/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, "../clusters/home/flux-system/kustomizations.yaml")
	count := 0
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range paths.FindAllStringSubmatch(string(b), -1) {
			count++
			path := filepath.Clean(match[1])
			if path == "." || path == "v2" || strings.HasPrefix(path, "v2/") {
				t.Fatalf("Flux consumes v2: %s", file)
			}
		}
	}
	if count == 0 {
		t.Fatal("no Flux paths checked; discovery contract changed")
	}
	store := read("tools/swhurl/apps/model/store.py")
	if !strings.Contains(store, "base: Path = Path('apps')") || !strings.Contains(store, `folder.glob(f'*/*/{name}')`) {
		t.Fatal("re-check v1 record discovery before changing this assertion")
	}
	platform := read("tools/swhurl/platform.py")
	inputs := regexp.MustCompile(`(?m)^CONSOLE_IMAGE_INPUTS = (.+)$`).FindStringSubmatch(platform)
	if len(inputs) != 2 || !strings.Contains(inputs[1], "'tools'") || !strings.Contains(inputs[1], "'v2'") {
		t.Fatal("re-check console content-hash inputs")
	}
	// Since the deletion plan's phase 2 (decision 2) the console image ships swhurl-v2: it writes v2's records by
	// running the binary, never by modelling them in Python. The image gets the compiled binary from one build
	// stage; no later COPY may pull v2's sources, and Flux still never deploys v2/ (the assertions above).
	dockerfile := read("images/console/Dockerfile")
	if !strings.Contains(dockerfile, "\nCOPY v2/ ./\n") {
		t.Fatal("console image does not build v2: expected a stage that copies v2/")
	}
	if !regexp.MustCompile(`(?m)^COPY --from=v2 /out/swhurl-v2 `).MatchString(dockerfile) {
		t.Fatal("console image does not install the swhurl-v2 binary")
	}
	stage := ""
	for _, line := range strings.Split(dockerfile, "\n") {
		if strings.HasPrefix(line, "FROM ") {
			stage = line
		}
		if !strings.HasPrefix(line, "COPY ") || strings.Contains(line, "--from=") {
			continue
		}
		words := strings.Fields(line)
		for _, source := range words[1 : len(words)-1] {
			if source == "." || strings.ContainsAny(source, "*[") || (strings.HasPrefix(source, "v2") && !strings.Contains(stage, "AS v2")) {
				t.Fatal("console COPY can consume more than its build inputs")
			}
		}
	}
	if !strings.Contains(read(".dockerignore"), "!v2/") {
		t.Fatal("v2 is not admitted to the console's image build context")
	}
}
