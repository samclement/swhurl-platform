package orchestration

import (
	"context"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/samclement/swhurl-platform/v2/pages"
)

// keptEvidence is how many renders (one unit at one commit) ConsoleEvidence keeps when Max is zero: four units for
// each of the sixty merged pull requests the server remembers.
const keptEvidence = 240

// ConsoleEvidence is the instances Activity follows a merged change by at its merge commit: each unit's render read
// from the four files it takes (unitFiles), not the commit's whole tree, and kept once read, since a commit never
// changes. Only a complete read is kept, an instance the commit was proved not to hold included; a failed read keeps
// nothing, so the next page reads again. It never adds to Bundle's trees, which stay whole: a commit whose tree
// Bundle already holds is answered from it, and Bundle's source does every read.
type ConsoleEvidence struct {
	Bundle *ConsoleBundle
	// Max is how many renders are kept, the oldest going first; zero is keptEvidence.
	Max int

	mu      sync.Mutex
	renders map[evidenceKey]pages.Render
	order   []evidenceKey
}

// evidenceKey is one unit at one commit: a render kept for one unit says nothing of another at the same commit.
type evidenceKey struct{ commit, unit string }

// Cached is whether every unit's render at commit is in hand, so a page's bounded reads are not spent on it: a unit
// not read yet makes the commit one to fetch, however many others are kept.
func (e *ConsoleEvidence) Cached(commit string, units []string) bool {
	if e.Bundle.Cached(commit) {
		return true
	}
	_, missing := e.inHand(commit, units)
	return len(missing) == 0
}

// Renders is each unit's render at commit, as ConsoleRenders reads the commit's tree: a unit the commit holds no
// instance for is the zero Render (absent). The units not in hand are read together, in one read of their files.
func (e *ConsoleEvidence) Renders(ctx context.Context, commit string, units []string) (map[string]pages.Render, error) {
	if tree, held := e.Bundle.held(commit); held {
		return unitRenders(tree, units)
	}
	found, missing := e.inHand(commit, units)
	if len(missing) == 0 {
		return found, nil
	}
	tree := map[string][]byte{}
	if paths := evidencePaths(missing); len(paths) > 0 {
		read, err := e.Bundle.Source.Files(ctx, commit, paths)
		if err != nil {
			return nil, err
		}
		tree = read
	}
	read, err := unitRenders(tree, missing)
	if err != nil {
		return nil, err
	}
	e.keep(commit, read)
	maps.Copy(found, read)
	return found, nil
}

// inHand is the units' renders kept at commit, and the units, once each, that are not.
func (e *ConsoleEvidence) inHand(commit string, units []string) (map[string]pages.Render, []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	found := map[string]pages.Render{}
	var missing []string
	for _, unit := range units {
		if render, kept := e.renders[evidenceKey{commit, unit}]; kept {
			found[unit] = render
		} else if !slices.Contains(missing, unit) {
			missing = append(missing, unit)
		}
	}
	return found, missing
}

// keep remembers one complete read's renders, forgetting the oldest once the bound is reached.
func (e *ConsoleEvidence) keep(commit string, read map[string]pages.Render) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.renders == nil {
		e.renders = map[evidenceKey]pages.Render{}
	}
	limit := e.Max
	if limit <= 0 {
		limit = keptEvidence
	}
	for _, unit := range slices.Sorted(maps.Keys(read)) {
		key := evidenceKey{commit, unit}
		if _, raced := e.renders[key]; raced {
			continue
		}
		for len(e.order) >= limit {
			delete(e.renders, e.order[0])
			e.order = e.order[1:]
		}
		e.renders[key] = read[unit]
		e.order = append(e.order, key)
	}
}

// unitFiles is the files one instance's render takes (renderOf): its app, instance and release records and its
// committed HelmRelease. False is a unit that cannot name a v2 instance, which no commit's tree holds.
func unitFiles(unit string) ([]string, bool) {
	app, env, found := strings.Cut(unit, "/")
	if !found || !pathSegment(app) || !pathSegment(env) {
		return nil, false
	}
	return []string{consoleRecords + "/" + app + "/app.json", consoleRecords + "/" + unit + "/instance.json",
		consoleRecords + "/" + unit + "/release.json", consoleApps + "/" + unit + "/helmrelease.json"}, true
}

// pathSegment is one folder name a record path may hold.
func pathSegment(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\")
}

// evidencePaths is every file the units' renders take, once each.
func evidencePaths(units []string) []string {
	var paths []string
	for _, unit := range units {
		files, _ := unitFiles(unit)
		for _, file := range files {
			if !slices.Contains(paths, file) {
				paths = append(paths, file)
			}
		}
	}
	return paths
}

// unitRenders is each unit's render in a tree, from only the files that unit takes: what ConsoleRenders finds for it
// in the whole tree, and the zero Render where it finds none.
func unitRenders(tree map[string][]byte, units []string) (map[string]pages.Render, error) {
	taken := map[string][]byte{}
	for _, file := range evidencePaths(units) {
		if body, held := tree[file]; held {
			taken[file] = body
		}
	}
	all, err := ConsoleRenders(taken)
	if err != nil {
		return nil, err
	}
	found := make(map[string]pages.Render, len(units))
	for _, unit := range units {
		found[unit] = all[unit]
	}
	return found, nil
}
