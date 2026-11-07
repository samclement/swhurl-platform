package clickstacktest

import (
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/clickstack"
	"github.com/samclement/swhurl-platform/v2/dashboards"
)

func TestFakeContract(t *testing.T) {
	scenario := Scenario{Key: "key", URI: "mongodb://uri", Email: "admin@example.test", Held: Inventory{
		Sources:  []dashboards.Source{{ID: "t1", Kind: "trace", Table: "otel_traces"}},
		Webhooks: []dashboards.Webhook{{ID: "w0", Name: "ntfy: platform", Service: "generic"}},
		Alerts:   []dashboards.Alert{{ID: "a1", Name: "swhurl:web:errors"}},
	}}
	Contract(t, scenario, func(_ *testing.T, s *Scenario) clickstack.Opener {
		held := *s
		return &held
	})
}

func TestFakeInstallerContract(t *testing.T) {
	InstallerContract(t, func(_ *testing.T, i *Installation) clickstack.Installer { return i })
}
