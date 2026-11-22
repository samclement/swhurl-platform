package health

import (
	"context"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	healthrules "github.com/samclement/swhurl-platform/v2/health/rules"
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
func Platform(ctx context.Context, inputs PlatformInputs) (healthrules.PlatformReport, error) {
	evidence, err := platformEvidence(ctx, inputs.Reader)
	if err != nil {
		return healthrules.PlatformReport{}, err
	}
	now := inputs.Now().UTC()
	evidence.Now = now
	return healthrules.PlatformReport{Source: inputs.Source, ObservedAt: now.Format(time.RFC3339),
		Checks: healthrules.PlatformChecks(evidence)}, nil
}

// platformEvidence makes the cluster checks' reads.
func platformEvidence(ctx context.Context, reader kube.Reader) (healthrules.PlatformEvidence, error) {
	evidence := healthrules.PlatformEvidence{}
	for _, list := range []struct {
		ref    observe.Ref
		target *observe.List
	}{
		{healthrules.PlatformUnits, &evidence.Units},
		{healthrules.PlatformAlerts, &evidence.Alerts},
		{healthrules.PlatformProviders, &evidence.Providers},
	} {
		found, err := reader.ListNamespace(ctx, list.ref)
		if err != nil {
			return healthrules.PlatformEvidence{}, err
		}
		*list.target = found
	}
	for _, read := range []struct {
		ref    observe.Ref
		target *observe.Read
	}{
		{healthrules.PlatformTraefik, &evidence.Traefik},
		{healthrules.PlatformNotify, &evidence.Notify},
		{healthrules.PlatformConsole, &evidence.Console},
		{healthrules.PlatformDashboards, &evidence.Dashboards},
		{healthrules.PlatformNotifications, &evidence.Notifications},
	} {
		found, err := reader.Read(ctx, read.ref)
		if err != nil {
			return healthrules.PlatformEvidence{}, err
		}
		*read.target = found
	}
	return evidence, nil
}
