package orchestration

import (
	"os"
	"testing"

	provisioningrules "github.com/samclement/swhurl-platform/v2/provisioning/rules"
)

const revision = "0123456789abcdef0123456789abcdef01234567"

// input is a compiled instance's documents: the fixture records, with the optional documents when given.
func input(t *testing.T, calls, egress []byte) provisioningrules.Inputs {
	t.Helper()
	read := func(name string) []byte {
		data, err := os.ReadFile("../testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	return provisioningrules.Inputs{Instance: read("prod.json"), Release: read("r41.json"), App: read("app.json"),
		Profile: read("profile.json"), Calls: calls, Egress: egress}
}
