package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
)

// runEgress runs the allocation command over a snapshot.
func runEgress(t *testing.T, snapshotPath string, extra ...string) (int, string, string) {
	t.Helper()
	var out, diagnostic bytes.Buffer
	args := append([]string{"egress", "--snapshot", snapshotPath}, extra...)
	status := run(args, &out, &diagnostic, os.ReadFile, files.New())
	return status, out.String(), diagnostic.String()
}

// allocationOf is the document the command wrote.
func allocationOf(t *testing.T, data string) map[string]any {
	t.Helper()
	document := map[string]any{}
	if err := json.Unmarshal([]byte(data), &document); err != nil {
		t.Fatalf("the allocation is not JSON: %v", err)
	}
	return document
}

// portsOf is each instance's allocated port, by environment/application.
func portsOf(t *testing.T, document map[string]any) map[string]float64 {
	t.Helper()
	ports := map[string]float64{}
	for _, raw := range document["instances"].([]any) {
		entry := raw.(map[string]any)
		ports[entry["environment"].(string)+"/"+entry["application"].(string)] = entry["port"].(float64)
	}
	return ports
}

// Every instance gets a port, and a second run keeps them: the port is how the proxy names the caller, so
// it must not move when an unrelated instance appears.
func TestEgressKeepsPorts(t *testing.T) {
	snapshotPath := writeSnapshot(t, twoInstances(t))
	status, stdout, stderr := runEgress(t, snapshotPath)
	if status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	document := allocationOf(t, stdout)
	if document["kind"] != "v2-egress" || document["version"] != float64(1) {
		t.Fatalf("envelope: %v", document)
	}
	ports := portsOf(t, document)
	if ports["staging/shop"] != 30101 || ports["staging/orders"] != 30102 {
		t.Fatalf("a first allocation: %v", ports)
	}
	previous := filepath.Join(t.TempDir(), "previous.json")
	if err := os.WriteFile(previous, []byte(stdout), 0o600); err != nil {
		t.Fatal(err)
	}
	// A new instance takes the next free port; the others keep theirs, whatever the order.
	grown := twoInstances(t)
	instances := grown["instances"].([]any)
	extra := map[string]any{}
	for key, value := range instances[0].(map[string]any) {
		extra[key] = value
	}
	extra["application"] = "billing"
	grown["instances"] = append(instances, extra)
	status, stdout, stderr = runEgress(t, writeSnapshot(t, grown), "--previous", previous)
	if status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	ports = portsOf(t, allocationOf(t, stdout))
	if ports["staging/shop"] != 30101 || ports["staging/orders"] != 30102 || ports["staging/billing"] != 30103 {
		t.Fatalf("growing the allocation: %v", ports)
	}
}

// compile takes the instance's own allocation: its port and the hosts it declares.
func TestCompileTakesTheAllocation(t *testing.T) {
	snapshotPath := writeSnapshot(t, snapshotWithEgress(t, []string{"api.stripe.com"}))
	status, stdout, stderr := runEgress(t, snapshotPath)
	if status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	allocationPath := filepath.Join(t.TempDir(), "egress.json")
	if err := os.WriteFile(allocationPath, []byte(stdout), 0o600); err != nil {
		t.Fatal(err)
	}
	egress, err := compileEgress([]byte(stdout), "shop", "staging")
	if err != nil || egress == nil || egress.Port != 30101 || len(egress.Grants) != 1 || egress.Grants[0] != "api.stripe.com" {
		t.Fatalf("the allocation: %+v %v", egress, err)
	}
	if _, err := compileEgress([]byte(stdout), "absent", "staging"); err == nil {
		t.Fatal("an instance with no allocation was accepted")
	}
	// A document this command did not write is refused.
	if _, err := compileEgress([]byte(`{"kind":"v2-calls","version":1}`), "shop", "staging"); err == nil {
		t.Fatal("another document kind was accepted")
	}
}

// snapshotWithEgress is the two-instance snapshot with one instance declaring hosts.
func snapshotWithEgress(t *testing.T, grants []string) map[string]any {
	t.Helper()
	document := twoInstances(t)
	instance := document["instances"].([]any)[0].(map[string]any)
	settings := instance["records"].(map[string]any)["instance"].(map[string]any)
	settings["egress"] = grants
	return document
}

// An instance whose records a handover removed keeps its allocation: v2 owns it now, and the port is how the
// proxy names it, so dropping it would take the migrated instance's way out away with the records.
func TestEgressKeepsAnInstanceTheSnapshotNoLongerHas(t *testing.T) {
	previous := `{"kind":"v2-egress","version":1,"revision":"` + strings.Repeat("b", 40) + `","instances":[
		{"application":"shop","environment":"staging","port":30101,"grants":[]},
		{"application":"ghost","environment":"staging","port":30105,"grants":["api.example.com:443"]}]}`
	path := filepath.Join(t.TempDir(), "previous.json")
	if err := os.WriteFile(path, []byte(previous), 0o600); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr := runEgress(t, writeSnapshot(t, twoInstances(t)), "--previous", path)
	if status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	document := allocationOf(t, stdout)
	ports := portsOf(t, document)
	if ports["staging/ghost"] != 30105 || ports["staging/shop"] != 30101 {
		t.Fatalf("a migrated instance lost its port: %v", ports)
	}
	// Its grants stay with it: they are what the proxy allows it, and the records that held them are gone.
	for _, raw := range document["instances"].([]any) {
		entry := raw.(map[string]any)
		if entry["application"] == "ghost" {
			if grants := entry["grants"].([]any); len(grants) != 1 || grants[0] != "api.example.com:443" {
				t.Fatalf("the migrated instance's grants: %v", grants)
			}
		}
	}
}

// An instance that declares nothing to reach outside, and keeps no database, gets no port: the proxy serves
// ports for the instances that need one, not for every instance that exists.
func TestEgressSkipsAnInstanceThatNeedsNoWayOut(t *testing.T) {
	document := twoInstances(t)
	settings := document["instances"].([]any)[0].(map[string]any)["records"].(map[string]any)["instance"].(map[string]any)
	delete(settings, "egress")
	status, stdout, stderr := runEgress(t, writeSnapshot(t, document))
	if status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	ports := portsOf(t, allocationOf(t, stdout))
	if _, given := ports["staging/shop"]; given {
		t.Fatalf("an instance that needs no way out was allocated one: %v", ports)
	}
	if ports["staging/orders"] != 30101 {
		t.Fatalf("the instance that does need one: %v", ports)
	}
}
