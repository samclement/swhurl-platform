package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/certissuer"
)

// platformCertsCheckout is a checkout holding this repository's own issuers and settings file.
func platformCertsCheckout(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{certissuer.IssuersDir, certissuer.Path} {
		source := filepath.Join("..", "..", "..", filepath.FromSlash(name))
		tree := []files.TreeFile{}
		if info, err := os.Stat(source); err != nil {
			t.Fatal(err)
		} else if info.IsDir() {
			found, err := files.Snapshot(source)
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range found {
				tree = append(tree, files.TreeFile{Path: name + "/" + file.Path, Body: file.Body})
			}
		} else {
			body, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			tree = append(tree, files.TreeFile{Path: name, Body: body})
		}
		if err := files.Unpack(root, tree); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func runPlatformCerts(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, diagnostic bytes.Buffer
	status := runWith(append([]string{"platform-certs"}, args...), &out, &diagnostic, cli{writer: files.New()})
	return status, out.String(), diagnostic.String()
}

func TestPlatformCertsEditsTheCheckoutsOneLine(t *testing.T) {
	root := platformCertsCheckout(t)
	path := filepath.Join(root, filepath.FromSlash(certissuer.Path))
	before, _ := os.ReadFile(path)
	if !bytes.Contains(before, []byte("\n  CERT_ISSUER: letsencrypt-prod\n")) {
		t.Fatalf("the repository's settings no longer name letsencrypt-prod:\n%s", before)
	}
	status, out, diagnostic := runPlatformCerts(t, "--root", root, "--issuer", "letsencrypt-staging", "--dry-run")
	if after, _ := os.ReadFile(path); status != 0 || diagnostic != "" || !bytes.Equal(after, before) ||
		out != "[INFO] CERT_ISSUER would update to letsencrypt-staging in "+certissuer.Path+"\n"+certissuer.Reminder+"\n" {
		t.Fatalf("dry run: exit %d\n%s%s", status, out, diagnostic)
	}
	status, out, diagnostic = runPlatformCerts(t, "--root", root, "--issuer", "letsencrypt-staging")
	after, _ := os.ReadFile(path)
	want := strings.Replace(string(before), "  CERT_ISSUER: letsencrypt-prod\n", "  CERT_ISSUER: letsencrypt-staging\n", 1)
	if status != 0 || diagnostic != "" || string(after) != want ||
		out != "[INFO] CERT_ISSUER updated to letsencrypt-staging in "+certissuer.Path+"\n"+certissuer.Reminder+"\n" {
		t.Fatalf("update: exit %d\n%s%s\n%s", status, out, diagnostic, after)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".swhurl-v2-*")); len(leftovers) != 0 {
		t.Fatalf("temporary file left behind: %q", leftovers)
	}
	status, out, _ = runPlatformCerts(t, "--root", root, "--issuer", "letsencrypt-staging")
	if status != 0 || out != "[INFO] CERT_ISSUER already set to letsencrypt-staging\n"+certissuer.Reminder+"\n" {
		t.Fatalf("already set: exit %d\n%s", status, out)
	}
}

func TestPlatformCertsRefusesWithOneErrorLine(t *testing.T) {
	root := platformCertsCheckout(t)
	status, out, diagnostic := runPlatformCerts(t, "--root", root, "--issuer", "selfsigned")
	if status != 1 || out != "" ||
		diagnostic != "[ERROR] unknown issuer 'selfsigned'; expected one of letsencrypt-prod, letsencrypt-staging\n" {
		t.Fatalf("exit %d\n%s%s", status, out, diagnostic)
	}
	status, _, diagnostic = runPlatformCerts(t, "--root", t.TempDir(), "--issuer", "letsencrypt-prod")
	if status != 1 || diagnostic != "[ERROR] unknown issuer 'letsencrypt-prod'; expected one of \n" {
		t.Fatalf("an empty checkout: exit %d\n%s", status, diagnostic)
	}
	if status, _, _ := runPlatformCerts(t, "--root", root); status != 2 {
		t.Fatalf("no issuer: exit %d", status)
	}
}
