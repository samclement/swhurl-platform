package console

import (
	"context"
	"errors"
	"github.com/samclement/swhurl-platform/v2/orchestration"
	"github.com/samclement/swhurl-platform/v2/pages"
)

// NewAppRead is what the New app form and its job read from one pinned commit: the Catalogue, the Teams, the apps
// staging offers, the site and the host the repository's webhook reaches.
type NewAppRead struct {
	Contract    []byte
	Catalogue   pages.StackCatalogue
	Teams       map[string]pages.TeamInfo
	Connectable pages.Connectable
	TeamsError  string
	HookHost    string
}

// NewAppRead reads the pinned commit once for one form.
func (s Server) NewApp(ctx context.Context) (NewAppRead, error) {
	tree, rows, err := s.Rows(ctx)
	if err != nil {
		return NewAppRead{}, err
	}
	Catalogue, err := StackCatalogue(tree)
	if err != nil {
		return NewAppRead{}, err
	}
	site, err := Site(tree, s.Repository)
	if err != nil {
		return NewAppRead{}, err
	}
	Teams, _ := TeamRegistry(tree)
	return NewAppRead{Contract: tree[stacksPath], Catalogue: Catalogue, Teams: Teams, HookHost: "flux-webhook." + site.Domain,
		Connectable: pages.ConnectableTo(rows, Offers(tree, rows, "staging"), "staging", "")}, nil
}

// callable is the apps staging offers a call to, as the form's checkboxes name them; nil when the lists could not be
// read, so a form that chose nothing is not refused for a Catalogue it did not use.
func (read NewAppRead) Callable() map[string]bool {
	if read.Connectable.Problem != "" {
		return nil
	}
	found := make(map[string]bool, len(read.Connectable.Apps))
	for _, one := range read.Connectable.Apps {
		found[one.App] = true
	}
	return found
}

// offered is the events staging publishes, as the form's checkboxes name them.
func (read NewAppRead) Offered() map[string]bool {
	if read.Connectable.Problem != "" {
		return nil
	}
	found := make(map[string]bool, len(read.Connectable.Events))
	for _, one := range read.Connectable.Events {
		found[one.Name] = true
	}
	return found
}

// NewAppRequest is the immutable input of a create/resume job. Contract is the
// catalogue the form read, never the one the console image happens to carry.
type NewAppRequest struct {
	Choices            pages.NewAppChoices
	Finishing          bool
	Identity, HookHost string
	Contract           []byte
}

// NewAppWork creates the repository, or resumes the earlier job's repository,
// through v2's typed command port and returns its staging pull request address.
func NewAppWork(chosen pages.NewAppChoices, finishing bool, identity string, read NewAppRead,
	open func(context.Context, NewAppRequest, func(string)) (string, error)) orchestration.JobWork {
	return func(ctx context.Context, line func(string)) (string, error) {
		if read.Contract == nil {
			return "", errors.New("v2/contract/stacks.json: not in the pinned commit")
		}
		return open(ctx, NewAppRequest{Choices: chosen, Finishing: finishing, Identity: identity,
			HookHost: read.HookHost, Contract: read.Contract}, line)
	}
}
