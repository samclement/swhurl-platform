package main

import (
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/samclement/swhurl-platform/v2/kernel"
)

// names checks that a rendered app uses only the names the platform provides: every file under --app-dir is
// searched for SWHURL_* and platform.swhurl.com/* names, and each one the --catalogue does not list is printed as
// "NAME (PATH)", PATH the first file using it in lexical order, sorted by name. The stack templates' check runs it
// over each app type the templates render. It reads only, inside DIR: a symlink that leaves DIR is a failed read.
func (c cli) names(r request) int {
	data, status := c.read(r.catalogue, "names catalogue")
	if status != 0 {
		return status
	}
	catalogue, err := kernel.DecodeNameCatalogue(data)
	if err != nil {
		return c.failure(err)
	}
	first, err := unlistedNames(catalogue, r.appDir)
	if err != nil {
		fmt.Fprintf(c.diagnostic, "io: names: %v\n", err)
		return 1
	}
	if len(first) == 0 {
		return 0
	}
	lines := make([]string, 0, len(first))
	for _, name := range slices.Sorted(maps.Keys(first)) {
		lines = append(lines, fmt.Sprintf("%s (%s)", name, first[name]))
	}
	if status := emit(c.out, c.diagnostic, []byte(strings.Join(lines, "\n"))); status != 0 {
		return status
	}
	return 1
}

// unlistedNames is each name the files under dir use that the catalogue does not list, with the first file using it.
func unlistedNames(catalogue kernel.NameCatalogue, dir string) (map[string]string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	first := map[string]string{}
	tree := root.FS()
	err = fs.WalkDir(tree, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		text, err := fs.ReadFile(tree, path)
		if err != nil {
			return err
		}
		for _, name := range catalogue.Unlisted(text) {
			if _, seen := first[name]; !seen {
				first[name] = path
			}
		}
		return nil
	})
	return first, err
}
