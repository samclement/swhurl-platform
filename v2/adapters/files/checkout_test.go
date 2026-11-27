package files_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
)

// A folder is empty when it is absent or holds nothing, and a folder holding only an empty folder is not; one that
// cannot be read is an I/O failure naming it.
func TestEmpty(t *testing.T) {
	root := t.TempDir()
	for _, folder := range []string{"empty", "nested/staging", "holding"} {
		if err := os.MkdirAll(filepath.Join(root, folder), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "holding", "app.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		folder string
		empty  bool
	}{
		{"absent", true},
		{"empty", true},
		{"nested", false},
		{"holding", false},
	} {
		empty, err := files.Empty(filepath.Join(root, test.folder), "records")
		if err != nil || empty != test.empty {
			t.Errorf("%s: empty %v, %v; want %v", test.folder, empty, err, test.empty)
		}
	}
	file := filepath.Join(root, "holding", "app.json")
	_, err := files.Empty(file, "records")
	var failure *files.Error
	if !errors.As(err, &failure) || failure.Code != files.IO || err.Error() != "io: records: cannot read "+file {
		t.Errorf("a folder that cannot be read gave %v", err)
	}
}

// One instance's record is read alone, and an instance the tree does not hold is an I/O failure naming it.
func TestInstance(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "v2-records", "web", "staging")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "instance.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	checkout := files.Checkout{Root: root, Records: filepath.Join(root, "v2-records")}
	if data, err := checkout.Instance("web", "staging"); err != nil || string(data) != "{}" {
		t.Errorf("the record read as %q, %v", data, err)
	}
	_, err := checkout.Instance("web", "prod")
	var failure *files.Error
	if !errors.As(err, &failure) || failure.Code != files.IO ||
		err.Error() != "io: instance: cannot read the record of web/prod" {
		t.Errorf("an absent record gave %v", err)
	}
}
