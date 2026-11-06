package orchestration

import (
	"context"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// PlatformInputs is one run of the platform checks: the reader, and the source and clock of its evidence.
type PlatformInputs struct {
	Reader kube.Reader
	Source string
	Now    func() time.Time
}

// Platform reads what the platform checks need — three namespace collections and five objects, however many
// instances there are — and reports their verdicts. A read the cluster answers with a failure is evidence the checks
// judge; a request the adapter refuses to make is an error, and no report is made.
func Platform(ctx context.Context, inputs PlatformInputs) (observe.PlatformReport, error) {
	evidence := observe.PlatformEvidence{}
	for _, list := range []struct {
		ref    observe.Ref
		target *observe.List
	}{
		{observe.PlatformUnits, &evidence.Units},
		{observe.PlatformAlerts, &evidence.Alerts},
		{observe.PlatformProviders, &evidence.Providers},
	} {
		found, err := inputs.Reader.ListNamespace(ctx, list.ref)
		if err != nil {
			return observe.PlatformReport{}, err
		}
		*list.target = found
	}
	for _, read := range []struct {
		ref    observe.Ref
		target *observe.Read
	}{
		{observe.PlatformTraefik, &evidence.Traefik},
		{observe.PlatformNotify, &evidence.Notify},
		{observe.PlatformConsole, &evidence.Console},
		{observe.PlatformDashboards, &evidence.Dashboards},
		{observe.PlatformNotifications, &evidence.Notifications},
	} {
		found, err := inputs.Reader.Read(ctx, read.ref)
		if err != nil {
			return observe.PlatformReport{}, err
		}
		*read.target = found
	}
	now := inputs.Now().UTC()
	evidence.Now = now
	return observe.PlatformReport{Source: inputs.Source, ObservedAt: now.Format(time.RFC3339),
		Checks: observe.PlatformChecks(evidence)}, nil
}
