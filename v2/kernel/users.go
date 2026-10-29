package kernel

import (
	"regexp"
	"slices"
	"strings"
)

// The server's user list is one platform document: NATS reads it as Helm values, so it is a ConfigMap whose
// single data key holds the users and their permissions. It is public material — keys and permissions, never a
// seed — so it can live in Git, and one instance signing in is a line in this file.
//
// Every instance that has a key is a user, whether or not it declares events: the same identity reads the
// instance's feature-flag overrides. The platform's own two identities are users too — the stream controller and
// the console — and they are first, because they are not instances.
const (
	usersObjectName = "nats-users"
	usersFolder     = "nats"
	usersKey        = "values.yaml"
	natsNamespace   = "messaging"
	consoleInbox    = "_INBOX.console"
	// usersOwner is the platform: the object names no instance, and one claiming it would be a second owner.
	usersOwner = "platform"
)

// NATSUsers is what the user list states: the platform's two service identities and every instance that signs in.
type NATSUsers struct {
	ControllerKey, ConsoleKey string
	Instances                 []NATSUser
}

// NATSUser is one instance's identity and what it may do: publish under its own name when its release declares
// events, and read the subscriptions that connected.
type NATSUser struct {
	Application, Environment, NKey string
	Publishes                      bool
	Subscriptions                  []NATSSubscription
}

// NATSSubscription is one connected subscription as the permission names it: the publisher's stream, this
// subscriber's durable consumer on it, and the subject it filters.
type NATSSubscription struct{ Stream, Consumer, Subject string }

// RenderUsers renders the user list from the platform's keys and every keyed instance. It refuses a key that is
// not a user public nkey, and an instance whose names could not be spelled in a permission.
func RenderUsers(users NATSUsers) (ResourceSet, error) {
	for _, key := range []struct{ field, nkey string }{{"users.controllerKey", users.ControllerKey},
		{"users.consoleKey", users.ConsoleKey}} {
		if !nkeyPattern().MatchString(key.nkey) {
			return ResourceSet{}, refuse(Invalid, key.field, "expected a user public nkey")
		}
	}
	entries := []any{controllerUser(users.ControllerKey), consoleUser(users.ConsoleKey)}
	claimed := map[string]bool{}
	for _, user := range sortedUsers(users.Instances) {
		if err := user.validate(); err != nil {
			return ResourceSet{}, err
		}
		if claimed[user.NKey] {
			return ResourceSet{}, refuse(Invalid, "users.nkey", "two instances claim one key")
		}
		claimed[user.NKey] = true
		entries = append(entries, appUser(user))
	}
	values := manifest{"config": manifest{"merge": manifest{"authorization": manifest{"users": entries}}}}
	document := manifest{"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": manifest{"name": usersObjectName, "namespace": natsNamespace,
			"labels": manifest{"reconcile.fluxcd.io/watch": "Enabled"}},
		// The key holds JSON, which is YAML, so Helm parses it as it parses the hand-written file it replaces.
		"data": manifest{usersKey: string(encodeManifest(values))}}
	return ResourceSet{
		Files:     []File{{usersFolder + "/users.json", encodeManifest(document)}},
		Inventory: []Claim{{Object{"", "ConfigMap", natsNamespace, usersObjectName}, usersOwner}},
	}, nil
}

// sortedUsers is the list's order: staging's instances before production's, and each environment's by app name.
// The order means nothing to NATS; it is fixed so the document is the same bytes every time.
func sortedUsers(users []NATSUser) []NATSUser {
	ordered := slices.Clone(users)
	slices.SortFunc(ordered, func(a, b NATSUser) int {
		if environmentRank(a.Environment) != environmentRank(b.Environment) {
			return environmentRank(a.Environment) - environmentRank(b.Environment)
		}
		if a.Application != b.Application {
			return strings.Compare(a.Application, b.Application)
		}
		return strings.Compare(a.Environment, b.Environment)
	})
	return ordered
}

// environmentRank is the order the platform lists environments in: staging, then production.
func environmentRank(environment string) int {
	if environment == "prod" {
		return 1
	}
	return 0
}

// validate checks what a permission could not be spelled without: the instance's names, the key it signs in
// with, and the three names of every subscription.
func (u NATSUser) validate() error {
	if !namePattern.MatchString(u.Application) || (u.Environment != "staging" && u.Environment != "prod") {
		return refuse(Invalid, "users.instance", "expected an app name and an environment")
	}
	if !nkeyPattern().MatchString(u.NKey) {
		return refuse(Invalid, "users.nkey", "expected a user public nkey")
	}
	for _, subscription := range u.Subscriptions {
		if subscription.Stream == "" || subscription.Consumer == "" || subscription.Subject == "" {
			return refuse(Invalid, "users.subscription", "expected a stream, a consumer and a subject")
		}
	}
	return nil
}

// appUser is one instance's entry: the platform's flag permissions, its own subject when it publishes, and the
// consumer calls for what it subscribes to.
func appUser(user NATSUser) manifest {
	allow := flagAllow(user.Environment, user.Application)
	if user.Publishes {
		// The whole prefix, not the list of events: an image can publish a new event before the platform has read
		// its description, and the app's own helper holds it to the events the document declares.
		allow = append(allow, user.Environment+"."+user.Application+".>")
	}
	for _, subscription := range user.Subscriptions {
		on := subscription.Stream + "." + subscription.Consumer
		allow = append(allow, "$JS.API.CONSUMER.INFO."+on, "$JS.API.CONSUMER.MSG.NEXT."+on, "$JS.ACK."+on+".>")
		// The Java client reads the stream's description before it binds to its consumer, and the JavaScript one
		// does not. Read-only, and of a stream whose events the subscription already reads.
		if info := "$JS.API.STREAM.INFO." + subscription.Stream; !slices.Contains(allow, info) {
			allow = append(allow, info)
		}
	}
	return manifest{"nkey": user.NKey, "permissions": manifest{
		"publish":   manifest{"allow": allow},
		"subscribe": manifest{"allow": []any{inbox(user.Environment, user.Application) + ".>"}}}}
}

// flagAllow is what an app needs to watch its own flag overrides, and nothing else in the bucket: an ordered
// consumer whose filter is in the request subject, so the server refuses a watch of any other key.
func flagAllow(environment, application string) []string {
	bucket := flagsStream(environment)
	return []string{"$JS.API.INFO", "$JS.API.STREAM.INFO." + bucket,
		"$JS.API.CONSUMER.CREATE." + bucket + ".*." + flagKey(environment, application),
		"$JS.API.CONSUMER.DELETE." + bucket + ".*", "$JS.API.CONSUMER.INFO." + bucket + ".*"}
}

// controllerUser is the stream controller: the JetStream API only. It creates and deletes the streams and
// consumers the instances declare.
func controllerUser(nkey string) manifest {
	return manifest{"nkey": nkey, "permissions": manifest{
		"publish": manifest{"allow": []any{"$JS.API.>"}}, "subscribe": manifest{"allow": []any{"_INBOX.>"}}}}
}

// consoleUser is the console: it writes and reads flag overrides in both environments' buckets, nothing else.
func consoleUser(nkey string) manifest {
	allow := []any{"$JS.API.INFO"}
	for _, environment := range []string{"staging", "prod"} {
		bucket := flagsStream(environment)
		allow = append(allow, "$KV."+flagsBucket(environment)+".>", "$JS.API.STREAM.INFO."+bucket,
			"$JS.API.DIRECT.GET."+bucket+".>", "$JS.API.STREAM.MSG.GET."+bucket,
			"$JS.API.CONSUMER.CREATE."+bucket+".>", "$JS.API.CONSUMER.DELETE."+bucket+".*",
			"$JS.API.CONSUMER.INFO."+bucket+".*")
	}
	return manifest{"nkey": nkey, "permissions": manifest{
		"publish": manifest{"allow": allow}, "subscribe": manifest{"allow": []any{consoleInbox + ".>"}}}}
}

// flagsBucket is one environment's key-value bucket for feature-flag overrides, as a subject prefix.
func flagsBucket(environment string) string { return "flags-" + environment }

// flagsStream is that bucket's own stream, which is what the JetStream API names.
func flagsStream(environment string) string { return "KV_" + flagsBucket(environment) }

// flagKey is one instance's keys in that bucket.
func flagKey(environment, application string) string {
	return "$KV." + flagsBucket(environment) + "." + environment + "." + application + ".>"
}

// inbox is where replies to an app's requests arrive: its own prefix, so no app can read another's.
func inbox(environment, application string) string {
	return "_INBOX." + environment + "." + application
}

// nkeyPattern is a user public nkey: base32 of the 35 bytes nkeys encodes, starting with U.
func nkeyPattern() *regexp.Regexp { return regexp.MustCompile(`^U[A-Z2-7]{55}$`) }
