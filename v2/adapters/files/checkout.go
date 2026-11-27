package files

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/samclement/swhurl-platform/v2/kernel"
)

// Checkout is where a use case over a records tree finds what it reads: Root, the destination it writes under and
// addresses every file by; Records, the records tree, which must be inside Root because a use case writes back what
// it read; and the documents beside them, each a path the caller named. An empty document path is one the caller did
// not give. The paths are the caller's: a command takes them from its flags, a change from its working copy.
type Checkout struct {
	Root, Records                    string
	Profile, Calls, Egress, Previous string
}

// OffersFolder is the folder of an instance's records that holds the documents it publishes.
const OffersFolder = "offers"

// maxInput bounds a document read by path: a record or a platform document is a few kilobytes.
const maxInput = 1 << 20

// Read reads one document the caller named. A missing or unreadable file is an I/O failure; one larger than any
// document is refused. what names the document in either error, which never carries its content.
func Read(path, what string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, &Error{IO, what, "cannot read input file"}
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxInput+1))
	switch {
	case err != nil:
		return nil, &Error{IO, what, "cannot read input file"}
	case len(data) > maxInput:
		return nil, fmt.Errorf("invalid: %s: input file is larger than 1 MiB", what)
	}
	return data, nil
}

// Optional reads a document the caller may not have named: no path is no document, and the use case that needs one
// refuses its absence in its own words.
func Optional(path, what string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	return Read(path, what)
}

// Present reads a file the tree under Root may not hold, by its repository path: an absent file is nil.
func (c Checkout) Present(relative, what string) ([]byte, error) {
	path := filepath.Join(c.Root, filepath.FromSlash(relative))
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return Read(path, what)
}

// Relative is a path as Root addresses it. A writer resolves only the paths inside the destination it was given, so
// a records tree outside Root is refused before anything is read.
func (c Checkout) Relative(path string) (string, error) {
	relative, err := filepath.Rel(filepath.Clean(c.Root), filepath.Clean(path))
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid: records: the records tree must be under the destination --out")
	}
	return filepath.ToSlash(relative), nil
}

// Instances is every instance the records tree holds, as ReadRecords reads it.
func (c Checkout) Instances() ([]InstanceRecords, error) { return ReadRecords(c.Records) }

// Find is one instance's records among a tree's, or a refusal when the tree holds no such instance.
func Find(records []InstanceRecords, application, environment string) (InstanceRecords, error) {
	for _, one := range records {
		if one.Application == application && one.Environment == environment {
			return one, nil
		}
	}
	return InstanceRecords{}, fmt.Errorf("invalid: records: %s/%s has no %s", application, environment,
		kernel.InstanceRecordName)
}

// InstanceFolder is one instance's records with the rest of its folder a write keeps: where its instance record is,
// as Root addresses it, its seed when it has one, and the documents it publishes. A write either carries them or is
// refused by the compiler, never drops them.
type InstanceFolder struct {
	InstanceRecords
	InstancePath string
	EventsSecret []byte
	Offers       []kernel.File
}

// Folder reads the rest of one instance's folder.
func (c Checkout) Folder(record InstanceRecords) (InstanceFolder, error) {
	folder := filepath.Join(c.Records, record.Application, record.Environment)
	instancePath, err := c.Relative(filepath.Join(folder, kernel.InstanceRecordName))
	if err != nil {
		return InstanceFolder{}, err
	}
	one := InstanceFolder{InstanceRecords: record, InstancePath: instancePath}
	what := record.Application + "/" + record.Environment
	secret := filepath.Join(folder, kernel.EventsSecretFile)
	if _, err := os.Stat(secret); err == nil {
		if one.EventsSecret, err = Read(secret, what+" events secret"); err != nil {
			return InstanceFolder{}, err
		}
	}
	if one.Offers, err = ReadOffers(filepath.Join(folder, OffersFolder)); err != nil {
		return InstanceFolder{}, err
	}
	return one, nil
}

// Folders is every instance folder the records tree holds, in app and environment order.
func (c Checkout) Folders() ([]InstanceFolder, error) {
	records, err := c.Instances()
	if err != nil {
		return nil, err
	}
	found := make([]InstanceFolder, 0, len(records))
	for _, record := range records {
		one, err := c.Folder(record)
		if err != nil {
			return nil, err
		}
		found = append(found, one)
	}
	return found, nil
}

// ReadOffers reads the documents an instance publishes: the plain files of its offers folder, each named by its own
// name, in name order. An absent folder is no documents; a folder inside it is refused, since the documents are
// files.
func ReadOffers(folder string) ([]kernel.File, error) {
	entries, err := os.ReadDir(folder)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, &Error{IO, "offers", "cannot read " + folder}
	}
	documents := make([]kernel.File, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			return nil, fmt.Errorf("invalid: offers: %s is a folder", entry.Name())
		}
		data, err := Read(filepath.Join(folder, entry.Name()), "offers document")
		if err != nil {
			return nil, err
		}
		documents = append(documents, kernel.File{Path: entry.Name(), Document: data})
	}
	return documents, nil
}

// Names is the repository path of every file in one folder of the tree under Root, in name order; an absent folder
// holds none.
func (c Checkout) Names(relative string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(c.Root, filepath.FromSlash(relative)))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, &Error{IO, relative, "cannot read the folder"}
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, relative+"/"+entry.Name())
		}
	}
	return names, nil
}

// Holds is whether the tree under Root holds exactly one file's bytes. A missing file does not; a path the writer
// would refuse, or a file that cannot be read, is an error rather than an answer.
func (c Checkout) Holds(file kernel.File) (bool, error) {
	base, err := filepath.Abs(c.Root)
	if err != nil {
		return false, &Error{IO, "destination", "cannot resolve the destination"}
	}
	target, err := Target(base, file.Path)
	if err != nil {
		return false, err
	}
	existing, err := os.ReadFile(target)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, &Error{IO, file.Path, "cannot read the existing file"}
	}
	return bytes.Equal(existing, file.Document), nil
}
