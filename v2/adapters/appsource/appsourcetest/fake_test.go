package appsourcetest_test

import (
	"github.com/samclement/swhurl-platform/v2/adapters/appsource"
	"github.com/samclement/swhurl-platform/v2/adapters/appsource/appsourcetest"
	"github.com/samclement/swhurl-platform/v2/producer"
	"testing"
)

func TestFakeContract(t *testing.T) {
	appsourcetest.Contract(t, func(t *testing.T) (appsource.Reader, string) {
		return &appsourcetest.Fake{Root: "app", Source: appsourcetest.Scenario(), EventMappings: map[string]producer.Object{"asyncapi.yaml": appsourcetest.EventScenario()}, Files: map[string][]byte{"asyncapi.yaml": []byte("asyncapi: 3.0.0\r\nchannels: {}\r\n"), "api.bin": {255, 0, 'A', 'P', 'I', '\r', '\n'}}}, "app"
	})
}
