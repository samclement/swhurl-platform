package otlptest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/otlp"
	"github.com/samclement/swhurl-platform/v2/adapters/otlp/otlptest"
)

func TestContract(t *testing.T) {
	otlptest.Contract(t, func(t *testing.T, refusing bool) (otlp.Exporter, func() []otlptest.Sent) {
		fake := &otlptest.Fake{Refusing: refusing}
		return fake, fake.Received
	})
}

// The fake counts every attempt, and a configured failure is returned without keeping anything.
func TestFakeCountsAndCanFail(t *testing.T) {
	refused := errors.New("refused")
	fake := &otlptest.Fake{Fail: refused}
	source, spans, _ := otlptest.Scenario()
	if err := fake.Traces(context.Background(), source, spans); err != refused {
		t.Fatalf("a configured failure was not returned: %v", err)
	}
	if fake.Attempts() != 1 || len(fake.Received()) != 0 {
		t.Fatalf("%d attempts, %d kept", fake.Attempts(), len(fake.Received()))
	}
}
