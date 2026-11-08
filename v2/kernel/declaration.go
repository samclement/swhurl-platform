package kernel

import "strconv"

// Declared is one instance as its records state it: the facts a page shows about it, with no evidence, and the
// relationships it declares with what the resolution made of them. Every field is derived here from the same
// inputs the compiler renders from, plus the two committed documents the resolver wrote, so nothing is read from
// the cluster and no rule is stated twice.
type Declared struct {
	Team          string               `json:"team"`
	Kind          string               `json:"kind"`
	Exposure      string               `json:"exposure"`
	Port          int32                `json:"port"`
	Hosts         []string             `json:"hosts"`
	Address       string               `json:"address"`
	API           string               `json:"api"`
	Artifact      DeclaredArtifact     `json:"artifact"`
	Database      DeclaredDatabase     `json:"database"`
	Offers        string               `json:"offers"`
	Egress        DeclaredEgress       `json:"egress"`
	Calls         []string             `json:"calls"`
	Allows        []string             `json:"allows"`
	Callers       []string             `json:"callers"`
	Publishes     []string             `json:"publishes"`
	Subscriptions []string             `json:"subscriptions"`
	Connections   []DeclaredConnection `json:"connections"`
}

// DeclaredArtifact is the image an instance runs: the repository it came from and the digest that identifies it.
type DeclaredArtifact struct {
	Image  string `json:"image"`
	Digest string `json:"digest"`
}

// DeclaredDatabase is the state an instance keeps itself: the kind its release asks for and the file inside the
// mounted volume it writes. Both are empty for an instance that keeps none.
type DeclaredDatabase struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}

// DeclaredEgress is an instance's place on the egress proxy: the port the allocation keeps for it, and the hosts
// outside the cluster it may reach through it. A port with no grants is an allocation, not a reachable host; the
// grants are the allocation's, so a database's backup host appears without the record naming it.
type DeclaredEgress struct {
	Port   int32    `json:"port"`
	Grants []string `json:"grants"`
}

// DeclaredConnection is one relationship as one row: the role, the app, event or host on the other side, and what
// the resolution says about it. Outcome is empty for a row that is only a declaration and for one the resolution
// document does not mention, which the page states as unread rather than connected.
type DeclaredConnection struct {
	Role    string `json:"role"`
	App     string `json:"app,omitempty"`
	Event   string `json:"event,omitempty"`
	Host    string `json:"host,omitempty"`
	Outcome string `json:"outcome,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// The roles a connection row states, in the order a page lists them: what this instance declared first, then what
// other instances declared about it.
const (
	RoleCalls      = "calls"
	RoleSubscribes = "subscribes"
	RolePublishes  = "publishes"
	RoleReaches    = "reaches"
	RoleCalledBy   = "called-by"
	RoleSubscriber = "subscriber"
)

// The outcomes a row states. A relationship that waits or is refused carries the resolver's own reason code.
const (
	Connected = "connected"
	Waiting   = "waiting"
	Refused   = "refused"
)

// Declare states one instance as its records describe it. The resolution and the allocation are the committed
// documents the renderer wrote; a caller that supplies neither gets declarations without outcomes and a zero
// egress, which is a fact the page states rather than a guessed connection.
func Declare(spec InstanceSpec, release ReleaseBundle, app AppSpec, profile PlatformProfile,
	resolution, allocation []byte) (Declared, error) {
	declared := Declared{
		Team: app.Team, Kind: release.Runtime.Kind, Exposure: spec.Grants.Exposure, Port: release.Runtime.Port,
		Hosts:    []string{},
		Artifact: DeclaredArtifact{Image: release.Artifact.Image, Digest: release.Artifact.Digest},
		Database: DeclaredDatabase{Kind: release.Database},
		Egress:   DeclaredEgress{Grants: []string{}},
		Calls:    clones(spec.Calls), Allows: clones(spec.Allows), Callers: []string{},
		Publishes: clones(release.Publishes), Subscriptions: clones(spec.Events.Subscriptions),
		Connections: []DeclaredConnection{},
	}
	if spec.Grants.Exposure == "authenticated-web" && profile.Routing.configured() {
		declared.Hosts = append(declared.Hosts, routeHost(InstanceFacts{Application: spec.Application,
			Environment: spec.Environment}, profile.Routing))
	}
	declared.Address = declaredAddress(spec, release.Runtime.Kind, release.Runtime.Port)
	declared.Database.Path = declaredPath(release.Database)
	declared.Offers, declared.API = declaredOffers(release, declared.Hosts)
	egress, err := declaredEgress(allocation, spec.Application, spec.Environment)
	if err != nil {
		return Declared{}, err
	}
	declared.Egress = egress
	document, err := declaredResolution(resolution)
	if err != nil {
		return Declared{}, err
	}
	declared.Callers = declaredCallers(document, spec)
	declared.Connections = declaredRows(spec, release, document, declared.Egress.Grants)
	return declared, nil
}

// declaredAddress is where an instance's own Service listens, as the variables the compiler writes name it. A
// worker renders no Service, so it has no in-cluster address to call.
func declaredAddress(spec InstanceSpec, kind string, port int32) string {
	if kind != "web" || port <= 0 {
		return ""
	}
	return "http://" + spec.Application + "." + NamespaceOf(spec.Application, spec.Environment) +
		".svc.cluster.local:" + strconv.Itoa(int(port))
}

// declaredPath is the file a database instance keeps its data in, or nothing when it keeps no database.
func declaredPath(database string) string {
	if database == "" {
		return ""
	}
	return DatabasePath
}

// declaredOffers is what an image publishes and where it serves it: the commit its documents were read at and the
// API description's address, which needs a host to be reachable. A release that states no documents offers
// nothing, and the page says so rather than naming a commit nobody read.
func declaredOffers(release ReleaseBundle, hosts []string) (commit, api string) {
	if len(release.Documents) == 0 {
		return "", ""
	}
	commit = offerCommit(release.Source.Commit)
	if release.APIPath != "" && len(hosts) > 0 {
		api = "https://" + hosts[0] + release.APIPath
	}
	return commit, api
}

// declaredEgress is the instance's allocation: its port, whether or not it may reach anything, and the hosts the
// allocation grants. A caller that supplies no allocation gets a zero egress.
func declaredEgress(allocation []byte, application, environment string) (DeclaredEgress, error) {
	if allocation == nil {
		return DeclaredEgress{Grants: []string{}}, nil
	}
	document, err := DecodeEgressDocument(allocation)
	if err != nil {
		return DeclaredEgress{}, err
	}
	own, err := document.EgressOf(application, environment)
	if err != nil {
		return DeclaredEgress{Grants: []string{}}, nil // the document does not name this instance
	}
	return DeclaredEgress{Port: own.Port, Grants: clones(own.Grants)}, nil
}

// declaredResolution reads the committed resolution. A caller that supplies none gets an empty document, so every
// declared relationship is a row without an outcome instead of a connection nobody agreed to.
func declaredResolution(document []byte) (ConnectionsDocument, error) {
	if document == nil {
		return ConnectionsDocument{}, nil
	}
	return resolution(document)
}

// declaredCallers is who may call this instance, in the resolution's order.
func declaredCallers(document ConnectionsDocument, spec InstanceSpec) []string {
	callers := []string{}
	for _, pair := range document.Pairs {
		if pair.Environment == spec.Environment && pair.Callee == spec.Application {
			callers = append(callers, pair.Caller)
		}
	}
	return callers
}

// declaredRows is one instance's relationships in the order a page lists them: what it declared, then what other
// instances declared about it.
func declaredRows(spec InstanceSpec, release ReleaseBundle, document ConnectionsDocument, grants []string) []DeclaredConnection {
	rows := declaredOwnRows(spec, release, document)
	for _, host := range grants {
		rows = append(rows, DeclaredConnection{Role: RoleReaches, Host: host})
	}
	return append(rows, declaredPeerRows(spec, document)...)
}

// declaredOwnRows is what this instance declared: the calls it makes, the events it receives and the events it
// publishes, each with the resolution's answer where it has one.
func declaredOwnRows(spec InstanceSpec, release ReleaseBundle, document ConnectionsDocument) []DeclaredConnection {
	rows := make([]DeclaredConnection, 0, len(spec.Calls)+len(spec.Events.Subscriptions)+len(release.Publishes))
	for _, callee := range spec.Calls {
		outcome, reason := callOutcome(document, spec, callee)
		rows = append(rows, DeclaredConnection{Role: RoleCalls, App: callee, Outcome: outcome, Reason: reason})
	}
	for _, name := range spec.Events.Subscriptions {
		publisher, event := splitSubscription(name)
		outcome, reason := subscriptionOutcome(document, spec, publisher, event)
		rows = append(rows, DeclaredConnection{Role: RoleSubscribes, App: publisher, Event: event,
			Outcome: outcome, Reason: reason})
	}
	for _, event := range release.Publishes {
		rows = append(rows, DeclaredConnection{Role: RolePublishes, Event: event})
	}
	return rows
}

// declaredPeerRows is what other instances declared about this one: who calls it, and which instances receive its
// events. The resolution states both only once they connected.
func declaredPeerRows(spec InstanceSpec, document ConnectionsDocument) []DeclaredConnection {
	rows := []DeclaredConnection{}
	for _, pair := range document.Pairs {
		if pair.Environment == spec.Environment && pair.Callee == spec.Application {
			rows = append(rows, DeclaredConnection{Role: RoleCalledBy, App: pair.Caller, Outcome: Connected})
		}
	}
	for _, subscription := range document.Subscriptions {
		if subscription.Environment == spec.Environment && subscription.Publisher == spec.Application {
			rows = append(rows, DeclaredConnection{Role: RoleSubscriber, App: subscription.Subscriber,
				Event: subscription.Event, Outcome: Connected})
		}
	}
	return rows
}

// callOutcome is what the resolution says about one declared call: connected, waiting for the other side, refused
// whatever it says, or nothing when the document does not mention it.
func callOutcome(document ConnectionsDocument, spec InstanceSpec, callee string) (outcome, reason string) {
	for _, pair := range document.Pairs {
		if namesInstance(spec, callee, pair.Caller, pair.Callee, pair.Environment) {
			return Connected, ""
		}
	}
	for _, wait := range document.Waiting {
		if namesInstance(spec, callee, wait.Caller, wait.Callee, wait.Environment) {
			return Waiting, wait.Reason
		}
	}
	for _, refusal := range document.Refused {
		if namesInstance(spec, callee, refusal.Application, refusal.Callee, refusal.Environment) {
			return Refused, refusal.Reason
		}
	}
	return "", ""
}

// names is whether a resolution row is this instance and callee in this instance's environment: the document states
// every instance of every environment, so the row's own environment is part of its identity.
func namesInstance(spec InstanceSpec, callee, caller, calleeInRow, environment string) bool {
	return environment == spec.Environment && caller == spec.Application && calleeInRow == callee
}

// subscriptionOutcome is what the resolution says about one declared subscription, by the same rule as a call.
func subscriptionOutcome(document ConnectionsDocument, spec InstanceSpec, publisher, event string) (outcome, reason string) {
	for _, subscription := range document.Subscriptions {
		if subscription.Environment == spec.Environment && sameSubscription(subscription, spec, publisher, event) {
			return Connected, ""
		}
	}
	for _, wait := range document.EventWaiting {
		if namesInstance(spec, publisher, wait.Caller, wait.Callee, wait.Environment) {
			return Waiting, wait.Reason
		}
	}
	return "", ""
}

// sameSubscription is whether a connected subscription is this instance receiving this event of this publisher.
func sameSubscription(row ConnectionsSubscription, spec InstanceSpec, publisher, event string) bool {
	return row.Subscriber == spec.Application && row.Publisher == publisher && row.Event == event
}

// splitSubscription is one subscription's `<publisher>.<event>`; a name the records refuse keeps its whole text as
// the event, so the row still names what was declared.
func splitSubscription(name string) (publisher, event string) {
	for index := 0; index < len(name); index++ {
		if name[index] == '.' {
			return name[:index], name[index+1:]
		}
	}
	return "", name
}

// clones is a copy that is never nil, so a document states an empty list rather than a missing one.
func clones(list []string) []string {
	if list == nil {
		return []string{}
	}
	return append([]string{}, list...)
}
