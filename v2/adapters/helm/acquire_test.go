package helm_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/helm"
	"github.com/samclement/swhurl-platform/v2/adapters/helm/helmtest"
)

const repository = "kind: HelmRepository\nmetadata: {name: pinned}\nspec: {url: https://charts.example.test}\n"

func TestRepository(t *testing.T) {
	for _, suffix := range []string{"", "---\n# empty\n", "---\nkind: ConfigMap\n"} {
		got, err := helm.Repository([]byte(repository+suffix), "pinned")
		if err != nil || got != "https://charts.example.test" {
			t.Fatalf("repository: %q %v", got, err)
		}
	}
	for _, input := range []string{"", "[]", "[", "kind: ConfigMap", repository + "---\n[", repository + "---\n" + repository,
		strings.Replace(repository, "url: https://charts.example.test", "url: 12", 1),
		strings.Replace(repository, "name: pinned", "name: pinned, name: other", 1), strings.Repeat("a", (1<<20)+1)} {
		if path, err := helm.Repository([]byte(input), "pinned"); err == nil || path != "" {
			t.Fatalf("invalid input accepted: %q %v", path, err)
		}
	}
}

// The script is an executable boundary fake, not a general command runner inside production code.
func puller(t *testing.T, body string) *helm.Command {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "helm")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nset -eu\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return &helm.Command{Binary: binary}
}

const successfulPull = `test "$1" = pull
test "$2" = boundary
test "$3" = --repo
test "$4" = https://charts.example.test
test "$5" = --version
test "$6" = 0.1.0
test "$7" = --untar
test "$8" = --untardir
test "${10}" = --repository-cache
test "${11}" = "$9/repository"
test "${12}" = --repository-config
test "${13}" = "$9/repositories.yaml"
test "$#" = 13
mkdir "$9/boundary"
printf 'apiVersion: v2\nname: boundary\nversion: 0.1.0\n' > "$9/boundary/Chart.yaml"
`

func TestAcquisitionContract(t *testing.T) {
	helmtest.AcquisitionContract(t, func(t *testing.T) (helm.Acquirer, string) {
		return puller(t, successfulPull), t.TempDir()
	})
	helmtest.AcquisitionContract(t, func(t *testing.T) (helm.Acquirer, string) {
		return &helmtest.Acquisition{}, t.TempDir()
	})
}

func TestColdPullThenCacheWithoutHelm(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "cache with spaces")
	pin := helm.Pin{Name: "boundary", Version: "0.1.0", Repository: "https://charts.example.test"}
	path, err := puller(t, successfulPull).Acquire(pin, cache)
	if err != nil {
		t.Fatal(err)
	}
	cached, err := (&helm.Command{Binary: "/must/not/run"}).Acquire(pin, cache)
	if err != nil || path != cached {
		t.Fatalf("cached: %q %v", cached, err)
	}
	entries, _ := os.ReadDir(cache)
	if len(entries) != 1 || entries[0].Name() != "boundary-0.1.0" {
		t.Fatalf("temporary directory leaked: %v", entries)
	}
	pin.Version = "0.2.0"
	if _, err := (&helm.Command{Binary: "/must/not/run"}).Acquire(pin, cache); err == nil {
		t.Fatal("different version reused another pin")
	}
}

func TestFailedAndMismatchedPullNeverPublish(t *testing.T) {
	for _, body := range []string{"echo private-diagnostic >&2; exit 7", "exit 0", strings.Replace(successfulPull, "name: boundary", "name: other", 1)} {
		t.Run(body[:min(8, len(body))], func(t *testing.T) {
			cache := t.TempDir()
			path, err := puller(t, body).Acquire(helm.Pin{Name: "boundary", Version: "0.1.0", Repository: "https://charts.example.test"}, cache)
			if err == nil || path != "" || strings.Contains(err.Error(), "private-diagnostic") {
				t.Fatalf("failed pull: %q %v", path, err)
			}
			entries, _ := os.ReadDir(cache)
			if len(entries) != 0 {
				t.Fatalf("failed pull left files: %v", entries)
			}
		})
	}
}

func TestInvalidPinsAndMismatchedCacheFail(t *testing.T) {
	pin := helm.Pin{Name: "boundary", Version: "0.1.0", Repository: "https://charts.example.test"}
	for _, invalid := range []helm.Pin{{Name: "../x", Version: pin.Version, Repository: pin.Repository},
		{Name: pin.Name, Version: "../../x", Repository: pin.Repository}, {Name: pin.Name, Version: pin.Version, Repository: "file:///tmp"}} {
		cache := filepath.Join(t.TempDir(), "absent")
		if _, err := (&helm.Command{Binary: "/must/not/run"}).Acquire(invalid, cache); err == nil {
			t.Fatal("invalid pin accepted")
		}
		if _, err := os.Stat(cache); !os.IsNotExist(err) {
			t.Fatal("invalid pin touched cache")
		}
	}
	cache := t.TempDir()
	target := filepath.Join(cache, "boundary-0.1.0")
	os.Mkdir(target, 0o700)
	os.WriteFile(filepath.Join(target, "Chart.yaml"), []byte("name: other\nversion: 0.1.0\n"), 0o600)
	if _, err := (&helm.Command{Binary: "/must/not/run"}).Acquire(pin, cache); err == nil {
		t.Fatal("mismatched cached metadata accepted")
	}
}

func TestConcurrentPullsPublishOneCompleteChart(t *testing.T) {
	cache := t.TempDir()
	command := puller(t, successfulPull)
	pin := helm.Pin{Name: "boundary", Version: "0.1.0", Repository: "https://charts.example.test"}
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			if _, err := command.Acquire(pin, cache); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	entries, _ := os.ReadDir(cache)
	if len(entries) != 1 || entries[0].Name() != "boundary-0.1.0" {
		t.Fatalf("concurrent pull leaked files: %v", entries)
	}
}
