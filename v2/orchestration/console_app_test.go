package orchestration

import (
	"testing"

	"github.com/samclement/swhurl-platform/v2/pages"
)

// The app page shows a HelmRelease's environment as the document states it: the map a rendered release holds and the
// list Kubernetes accepts, a plain value as itself and a reference by where it comes from.
func TestConsoleDesiredVariablesReadsBothForms(t *testing.T) {
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
			tree := map[string][]byte{consoleApps + "/web/staging/helmrelease.json": []byte(
				`{"spec":{"values":{"controllers":{"main":{"containers":{"main":{"env":` + test.env + `}}}}}}}`)}
			found, known := ConsoleDesiredVariables(tree, "web", "staging")
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
func TestConsoleSettings(t *testing.T) {
	tree := map[string][]byte{consoleRecords + "/web/prod/instance.json": []byte(
		`{"schemaVersion":1,"application":"web","environment":"prod","configuration":{"replicas":3,"memoryMiB":256}}`)}
	settings := ConsoleSettings(tree, "web", "prod")
	if settings["replicas"] != "3" || settings["memoryMiB"] != "256" {
		t.Fatalf("settings = %v", settings)
	}
	if none := ConsoleSettings(tree, "web", "staging"); len(none) != 0 {
		t.Fatalf("an absent record states %v", none)
	}
}
