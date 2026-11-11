package observe

// The checks of the apps: what Git declares for the instances — their records and v2's compiled tree — is what the
// cluster, the registry and NATS have.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ImageEvidence is what the image-automation check reads: the apps' ImagePolicies, what the checkout's staging
// release records state, and what the registry says each policy's newest tag is.
type ImageEvidence struct {
	Policies List
	// Records is each app's staging release record, by app; an app the checkout has no record for is not in it.
	Records map[string]StagingRecord
	// Newest is the registry's digest for each Ready policy's newest tag, by policy name.
	Newest map[string]NewestImage
}

// StagingRecord is what one staging release record states: the image and the digest staging should run, or why the
// record cannot be read.
type StagingRecord struct {
	Image, Digest, Problem string
}

// NewestImage is the digest of a tag's linux/amd64 manifest, as the registry answers it, or why it could not be read.
type NewestImage struct {
	Digest, Problem string
}

// ImagePolicy is one app's ImagePolicy as Flux reports it: its Ready condition and the newest tag it picked.
type ImagePolicy struct {
	Name, App, Status, Message, Tag string
}

// Ready is whether Flux says the policy picked a tag: only then is there a newest image to compare staging with.
func (p ImagePolicy) Ready() bool { return p.Status == "True" }

// ImagePolicies are the listed policies, by name; none when the list was not read.
func ImagePolicies(list List) []ImagePolicy {
	found := []ImagePolicy{}
	if list.Status != Present {
		return found
	}
	for _, item := range list.Items {
		var policy struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Status struct {
				LatestRef struct {
					Tag string `json:"tag"`
				} `json:"latestRef"`
			} `json:"status"`
		}
		_ = json.Unmarshal(item, &policy) // the adapter checked each item's identity; a status it cannot read is not Ready
		status, message := readyCondition(item)
		found = append(found, ImagePolicy{policy.Metadata.Name, policy.Metadata.Labels[AppLabel], status, message,
			policy.Status.LatestRef.Tag})
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Name < found[j].Name })
	return found
}

func platformImages(e ImageEvidence) []PlatformEntry {
	if e.Policies.Status != Present {
		return []PlatformEntry{bad("cannot read the apps' ImagePolicies, so no staging deploy can be checked (kubectl -n " +
			"flux-system get imagepolicies; is image-reflector-controller running? make flux-install)")}
	}
	policies := ImagePolicies(e.Policies)
	if len(policies) == 0 {
		return []PlatformEntry{ok("no app has an ImagePolicy, so no staging deploy is checked")}
	}
	entries := make([]PlatformEntry, 0, len(policies))
	for _, policy := range policies {
		if !policy.Ready() {
			entries = append(entries, bad(fmt.Sprintf("ImagePolicy %s is not Ready, so staging gets no new images: %s "+
				"(kubectl -n flux-system describe imagerepository %s; make reconcile UNIT=app-%s)",
				policy.Name, policy.Message, policy.App, policy.Name)))
			continue
		}
		entries = append(entries, platformStaging(policy, e.Records, e.Newest[policy.Name]))
	}
	return entries
}

// platformStaging is whether staging runs the image its policy picked, read from the record that owns the instance:
// the record states the digest of the image's linux/amd64 manifest, and the registry resolves the picked tag to one.
func platformStaging(policy ImagePolicy, records map[string]StagingRecord, newest NewestImage) PlatformEntry {
	stated := CheckoutRecords + "/" + policy.App + "/" + StagingEnvironment + "/release.json"
	record, found := records[policy.App]
	switch {
	case !found:
		return warn(fmt.Sprintf("%s: newest image %s, but this checkout has no %s (git pull if it is behind main)",
			policy.Name, policy.Tag, stated))
	case record.Problem != "":
		return bad(fmt.Sprintf("%s cannot be read (%s), so whether staging runs %s is unchecked", stated, record.Problem, policy.Tag))
	case record.Image == "":
		return bad(fmt.Sprintf("%s states no image, so whether staging runs %s is unchecked", stated, policy.Tag))
	}
	_, repository, _ := strings.Cut(record.Image, "/")
	switch {
	case newest.Problem != "":
		return warn(fmt.Sprintf("%s: cannot read the digest of %s:%s from the registry (%s), so whether %s is deployed is unchecked",
			policy.Name, repository, policy.Tag, newest.Problem, stated))
	case newest.Digest == record.Digest:
		return ok(fmt.Sprintf("%s: newest image %s, deployed", policy.Name, policy.Tag))
	}
	return warn(fmt.Sprintf("%s: newest image %s is not what v2 records state: %s names %s, the registry says %s. The Deploy "+
		"v2 workflow deploys it within the hour (git pull: it may already have); to deploy it now: make v2-deploy, commit "+
		"and push", policy.Name, policy.Tag, stated, record.Digest, newest.Digest))
}

// IsolationEvidence is what the isolation check reads: the app namespaces, each compiled instance's namespace's
// ResourceQuotas and NetworkPolicies, and the team registry.
type IsolationEvidence struct {
	Namespaces List
	// Quotas and Policies are each compiled instance's namespace's collections, by namespace.
	Quotas, Policies map[string]List
	// Teams are the registered teams; TeamsProblem says why the registry could not be read.
	Teams        map[string]bool
	TeamsProblem string
}

const isolationUnread = "cannot read app namespaces, ResourceQuotas or NetworkPolicies, so app isolation is unchecked " +
	"(kubectl get namespaces -l " + AppLabel + ")"

// platformIsolation is whether every instance v2 compiled has, live, its namespace with Git's labels, a registered
// team, the platform quota and a NetworkPolicy on its pods. Namespaces kept for their data after an app was removed
// are not instances and are not checked.
func platformIsolation(e IsolationEvidence, compiled []CompiledInstance) []PlatformEntry {
	if !isolationRead(e, compiled) {
		return []PlatformEntry{bad(isolationUnread)}
	}
	live := map[string]map[string]string{}
	for _, item := range e.Namespaces.Items {
		var namespace compiledObject
		_ = json.Unmarshal(item, &namespace) // the adapter checked each item's identity
		live[namespace.Metadata.Name] = namespace.Metadata.Labels
	}
	entries, complete := []PlatformEntry{}, 0
	if e.TeamsProblem != "" {
		entries = append(entries, bad(fmt.Sprintf("%s cannot be read (%s), so whether each namespace's team is registered "+
			"is unchecked", CheckoutTeams, e.TeamsProblem)))
	}
	for _, instance := range compiled {
		entry, isolated := isolationEntry(e, instance, live)
		if isolated {
			complete++
			continue
		}
		entries = append(entries, entry)
	}
	if complete > 0 {
		entries = append(entries, ok(fmt.Sprintf("%d app namespace(s) have a registered team, the platform quota and a NetworkPolicy", complete)))
	}
	if len(entries) == 0 {
		entries = append(entries, ok("no instance is compiled, so no app namespace is checked"))
	}
	return entries
}

// isolationRead is whether every collection the check needs was read.
func isolationRead(e IsolationEvidence, compiled []CompiledInstance) bool {
	if e.Namespaces.Status != Present {
		return false
	}
	for _, instance := range compiled {
		if namespace := instance.Namespace(); namespace != "" &&
			(e.Quotas[namespace].Status != Present || e.Policies[namespace].Status != Present) {
			return false
		}
	}
	return true
}

// isolationEntry judges one instance; true when it is isolated as Git declares.
func isolationEntry(e IsolationEvidence, instance CompiledInstance, live map[string]map[string]string) (PlatformEntry, bool) {
	namespace, quota, policy, err := isolationObjects(instance)
	if err != nil {
		return bad(fmt.Sprintf("%s: its compiled objects cannot be read (%s), so whether the cluster has them is unchecked "+
			"(make check-apps recompiles them)", instance.folder(), err)), false
	}
	name := namespace.Metadata.Name
	found, exists := live[name]
	if !exists {
		return bad(fmt.Sprintf("%s: the namespace does not exist, so the instance is not running (make app-reconcile APP=%s "+
			"ENV=%s; make flux-reconcile shows why it fails)", name, instance.Application, instance.Environment)), false
	}
	problems := isolationProblems(e, namespace, found)
	if !anyItem(e.Quotas[name], func(item compiledObject) bool { return sameJSON(item.Spec.Hard, quota.Spec.Hard) }) {
		problems = append(problems, "no ResourceQuota as Git declares it")
	}
	if !anyItem(e.Policies[name], func(item compiledObject) bool { return sameJSON(item.Spec.PodSelector, policy.Spec.PodSelector) }) {
		problems = append(problems, "no NetworkPolicy on the app's pods")
	}
	if len(problems) == 0 {
		return PlatformEntry{}, true
	}
	return bad(fmt.Sprintf("%s: %s (make check-apps, then make app-reconcile APP=%s ENV=%s)", name,
		strings.Join(problems, "; "), instance.Application, instance.Environment)), false
}

// isolationObjects are the instance's compiled Namespace, ResourceQuota and NetworkPolicy.
func isolationObjects(instance CompiledInstance) (compiledObject, compiledObject, compiledObject, error) {
	namespace, err := instance.object("namespace.json")
	if err != nil {
		return compiledObject{}, compiledObject{}, compiledObject{}, err
	}
	quota, err := instance.object("resourcequota.json")
	if err != nil {
		return compiledObject{}, compiledObject{}, compiledObject{}, err
	}
	policy, err := instance.object("networkpolicy.json")
	return namespace, quota, policy, err
}

// isolationProblems are the namespace's labels that differ from Git's and a team the registry does not name. The
// cluster adds labels of its own to what Flux applies, so only Git's are compared.
func isolationProblems(e IsolationEvidence, namespace compiledObject, found map[string]string) []string {
	moved := []string{}
	for key, value := range namespace.Metadata.Labels {
		if current, ok := found[key]; !ok || current != value {
			moved = append(moved, key)
		}
	}
	sort.Strings(moved)
	problems := []string{}
	if len(moved) > 0 {
		problems = append(problems, "the namespace labels differ from Git ("+strings.Join(moved, ", ")+")")
	}
	team := namespace.Metadata.Labels[teamLabel]
	if e.TeamsProblem == "" && !e.Teams[team] {
		problems = append(problems, fmt.Sprintf("team '%s' is not in %s (make team-add NAME=%s DESCRIPTION=\"...\")",
			team, CheckoutTeams, team))
	}
	return problems
}

// teamLabel names the team that owns an app's namespace.
const teamLabel = "platform.swhurl.com/team"

// anyItem is whether one of a collection's items satisfies the test.
func anyItem(list List, test func(compiledObject) bool) bool {
	for _, item := range list.Items {
		var object compiledObject
		if json.Unmarshal(item, &object) == nil && test(object) {
			return true
		}
	}
	return false
}

// SigningEvidence is what the signing-keys check reads: the cluster's token issuer and key set, and what the
// platform profile states apps verify calls with.
type SigningEvidence struct {
	// Issuer and Keys are the cluster's; Unread says why they were not read.
	Issuer string
	Keys   []byte
	Unread string
	// StatedIssuer and StatedKeys are the profile's auth block; Unstated says why the profile states none.
	StatedIssuer, StatedKeys, Unstated string
}

func platformSigning(e SigningEvidence) []PlatformEntry {
	if e.Unread != "" {
		return []PlatformEntry{bad("cannot read the cluster's token issuer or signing keys (kubectl get --raw /openid/v1/jwks)")}
	}
	const advice = "so apps refuse every call between them: put the cluster's key set in its auth block and render platform/auth from it"
	var stated any
	if e.Unstated != "" || json.Unmarshal([]byte(e.StatedKeys), &stated) != nil {
		return []PlatformEntry{bad(CheckoutProfile + " states no signing keys, " + advice)}
	}
	entries := []PlatformEntry{}
	if sameJSON([]byte(e.StatedKeys), e.Keys) {
		var set struct {
			Keys []json.RawMessage `json:"keys"`
		}
		_ = json.Unmarshal(e.Keys, &set) // the adapter read a key set
		entries = append(entries, ok(fmt.Sprintf("%s states the cluster's %d signing key(s)", CheckoutProfile, len(set.Keys))))
	} else {
		entries = append(entries, bad(CheckoutProfile+" does not state the cluster's signing key set, "+advice+
			" (docs/operations.md#signing-keys)"))
	}
	if e.StatedIssuer == e.Issuer {
		return append(entries, ok("token issuer is "+e.Issuer))
	}
	return append(entries, bad(fmt.Sprintf("token issuer is %s, but the profile states %s, so apps refuse every call "+
		"between them: fix its auth.issuer and render platform/auth from it (docs/operations.md#signing-keys)",
		e.Issuer, e.StatedIssuer)))
}

// EventEvidence is what the events check reads: NATS, its stream controller, and the streams and consumers in every
// namespace an instance declares one in (EventNamespaces), by namespace.
type EventEvidence struct {
	Server, Controller Read
	Streams, Consumers map[string]List
}

const eventsLost = "no events are published or delivered (make reconcile UNIT=platform-nats)"

func platformEvents(e EventEvidence, compiled []CompiledInstance) []PlatformEntry {
	if !eventsRead(e, compiled) {
		return []PlatformEntry{bad("cannot read NATS, its controller, or the streams and consumers (make reconcile UNIT=platform-nats)")}
	}
	entries := []PlatformEntry{eventWorkload("NATS", e.Server), eventWorkload("the stream controller", e.Controller)}
	wanted, found := 0, true
	for _, instance := range compiled {
		for _, name := range instance.eventObjects() {
			wanted++
			if entry, exists := eventObject(e, instance, name); !exists {
				entries, found = append(entries, entry), false
			}
		}
	}
	if wanted > 0 && found {
		entries = append(entries, ok(fmt.Sprintf("%d declared stream(s) and consumer(s) exist in NATS", wanted)))
	}
	return entries
}

// eventsRead is whether NATS, its controller and every namespace's streams and consumers were read: an absent
// workload is read, and reported as not running.
func eventsRead(e EventEvidence, compiled []CompiledInstance) bool {
	for _, read := range []Read{e.Server, e.Controller} {
		if read.Status == Unavailable || read.Status == "" {
			return false
		}
	}
	for _, namespace := range EventNamespaces(compiled) {
		if e.Streams[namespace].Status != Present || e.Consumers[namespace].Status != Present {
			return false
		}
	}
	return true
}

func eventWorkload(name string, read Read) PlatformEntry {
	if read.Status == Absent {
		return bad(name + " is missing: " + eventsLost)
	}
	if ready, _, _ := readyReplicas(read.Object); ready > 0 {
		return ok(name + " is running")
	}
	return bad(name + " is not running: " + eventsLost)
}

// eventObject is one declared stream or consumer: false, with the entry, when NATS lacks it or it is not Ready.
func eventObject(e EventEvidence, instance CompiledInstance, file string) (PlatformEntry, bool) {
	declared, err := instance.object(file)
	if err != nil {
		return bad(fmt.Sprintf("%s: its compiled objects cannot be read (%s), so whether NATS has them is unchecked "+
			"(make check-apps recompiles them)", instance.folder(), err)), false
	}
	collection := e.Streams
	if declared.Kind == PlatformConsumers.Kind {
		collection = e.Consumers
	}
	where := fmt.Sprintf("%s %s/%s", declared.Kind, declared.Metadata.Namespace, declared.Metadata.Name)
	for _, item := range collection[declared.Metadata.Namespace].Items {
		var live compiledObject
		if json.Unmarshal(item, &live) != nil || live.Metadata.Name != declared.Metadata.Name {
			continue
		}
		status, message := readyCondition(item)
		if status == "True" {
			return PlatformEntry{}, true
		}
		return bad(fmt.Sprintf("%s is not Ready, so its events are not kept or delivered: %s (kubectl -n %s describe %s %s)",
			where, message, declared.Metadata.Namespace, strings.ToLower(declared.Kind), declared.Metadata.Name)), false
	}
	return bad(fmt.Sprintf("%s is what Git declares, but NATS has no such %s (make reconcile UNIT=platform-nats; make "+
		"check-apps says whether Git holds the declaration)", where, strings.ToLower(declared.Kind))), false
}

// EgressEvidence is what the egress check reads: the proxy, and the ports v2's allocation gives the instances.
type EgressEvidence struct {
	Proxy Read
	Ports []InstancePort
	// Problem says why the allocation could not be read.
	Problem string
}

// InstancePort is one instance's port on the egress proxy.
type InstancePort struct {
	Application, Environment string
	Port                     int32
}

const egressLost = "apps cannot reach outside the cluster"

func platformEgress(e EgressEvidence, compiled []CompiledInstance) []PlatformEntry {
	switch e.Proxy.Status {
	case Present:
	case Absent:
		return []PlatformEntry{bad("the egress proxy (egress/egress) is missing: " + egressLost + " (make reconcile UNIT=platform-egress)")}
	default:
		return []PlatformEntry{bad("cannot read the egress proxy (egress/egress): " + egressLost + " (make reconcile UNIT=platform-egress)")}
	}
	ready, wanted, _ := readyReplicas(e.Proxy.Object)
	entries := []PlatformEntry{}
	switch {
	case ready == 0:
		entries = append(entries, bad("the egress proxy is not running: "+egressLost+" (kubectl -n egress get pods; make "+
			"reconcile UNIT=platform-egress)"))
	case ready < wanted:
		entries = append(entries, warn(fmt.Sprintf("the egress proxy runs %d of %d replicas: a restart would stop outbound "+
			"traffic meanwhile (kubectl -n egress get pods)", ready, wanted)))
	default:
		entries = append(entries, ok(fmt.Sprintf("the egress proxy runs %d replicas", ready)))
	}
	problems := portProblems(e, compiled)
	for _, problem := range problems {
		entries = append(entries, bad(problem))
	}
	if len(problems) == 0 {
		users := 0
		for _, instance := range compiled {
			if _, uses := instance.proxyPort(); uses {
				users++
			}
		}
		entries = append(entries, ok(fmt.Sprintf("%d instance(s) use the proxy, each on a port of its own", users)))
	}
	return entries
}

// portProblems are the compiled instances that reach the proxy on a port the allocation does not give them, and every
// port two of them share. An instance with no grant and no database reaches no proxy, and the render gives it no port.
func portProblems(e EgressEvidence, compiled []CompiledInstance) []string {
	if e.Problem != "" {
		return []string{fmt.Sprintf("%s cannot be read (%s), so the proxy's ports are unchecked", CheckoutAllocation, e.Problem)}
	}
	ports := map[string]int32{}
	for _, allocated := range e.Ports {
		ports[allocated.Application+"/"+allocated.Environment] = allocated.Port
	}
	found, owners := []string{}, map[int32][]string{}
	for _, instance := range compiled {
		reached, uses := instance.proxyPort()
		if !uses {
			continue
		}
		name := instance.Application + "/" + instance.Environment
		port, allocated := ports[name]
		switch {
		case !allocated:
			found = append(found, fmt.Sprintf("%s reaches the proxy on port %d, which the allocation does not give it: its "+
				"records render the allocation (docs/services.md#egress-proxy)", name, reached))
			continue
		case port != reached:
			found = append(found, fmt.Sprintf("%s reaches the proxy on port %d, but the allocation gives it %d: its "+
				"records render the allocation (docs/services.md#egress-proxy)", name, reached, port))
			continue
		}
		owners[port] = append(owners[port], name)
	}
	shared := make([]int32, 0, len(owners))
	for port, held := range owners {
		if len(held) > 1 {
			shared = append(shared, port)
		}
	}
	sort.Slice(shared, func(i, j int) bool { return shared[i] < shared[j] })
	for _, port := range shared {
		found = append(found, fmt.Sprintf("port %d is shared by %s", port, strings.Join(owners[port], ", ")))
	}
	return found
}

// proxyPort is the port the instance's compiled NetworkPolicy lets it reach the proxy on, and whether it reaches the
// proxy at all: a rule to the proxy's namespace, as the render writes one for an instance that needs egress.
func (c CompiledInstance) proxyPort() (int32, bool) {
	var policy struct {
		Spec struct {
			Egress []struct {
				Ports []struct{ Port int32 }
				To    []struct {
					NamespaceSelector struct{ MatchLabels map[string]string }
				}
			}
		}
	}
	if json.Unmarshal(c.Files["networkpolicy.json"], &policy) != nil {
		return 0, false
	}
	for _, rule := range policy.Spec.Egress {
		for _, to := range rule.To {
			if to.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] == PlatformEgress.Namespace && len(rule.Ports) == 1 {
				return rule.Ports[0].Port, true
			}
		}
	}
	return 0, false
}
