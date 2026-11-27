package console_test

import (
	"testing"

	"github.com/samclement/swhurl-platform/v2/console"
	"github.com/samclement/swhurl-platform/v2/pages"
)

// The app page shows a HelmRelease's environment as the document states it: the map a rendered release holds and the
// list Kubernetes accepts, a plain value as itself and a reference by where it comes from.
func TestDesiredVariablesReadsBothForms(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want []pages.NamedValue
	}{
		{"map", `{"PLAIN":"a","FROM_POD":{"valueFrom":{"fieldRef":{"fieldPath":"status.hostIP"}}},"FROM_SECRET":{"valueFrom":{"secretKeyRef":{"name":"s","key":"k"}}}}`,
			[]pages.NamedValue{{Name: "PLAIN", Value: "a"}, {Name: "FROM_POD", Value: "from the pod (status.hostIP)"},
				{Name: "FROM_SECRET", Value: "from a reference"}}},
		{"list", `[{"name":"PLAIN","value":"a"},{"name":"FROM_POD","valueFrom":{"fieldRef":{"fieldPath":"metadata.name"}}}]`,
			[]pages.NamedValue{{Name: "PLAIN", Value: "a"}, {Name: "FROM_POD", Value: "from the pod (metadata.name)"}}},
		{"empty", `{}`, []pages.NamedValue{}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			tree := map[string][]byte{pages.ConsoleAppsRoot + "/web/staging/helmrelease.json": []byte(
				`{"spec":{"values":{"controllers":{"main":{"containers":{"main":{"env":` + test.env + `}}}}}}}`)}
			found, known := console.DesiredVariables(tree, "web", "staging")
			if !known || len(found) != len(test.want) {
				t.Fatalf("found %v (known %v)", found, known)
			}
			for i := range test.want {
				if found[i] != test.want[i] {
					t.Fatalf("row %d = %+v, want %+v", i, found[i], test.want[i])
				}
			}
		})
	}
}

// The Scale form starts from the configuration the record states, and a record that is not there states nothing.
func TestSettings(t *testing.T) {
	tree := map[string][]byte{"v2-records/web/prod/instance.json": []byte(
		`{"schemaVersion":1,"application":"web","environment":"prod","configuration":{"replicas":3,"memoryMiB":256}}`)}
	settings := console.Settings(tree, "web", "prod")
	if settings["replicas"] != "3" || settings["memoryMiB"] != "256" {
		t.Fatalf("settings = %v", settings)
	}
	if none := console.Settings(tree, "web", "staging"); len(none) != 0 {
		t.Fatalf("an absent record states %v", none)
	}
}

// A promotion is reviewed only between two instances whose records are in Git: an instance the cluster is still
// removing, or one the records do not declare, is not a side of it.
func TestPromotionReviewNeedsBothRecords(t *testing.T) {
	declared := &pages.Inventory{}
	row := func(env string, inventory *pages.Inventory, removed string) pages.AppRow {
		return pages.AppRow{Instance: pages.Instance{App: "web", Env: env}, Inventory: inventory, Removed: removed}
	}
	cases := []struct {
		name    string
		rows    []pages.AppRow
		problem string
	}{
		{"no staging records", []pages.AppRow{row("staging", nil, ""), row("prod", declared, "")},
			"its records are not in Git, so what it declares is unknown"},
		{"staging being removed", []pages.AppRow{row("staging", declared, "gone"), row("prod", declared, "")},
			"its records are not in Git, so what it declares is unknown"},
		{"no production", []pages.AppRow{row("staging", declared, "")},
			"v2 has no first promotion: production has no records yet"},
		{"another app's production", []pages.AppRow{row("staging", declared, ""),
			{Instance: pages.Instance{App: "api", Env: "prod"}, Inventory: declared}},
			"v2 has no first promotion: production has no records yet"},
		{"both", []pages.AppRow{row("staging", declared, ""), row("prod", declared, "")}, ""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			review, problem := console.PromotionReview("web", test.rows)
			if problem != test.problem {
				t.Fatalf("problem = %q, want %q", problem, test.problem)
			}
			if test.problem == "" && review.App != "web" {
				t.Fatalf("review = %+v", review)
			}
		})
	}
}
