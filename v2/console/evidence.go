package console

import (
	"context"
	"strings"

	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/pages"
)

// MergeTarget is one unit's render at the commit that merged its change: the target Activity follows the change to,
// read once and then kept in the job's record (Jobs.Capture). It reads only the four files the render takes
// (unitFiles), or the commit's whole tree when the bundle already holds it, and finds what ConsoleRenders finds for the
// unit there: the zero Render when the commit holds no instance for it, which a unit that cannot name one is without
// a read.
func MergeTarget(ctx context.Context, bundle *Bundle, commit, unit string) (pages.Render, error) {
	tree, held := bundle.Held(commit)
	files, named := unitFiles(unit)
	if !held && named {
		read, err := bundle.Source.Files(ctx, commit, files)
		if err != nil {
			return pages.Render{}, err
		}
		tree = read
	}
	taken := map[string][]byte{}
	for _, file := range files {
		if body, found := tree[file]; found {
			taken[file] = body
		}
	}
	found, err := ConsoleRenders(taken)
	return found[unit], err
}

// unitFiles is the files one instance's render takes (renderOf): its app, instance and release records and its
// committed HelmRelease. False is a unit that cannot name a v2 instance, which no commit's tree holds.
func unitFiles(unit string) ([]string, bool) {
	app, env, found := strings.Cut(unit, "/")
	if !found || !pathSegment(app) || !pathSegment(env) {
		return nil, false
	}
	return []string{kernel.AppRecordPath(kernel.RecordsRoot, app), kernel.InstanceRecordPath(kernel.RecordsRoot, app, env),
		kernel.ReleaseRecordPath(kernel.RecordsRoot, app, env), pages.ConsoleAppsRoot + "/" + unit + "/helmrelease.json"}, true
}

// pathSegment is one folder name a record path may hold.
func pathSegment(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\")
}
