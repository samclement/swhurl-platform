package orchestration

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/observe"
)

type statusInstance struct {
	application, environment string
	target                   observe.Target
	declaration              kernel.Declared
}

// StatusAll compiles pinned instances before reading anything live, batches each necessary list once,
// then uses precisely the single-status evaluator. Only the newest known backup Job is read per database.
func StatusAll(ctx context.Context, inputs StatusInputs, instances []Inputs) (observe.Collection, error) {
	if inputs.Changes != nil {
		if err := inputs.Changes.Validate(); err != nil {
			return observe.Collection{}, err
		}
	}
	compiled, err := statusInstances(inputs, instances)
	if err != nil {
		return observe.Collection{}, err
	}
	targets := make([]observe.Target, 0, len(compiled))
	for _, instance := range compiled {
		targets = append(targets, instance.target)
	}
	candidates, err := readCandidates(ctx, inputs, targets)
	if err != nil {
		return observe.Collection{}, err
	}
	result := observe.Collection{Source: inputs.Source, ObservedAt: inputs.Now().UTC().Format(time.RFC3339), Revision: inputs.Revision, Instances: []observe.InstanceReport{}}
	for _, instance := range compiled {
		evidence := candidates.EvidenceFor(instance.target)
		report, err := statusReport(ctx, inputs, instance.target, evidence, result.ObservedAt)
		if err != nil {
			return observe.Collection{}, err
		}
		declaration, err := json.Marshal(instance.declaration)
		if err != nil {
			return observe.Collection{}, err
		}
		result.Instances = append(result.Instances, observe.InstanceReport{Application: instance.application, Environment: instance.environment, Report: report, Inventory: declaration})
	}
	result.Removing = candidates.Removed(targets)
	return result, nil
}

func statusInstances(inputs StatusInputs, instances []Inputs) ([]statusInstance, error) {
	result := make([]statusInstance, 0, len(instances))
	seen := map[string]bool{}
	for _, instance := range instances {
		one := inputs
		one.Inputs = instance
		target, err := statusScopedTarget(one)
		if err != nil {
			return nil, err
		}
		declared, err := declaredInstance(instance)
		if err != nil {
			return nil, err
		}
		identity := declared.application + "/" + declared.environment
		if seen[identity] {
			return nil, &observe.Error{Field: "instances", Reason: "duplicate_identity"}
		}
		seen[identity] = true
		declared.target = target
		result = append(result, declared)
	}
	slices.SortFunc(result, func(a, b statusInstance) int {
		if order := strings.Compare(a.application, b.application); order != 0 {
			return order
		}
		return strings.Compare(a.environment, b.environment)
	})
	return result, nil
}

// declaredInstance is one instance's identity and the declaration its records state: the compiler's own facts and
// the two committed resolution documents, never a read of the cluster.
func declaredInstance(instance Inputs) (statusInstance, error) {
	spec, err := kernel.DecodeInstance(instance.Instance)
	if err != nil {
		return statusInstance{}, err
	}
	profile, err := kernel.DecodeProfile(instance.Profile)
	if err != nil {
		return statusInstance{}, err
	}
	release, err := kernel.DecodeRelease(instance.Release)
	if err != nil {
		return statusInstance{}, err
	}
	app := kernel.AppSpec{}
	if instance.App != nil {
		if app, err = kernel.DecodeApp(instance.App); err != nil {
			return statusInstance{}, err
		}
	}
	declaration, err := kernel.Declare(spec, release, app, profile, instance.Calls, instance.Egress)
	if err != nil {
		return statusInstance{}, err
	}
	return statusInstance{application: spec.Application, environment: spec.Environment, declaration: declaration}, nil
}

func readCandidates(ctx context.Context, inputs StatusInputs, targets []observe.Target) (observe.Candidates, error) {
	lists := []observe.List{}
	for _, query := range observe.BulkPlan(targets) {
		var list observe.List
		var err error
		if query.Ref.Namespace == "" {
			list, err = inputs.Reader.ListAllNamespaces(ctx, query.Ref, query.Selector)
		} else {
			list, err = inputs.Reader.List(ctx, query.Ref, query.Selector)
		}
		if err != nil {
			return observe.Candidates{}, err
		}
		if list.Ref != query.Ref || list.Selector != query.Selector {
			return observe.Candidates{}, &observe.Error{Field: "lists", Reason: "unexpected_collection"}
		}
		lists = append(lists, list)
	}
	return observe.IndexCandidates(lists)
}

func statusReport(ctx context.Context, inputs StatusInputs, target observe.Target, evidence observe.Evidence, at string) (observe.Report, error) {
	evidence.Source, evidence.ObservedAt, evidence.Changes = inputs.Source, at, inputs.Changes
	if evidence.Backup != nil {
		evidence.Backup.MaxAge = inputs.MaxAge
		if evidence.Backup.Pods.Status == observe.Present && evidence.Backup.Run == nil {
			run, err := runEvidence(ctx, inputs.Reader, target, evidence.Backup.Pods)
			if err != nil {
				return observe.Report{}, err
			}
			evidence.Backup.Run = run
		}
		if inputs.Archive != nil {
			listed, err := inputs.Archive(ctx, target.Namespace)
			if err != nil {
				return observe.Report{}, err
			}
			evidence.Backup.Archive = &listed
		}
	}
	return observe.Explain(target, evidence)
}
