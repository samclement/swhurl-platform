package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/observe"
)

const waitRev = "main@sha1:new"

// waitCluster answers a wait's reads from scripts: each read of the source takes the next revision and each list the
// next set of units, and the last of each repeats.
type waitCluster struct {
	revisions []string
	polls     [][]string
	sourceErr error
	lists     int
}

func (c *waitCluster) Read(_ context.Context, ref observe.Ref) (observe.Read, error) {
	if ref.Kind != "GitRepository" || ref.Name != observe.FluxSource || ref.Namespace != "flux-system" {
		return observe.Read{}, fmt.Errorf("unexpected read of %+v", ref)
	}
	if c.sourceErr != nil {
		return observe.Read{}, c.sourceErr
	}
	revision := next(&c.revisions)
	artifact := ""
	if revision != "" {
		artifact = fmt.Sprintf(`,"artifact":{"revision":%q}`, revision)
	}
	return observe.Read{Ref: ref, Status: observe.Present, Object: []byte(`{"status":{"observedGeneration":1` + artifact + `}}`)}, nil
}

func (c *waitCluster) ListNamespace(_ context.Context, ref observe.Ref) (observe.List, error) {
	if ref != observe.PlatformUnits {
		return observe.List{}, fmt.Errorf("unexpected list of %+v", ref)
	}
	c.lists++
	units := c.polls[0]
	if len(c.polls) > 1 {
		c.polls = c.polls[1:]
	}
	items := make([][]byte, 0, len(units))
	for _, unit := range units {
		items = append(items, []byte(unit))
	}
	return observe.List{Ref: ref, Status: observe.Present, Items: items}, nil
}

func (c *waitCluster) List(context.Context, observe.Ref, string) (observe.List, error) {
	return observe.List{}, errors.New("a wait lists one namespace")
}

func (c *waitCluster) ListAllNamespaces(context.Context, observe.Ref, string) (observe.List, error) {
	return observe.List{}, errors.New("a wait lists one namespace")
}

func next(script *[]string) string {
	value := (*script)[0]
	if len(*script) > 1 {
		*script = (*script)[1:]
	}
	return value
}

// waitUnitDoc is one unit: ready at applied, or with ready "False", failed at waitRev for reason with message.
func waitUnitDoc(name, ready, applied, reason, message string, suspend bool) string {
	return fmt.Sprintf(`{"metadata":{"name":%q,"generation":1},"spec":{"suspend":%t,"sourceRef":{"kind":"GitRepository","name":"swhurl-platform"}},`+
		`"status":{"observedGeneration":1,"lastAppliedRevision":%q,"lastAttemptedRevision":%q,`+
		`"conditions":[{"type":"Ready","status":%q,"reason":%q,"message":%q}]}}`,
		name, suspend, applied, waitRev, ready, reason, message)
}

func readyAt(name, revision string) string {
	return waitUnitDoc(name, "True", revision, "Succeeded", "", false)
}

// runWait waits against cluster with a clock that only its own pauses advance, five seconds each.
func runWait(cluster *waitCluster, timeout time.Duration) (bool, []string, error) {
	clock := time.Date(2026, time.November, 12, 12, 0, 0, 0, time.UTC)
	var lines []string
	passed, err := FluxWait(context.Background(), FluxWaitInputs{Reader: cluster, Timeout: timeout, Poll: 5 * time.Second,
		Now:      func() time.Time { return clock },
		Sleep:    func(d time.Duration) { clock = clock.Add(d) },
		Progress: func(line FluxWaitLine) { lines = append(lines, string(line.Level)+" "+line.Text) }})
	return passed, lines, err
}

func TestFluxWaitWaitsUntilEveryUnitAppliesTheRevision(t *testing.T) {
	cluster := &waitCluster{revisions: []string{waitRev},
		polls: [][]string{{readyAt("a", waitRev), readyAt("b", "main@sha1:old")}, {readyAt("a", waitRev), readyAt("b", waitRev)}}}
	passed, lines, err := runWait(cluster, time.Minute)
	want := []string{"heading Flux units at " + waitRev, "ok a (0s)", "ok b (5s)"}
	if !passed || err != nil || strings.Join(lines, "\n") != strings.Join(want, "\n") || cluster.lists != 2 {
		t.Fatalf("passed %v, err %v, %d lists, lines:\n%s", passed, err, cluster.lists, strings.Join(lines, "\n"))
	}
}

func TestFluxWaitFollowsACommitThatLandsWhileWaiting(t *testing.T) {
	newer := "main@sha1:newer"
	cluster := &waitCluster{revisions: []string{waitRev, waitRev, newer},
		polls: [][]string{{readyAt("a", waitRev), readyAt("b", "main@sha1:old")}, {readyAt("a", newer), readyAt("b", newer)}}}
	passed, lines, err := runWait(cluster, time.Minute)
	text := strings.Join(lines, "\n")
	if !passed || err != nil || !strings.Contains(text, "info source moved to "+newer+"; waiting for every unit at that") ||
		strings.Count(text, "ok a") != 2 || !strings.Contains(text, "ok b (5s)") {
		t.Fatalf("passed %v, err %v, lines:\n%s", passed, err, text)
	}
}

func TestFluxWaitStopsAtTheFirstFailureWithItsMessage(t *testing.T) {
	cluster := &waitCluster{revisions: []string{waitRev}, polls: [][]string{{readyAt("a", waitRev),
		waitUnitDoc("b", "False", "old", "BuildFailed", "kustomize build", false), readyAt("c", "old")}}}
	passed, lines, err := runWait(cluster, time.Minute)
	if passed || err != nil || lines[len(lines)-1] != "bad b failed: kustomize build" || cluster.lists != 1 {
		t.Fatalf("passed %v, err %v, %d lists, lines:\n%s", passed, err, cluster.lists, strings.Join(lines, "\n"))
	}
}

func TestFluxWaitSkipsASuspendedUnitWithAWarning(t *testing.T) {
	cluster := &waitCluster{revisions: []string{waitRev},
		polls: [][]string{{readyAt("a", waitRev), waitUnitDoc("s", "False", "old", "x", "", true)}}}
	passed, lines, err := runWait(cluster, time.Minute)
	if !passed || err != nil || lines[len(lines)-1] != "warn s is suspended; skipped" {
		t.Fatalf("passed %v, err %v, lines:\n%s", passed, err, strings.Join(lines, "\n"))
	}
}

func TestFluxWaitTimesOutNamingTheUnitsStillWaiting(t *testing.T) {
	cluster := &waitCluster{revisions: []string{waitRev}, polls: [][]string{{readyAt("a", waitRev), readyAt("slow", "old")}}}
	passed, lines, err := runWait(cluster, 12*time.Second)
	if passed || err != nil || lines[len(lines)-1] != "bad timed out after 12s waiting for: slow" || cluster.lists != 4 {
		t.Fatalf("passed %v, err %v, %d lists, lines:\n%s", passed, err, cluster.lists, strings.Join(lines, "\n"))
	}
}

func TestFluxWaitStopsAtASourceWithNoArtifact(t *testing.T) {
	cluster := &waitCluster{revisions: []string{""}, polls: [][]string{{}}}
	passed, lines, err := runWait(cluster, time.Minute)
	if passed || err != nil || strings.Join(lines, "\n") != "bad GitRepository swhurl-platform has no artifact yet" || cluster.lists != 0 {
		t.Fatalf("passed %v, err %v, lines:\n%s", passed, err, strings.Join(lines, "\n"))
	}
}

func TestFluxWaitReportsAFailedReadAsAnError(t *testing.T) {
	cluster := &waitCluster{sourceErr: errors.New("connection refused"), polls: [][]string{{}}}
	if passed, _, err := runWait(cluster, time.Minute); passed || err == nil {
		t.Fatalf("a failed read passed %v with error %v", passed, err)
	}
	malformed := &waitCluster{revisions: []string{waitRev}, polls: [][]string{{`{"metadata":{}}`}}}
	if passed, _, err := runWait(malformed, time.Minute); passed || err == nil {
		t.Fatalf("a malformed unit passed %v with error %v", passed, err)
	}
}
