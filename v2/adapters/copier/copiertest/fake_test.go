package copiertest

import (
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/copier"
)

func TestFake(t *testing.T) {
	Contract(t, func(*testing.T, copier.Request) copier.Renderer { return New() }, Scenario())
}
