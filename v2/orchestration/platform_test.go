package orchestration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/kube/kubetest"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// The use case reads the eight things the checks name through the adapter's own refs, so a kind or version the
// adapter does not read is refused here and never reaches the cluster.
func TestPlatformReadsWhatTheChecksName(t *testing.T) {
	for _, ref := range []observe.Ref{observe.PlatformUnits, observe.PlatformAlerts, observe.PlatformProviders,
		observe.PlatformTraefik, observe.PlatformNotify, observe.PlatformConsole, observe.PlatformDashboards,
		observe.PlatformNotifications} {
		group, _, _ := strings.Cut(ref.APIVersion, "/")
		stated, err := kube.RefOf(group, ref.Kind, ref.Namespace, ref.Name)
		if err != nil || stated != ref {
			t.Fatalf("%+v: the adapter states %+v, %v", ref, stated, err)
		}
	}
	fake := kubetest.New(kubetest.Answers{})
	fake.Objects = map[observe.Ref][]byte{}
	fake.Collections = map[observe.Ref][][]byte{observe.PlatformUnits: {}, observe.PlatformAlerts: {}, observe.PlatformProviders: {}}
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.FixedZone("x", 3600))
	report, err := Platform(context.Background(), PlatformInputs{Reader: fake, Source: "test", Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.Reads) != 5 || len(fake.Selectors) != 3 {
		t.Fatalf("reads %v, lists %v", fake.Reads, fake.Selectors)
	}
	if report.ObservedAt != "2026-11-06T11:00:00Z" || report.Source != "test" || len(report.Checks) != 6 || report.Passed() {
		t.Fatalf("%+v", report)
	}
}

func TestPlatformStopsOnARefusedRequest(t *testing.T) {
	fake := kubetest.New(kubetest.Answers{})
	fake.Refuse = &kube.Error{Code: kube.RefusedRequest, Subject: "resource", Reason: "unsupported_kind"}
	_, err := Platform(context.Background(), PlatformInputs{Reader: fake, Now: time.Now})
	var refusal *kube.Error
	if !errors.As(err, &refusal) {
		t.Fatalf("%v", err)
	}
}
