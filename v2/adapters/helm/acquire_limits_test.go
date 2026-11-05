package helm

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPullFailuresAreBoundedPrivateAndCleaned(t *testing.T) {
	pin := Pin{Name: "boundary", Version: "0.1.0", Repository: "https://charts.example.test"}
	for _, body := range []string{"exec sleep 10", "head -c " + strconv.Itoa(MaxOutput+1) + " /dev/zero",
		"head -c " + strconv.Itoa(MaxStderr+1) + " /dev/zero >&2", "echo sensitive-sentinel >&2; exit 7"} {
		cache := t.TempDir()
		command := &Command{Binary: executable(t, body), timeout: 50 * time.Millisecond}
		path, err := command.Acquire(pin, cache)
		if path != "" || err == nil || strings.Contains(err.Error(), "sensitive-sentinel") {
			t.Fatalf("pull failure: %q %v", path, err)
		}
		if body == "exec sleep 10" && err.Error() != "refused_chart: timeout" {
			t.Fatalf("timeout not identified: %v", err)
		}
		entries, _ := os.ReadDir(cache)
		if len(entries) != 0 {
			t.Fatal("failed pull retained temporary data")
		}
	}
	cache := t.TempDir()
	if _, err := (&Command{Binary: filepath.Join(cache, "missing")}).Acquire(pin, cache); err == nil || err.Error() != "refused_chart: helm_not_found" {
		t.Fatalf("missing Helm not identified: %v", err)
	}
	partial := filepath.Join(cache, "boundary-0.1.0")
	os.Mkdir(partial, 0o700)
	os.WriteFile(filepath.Join(partial, "keep"), []byte("partial"), 0o600)
	body := `mkdir "$9/boundary"; printf 'name: boundary\nversion: 0.1.0\n' > "$9/boundary/Chart.yaml"`
	if _, err := (&Command{Binary: executable(t, body)}).Acquire(pin, cache); err == nil || err.Error() != "refused_chart: cache_publish_failed" {
		t.Fatalf("incomplete concurrent target not identified: %v", err)
	}
	if contents, _ := os.ReadFile(filepath.Join(partial, "keep")); string(contents) != "partial" {
		t.Fatal("replaced or deleted another caller's incomplete cache")
	}
}
