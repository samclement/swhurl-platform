package files

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
)

// maxArchiveFile bounds one file read from an archive, and maxArchive everything the archive decompresses to: its
// headers, the entries the read skips and the files it keeps. The caller's download bounds only the compressed size.
const (
	maxArchiveFile = 8 << 20
	maxArchive     = 128 << 20
)

// errArchiveTooLarge is an archive that decompresses to more than its bound.
var errArchiveTooLarge = errors.New("its contents are too large")

// ReadArchive reads the regular files of a repository archive that keep selects, by repository path and in path
// order, each with the mode the archive states. The archive may carry GitHub's leading folder (prefixed: the tarball)
// or none (a checkout's `git archive`), gzipped or not. A path that would leave the tree refuses the whole archive,
// and a link is never followed: only regular files are read.
func ReadArchive(content []byte, prefixed bool, keep func(file string) bool) ([]TreeFile, error) {
	return readArchive(content, prefixed, keep, maxArchive)
}

// readArchive is ReadArchive refusing an archive that decompresses to more than limit bytes.
func readArchive(content []byte, prefixed bool, keep func(string) bool, limit int64) ([]TreeFile, error) {
	reader, closeArchive := archiveReader(content)
	defer closeArchive()
	archive := tar.NewReader(&boundedReader{reader, limit})
	found := []TreeFile{}
	for {
		entry, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("cannot read repository archive: %w", err)
		}
		file, ok, err := archiveEntry(archive, entry, prefixed, keep)
		if err != nil {
			return nil, err
		}
		if ok {
			found = append(found, file)
		}
	}
	slices.SortFunc(found, func(a, b TreeFile) int { return strings.Compare(a.Path, b.Path) })
	return found, nil
}

// archiveEntry is one entry as the read keeps it, false for one it skips, or why the archive is refused.
func archiveEntry(archive *tar.Reader, entry *tar.Header, prefixed bool, keep func(string) bool) (TreeFile, bool, error) {
	if leaving(entry.Name) {
		return TreeFile{}, false, fmt.Errorf("cannot read repository archive: %s leaves the repository", entry.Name)
	}
	file, ok := archivePath(entry.Name, prefixed)
	if !ok || entry.Typeflag != tar.TypeReg || !keep(file) {
		return TreeFile{}, false, nil
	}
	body, err := io.ReadAll(io.LimitReader(archive, maxArchiveFile+1))
	if err != nil {
		return TreeFile{}, false, fmt.Errorf("cannot read repository archive: %w", err)
	}
	if len(body) > maxArchiveFile {
		return TreeFile{}, false, fmt.Errorf("cannot read repository archive: %s is too large", file)
	}
	return TreeFile{Path: file, Executable: entry.Mode&0o111 != 0, Body: body}, true, nil
}

// archiveReader reads a repository archive as tar, gzipped or not: GitHub serves a tarball, a checkout's `git archive`
// need not be compressed.
func archiveReader(content []byte) (io.Reader, func()) {
	gzipped, err := gzip.NewReader(bytes.NewReader(content))
	if err != nil {
		return bytes.NewReader(content), func() {}
	}
	return gzipped, func() { gzipped.Close() }
}

// boundedReader reads at most left more bytes, and refuses with errArchiveTooLarge when the stream holds more.
type boundedReader struct {
	reader io.Reader
	left   int64
}

func (b *boundedReader) Read(p []byte) (int, error) {
	if b.left == 0 {
		var probe [1]byte
		if n, err := io.ReadFull(b.reader, probe[:]); n == 0 {
			return 0, err
		}
		return 0, errArchiveTooLarge
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.reader.Read(p)
	b.left -= int64(n)
	return n, err
}

// leaving reports whether an entry's name would escape the repository, which no archive GitHub serves may contain.
func leaving(name string) bool {
	return strings.HasPrefix(name, "/") || strings.Contains(name, "..")
}

// archivePath is an entry's repository path, with GitHub's leading folder removed, or false for a path that names
// nothing.
func archivePath(name string, prefixed bool) (string, bool) {
	clean := path.Clean("/" + name)
	if clean == "/" {
		return "", false
	}
	parts := strings.Split(strings.TrimPrefix(clean, "/"), "/")
	if prefixed {
		if len(parts) < 2 {
			return "", false
		}
		parts = parts[1:]
	}
	return strings.Join(parts, "/"), true
}
