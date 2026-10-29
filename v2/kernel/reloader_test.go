package kernel

import (
	"encoding/json"
	"slices"
	"testing"
)

// reloaderList decodes the values the ConfigMap carries, as Helm parses them.
func reloaderList(t *testing.T, rendered ResourceSet) []string {
	t.Helper()
	if len(rendered.Files) != 1 {
		t.Fatalf("files %d, want the ConfigMap alone", len(rendered.Files))
	}
	var document struct {
		Metadata struct{ Name, Namespace string }
		Data     map[string]string
	}
	if err := json.Unmarshal(rendered.Files[0].Document, &document); err != nil {
		t.Fatal(err)
	}
	if document.Metadata.Name != "reloader-values" || document.Metadata.Namespace != "platform-system" {
		t.Fatalf("the object: %+v", document.Metadata)
	}
	var values struct {
		Reloader struct{ Namespaces []string }
	}
	if err := json.Unmarshal([]byte(document.Data["values.yaml"]), &values); err != nil {
		t.Fatalf("the data key is not JSON: %v", err)
	}
	return values.Reloader.Namespaces
}

// A namespace is watched when the instance reads a Secret a change must restart it for: its broker seed, or a
// Secret its release declares. The platform's own namespaces come first, in the profile's order, then staging's
// instances and production's, each by app name.
func TestReloaderWatchList(t *testing.T) {
	instance := "UCWLMGY4L75WNKJ2FRJFZ7QDGKWDDHDLJCJVU3OPXVUJ42THGXEDBJPI"
	reloader := Reloader{Platform: []string{"ingress", "logging", "console"}, Instances: []ReloaderInstance{
		{Application: "orders", Environment: "prod", BrokerKey: instance},
		{Application: "orders", Environment: "staging", Secrets: []string{"API_TOKEN"}},
		{Application: "quiet", Environment: "staging"},
		{Application: "alpha", Environment: "prod", BrokerKey: instance},
	}}
	names, err := reloader.Namespaces()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ingress", "logging", "console", "orders-staging", "alpha-prod", "orders-prod"}
	if !slices.Equal(names, want) {
		t.Fatalf("watch list %v, want %v", names, want)
	}
	rendered, err := RenderReloader(names)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaderList(t, rendered); !slices.Equal(got, want) {
		t.Fatalf("the ConfigMap holds %v, want %v", got, want)
	}
	if len(rendered.Inventory) != 1 || rendered.Inventory[0].Owner != "platform" ||
		rendered.Inventory[0].Object != (Object{"", "ConfigMap", "platform-system", "reloader-values"}) {
		t.Fatalf("the claim: %+v", rendered.Inventory)
	}
	if rendered.Files[0].Path != "reloader/values.json" {
		t.Fatalf("the file: %s", rendered.Files[0].Path)
	}
}

// A platform that watches no namespace of its own, and has no instance that needs one, still renders: the list
// is an empty list rather than absent.
func TestReloaderEmptyList(t *testing.T) {
	names, err := Reloader{}.Namespaces()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 0 {
		t.Fatalf("watch list %v, want none", names)
	}
	rendered, err := RenderReloader(names)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaderList(t, rendered); len(got) != 0 {
		t.Fatalf("the ConfigMap holds %v, want an empty list", got)
	}
}

// The watch list refuses what it could not state: a name that is not a namespace, a namespace listed twice, a
// platform namespace that is also an instance's, and an instance without a name or a known environment.
func TestReloaderRefusals(t *testing.T) {
	instance := "UCWLMGY4L75WNKJ2FRJFZ7QDGKWDDHDLJCJVU3OPXVUJ42THGXEDBJPI"
	for _, tt := range []struct {
		name     string
		reloader Reloader
		field    string
	}{
		{"platform namespace", Reloader{Platform: []string{"Not_A_Namespace"}}, "reloader.namespaces"},
		{"platform duplicate", Reloader{Platform: []string{"ingress", "ingress"}}, "reloader.namespaces"},
		{"collision", Reloader{Platform: []string{"orders-staging"},
			Instances: []ReloaderInstance{{Application: "orders", Environment: "staging", BrokerKey: instance}}},
			"reloader.namespaces"},
		{"application", Reloader{Instances: []ReloaderInstance{{Application: "Orders_", Environment: "staging", BrokerKey: instance}}},
			"reloader.instance"},
		{"environment", Reloader{Instances: []ReloaderInstance{{Application: "orders", Environment: "test", BrokerKey: instance}}},
			"reloader.instance"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.reloader.Namespaces()
			refusal(t, err, Invalid, tt.field)
		})
	}
	_, err := RenderReloader([]string{"INGRESS"})
	refusal(t, err, Invalid, "reloader.namespaces")
	_, err = RenderReloader([]string{"ingress", "ingress"})
	refusal(t, err, Invalid, "reloader.namespaces")
}
