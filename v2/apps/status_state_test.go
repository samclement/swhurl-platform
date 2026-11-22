package apps

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	deliveryrules "github.com/samclement/swhurl-platform/v2/delivery/rules"
	"github.com/samclement/swhurl-platform/v2/observe"
	"github.com/samclement/swhurl-platform/v2/pages"
	provisioningrules "github.com/samclement/swhurl-platform/v2/provisioning/rules"
)

// Status and StatusAll must agree with delivery, including the cases where observation's old state rules did not.
// The cluster fake is the external boundary; both real entry points compile, read and assemble their reports.
func TestStatusUsesDeliveryHealth(t *testing.T) {
	for _, test := range []struct {
		name        string
		change      func(*statusCluster, observe.Target)
		want, label string
	}{
		{"healthy", func(*statusCluster, observe.Target) {}, observe.StateHealthy, "Healthy"},
		{"rollback despite blocked unit and older revision", func(f *statusCluster, target observe.Target) {
			changeStatusObject(f, target.Release, func(d map[string]any) {
				d["status"] = map[string]any{"conditions": []any{
					statusCondition("Ready", "False", "UpgradeFailed"), statusCondition("Remediated", "True", "RollbackSucceeded"),
				}}
			})
			changeStatusObject(f, target.Unit, func(d map[string]any) {
				d["status"] = map[string]any{"lastAppliedRevision": "main@sha1:" + strings.Repeat("a", 40), "conditions": []any{statusCondition("Ready", "False", "DependencyNotReady")}}
			})
		}, observe.StateRolledBack, "Rolled back"},
		{"retrying install failed is stalled", func(f *statusCluster, target observe.Target) {
			changeStatusObject(f, target.Release, func(d map[string]any) {
				d["status"] = map[string]any{"conditions": []any{
					statusCondition("Ready", "False", "InstallFailed"), statusCondition("Reconciling", "True", "Retrying"),
				}}
			})
		}, observe.StateStalled, "Stalled"},
		{"unit not ready is stalled", func(f *statusCluster, target observe.Target) {
			changeStatusObject(f, target.Unit, func(d map[string]any) {
				d["status"].(map[string]any)["conditions"] = []any{statusCondition("Ready", "False", "DependencyNotReady")}
			})
		}, observe.StateStalled, "Stalled"},
		{"suspended but unready workload is failing", func(f *statusCluster, target observe.Target) {
			changeStatusObject(f, target.Release, func(d map[string]any) { d["spec"].(map[string]any)["suspend"] = true })
			unreadyStatusWorkload(f, target)
		}, observe.StateFailing, "Failing"},
		{"unreadable workload stays unknown", func(f *statusCluster, target observe.Target) {
			f.objects[target.Workload] = observe.Read{Ref: target.Workload, Status: observe.Unavailable, Reason: observe.ReasonForbidden}
			f.failedKind, f.failedReason = "Deployment", observe.ReasonForbidden
		}, observe.StateUnknown, "Unknown"},
		{"deleting release is unjudged", func(f *statusCluster, target observe.Target) {
			changeStatusObject(f, target.Release, func(d map[string]any) { d["metadata"].(map[string]any)["deletionTimestamp"] = "2026-11-03T09:59:00Z" })
		}, observe.StateUnknown, "Unknown"},
		{"rollout in progress is updating", func(f *statusCluster, target observe.Target) {
			changeStatusObject(f, target.Release, func(d map[string]any) {
				d["status"] = map[string]any{"conditions": []any{
					statusCondition("Ready", "False", "Progressing"), statusCondition("Reconciling", "True", "Progressing"),
				}}
			})
			unreadyStatusWorkload(f, target)
		}, observe.StateUpdating, "Updating"},
		{"suspended ready workload", func(f *statusCluster, target observe.Target) {
			changeStatusObject(f, target.Release, func(d map[string]any) { d["spec"].(map[string]any)["suspend"] = true })
		}, observe.StateSuspended, "Suspended"},
		{"stopped release", func(f *statusCluster, target observe.Target) {
			changeStatusObject(f, target.Release, func(d map[string]any) {
				d["spec"].(map[string]any)["values"].(map[string]any)["controllers"].(map[string]any)["main"].(map[string]any)["replicas"] = 0
			})
		}, observe.StateStopped, "Stopped"},
		{"missing release is stalled", func(f *statusCluster, target observe.Target) { delete(f.objects, target.Release) }, observe.StateStalled, "Stalled"},
		{"container failure is failing", func(f *statusCluster, target observe.Target) {
			unreadyStatusWorkload(f, target)
			pod := target.Pods
			pod.Name = "main-pod"
			changeStatusObject(f, pod, func(d map[string]any) {
				d["status"] = map[string]any{"containerStatuses": []any{map[string]any{"name": "main", "ready": false, "state": map[string]any{"waiting": map[string]any{"reason": "ImagePullBackOff"}}}}}
			})
		}, observe.StateFailing, "Failing"},
	} {
		for _, unaffected := range []bool{false, true} {
			mode := "applied"
			if unaffected {
				mode = "unaffected"
			}
			t.Run(test.name+"/"+mode, func(t *testing.T) {
				records := statusVariant(t, "hello", "staging", false, false)
				f := cluster()
				target := addStatusInstance(t, f, records)
				test.change(f, target)
				if unaffected {
					changeStatusObject(f, target.Unit, func(d map[string]any) {
						d["status"].(map[string]any)["lastAppliedRevision"] = "main@sha1:" + strings.Repeat("a", 40)
					})
				}
				inputs := observationInputs(f)
				inputs.Inputs = records
				if unaffected {
					inputs.Changes = &deliveryrules.Changes{Kind: "v2-changes", Version: 1, Comparisons: []deliveryrules.Comparison{{From: strings.Repeat("a", 40), To: revision, Status: "complete", Paths: []string{"v2-records/another/staging/instance.json"}}}}
				}
				single, err := Status(t.Context(), inputs)
				if err != nil {
					t.Fatal(err)
				}
				if len(f.reads) != 3 || len(f.lists) != 1 {
					t.Fatalf("unexpected API reads %v lists %v", f.reads, f.lists)
				}
				bulk, err := StatusAll(t.Context(), inputs, []provisioningrules.Inputs{records})
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(single, bulk.Instances[0].Report) {
					t.Fatal("single and bulk reports differ")
				}
				if unaffected && single.Currentness.State != "unaffected" {
					t.Fatalf("currentness %+v", single.Currentness)
				}
				if single.State.Name != test.want {
					t.Fatalf("state %+v, want %s", single.State, test.want)
				}
				if label := pages.StateOf(single.State.Name, single.State.Reason).Label(); label != test.label {
					t.Fatalf("label %s", label)
				}
				encoded, err := observe.EncodeReport(single)
				if err != nil {
					t.Fatal(err)
				}
				var document struct {
					Version   int
					State     observe.State
					Promotion deliveryrules.PromotionDecision
				}
				if err := json.Unmarshal(encoded, &document); err != nil {
					t.Fatal(err)
				}
				if document.Version != 6 || document.State != single.State || document.Promotion.Ready != (test.want == observe.StateHealthy) {
					t.Fatalf("document %+v", document)
				}
				if strings.Contains(test.name, "container failure") && (len(single.Problems) != 1 || single.Problems[0].Reason != "ImagePullBackOff") {
					t.Fatalf("lost problems %+v", single.Problems)
				}
			})
		}
	}
}

func statusCondition(kind, status, reason string) map[string]any {
	return map[string]any{"type": kind, "status": status, "reason": reason, "observedGeneration": 1}
}
func changeStatusObject(f *statusCluster, ref observe.Ref, change func(map[string]any)) {
	var document map[string]any
	if err := json.Unmarshal(f.objects[ref].Object, &document); err != nil {
		panic(err)
	}
	change(document)
	f.put(ref, document)
}
func unreadyStatusWorkload(f *statusCluster, target observe.Target) {
	changeStatusObject(f, target.Workload, func(d map[string]any) {
		d["status"].(map[string]any)["readyReplicas"] = 0
		d["status"].(map[string]any)["availableReplicas"] = 0
	})
}

// Only complete matching revision evidence can exempt a healthy instance from Updating. Unknown/malformed
// revisions stay unknown; these decisions compose with, rather than replace, the health cases above.
func TestStatusComposesRevisionCurrentness(t *testing.T) {
	old := strings.Repeat("a", 40)
	proof := func(status string, paths ...string) *deliveryrules.Changes {
		return &deliveryrules.Changes{Kind: "v2-changes", Version: 1, Comparisons: []deliveryrules.Comparison{{From: old, To: revision, Status: status, Paths: append([]string{}, paths...)}}}
	}
	for _, test := range []struct {
		name, applied     string
		changes           *deliveryrules.Changes
		want, currentness string
	}{
		{"applied", "main@sha1:" + revision, nil, observe.StateHealthy, "applied"},
		{"unproved older revision", "main@sha1:" + old, nil, observe.StateUpdating, "unknown"},
		{"unaffected older revision", "main@sha1:" + old, proof("complete", "v2-records/other/staging/instance.json"), observe.StateHealthy, "unaffected"},
		{"changed instance", "main@sha1:" + old, proof("complete", "v2-records/hello/staging/instance.json"), observe.StateUpdating, "pending"},
		{"incomplete proof", "main@sha1:" + old, proof("incomplete"), observe.StateUpdating, "unknown"},
		{"unavailable proof", "main@sha1:" + old, proof("unavailable"), observe.StateUpdating, "unknown"},
		{"no applied revision", "", nil, observe.StateUpdating, "pending"},
		{"malformed revision", "main@sha1:deadbeef", nil, observe.StateUnknown, "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := cluster()
			records := statusVariant(t, "hello", "staging", false, false)
			target := addStatusInstance(t, f, records)
			changeStatusObject(f, target.Unit, func(d map[string]any) { d["status"].(map[string]any)["lastAppliedRevision"] = test.applied })
			inputs := observationInputs(f)
			inputs.Inputs, inputs.Changes = records, test.changes
			single, err := Status(t.Context(), inputs)
			if err != nil {
				t.Fatal(err)
			}
			bulk, err := StatusAll(t.Context(), inputs, []provisioningrules.Inputs{records})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(single, bulk.Instances[0].Report) {
				t.Fatal("single and bulk reports differ")
			}
			if single.State.Name != test.want || single.Currentness.State != test.currentness || single.Currentness.AppliedRevision != test.applied {
				t.Fatalf("state %+v, currentness %+v", single.State, single.Currentness)
			}
		})
	}
}

// Both new states need operator attention, rather than being counted as Healthy or Updating or hidden in a
// collapsed app row. The page's key is also a single CSS class even though the report says "rolled back".
func TestDeploymentStatesRemainVisibleInConsole(t *testing.T) {
	for _, name := range []string{observe.StateRolledBack, observe.StateStalled} {
		t.Run(name, func(t *testing.T) {
			state := pages.StateOf(name, "deployment verdict")
			if strings.Contains(state.Key, " ") {
				t.Fatalf("CSS key %q", state.Key)
			}
			page := pages.AppsPageOf(pages.AppsInput{Rows: []pages.AppRow{{Instance: pages.Instance{App: "hello", Env: "staging"}, State: state}}, Attention: true})
			if page.Tally.Attention != 1 || page.Tally.Healthy != 0 || page.Tally.Updating != 0 || len(page.Apps) != 1 || !page.Apps[0].Open {
				t.Fatalf("state %s page %+v", name, page)
			}
		})
	}
}
