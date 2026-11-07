package orchestration

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/notifysvc"
	"github.com/samclement/swhurl-platform/v2/adapters/ntfy"
	"github.com/samclement/swhurl-platform/v2/incident"
	"github.com/samclement/swhurl-platform/v2/notify"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// CheckerPorts is the checker's only effects: complete cluster reads, its named state and the two transports.
type CheckerPorts struct {
	Cluster  kube.Collector
	Data     kube.Data
	Platform ntfy.Publisher
	Copies   notifysvc.Sender
}

// CheckOptions supplies all environment and clock facts. Empty NotifyAddress disables new copies; existing copies
// still retry. DryRun reads/evaluates only, with neither state checkpoints nor transport calls.
type CheckOptions struct {
	Now                                         float64
	Base, PlatformAddress, NotifyAddress, Token string
	DryRun                                      bool
}

// CheckReport says how far one pass got, including a candidate state for dry-run/unknown evidence and each offered
// notice. Snapshot failure is always an error, even if its monitor notice was delivered successfully.
type CheckReport struct {
	State   *incident.State
	Offered []CheckerDelivery
}

// CheckerDelivery is what became of a notice: acceptance, or failure with whether a copy remains for retry.
type CheckerDelivery struct {
	Entry incident.Entry
	Err   error
	Retry bool
}

// CheckNotifications runs one pass. A state read/decode failure stops before cluster reads or side effects. An
// unknown snapshot retains history, cancels recovery and can deliver the monitor's incident, but never exits well.
func CheckNotifications(ctx context.Context, ports CheckerPorts, options CheckOptions) (CheckReport, error) {
	previous, err := checkerState(ctx, ports.Data)
	if err != nil {
		return CheckReport{}, err
	}
	snapshot, snapshotErr := checkerSnapshot(ctx, ports.Cluster)
	var state *incident.State
	if snapshotErr == nil {
		state, snapshotErr = incident.Evaluate(previous, snapshot, options.Now, options.Base, options.NotifyAddress != "")
	}
	if snapshotErr != nil {
		state = incident.Unknown(previous, options.Now, options.Base)
	}
	report := CheckReport{State: state}
	if !options.DryRun {
		err = checkerDeliver(ctx, ports, options, &report, snapshotErr == nil)
	}
	if err != nil {
		return report, err
	}
	if snapshotErr != nil {
		return report, errors.New("Kubernetes snapshot failed; no recovery inferred")
	}
	return report, nil
}
func checkerState(ctx context.Context, data kube.Data) (*incident.State, error) {
	found, err := data.GetConfigMap(ctx, incident.Namespace, incident.StateName)
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, nil
	}
	return incident.DecodeState(found.Data[incident.StateKey])
}
func saveChecker(ctx context.Context, data kube.Data, state *incident.State) error {
	raw, err := state.Encode()
	if err != nil {
		return err
	}
	return data.SetConfigMapKey(ctx, incident.Namespace, incident.StateName, incident.StateKey, raw, incident.FieldManager)
}

// checkerSnapshot reads only the collections the model uses. A failed or malformed later read yields no partial
// snapshot; absence is inferred only from complete collections. Lifecycle Events use core v1 field selectors.
func checkerSnapshot(ctx context.Context, cluster kube.Collector) (incident.Snapshot, error) {
	var snapshot incident.Snapshot
	for _, request := range []struct {
		ref  observe.Ref
		into *[]incident.Object
	}{
		{observe.Ref{APIVersion: "kustomize.toolkit.fluxcd.io/v1", Kind: "Kustomization", Namespace: "flux-system"}, &snapshot.Units},
		{observe.Ref{APIVersion: "helm.toolkit.fluxcd.io/v2", Kind: "HelmRelease"}, &snapshot.Releases},
		{observe.Ref{APIVersion: "apps/v1", Kind: "Deployment"}, &snapshot.Workloads},
		{observe.Ref{APIVersion: "apps/v1", Kind: "StatefulSet"}, &snapshot.Workloads},
		{observe.Ref{APIVersion: "v1", Kind: "Pod"}, &snapshot.Pods},
	} {
		var list observe.List
		var err error
		if request.ref.Namespace != "" {
			list, err = cluster.ListNamespace(ctx, request.ref)
		} else {
			list, err = cluster.CollectAllNamespaces(ctx, request.ref)
		}
		if err := appendCheckerItems(request.into, list, err); err != nil {
			return incident.Snapshot{}, err
		}
	}
	for _, kind := range []string{"HelmRelease", "Kustomization"} {
		list, err := cluster.ListEvents(ctx, kind)
		if err := appendCheckerItems(&snapshot.Events, list, err); err != nil {
			return incident.Snapshot{}, err
		}
	}
	return snapshot, nil
}
func appendCheckerItems(into *[]incident.Object, list observe.List, err error) error {
	if err != nil {
		return err
	}
	if list.Status != observe.Present {
		return errors.New("snapshot collection unavailable")
	}
	for _, raw := range list.Items {
		var item incident.Object
		if json.Unmarshal(raw, &item) != nil || item == nil {
			return errors.New("snapshot collection malformed")
		}
		// API collection items may omit kind; the collection owns that fact, not the model.
		if _, has := item["kind"]; !has {
			item["kind"] = list.Ref.Kind
		}
		*into = append(*into, item)
	}
	return nil
}
func checkerDeliver(ctx context.Context, ports CheckerPorts, options CheckOptions, report *CheckReport, recordServiceHealth bool) error {
	state := report.State
	if err := saveChecker(ctx, ports.Data, state); err != nil {
		return err
	}
	accepted, refused := 0, 0
	// Offer each original notice once. A retry moves to the tail immediately, before any later checkpoint.
	for remaining := len(state.Pending()); remaining > 0; remaining-- {
		entry := state.Pending()[0]
		err := sendCheckerEntry(ctx, ports, options, entry, refused > 0)
		delivery := CheckerDelivery{Entry: entry, Err: err}
		if len(entry.About()) == 0 && err != nil {
			report.Offered = append(report.Offered, delivery)
			return err
		}
		delivery.Retry = state.Settle(err == nil, options.Now)
		report.Offered = append(report.Offered, delivery)
		if len(entry.About()) > 0 {
			if err == nil {
				accepted++
			} else {
				refused++
			}
		}
		if err := saveChecker(ctx, ports.Data, state); err != nil {
			return err
		}
	}
	if !recordServiceHealth || accepted+refused == 0 {
		return nil
	}
	state.CopyHealth(accepted, refused, options.Now, options.Base)
	return saveChecker(ctx, ports.Data, state)
}
func sendCheckerEntry(ctx context.Context, ports CheckerPorts, options CheckOptions, entry incident.Entry, refused bool) error {
	if about := entry.About(); len(about) > 0 {
		if refused {
			return errors.New("notify service did not accept the previous copy")
		}
		if len(about) != 2 || ports.Copies == nil {
			return errors.New("notify service unavailable")
		}
		priority := "normal"
		if entry.Priority() >= 4 {
			priority = "high"
		}
		request := notifysvc.Request{App: about[0], Env: about[1], Key: entry.Key(), Title: entry.Title(), Message: entry.Message(), Priority: priority, Click: entry.Click()}
		return ports.Copies.Send(ctx, options.NotifyAddress, options.Token, request)
	}
	if ports.Platform == nil {
		return errors.New("notification sender is required outside dry run")
	}
	tags := []string{"white_check_mark"}
	if entry.Priority() >= 4 {
		tags = []string{"warning"}
	}
	return ports.Platform.Publish(ctx, options.PlatformAddress, notify.Publication{Title: entry.Title(), Message: entry.Message(), Priority: entry.Priority(), Tags: tags, Click: entry.Click()})
}
