package observe

// The platform scope: every verify-platform check that reads beyond the six cluster checks — more of the cluster, the
// checkout the operator runs from, the registry, the object store, and inputs the caller supplied. Each check is one
// function over the evidence it needs; reading it, and the time, are the caller's. A failed read is a bad entry, never
// absence, and an input the scope needs but was not given is bad "not supplied", never a skipped check.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// The checkout's files the checks compare the cluster with, relative to its root.
const (
	CheckoutProfile    = "v2/testdata/profile.json"
	CheckoutTeams      = "v2-records/teams.json"
	CheckoutRecords    = "v2-records"
	CheckoutCompiled   = "v2-apps/instances"
	CheckoutAllocation = "platform/egress/allocation.json"
	checkoutRoutes     = "platform/notify/routes.json"
)

// StagingEnvironment is the environment that follows each app's newest image.
const StagingEnvironment = "staging"

// The cluster objects the platform scope reads by name, and the collections it reads.
var (
	PlatformImagePolicies   = Ref{APIVersion: "image.toolkit.fluxcd.io/v1", Kind: "ImagePolicy", Namespace: "flux-system"}
	PlatformNamespaces      = Ref{APIVersion: "v1", Kind: "Namespace"}
	PlatformQuotas          = Ref{APIVersion: "v1", Kind: "ResourceQuota"}
	PlatformNetworkPolicies = Ref{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"}
	PlatformNATS            = Ref{APIVersion: "apps/v1", Kind: "StatefulSet", Namespace: "messaging", Name: "nats"}
	PlatformNATSController  = Ref{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "messaging", Name: "nats-controller"}
	PlatformStreams         = Ref{APIVersion: "jetstream.nats.io/v1beta2", Kind: "Stream"}
	PlatformConsumers       = Ref{APIVersion: "jetstream.nats.io/v1beta2", Kind: "Consumer"}
	PlatformEgress          = Ref{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "egress", Name: "egress"}
	PlatformMongoBackup     = Ref{APIVersion: "batch/v1", Kind: "CronJob", Namespace: "observability", Name: "backup-mongodb"}
)

// AppLabel names the app an app namespace or ImagePolicy belongs to; its presence selects them.
const AppLabel = "platform.swhurl.com/app"

// ScopeEvidence is what the platform scope's checks read, beyond the cluster checks' PlatformEvidence.
type ScopeEvidence struct {
	// Compiled is every instance v2 compiled, in order: what Git declares to the cluster.
	Compiled                   []CompiledInstance
	Images                     ImageEvidence
	Isolation                  IsolationEvidence
	Signing                    SigningEvidence
	Events                     EventEvidence
	Egress                     EgressEvidence
	Channels                   ChannelEvidence
	Backups                    MongoBackupEvidence
	Databases                  DatabaseEvidence
	Console                    ConsoleEvidence
	PushWebhook, ImageWebhooks WebhookEvidence
	ConsoleTokens              ConsoleTokenEvidence
	// Credentials is whether object-store credentials were supplied: without them no backup listing is read.
	Credentials bool
	// MaxAge is how old the newest complete backup may be.
	MaxAge time.Duration
}

// PlatformScopeChecks judges the cluster checks' evidence and the scope's, in the order the checks run: the six
// cluster checks, then the scope's own.
func PlatformScopeChecks(e PlatformEvidence, s ScopeEvidence) []PlatformCheck {
	return append(PlatformChecks(e),
		PlatformCheck{"image-automation", "Image Automation", platformImages(s.Images)},
		PlatformCheck{"isolation", "App isolation", platformIsolation(s.Isolation, s.Compiled)},
		PlatformCheck{"signing-keys", "Call identity", platformSigning(s.Signing)},
		PlatformCheck{"events", "Events", platformEvents(s.Events, s.Compiled)},
		PlatformCheck{"egress", "Egress proxy", platformEgress(s.Egress, s.Compiled)},
		PlatformCheck{"channels", "Notification channels", platformChannels(s.Channels)},
		PlatformCheck{"backups", "Backups", platformBackups(s.Backups, s.Credentials, s.MaxAge, e.Now)},
		PlatformCheck{"sqlite-backups", "App SQLite backups", platformDatabases(s.Databases, s.Credentials, s.MaxAge, e.Now)},
		PlatformCheck{"console", "Console", platformConsole(e.Console, s.Console)},
		PlatformCheck{"push-webhook", "Push Webhook", platformPushWebhook(s.PushWebhook)},
		PlatformCheck{"image-webhooks", "Image Webhooks", platformImageWebhooks(s.ImageWebhooks)},
		PlatformCheck{"console-token", "Console GitHub Token", platformConsoleTokens(s.ConsoleTokens, e.Now)},
	)
}

// CompiledInstance is one instance's folder in the compiled tree, v2-apps/instances/<app>/<env>/: the objects v2
// rendered from its records that the checks compare the cluster with, by file name (CompiledObject says which).
type CompiledInstance struct {
	Application, Environment string
	Files                    map[string][]byte
}

// CompiledObject is whether a compiled file is one the checks compare: the namespace, its quota and NetworkPolicy, and
// the stream and consumers the instance declares.
func CompiledObject(name string) bool {
	switch name {
	case "namespace.json", "resourcequota.json", "networkpolicy.json", "stream.json":
		return true
	}
	return strings.HasPrefix(name, "consumer-") && strings.HasSuffix(name, ".json")
}

// folder is where the instance's compiled objects are, as the checks name it.
func (c CompiledInstance) folder() string {
	return CheckoutCompiled + "/" + c.Application + "/" + c.Environment
}

// compiledObject is the identity and labels of a compiled or live object, and the fields the checks compare.
type compiledObject struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name      string            `json:"name"`
		Namespace string            `json:"namespace"`
		Labels    map[string]string `json:"labels"`
	} `json:"metadata"`
	Spec struct {
		Hard        json.RawMessage `json:"hard"`
		PodSelector json.RawMessage `json:"podSelector"`
	} `json:"spec"`
}

// object decodes one compiled file; a file the folder does not have, or cannot be read as an object, is an error that
// names it.
func (c CompiledInstance) object(name string) (compiledObject, error) {
	var found compiledObject
	data, ok := c.Files[name]
	if !ok {
		return found, fmt.Errorf("%s is missing", name)
	}
	if err := json.Unmarshal(data, &found); err != nil || found.Metadata.Name == "" {
		return found, fmt.Errorf("%s is not a compiled object", name)
	}
	return found, nil
}

// Namespace is the instance's namespace, as its compiled Namespace names it; empty when that cannot be read.
func (c CompiledInstance) Namespace() string {
	namespace, err := c.object("namespace.json")
	if err != nil {
		return ""
	}
	return namespace.Metadata.Name
}

// eventObjects are the instance's compiled stream and consumers, in file-name order.
func (c CompiledInstance) eventObjects() []string {
	names := []string{}
	for name := range c.Files {
		if name == "stream.json" || strings.HasPrefix(name, "consumer-") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// EventNamespaces are the namespaces the compiled instances declare a stream or consumer in, sorted: where the
// events check reads NATS's objects.
func EventNamespaces(compiled []CompiledInstance) []string {
	seen := map[string]bool{}
	for _, instance := range compiled {
		for _, name := range instance.eventObjects() {
			if object, err := instance.object(name); err == nil && object.Metadata.Namespace != "" {
				seen[object.Metadata.Namespace] = true
			}
		}
	}
	found := make([]string, 0, len(seen))
	for namespace := range seen {
		found = append(found, namespace)
	}
	sort.Strings(found)
	return found
}

// readyCondition is an object's Ready condition's status and message, as Flux and the NATS controller write it.
func readyCondition(object []byte) (string, string) {
	var document struct {
		Status struct {
			Conditions []struct {
				Type, Status, Message string
			} `json:"conditions"`
		} `json:"status"`
	}
	_ = json.Unmarshal(object, &document) // an object it cannot read has no Ready condition
	for _, condition := range document.Status.Conditions {
		if condition.Type == "Ready" {
			return condition.Status, condition.Message
		}
	}
	return "Unknown", "no Ready condition"
}

// sameJSON is whether two documents state the same value, whatever their key order and spacing.
func sameJSON(a, b []byte) bool {
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return false
	}
	encodedLeft, _ := json.Marshal(left) // a decoded value always encodes
	encodedRight, _ := json.Marshal(right)
	return bytes.Equal(encodedLeft, encodedRight)
}

// readyReplicas is a workload's ready replicas and the replicas it wants (one when it states none); false when the
// object cannot be read as a workload.
func readyReplicas(object []byte) (int64, int64, bool) {
	var workload struct {
		Spec *struct {
			Replicas *int64 `json:"replicas"`
		} `json:"spec"`
		Status struct {
			ReadyReplicas int64 `json:"readyReplicas"`
		} `json:"status"`
	}
	if json.Unmarshal(object, &workload) != nil {
		return 0, 0, false
	}
	wanted := int64(1)
	if workload.Spec != nil && workload.Spec.Replicas != nil {
		wanted = *workload.Spec.Replicas
	}
	return workload.Status.ReadyReplicas, wanted, true
}
