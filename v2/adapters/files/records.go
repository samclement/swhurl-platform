package files

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/samclement/swhurl-platform/v2/kernel"
)

// InstanceRecords is one instance's records as read: the folder they were read from, its own two records, and the
// app record it shares with the app's other environments. The bytes are not decoded: what they mean is the caller's.
type InstanceRecords struct {
	Application, Environment string
	App, Instance, Release   []byte
}

// Records is every instance a tree of repository paths holds under root, in app and environment order, whether the
// tree was read from an archive or from a directory. A folder is an instance exactly when it holds an instance
// record; an instance whose release or app record is missing refuses the whole tree rather than being read in part.
func Records(tree map[string][]byte, root string) ([]InstanceRecords, error) {
	found := []InstanceRecords{}
	for file, body := range tree {
		if application, environment, ok := kernel.InstanceRecordOf(root, file); ok {
			found = append(found, InstanceRecords{Application: application, Environment: environment, Instance: body})
		}
	}
	slices.SortFunc(found, func(a, b InstanceRecords) int {
		return cmp.Or(cmp.Compare(a.Application, b.Application), cmp.Compare(a.Environment, b.Environment))
	})
	for i := range found {
		one := &found[i]
		release, held := tree[kernel.ReleaseRecordPath(root, one.Application, one.Environment)]
		if !held {
			return nil, missing(one.Application+"/"+one.Environment, kernel.ReleaseRecordName)
		}
		app, held := tree[kernel.AppRecordPath(root, one.Application)]
		if !held {
			return nil, missing(one.Application, kernel.AppRecordName)
		}
		one.Release, one.App = release, app
	}
	return found, nil
}

// missing is the refusal of a tree that names an instance without one of the records it needs.
func missing(folder, record string) error {
	return fmt.Errorf("invalid: records: %s has no %s", folder, record)
}

// ReadRecords is every instance in a records directory, read as Records reads a tree: the directory's records are
// read into a tree rooted at dir, and nothing else in it is. A record that exists but cannot be read is an I/O failure.
func ReadRecords(dir string) ([]InstanceRecords, error) {
	root := filepath.ToSlash(filepath.Clean(dir))
	tree := map[string][]byte{}
	applications, err := folders(root)
	if err != nil {
		return nil, err
	}
	for _, application := range applications {
		if err := readApp(tree, root, application); err != nil {
			return nil, err
		}
	}
	return Records(tree, root)
}

// readApp reads one app folder's records into tree: each environment's instance and release, and the app record once
// any environment holds an instance.
func readApp(tree map[string][]byte, root, application string) error {
	environments, err := folders(root + "/" + application)
	if err != nil {
		return err
	}
	for _, environment := range environments {
		held, err := readRecord(tree, kernel.InstanceRecordPath(root, application, environment))
		if err != nil {
			return err
		}
		if !held {
			continue
		}
		if _, err := readRecord(tree, kernel.ReleaseRecordPath(root, application, environment)); err != nil {
			return err
		}
		if _, err := readRecord(tree, kernel.AppRecordPath(root, application)); err != nil {
			return err
		}
	}
	return nil
}

// folders is the names of the folders directly in one, in name order; a link to a folder is not one.
func folders(folder string) ([]string, error) {
	entries, err := os.ReadDir(filepath.FromSlash(folder))
	if err != nil {
		return nil, &Error{IO, folder, "cannot read the records folder"}
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

// readRecord reads one record into tree at its path, and reports whether it exists.
func readRecord(tree map[string][]byte, file string) (bool, error) {
	body, err := os.ReadFile(filepath.FromSlash(file))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	case err != nil:
		return false, &Error{IO, file, "cannot read the record"}
	}
	tree[file] = body
	return true, nil
}
