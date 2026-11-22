package delivery

import (
	"os"
	"path/filepath"
	"testing"
)

// fixture reads one document from the kernel's own fixtures. (Copied from orchestration/deploy_test.go.)
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}
