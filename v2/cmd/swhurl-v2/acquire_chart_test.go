package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/helm/helmtest"
)

func TestChartCommandUsesNamedInputsAndReportsOnlyPath(t *testing.T) {
	profile, err := os.ReadFile("../../testdata/profile.json")
	if err != nil {
		t.Fatal(err)
	}
	const repos = "kind: HelmRepository\nmetadata: {name: bjw-s}\nspec: {url: https://from-requested-checkout.example.test}\n"
	var out, diagnostic bytes.Buffer
	fake := &helmtest.Acquisition{}
	reads := []string{}
	c := cli{acquirer: fake, readFile: func(path string) ([]byte, error) {
		reads = append(reads, path)
		switch path {
		case "checkout/profile.json":
			return profile, nil
		case "checkout/repositories.yaml":
			return []byte(repos), nil
		}
		return nil, errors.New("unexpected read")
	}}
	args := []string{"chart", "--profile", "checkout/profile.json", "--repositories", "checkout/repositories.yaml", "--cache", t.TempDir()}
	status := runWith(args, &out, &diagnostic, c)
	if status != 0 || len(fake.Pins) != 1 || diagnostic.Len() != 0 {
		t.Fatalf("chart: %d %s %s pins=%v reads=%v", status, &out, &diagnostic, fake.Pins, reads)
	}
	if fake.Pins[0].Repository != "https://from-requested-checkout.example.test" || !filepath.IsAbs(strings.TrimSpace(out.String())) {
		t.Fatalf("wrong chart result: %s %v", &out, fake.Pins)
	}
	for _, failure := range []string{"pull", "repositories", "profile"} {
		out.Reset()
		diagnostic.Reset()
		fake.Pins = nil
		fake.Err = errors.New("refused_chart: pull_failed")
		previous := c.readFile
		c.readFile = func(path string) ([]byte, error) {
			if strings.Contains(path, failure) {
				return []byte("["), nil
			}
			return previous(path)
		}
		if code := runWith(args, &out, &diagnostic, c); code == 0 || out.Len() != 0 || diagnostic.Len() == 0 {
			t.Fatalf("%s failure not atomic: %d %s %s", failure, code, &out, &diagnostic)
		}
		if failure != "pull" && len(fake.Pins) != 0 {
			t.Fatal("acquisition ran after invalid input")
		}
		c.readFile = previous
	}
}

func TestChartResolvesTheRealProfileAndRepositoryRegistry(t *testing.T) {
	var out, diagnostic bytes.Buffer
	fake := &helmtest.Acquisition{}
	args := []string{"chart", "--profile", "../../testdata/profile.json", "--repositories",
		"../../../clusters/home/flux-system/sources/helmrepositories.yaml", "--cache", t.TempDir()}
	status := runWith(args, &out, &diagnostic, cli{acquirer: fake, readFile: readInput})
	if status != 0 || len(fake.Pins) != 1 {
		t.Fatalf("real metadata: %d %s", status, &diagnostic)
	}
	if pin := fake.Pins[0]; pin.Name != "app-template" || pin.Repository != "https://bjw-s-labs.github.io/helm-charts" {
		t.Fatalf("wrong real pin: %+v", pin)
	}
}

func TestChartUsageFailsBeforeReadsOrAcquisition(t *testing.T) {
	for _, args := range [][]string{{"chart"}, {"chart", "--profile", "p", "--repositories", "r"},
		{"chart", "--profile", "p", "--repositories", "r", "--cache", "c", "extra"},
		{"chart", "--profile", "p", "--profile", "p", "--repositories", "r", "--cache", "c"}} {
		var out, diagnostic bytes.Buffer
		fake := &helmtest.Acquisition{}
		c := cli{acquirer: fake, readFile: func(string) ([]byte, error) { t.Fatal("usage read input"); return nil, nil }}
		if status := runWith(args, &out, &diagnostic, c); status != 2 || len(fake.Pins) != 0 || out.Len() != 0 {
			t.Fatalf("usage: %v %d", args, status)
		}
	}
}
