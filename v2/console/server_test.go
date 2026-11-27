package console_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/kube/kubetest"
	"github.com/samclement/swhurl-platform/v2/console"
	healthrules "github.com/samclement/swhurl-platform/v2/health/rules"
	"github.com/samclement/swhurl-platform/v2/observe"
	"github.com/samclement/swhurl-platform/v2/pages"
)

func TestConsoleChecksRetainsClusterReportAndFailedEvidence(t *testing.T) {
	reader := kubetest.New(kubetest.Answers{Failed: healthrules.PlatformTraefik})
	reader.Objects = map[observe.Ref][]byte{}
	reader.Collections = map[observe.Ref][][]byte{
		healthrules.PlatformUnits: {}, healthrules.PlatformAlerts: {}, healthrules.PlatformProviders: {},
	}
	clockCalls := 0
	checks, err := console.Server{Reader: reader, Now: func() time.Time {
		clockCalls++
		return time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	}}.Checks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var sections []string
	for _, section := range checks.Sections {
		sections = append(sections, section.Title)
		if len(section.Entries) == 0 {
			t.Fatalf("empty section: %+v", section)
		}
	}
	want := []string{"Flux Kustomizations", "Ingress", "Alerts", "Notify service", "App dashboards and alerts", "Notifications"}
	if checks.Passed || !reflect.DeepEqual(sections, want) || clockCalls != 1 {
		t.Fatalf("checks %+v, clock calls %d", checks, clockCalls)
	}
	ingress := checks.Sections[1].Entries
	if len(ingress) != 1 || ingress[0].Level != "bad" || !strings.Contains(ingress[0].Message, "cannot read Traefik") {
		t.Fatalf("failed read must remain a bad entry: %+v", ingress)
	}
	wantReads := []observe.Ref{healthrules.PlatformTraefik, healthrules.PlatformNotify, healthrules.PlatformConsole,
		healthrules.PlatformDashboards, healthrules.PlatformNotifications}
	if !reflect.DeepEqual(reader.Reads, wantReads) || len(reader.Selectors) != 3 || len(reader.Calls) != 0 {
		t.Fatalf("reads %v, lists %v, data calls %v", reader.Reads, reader.Selectors, reader.Calls)
	}
}

func TestConsoleChecksWrapsRefusalWithoutReport(t *testing.T) {
	refusal := &kube.Error{Code: kube.RefusedRequest, Subject: "resource", Reason: "unsupported_kind"}
	reader := kubetest.New(kubetest.Answers{})
	reader.Refuse = refusal
	checks, err := console.Server{Reader: reader, Now: func() time.Time {
		t.Fatal("a refused read must not ask the clock")
		return time.Time{}
	}}.Checks(context.Background())
	if !errors.Is(err, refusal) || !strings.HasPrefix(err.Error(), "platform checks unavailable: ") ||
		!reflect.DeepEqual(checks, pages.Checks{}) {
		t.Fatalf("checks %+v, error %v", checks, err)
	}
	if len(reader.Reads) != 0 || len(reader.Selectors) != 1 {
		t.Fatalf("reads continued after refusal: %+v", reader)
	}
}
