package workstreams

import (
	"fmt"
	"strings"
	"testing"
)

// Each rule gets a small checkout that satisfies it and one that breaks it: the point of the check is that it fails,
// so a test that only shows the passing case proves nothing.

const (
	head = "| Workstream | Kind | State | Current slice | Next gate | Needs | Page | Paths |\n" +
		"| --- | --- | --- | --- | --- | --- | --- | --- |\n"
	statusBlock = "Status: approved\nGate: a decision\nUncommitted: none\n"
	alpha       = "docs/alpha-status.md"
	page        = "# Alpha: status\n\n" + statusBlock +
		"\n## Where it stands\n\n" +
		"| Capability | State | Evidence |\n| --- | --- | --- |\n" +
		"| the model | done | [the model](#where-it-stands) |\n" +
		"\n## Remaining work\n\n1. the next thing.\n" +
		"\n## Deferred features\n\n" +
		"| Feature | v2 today | Reopen when |\n| --- | --- | --- |\n" +
		"| alerts | refused by name | an instance needs one |\n" +
		"\n## Durable decisions\n\n- One writer per fact.\n" +
		"\n## How this page is kept\n\nOverwritten, not appended to.\n"
)

type registryRow struct{ name, kind, state, gate, needs, page, paths string }

func defaults() registryRow {
	return registryRow{"Alpha", "main", "approved", "a decision", "operator approval", "[alpha status](alpha-status.md)", "`v2/`"}
}

func (r registryRow) String() string {
	return fmt.Sprintf("| %s | %s | %s | a slice | %s | %s | %s | %s |\n", r.name, r.kind, r.state, r.gate, r.needs, r.page, r.paths)
}

// checkout is a consistent checkout: the registry with table, the status page when page is not "", and v2/ tracked.
func checkout(table, text string) Checkout {
	c := Checkout{
		Tracked:  []string{"v2/file.go"},
		Present:  []string{Registry, "v2/file.go"},
		Markdown: map[string]string{Registry: "# Workstreams\n\n## Workstreams\n\n" + table},
	}
	if text != "" {
		c.Tracked = append(c.Tracked, alpha)
		c.Present = append(c.Present, alpha)
		c.Markdown[alpha] = text
	}
	return c
}

func standard() Checkout { return checkout(head+defaults().String(), page) }

// report is the check's problems and warnings, one per line, as the command prints them.
func report(c Checkout) (int, string) {
	var out []string
	failures := 0
	for _, r := range Check(c) {
		failures += len(r.Problems)
		for _, problem := range r.Problems {
			out = append(out, "[BAD] "+problem)
		}
		if r.Warning != "" {
			out = append(out, "[WARN] "+r.Warning)
		}
	}
	return failures, strings.Join(out, "\n")
}

func passes(t *testing.T, c Checkout) {
	t.Helper()
	if failures, out := report(c); failures != 0 {
		t.Fatalf("%d failures:\n%s", failures, out)
	}
}

func reports(t *testing.T, c Checkout, want ...string) {
	t.Helper()
	failures, out := report(c)
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("no %q in:\n%s", w, out)
		}
	}
	if failures == 0 {
		t.Error("no failure")
	}
}

func TestAConsistentRegistryPasses(t *testing.T) {
	passes(t, standard())
	results := Check(standard())
	if len(results) != 10 || results[0].Okay != Registry+" declares 1 workstreams" {
		t.Fatalf("results: %+v", results)
	}
}

func TestAMissingRegistryIsTheOnlyResult(t *testing.T) {
	c := standard()
	delete(c.Markdown, Registry)
	if results := Check(c); len(results) != 1 || results[0].Problems[0] != Registry+" is missing" {
		t.Fatalf("results: %+v", results)
	}
}

func TestTheTableMustHaveTheStatedColumns(t *testing.T) {
	reports(t, checkout(strings.Replace(head, "| Kind |", "| Sort |", 1)+defaults().String(), page), "columns are")
}

func TestTheTableMustExistOnce(t *testing.T) {
	reports(t, checkout("No table here.\n", page), `expected exactly one table headed "| Workstream |", found 0`)
	reports(t, checkout(head+defaults().String()+"\n"+head+defaults().String(), page), "found 2")
}

func TestATableWithoutRowsFails(t *testing.T) {
	reports(t, checkout(head, page), "the table has no rows")
}

func TestARowMustHaveEveryCellAndALinkedPage(t *testing.T) {
	reports(t, checkout(head+strings.Replace(defaults().String(), "| a slice |", "", 1), page), "not 8")
	r := defaults()
	r.page = "alpha-status.md"
	reports(t, checkout(head+r.String(), page), "Page must be a Markdown link")
}

func TestKindAndStateMustBeKnown(t *testing.T) {
	r := defaults()
	r.kind, r.state = "extra", "nearly"
	reports(t, checkout(head+r.String(), page), `Kind is "extra"`, `State is "nearly"`)
}

func TestARowWithoutANextGateOrNeedFails(t *testing.T) {
	r := defaults()
	r.gate, r.needs = "—", ""
	reports(t, checkout(head+r.String(), page), "Next gate is empty", "Needs is empty")
}

func TestAnOwnedPathMustHoldATrackedFile(t *testing.T) {
	c := standard()
	c.Tracked = []string{alpha}
	reports(t, c, "Paths names v2/, which holds no tracked file")
	c.Tracked = []string{alpha, "v2/kernel/render.go"}
	passes(t, c)
}

func TestAPageOrPathThatDoesNotExistFails(t *testing.T) {
	r := defaults()
	r.page, r.paths = "[alpha status](missing.md)", "`absent/`"
	reports(t, checkout(head+r.String(), page), "Page missing.md does not exist", "Paths names absent/, which does not exist")
	r.page = "[outside](../../elsewhere.md)"
	reports(t, checkout(head+r.String(), page), "Page ../../elsewhere.md does not exist")
}

func TestANamedHeadingMustExist(t *testing.T) {
	r := defaults()
	r.page = "[alpha status](alpha-status.md#no-such-heading)"
	reports(t, checkout(head+r.String(), page), "has no heading no-such-heading")
	r.page = "[alpha status](alpha-status.md#where-it-stands)"
	passes(t, checkout(head+r.String(), page))
}

func TestOnlyOneWorkstreamMayBeInProgress(t *testing.T) {
	one, two := defaults(), defaults()
	one.state, two.name, two.state = "in progress", "Beta", "in progress"
	reports(t, checkout(head+one.String()+two.String(), strings.Replace(page, "Status: approved", "Status: in progress", 1)),
		"2 workstreams are in progress (Alpha, Beta); one at a time")
}

func TestAStatusPageMustStateItsBlock(t *testing.T) {
	reports(t, checkout(head+defaults().String(), "# Alpha: status\n\nNo block here.\n"), "missing Status, Gate, Uncommitted")
	// The block is read from the first lines only.
	late := "# Alpha: status\n" + strings.Repeat("\n", blockLines) + statusBlock
	reports(t, checkout(head+defaults().String(), late), "missing Status, Gate, Uncommitted")
}

func TestThePageAndRegistryMustAgree(t *testing.T) {
	text := strings.Replace(strings.Replace(page, "Status: approved", "Status: blocked", 1), "Gate: a decision", "Gate: something else", 1)
	reports(t, checkout(head+defaults().String(), text),
		`Status is "blocked" but the registry says "approved"`, `Gate is "something else"`)
}

func TestAStatusPageNoRowNamesFails(t *testing.T) {
	c := standard()
	c.Tracked = append(c.Tracked, "docs/other-status.md")
	reports(t, c, "docs/other-status.md: a status page no row of docs/workstreams.md names")
}

func TestAPlainPageNeedsNoStatusBlock(t *testing.T) {
	r := defaults()
	r.page = "[plan](plan.md)"
	c := checkout(head+r.String(), "")
	c.Present = append(c.Present, "docs/plan.md")
	c.Markdown["docs/plan.md"] = "# Plan\n\nNo status block.\n"
	passes(t, c)
}

func TestAPageMissingASectionFails(t *testing.T) {
	reports(t, checkout(head+defaults().String(), strings.Replace(page, "## Durable decisions", "## Notes", 1)),
		"missing the section(s) Durable decisions")
}

func TestACapabilityStateMustBeDoneOrPartial(t *testing.T) {
	reports(t, checkout(head+defaults().String(), strings.Replace(page, "| done |", "| nearly |", 1)), "expected done or partial")
	passes(t, checkout(head+defaults().String(), strings.Replace(page, "| done |", "| **partial** |", 1)))
}

func TestTheCapabilityTableMustExistAndHaveRows(t *testing.T) {
	without := strings.Replace(page, "| Capability | State | Evidence |", "| Thing | State | Evidence |", 1)
	reports(t, checkout(head+defaults().String(), without), `Where it stands has no "| Capability | State | Evidence |" table`)
	empty := strings.Replace(page, "| the model | done | [the model](#where-it-stands) |\n", "", 1)
	reports(t, checkout(head+defaults().String(), empty), "Where it stands has no capability row")
	short := strings.Replace(page, "| the model | done | [the model](#where-it-stands) |", "| the model | done |", 1)
	reports(t, checkout(head+defaults().String(), short), "capability the model: expected 3 cells, found 2")
}

func TestACapabilityEvidenceLinkMustResolve(t *testing.T) {
	reports(t, checkout(head+defaults().String(), strings.Replace(page, "(#where-it-stands)", "(#no-such-heading)", 1)),
		"Evidence #no-such-heading (missing heading)")
	reports(t, checkout(head+defaults().String(), strings.Replace(page, "[the model](#where-it-stands)", "since we did it", 1)),
		"Evidence has no link")
	reports(t, checkout(head+defaults().String(), strings.Replace(page, "(#where-it-stands)", "(../v2/absent.go)", 1)),
		"Evidence ../v2/absent.go (missing file)")
	reports(t, checkout(head+defaults().String(), strings.Replace(page, "(#where-it-stands)", "(../../outside.md)", 1)),
		"(missing file)")
}

func TestEvidenceResolvesFilesFoldersOtherPagesAndSchemes(t *testing.T) {
	for _, target := range []string{"../v2/file.go", "../v2", "../v2/", "./alpha-status.md#remaining-work",
		"current.md#a-dated-entry-5-november-2026", "https://example.com/missing", "../README.md"} {
		c := checkout(head+defaults().String(), strings.Replace(page, "(#where-it-stands)", "("+target+")", 1))
		c.Present = append(c.Present, "docs/current.md", "README.md")
		c.Markdown["docs/current.md"] = "# Current\n\n## A dated entry (5 November 2026)\n"
		passes(t, c)
	}
}

func TestADeferredRowMustSayWhatReopensIt(t *testing.T) {
	reports(t, checkout(head+defaults().String(), strings.Replace(page, "| an instance needs one |", "| |", 1)),
		"state what v2 does today and what reopens it")
	reports(t, checkout(head+defaults().String(), strings.Replace(page, "| Feature | v2 today | Reopen when |", "| Feature |", 1)),
		"Deferred features has no")
	reports(t, checkout(head+defaults().String(), strings.Replace(page, "| alerts | refused by name | an instance needs one |", "| alerts |", 1)),
		"a deferred row has 1 cells, not 3")
}

func TestASliceRecordHeadingFails(t *testing.T) {
	reports(t, checkout(head+defaults().String(), page+"\n## Slice record: one more thing\n"), `has a "## Slice record" heading`)
}

func TestAPageOverTheCapFails(t *testing.T) {
	text := page + strings.Repeat("filler\n", MaxLines)
	reports(t, checkout(head+defaults().String(), text), fmt.Sprintf("stays at or under %d", MaxLines))
	passes(t, checkout(head+defaults().String(), page+strings.Repeat("filler\n", MaxLines-len(lines(page)))))
}

func TestUncommittedNoneWithDirtyPathsFails(t *testing.T) {
	c := standard()
	c.Uncommitted = []string{"v2/kernel/render.go", alpha}
	reports(t, c, "Uncommitted says none, but Alpha has uncommitted work: v2/kernel/render.go")
}

func TestNamedUncommittedPathsMustBeDirty(t *testing.T) {
	c := checkout(head+defaults().String(), strings.Replace(page, "Uncommitted: none", "Uncommitted: `v2/kernel/render.go`", 1))
	reports(t, c, "but nothing under v2/ is uncommitted")
}

func TestUncommittedMustNotOmitDirtyPaths(t *testing.T) {
	c := checkout(head+defaults().String(), strings.Replace(page, "Uncommitted: none", "Uncommitted: `v2/other.go`", 1))
	c.Uncommitted = []string{alpha, "v2/kernel/render.go"}
	reports(t, c, "Uncommitted omits v2/kernel/render.go")
}

func TestNamedUncommittedPathsThatMatchPass(t *testing.T) {
	c := checkout(head+defaults().String(), strings.Replace(page, "Uncommitted: none", "Uncommitted: `v2/kernel/`; v2/file.go", 1))
	c.Uncommitted = []string{alpha, "v2/file.go", "v2/kernel/render.go"}
	passes(t, c)
}

// A row without paths owns the whole repository: anything dirty is its uncommitted work.
func TestARowWithoutPathsOwnsEverything(t *testing.T) {
	r := defaults()
	r.paths = "—"
	c := checkout(head+r.String(), page)
	c.Uncommitted = []string{"apps/demo/instance.yaml"}
	reports(t, c, "Alpha has uncommitted work: apps/demo/instance.yaml")
	c.Markdown[alpha] = strings.Replace(page, "Uncommitted: none", "Uncommitted: `v2/`", 1)
	c.Uncommitted = nil
	reports(t, c, "but nothing under . is uncommitted")
}

func TestACitedCommitMustBeReachableFromARef(t *testing.T) {
	c := checkout(head+defaults().String(), page+"\nCommitted in `abcdef1234`, and again in `abcdef1234`.\n")
	c.Commits = []string{"deadbeef00000000000000000000000000000000"}
	failures, out := report(c)
	if failures != 1 || !strings.Contains(out, "docs/alpha-status.md: cites abcdef1234, which no ref reaches") {
		t.Fatalf("%d failures:\n%s", failures, out)
	}
	c.Commits = []string{"abcdef1234000000000000000000000000000000"}
	passes(t, c)
}

func TestAShallowCheckoutSkipsCitationsRatherThanFailingThem(t *testing.T) {
	c := checkout(head+defaults().String(), page+"\nCommitted in `abcdef1234`.\n")
	c.Shallow = true
	failures, out := report(c)
	if failures != 0 || !strings.Contains(out, "[WARN] the checkout is shallow") {
		t.Fatalf("%d failures:\n%s", failures, out)
	}
}

func TestChangingAWorkstreamWithoutItsPageFails(t *testing.T) {
	c := checkout(head+defaults().String(), strings.Replace(page, "Uncommitted: none", "Uncommitted: v2/", 1))
	c.Uncommitted = []string{"v2/kernel/render.go"}
	reports(t, c, "v2/kernel/render.go changed without docs/alpha-status.md")
	c.Uncommitted = []string{alpha, "v2/kernel/render.go"}
	passes(t, c)
}

// An untracked file is a change too: the page changes with it.
func TestANewFileIsAChange(t *testing.T) {
	c := checkout(head+defaults().String(), strings.Replace(page, "Uncommitted: none", "Uncommitted: v2/", 1))
	c.Present = append(c.Present, "v2/new.go")
	c.Uncommitted = []string{"v2/new.go"}
	reports(t, c, "v2/new.go changed without docs/alpha-status.md")
}

func TestAChangeElsewhereNeedsNoPage(t *testing.T) {
	c := standard()
	c.Uncommitted = []string{"apps/demo/instance.yaml"}
	passes(t, c)
}

func TestAnchorsFollowGitHub(t *testing.T) {
	got := anchors("# Title\n## Slice: `swhurl-v2 status` (approved 3 November 2026)\nnot a heading\n### Café, naïve_thing\n")
	for _, want := range []string{"title", "slice-swhurl-v2-status-approved-3-november-2026", "café-naïve_thing"} {
		if !got[want] {
			t.Errorf("no anchor %q in %v", want, got)
		}
	}
	if len(got) != 3 {
		t.Errorf("anchors: %v", got)
	}
}

func TestResolveStaysInsideTheRepository(t *testing.T) {
	for _, c := range []struct {
		folder, target, want string
		inside               bool
	}{{"docs", "plan.md", "docs/plan.md", true}, {"docs", "../v2/kernel", "v2/kernel", true}, {"docs", "./a/../b.md", "docs/b.md", true},
		{"docs", "..", "", true}, {"docs", "../..", "", false}, {"docs", "/etc/passwd", "", false}} {
		if got, inside := resolve(c.folder, c.target); got != c.want || inside != c.inside {
			t.Errorf("resolve(%q, %q) = %q, %v", c.folder, c.target, got, inside)
		}
	}
}

// Needs names only what the check reads, in rounds: the registry, then its pages, then the pages evidence names a
// heading of; never an absent file, a file it holds, or one nothing uses.
func TestNeedsNamesWhatTheCheckReads(t *testing.T) {
	c := standard()
	c.Markdown = map[string]string{}
	c.Present = append(c.Present, "docs/current.md", "docs/unused.md", "docs/plain.md")
	steps := [][]string{{Registry}, {alpha}, {"docs/current.md"}, nil}
	text := map[string]string{Registry: standard().Markdown[Registry],
		alpha: strings.Replace(page, "[the model](#where-it-stands)",
			"[a](current.md#an-entry) [b](plain.md) [c](absent.md#x) [d](https://example.com/x.md#y) [e](#where-it-stands)", 1),
		"docs/current.md": "# Current\n\n## An entry\n"}
	for i, want := range steps {
		got := Needs(c)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("round %d: needs %q, want %q", i, got, want)
		}
		for _, path := range got {
			c.Markdown[path] = text[path]
		}
	}
	if failures, out := report(c); failures != 1 || !strings.Contains(out, "Evidence absent.md#x (missing file)") {
		t.Fatalf("%d failures:\n%s", failures, out)
	}
	absent := standard()
	absent.Markdown, absent.Present = map[string]string{}, nil
	if got := Needs(absent); got != nil {
		t.Fatalf("an absent registry: needs %q", got)
	}
}

// A path owns what is inside its folder, not a sibling that shares its prefix: `v2` is not `v2-records`.
func TestPathsAreBoundedByFolders(t *testing.T) {
	r := defaults()
	r.paths = "`v2`"
	c := checkout(head+r.String(), page)
	c.Tracked, c.Present = []string{alpha, "v2-records/x.json"}, []string{Registry, alpha, "v2", "v2-records/x.json"}
	c.Uncommitted = []string{"v2-records/x.json"}
	failures, out := report(c)
	if failures != 1 || !strings.Contains(out, "Paths names v2, which holds no tracked file") {
		t.Fatalf("%d failures:\n%s", failures, out)
	}
}

// The block's first line of each key wins, so a later example in the page cannot change what it states.
func TestTheFirstBlockLineWins(t *testing.T) {
	passes(t, checkout(head+defaults().String(), strings.Replace(page, "\n## Where it stands", "\nStatus: blocked\n\n## Where it stands", 1)))
	reports(t, checkout(head+defaults().String(), strings.Replace(page, "Status: approved", "Status: blocked\nStatus: approved", 1)),
		`Status is "blocked"`)
}
