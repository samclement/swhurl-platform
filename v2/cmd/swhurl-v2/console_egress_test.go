package main

import (
	"bytes"
	"testing"

	"github.com/samclement/swhurl-platform/v2/pages"
)

// TestEgressPageRendersAGrantedHost keeps the Egress page renderable once an instance has a grant: the row's hosts are
// the row's, not the page's, and a page whose table is only ever rendered empty hides the difference.
func TestEgressPageRendersAGrantedHost(t *testing.T) {
	server, err := newConsoleServer(consoleConfig{Dev: true})
	if err != nil {
		t.Fatal(err)
	}
	data := egressData{shell: fixtureShell("/egress", "Egress"), EgressPage: pages.EgressPage{
		Instances: 1, Granted: 1, Hosts: 2,
		Environments: []pages.EgressEnvironment{{Env: "staging", Rows: []pages.EgressRow{
			{Name: "demo-staging", Href: "/apps/demo/staging", Port: 8080,
				Egress: []string{"api.example.test", "smtp.example.test"}},
			{Name: "bare-staging", Href: "/apps/bare/staging"},
		}}},
	}}
	var rendered bytes.Buffer
	if err := server.templates["egress.html"].ExecuteTemplate(&rendered, "base", data); err != nil {
		t.Fatal(err)
	}
}
