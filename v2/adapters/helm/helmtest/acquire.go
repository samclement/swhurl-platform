package helmtest

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/helm"
)

// Acquisition records pins without starting Helm, writing a cache or using the network.
type Acquisition struct {
	Pins   []helm.Pin
	Caches []string
	Err    error
}

var _ helm.Acquirer = (*Acquisition)(nil)

func (f *Acquisition) Acquire(pin helm.Pin, cache string) (string, error) {
	f.Pins = append(f.Pins, pin)
	f.Caches = append(f.Caches, cache)
	if f.Err != nil {
		return "", f.Err
	}
	if pin.Name == "" || pin.Version == "" || pin.Repository == "" || cache == "" {
		return "", fmt.Errorf("refused_chart: invalid_pin")
	}
	return filepath.Abs(filepath.Join(cache, pin.Name+"-"+pin.Version))
}

// AcquisitionContract proves repeatable paths, separate pins and atomic refusal for real and fake acquisition.
func AcquisitionContract(t *testing.T, open func(*testing.T) (helm.Acquirer, string)) {
	t.Helper()
	acquirer, cache := open(t)
	pin := helm.Pin{Name: "boundary", Version: "0.1.0", Repository: "https://charts.example.test"}
	first, err := acquirer.Acquire(pin, cache)
	if err != nil || first != filepath.Join(cache, "boundary-0.1.0") {
		t.Fatalf("acquire: %q, %v", first, err)
	}
	second, err := acquirer.Acquire(pin, cache)
	if err != nil || first != second {
		t.Fatal("acquisition not repeatable")
	}
	pin.Name = ""
	if path, err := acquirer.Acquire(pin, cache); err == nil || path != "" {
		t.Fatal("invalid pin returned a path")
	}
}
