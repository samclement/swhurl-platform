package pages_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/pages"
)

// catalogue is the form's stack catalogue: two stacks, three types, one of them a worker that has no exposure.
func catalogue() pages.StackCatalogue {
	return pages.StackCatalogue{
		Stacks: []pages.StackInfo{{Name: "typescript"}, {Name: "python"}},
		Types: []pages.AppType{
			{Key: "web-app", Title: "Web app", Stacks: []string{"typescript", "python"}, Exposure: "authenticated-web",
				Kind:    "web",
				Answers: []pages.NamedValue{{Name: "kind", Value: "web"}, {Name: "frontend", Value: "vite"}}},
			{Key: "api", Title: "API", Stacks: []string{"python"}, Exposure: "private", Kind: "web",
				Answers: []pages.NamedValue{{Name: "kind", Value: "web"}, {Name: "api", Value: "openapi"}}},
			{Key: "worker", Title: "Worker", Stacks: []string{"python"}, Kind: "worker",
				Answers: []pages.NamedValue{{Name: "kind", Value: "worker"}}},
		},
	}
}

// TestNewAppChoose keeps the New app form's answers as v2's own flags, and the sentences that refuse a choice the
// repository could not be created with: the console decides these before anything is written to GitHub.
func TestNewAppChoose(t *testing.T) {
	teams := map[string]pages.TeamInfo{"shop": {}, "ops": {}}
	callable := map[string]bool{"demo-api": true}
	offered := map[string]bool{"demo-api.order": true}
	base := map[string]string{"name": "shop-web", "type": "web-app", "stack": "typescript", "team": "shop"}
	with := func(more map[string]string) map[string][]string {
		values := map[string]string{}
		for name, value := range base {
			values[name] = value
		}
		for name, value := range more {
			values[name] = value
		}
		return form(values)
	}
	cases := []struct {
		name    string
		fields  map[string][]string
		want    []string
		problem string
	}{
		{name: "a web app", fields: with(nil),
			want: []string{"team=shop", "exposure=authenticated-web"}},
		{name: "a chosen exposure", fields: with(map[string]string{"exposure": "private"}),
			want: []string{"team=shop", "exposure=private"}},
		{name: "a worker has none", fields: form(map[string]string{"name": "shop-job", "type": "worker",
			"stack": "python", "team": "shop", "exposure": "private"}), want: []string{"team=shop"}},
		{name: "connections", fields: with(map[string]string{"calls": "demo-api", "subscribe": "demo-api.order"}),
			want: []string{"team=shop", "exposure=authenticated-web", "calls=demo-api", "subscriptions=demo-api.order"}},
		{name: "a cleaned description", fields: with(map[string]string{"description": "  A   shop ", "stack": "python"}),
			want: []string{"team=shop", "exposure=authenticated-web"}},
		{name: "no name", fields: with(map[string]string{"name": ""}),
			problem: "name must be a DNS label: lowercase letters, digits and hyphens, at most 40 characters"},
		{name: "the reserved name", fields: with(map[string]string{"name": "notify"}),
			problem: "notify is reserved for the platform notification service"},
		{name: "no such type", fields: with(map[string]string{"type": "ansible"}),
			problem: "type must be one of web-app, api, worker"},
		{name: "a stack the type is not built with", fields: with(map[string]string{"type": "api"}),
			problem: "a API is built with python, not typescript"},
		{name: "no team", fields: form(map[string]string{"name": "shop-web", "type": "web-app", "stack": "typescript"}),
			problem: "choose the team that owns the app: one of ops, shop (v2-records/teams.json)"},
		{name: "a host", fields: with(map[string]string{"host": "x.example.com"}),
			problem: "v2 derives the address from the app name and the exposure: it does not model a host"},
		{name: "public", fields: with(map[string]string{"exposure": "public"}),
			problem: "v2 does not model public exposure yet: choose signed-in, or private for an API, worker or job"},
		{name: "a call nothing offers", fields: with(map[string]string{"calls": "other"}),
			problem: "other: not a web app, or an event, that staging has now; reload the form"},
	}
	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			found, err := pages.NewAppChoose(one.fields, catalogue(), teams, callable, offered)
			if one.problem != "" {
				if err == nil || err.Error() != one.problem {
					t.Fatalf("refusal = %v, want %q", err, one.problem)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(found.Flags, one.want) {
				t.Fatalf("flags = %v, want %v", found.Flags, one.want)
			}
			kept, err := pages.NewAppFromRecord(found.Record, catalogue())
			if err != nil {
				t.Fatalf("the record the form kept cannot be read back: %v", err)
			}
			if kept.Name != found.Name || kept.Type != found.Type || !reflect.DeepEqual(kept.Flags, found.Flags) {
				t.Fatalf("record = %+v, want %+v", kept, found)
			}
		})
	}
}

// TestNewAppFromRecord keeps a kept record usable only while v2 could still create the app it describes.
func TestNewAppFromRecord(t *testing.T) {
	record := func(values map[string]any) map[string]any {
		found := map[string]any{"kind": "new-app", "name": "shop-web", "stack": "typescript", "description": "",
			"answers": map[string]any{}, "extra": []any{"team=shop"}, "type": "web-app"}
		for name, value := range values {
			found[name] = value
		}
		return found
	}
	cases := []struct {
		name        string
		record      map[string]any
		wantFlags   []string
		wantType    string
		wantProblem string
	}{
		{name: "as kept", record: record(nil), wantFlags: []string{"team=shop"}, wantType: "web-app"},
		{name: "no name", record: record(map[string]any{"name": "Shop Web"}),
			wantProblem: "the record of that job cannot be used to finish it; create the app again from New app"},
		{name: "no such stack", record: record(map[string]any{"stack": "java"}),
			wantProblem: "the record of that job cannot be used to finish it; create the app again from New app"},
		{name: "a flag that is not one", record: record(map[string]any{"extra": []any{"team"}}),
			wantProblem: "the record of that job cannot be used to finish it; create the app again from New app"},
		{name: "a publication", record: record(map[string]any{"extra": []any{"team=shop", "publish=order"}}),
			wantProblem: "these choices are no longer supported: the image states what it publishes, and v2 derives " +
				"the address; start a new job"},
		// A record that states no type is finished only when its answers are exactly one type's: the command would
		// otherwise render a different app.
		{name: "no type, no answers", record: record(map[string]any{"type": ""}),
			wantProblem: "the record of that job states no app type and its answers match none: create the app again " +
				"from New app"},
		{name: "no type, one type's answers", record: record(map[string]any{"type": "", "stack": "python",
			"answers": map[string]any{"kind": "worker"}}), wantFlags: []string{"team=shop"}, wantType: "worker"},
	}
	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			found, err := pages.NewAppFromRecord(one.record, catalogue())
			if one.wantProblem != "" {
				if err == nil || err.Error() != one.wantProblem {
					t.Fatalf("refusal = %v, want %q", err, one.wantProblem)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(found.Flags, one.wantFlags) || found.Type != one.wantType {
				t.Fatalf("flags = %v type = %s, want %v %s", found.Flags, found.Type, one.wantFlags, one.wantType)
			}
			if strings.TrimSpace(found.Name) != "shop-web" {
				t.Fatalf("name = %q", found.Name)
			}
		})
	}
}
