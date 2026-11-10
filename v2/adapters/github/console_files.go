package github

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"maps"
	"net/http"
	"path"
	"slices"
	"strconv"
	"sync"
)

// The bounds of one targeted read: every record the console reads is far smaller than maxBlob, and a page waits for
// at most maxBlobReads blobs at once.
const (
	maxBlob      = 1 << 20
	maxBlobReads = 4
)

// wireTree is GitHub's recursive tree listing. Truncated and Tree are pointers so that an answer without them is
// malformed rather than a complete, empty tree.
type wireTree struct {
	SHA       string      `json:"sha"`
	Truncated *bool       `json:"truncated"`
	Tree      *[]wireNode `json:"tree"`
}

// wireNode is one path of a listing: a blob (file or link, by mode), a tree, or a commit (a submodule).
type wireNode struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
	Size *int   `json:"size"`
}

// wantedBlob is one blob a read takes: its declared size and every requested path that holds it.
type wantedBlob struct {
	size  int
	paths []string
}

func (a *API) CommitFiles(ctx context.Context, repository, token, revision string, paths []string) (map[string][]byte, error) {
	if !commitID(revision) || len(paths) == 0 || slices.ContainsFunc(paths, func(p string) bool { return !treePath(p) }) {
		return nil, &Error{"invalid files reference"}
	}
	api := &consoleAPI{a.client}
	wanted, err := api.listing(ctx, repository, token, revision, paths)
	if err != nil {
		return nil, err
	}
	return api.blobs(ctx, repository, token, wanted)
}

// treePath is a repository path as a listing names it: relative, clean, and inside the tree.
func treePath(file string) bool {
	return file != "" && file != "." && file != ".." && file[0] != '/' && path.Clean(file) == file &&
		!(len(file) > 3 && file[:3] == "../")
}

// listing is the requested paths a commit's complete tree holds as regular files, by blob. A truncated or malformed
// listing is refused rather than read as absence.
func (a *consoleAPI) listing(ctx context.Context, repository, token, revision string, paths []string) (map[string]*wantedBlob, error) {
	var found wireTree
	if err := a.json(ctx, repository, "/git/trees/"+revision+"?recursive=1", token, &found); err != nil {
		return nil, err
	}
	if found.Tree == nil || found.Truncated == nil || !commitID(found.SHA) {
		return nil, &Error{"tree listing malformed"}
	}
	if *found.Truncated {
		return nil, &Error{"tree listing truncated"}
	}
	seen := map[string]bool{}
	for _, node := range *found.Tree {
		if !treePath(node.Path) || seen[node.Path] || !commitID(node.SHA) {
			return nil, &Error{"tree listing malformed"}
		}
		seen[node.Path] = true
	}
	return wantedOf(*found.Tree, paths)
}

// wantedOf is the blobs holding the requested paths. Links and submodules are not files, as the archive read skips
// them.
func wantedOf(nodes []wireNode, paths []string) (map[string]*wantedBlob, error) {
	wanted := map[string]*wantedBlob{}
	for _, node := range nodes {
		if !slices.Contains(paths, node.Path) || node.Type != "blob" || (node.Mode != "100644" && node.Mode != "100755") {
			continue
		}
		if node.Size == nil || *node.Size < 0 || *node.Size > maxBlob {
			return nil, &Error{"tree listing names a file too large or of no size"}
		}
		blob := wanted[node.SHA]
		if blob == nil {
			blob = &wantedBlob{size: *node.Size}
			wanted[node.SHA] = blob
		}
		blob.paths = append(blob.paths, node.Path)
	}
	return wanted, nil
}

// blobs reads each wanted blob once, at most maxBlobReads at a time, and answers every path that holds it. The first
// failure stops the reads still to start and is the answer: never a partial set of files.
func (a *consoleAPI) blobs(ctx context.Context, repository, token string, wanted map[string]*wantedBlob) (map[string][]byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	var failed error
	found := map[string][]byte{}
	slots := make(chan struct{}, maxBlobReads)
	var running sync.WaitGroup
	for _, id := range slices.Sorted(maps.Keys(wanted)) {
		slots <- struct{}{}
		if ctx.Err() != nil {
			<-slots
			break
		}
		running.Go(func() {
			defer func() { <-slots }()
			body, err := a.blob(ctx, repository, token, id, wanted[id].size)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if failed == nil {
					failed = err
					cancel()
				}
				return
			}
			for _, file := range wanted[id].paths {
				found[file] = body
			}
		})
	}
	running.Wait()
	if failed == nil && ctx.Err() != nil {
		failed = &Error{"request failed"}
	}
	if failed != nil {
		return nil, failed
	}
	return found, nil
}

// blob is one blob's bytes, read by its immutable id and checked against it and the size the listing declared.
func (a *consoleAPI) blob(ctx context.Context, repository, token, id string, size int) ([]byte, error) {
	body, status, err := a.get(ctx, repository, "/git/blobs/"+id, token, "application/vnd.github.raw+json", maxBlob)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, &Error{"HTTP " + strconv.Itoa(status)}
	}
	if len(body) != size || blobID(body) != id {
		return nil, &Error{"blob does not match its identity"}
	}
	return body, nil
}

// blobID is Git's identity of a file's bytes.
func blobID(body []byte) string {
	sum := sha1.New()
	fmt.Fprintf(sum, "blob %d\x00", len(body))
	sum.Write(body)
	return hex.EncodeToString(sum.Sum(nil))
}
