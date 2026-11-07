package observe

// The catalogue is the operations page's check table: a test reads the page and fails when a row's order, name or
// wording differs from the catalogue, or a row is missing or extra. The report is built from the same list, so the
// page, the report's sections and the checks cannot drift apart.

import (
	"os"
	"strings"
	"testing"
)

const operationsPage = "../../docs/operations.md"

// pageChecks are the "| Check | Passes when | Reads |" table's rows: name, what passing means and what it reads.
func pageChecks(t *testing.T) [][3]string {
	t.Helper()
	page, err := os.ReadFile(operationsPage)
	if err != nil {
		t.Fatal(err)
	}
	rows, inside := [][3]string{}, false
	for _, line := range strings.Split(string(page), "\n") {
		switch {
		case strings.HasPrefix(line, "| Check | Passes when | Reads |"):
			inside = true
		case !inside:
		case !strings.HasPrefix(line, "|"):
			return rows
		case strings.HasPrefix(line, "| --- "):
		default:
			cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), " | ")
			if len(cells) != 3 {
				t.Fatalf("%s: a row has %d cells: %s", operationsPage, len(cells), line)
			}
			rows = append(rows, [3]string{strings.Trim(strings.TrimSpace(cells[0]), "`"), strings.TrimSpace(cells[1]),
				strings.TrimSpace(cells[2])})
		}
	}
	return rows
}

func TestOperationsPageListsTheCatalogue(t *testing.T) {
	rows := pageChecks(t)
	if len(rows) != len(PlatformCatalogue) {
		t.Fatalf("%s lists %d checks; the catalogue has %d", operationsPage, len(rows), len(PlatformCatalogue))
	}
	for i, entry := range PlatformCatalogue {
		if rows[i][0] != entry.Name || rows[i][1] != entry.Passes || rows[i][2] != entry.Reads {
			t.Errorf("%s row %d is %q / %q / %q; the catalogue says %q / %q / %q", operationsPage, i+1,
				rows[i][0], rows[i][1], rows[i][2], entry.Name, entry.Passes, entry.Reads)
		}
	}
}

// Every group the report carries is catalogued, in the catalogue's order and under its section: an uncatalogued
// name would print under no section at all.
func TestEveryReportedGroupIsCatalogued(t *testing.T) {
	checks := PlatformScopeChecks(PlatformEvidence{}, ScopeEvidence{})
	if len(checks) != len(PlatformCatalogue) {
		t.Fatalf("the report has %d groups; the catalogue has %d", len(checks), len(PlatformCatalogue))
	}
	for i, check := range checks {
		if check.Name != PlatformCatalogue[i].Name || check.Section != PlatformCatalogue[i].Section {
			t.Errorf("group %d is %q/%q; the catalogue says %q/%q", i, check.Name, check.Section,
				PlatformCatalogue[i].Name, PlatformCatalogue[i].Section)
		}
	}
	for _, check := range PlatformChecks(PlatformEvidence{}) {
		if check.Section == "" {
			t.Errorf("cluster group %q has no catalogued section", check.Name)
		}
	}
}
