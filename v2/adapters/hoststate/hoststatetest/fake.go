// Package hoststatetest is the fake for hoststate.Store and the contract suite both implementations run.
package hoststatetest

import (
	"bytes"
	"errors"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/hoststate"
)

// Scenario is what the contract runs against both implementations: a file that holds Held, a path where none exists
// (its directory need not exist either), a path that cannot be read, and one that cannot be replaced.
type Scenario struct {
	Existing   string
	Held       []byte
	Absent     string
	Unreadable string
	Unwritable string
}

// Fake keeps files in memory and records every read and replacement.
type Fake struct {
	Files      map[string][]byte
	Unreadable map[string]bool // paths whose read fails
	Unwritable map[string]bool // paths whose replacement fails, leaving what they held
	Reads      []string
	Replaced   []string
}

var _ hoststate.Store = (*Fake)(nil)

// New returns a fake that holds no file.
func New() *Fake {
	return &Fake{Files: map[string][]byte{}, Unreadable: map[string]bool{}, Unwritable: map[string]bool{}}
}

// Read answers a copy of the file, false for one it does not hold, and fails as the adapter does for one it cannot
// read.
func (f *Fake) Read(path string) ([]byte, bool, error) {
	f.Reads = append(f.Reads, path)
	if f.Unreadable[path] {
		return nil, false, &hoststate.Error{Code: hoststate.Unreadable, Reason: "cannot read"}
	}
	data, found := f.Files[path]
	return bytes.Clone(data), found, nil
}

// Replace keeps a copy of data, or fails as the adapter does and keeps what the path held.
func (f *Fake) Replace(path string, data []byte) error {
	f.Replaced = append(f.Replaced, path)
	if f.Unwritable[path] {
		return &hoststate.Error{Code: hoststate.Unwritable, Reason: "cannot prepare the directory"}
	}
	f.Files[path] = bytes.Clone(data)
	return nil
}

// Contract is the behaviour every hoststate.Store must have: an existing file reads back byte for byte, an absent one
// is absent with no error, a replacement reads back and a second replaces the first, a file that cannot be read is an
// error and never absence, and a failed replacement is an error that leaves nothing new to read. open returns the
// store with the scenario's files in place.
func Contract(t *testing.T, scenario Scenario, open func(*testing.T, Scenario) hoststate.Store) {
	t.Helper()
	store := open(t, scenario)
	reads(t, store, scenario.Existing, scenario.Held)
	if data, found, err := store.Read(scenario.Absent); err != nil || found || data != nil {
		t.Fatalf("an absent file read %q, %t, %v", data, found, err)
	}
	for _, data := range [][]byte{[]byte(`{"alerting_since":1.5,"last_alert":2.0}` + "\n"), []byte("{}\n")} {
		if err := store.Replace(scenario.Absent, data); err != nil {
			t.Fatalf("replacing: %v", err)
		}
		reads(t, store, scenario.Absent, data)
	}
	refuses(t, store, scenario)
}

// refuses is that a file that cannot be read is an error, never absence, and that a failed replacement is an error
// that leaves nothing new to read.
func refuses(t *testing.T, store hoststate.Store, scenario Scenario) {
	t.Helper()
	if _, found, err := store.Read(scenario.Unreadable); !failed(err, hoststate.Unreadable) || found {
		t.Fatalf("an unreadable file read %t, %v", found, err)
	}
	if err := store.Replace(scenario.Unwritable, []byte("new\n")); !failed(err, hoststate.Unwritable) {
		t.Fatalf("an unwritable file was replaced: %v", err)
	}
	if data, found, _ := store.Read(scenario.Unwritable); found && bytes.Equal(data, []byte("new\n")) {
		t.Fatal("a failed replacement left its bytes")
	}
}

// reads is that path holds exactly want.
func reads(t *testing.T, store hoststate.Store, path string, want []byte) {
	t.Helper()
	data, found, err := store.Read(path)
	if err != nil || !found || !bytes.Equal(data, want) {
		t.Fatalf("read %q, %t, %v; want %q", data, found, err, want)
	}
}

// failed is whether err is the adapter's error with this code.
func failed(err error, code string) bool {
	var failure *hoststate.Error
	return errors.As(err, &failure) && failure.Code == code
}
