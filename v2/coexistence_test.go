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
	if len(inputs) != 2 || strings.Contains(inputs[1], "v2") || !strings.Contains(inputs[1], "'tools'") {
		t.Fatal("re-check console content-hash inputs")
	}
	for _, line := range strings.Split(read("images/console/Dockerfile"), "\n") {
		if !strings.HasPrefix(line, "COPY ") || strings.Contains(line, "--from=") {
			continue
		}
		words := strings.Fields(line)
		for _, source := range words[1 : len(words)-1] {
			if source == "." || strings.ContainsAny(source, "*[") || strings.HasPrefix(source, "v2") {
				t.Fatal("console COPY can consume v2")
			}
		}
	}
	if strings.Contains(read(".dockerignore"), "!v2") {
		t.Fatal("v2 admitted to image build context")
	}
}
