package pages

import "github.com/samclement/swhurl-platform/v2/kernel"

// The repository's own layout, as a console change carries it.
const (
	// ConsoleRecordsRoot is the folder the platform's records live in.
	ConsoleRecordsRoot = kernel.RecordsRoot
	// ConsoleAppsRoot is the folder the compiler writes one instance's files into.
	ConsoleAppsRoot = "v2-apps/instances"
	// ConsoleUnitsRoot is the folder Flux applies an instance from.
	ConsoleUnitsRoot = "clusters/home"
	// ConsoleAuthFolder is the folder v2 renders the platform's signing keys into, one ConfigMap per namespace.
	ConsoleAuthFolder = "platform/auth"
	// ConsoleCallsFile is the resolution every instance's declarations are decided in.
	ConsoleCallsFile = ConsoleRecordsRoot + "/calls.json"
	// ConsoleEgressFile is the allocation the proxy's port and the instance's grants are decided in.
	ConsoleEgressFile = "platform/egress/allocation.json"
	// ConsoleTeamsFile is the team registry: one record for the whole platform.
	ConsoleTeamsFile = ConsoleRecordsRoot + "/" + kernel.TeamsRecordName
	// ConsoleRoutesFile is what v2 renders from the registry for the notification service.
	ConsoleRoutesFile = "platform/notify/routes.json"
	// ConsoleChannelsFolder is where v2 renders one encrypted Secret per team channel.
	ConsoleChannelsFolder = "platform/notify/channels"
)

// ConsoleSharedFiles are the platform files any change may carry beside the instance's own: what the render writes
// for the platform as a whole rather than for one instance. A render that writes a new one adds it here, and the
// check that a change carries only what its kind derives reads the same list; the render's own platform outputs are
// tested against it, so the two cannot drift apart.
var ConsoleSharedFiles = []string{
	ConsoleCallsFile,
	ConsoleEgressFile,
	"platform/egress/configmap.json",
	"platform/egress/networkpolicy.json",
	"platform/nats/users.json",
	"platform/reloader/values.json",
	ConsoleUnitsRoot + "/kustomization.yaml",
}
