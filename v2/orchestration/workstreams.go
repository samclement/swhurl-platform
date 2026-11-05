package orchestration

import (
	"github.com/samclement/swhurl-platform/v2/adapters/git"
	"github.com/samclement/swhurl-platform/v2/workstreams"
)

// CheckWorkstreams compares the workstream record with one checkout: Git says what the checkout holds, read supplies
// the text of each Markdown file workstreams.Needs names, round by round, and workstreams.Check judges them. It reads
// only what the check uses and writes nothing. A refused Git read or a failed file read stops it with that error;
// findings are results, not errors.
func CheckWorkstreams(root string, reader git.Reader, read func(path string) ([]byte, error)) ([]workstreams.Result, error) {
	checkout, err := reader.Read(root)
	if err != nil {
		return nil, err
	}
	facts := workstreams.Checkout{Tracked: checkout.Tracked, Present: checkout.Present, Uncommitted: checkout.Uncommitted,
		Shallow: checkout.Shallow, Commits: checkout.Commits, Markdown: map[string]string{}}
	// Each round reads files not yet held, so the loop ends once the present files run out.
	for needed := workstreams.Needs(facts); len(needed) > 0; needed = workstreams.Needs(facts) {
		for _, path := range needed {
			text, err := read(path)
			if err != nil {
				return nil, err
			}
			facts.Markdown[path] = string(text)
		}
	}
	return workstreams.Check(facts), nil
}
