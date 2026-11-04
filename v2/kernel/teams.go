package kernel

import (
	"bytes"
	"encoding/json"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// The team registry is one record for the whole platform: the teams an app record may name and an `allows` grant
// may admit, each with a description and at most one notification channel. A channel is a name and a transport;
// its address is a credential that lives only in its encrypted Secret, so the registry and every file rendered from
// it carry names and never an address. A channel belongs to one team: a team that wants another team's messages
// shares nothing, it gets its own channel.
const (
	// TeamsRecordName is the registry's file in the records tree, beside the resolution.
	TeamsRecordName = "teams.json"
	// NotifyFolder is where, under the platform folder, the notification service's documents live.
	NotifyFolder = "notify"
	// ChannelsFolder is the folder, under NotifyFolder, that holds one encrypted Secret per channel and the
	// kustomization that lists them.
	ChannelsFolder = NotifyFolder + "/channels"

	maxDescription    = 200
	notifyNamespace   = "notify"
	routesObjectName  = "notification-routes"
	routesKey         = "routes.json"
	channelPrefix     = "channel-"
	channelAddressKey = "address"
	transportLabel    = "platform.swhurl.com/notify-transport"
	ntfyTransport     = "ntfy"
	// teamsOwner is the platform: the routes name no instance.
	teamsOwner = "platform"
)

// Refusals a team change can meet, beside the record codes.
const (
	TeamExists   Code = "team_exists"
	NoTeam       Code = "no_team"
	TeamInUse    Code = "team_in_use"
	ChannelTaken Code = "channel_taken"
)

// ntfyAddress is an ntfy topic URL: HTTPS, a host, and one topic segment of the characters ntfy allows, with no
// credentials, query or fragment.
var ntfyAddress = regexp.MustCompile(`^https://[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?/[A-Za-z0-9_-]{1,64}$`)

// TeamRegistry is the registry record.
type TeamRegistry struct {
	SchemaVersion int32           `json:"schemaVersion"`
	Teams         map[string]Team `json:"teams"`
}

// Team is one registered team: what it owns, in one line, and the channel its apps' lifecycle messages also go to.
type Team struct {
	Description string   `json:"description,omitempty"`
	Channel     *Channel `json:"channel,omitempty"`
}

// Channel is a notification destination's name and transport; the name is its Secret's, channel-<name>.
type Channel struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
}

// TeamAction is one change the team command makes.
type TeamAction string

const (
	AddTeam      TeamAction = "add"
	DescribeTeam TeamAction = "describe"
	RenameTeam   TeamAction = "rename"
	RemoveTeam   TeamAction = "remove"
	// GiveChannel gives a team its channel, named after the team, or keeps the one it has for a new address.
	GiveChannel TeamAction = "channel"
	// DropChannel removes a team's channel, and with it the channel's Secret.
	DropChannel TeamAction = "drop-channel"
)

// TeamChange is one change: the team it is about and what the action takes — a description for add and describe,
// the new name for rename, a transport for channel.
type TeamChange struct {
	Action                               TeamAction
	Team, Description, Rename, Transport string
}

// DecodeTeams reads the registry record and validates it whole.
func DecodeTeams(data []byte) (TeamRegistry, error) {
	if err := document(data); err != nil {
		return TeamRegistry{}, err
	}
	root, err := fields(data, "teams", []string{"schemaVersion", "teams"})
	if err != nil {
		return TeamRegistry{}, err
	}
	var teams map[string]json.RawMessage
	if json.Unmarshal(root["teams"], &teams) != nil || teams == nil {
		return TeamRegistry{}, refuse(Invalid, "teams.teams", "expected an object")
	}
	for _, name := range slices.Sorted(maps.Keys(teams)) {
		entry, err := fields(teams[name], "teams.team", nil, "description", "channel")
		if err != nil {
			return TeamRegistry{}, err
		}
		if channel, present := entry["channel"]; present {
			if _, err := fields(channel, "teams.team.channel", []string{"name", "transport"}); err != nil {
				return TeamRegistry{}, err
			}
		}
	}
	var registry TeamRegistry
	if err := json.Unmarshal(data, &registry); err != nil {
		return TeamRegistry{}, refuse(Invalid, "teams", "incorrect field type or integer range")
	}
	return registry, registry.validate()
}

// EncodeTeams is the record's bytes: indented, teams in name order.
func EncodeTeams(registry TeamRegistry) ([]byte, error) {
	if err := registry.validate(); err != nil {
		return nil, err
	}
	return json.MarshalIndent(registry, "", "  ")
}

func (r TeamRegistry) validate() error {
	if r.SchemaVersion != 1 {
		return refuse(Unsupported, "schemaVersion", "only version 1 is supported")
	}
	owner := map[string]bool{}
	for _, name := range slices.Sorted(maps.Keys(r.Teams)) {
		team := r.Teams[name]
		if !namePattern.MatchString(name) {
			return refuse(Invalid, "teams", "expected a team name")
		}
		if err := validateDescription(team.Description); err != nil {
			return err
		}
		if team.Channel == nil {
			continue
		}
		if !namePattern.MatchString(team.Channel.Name) {
			return refuse(Invalid, "teams.team.channel.name", "expected a channel name")
		}
		if team.Channel.Transport != ntfyTransport {
			return refuse(Unsupported, "teams.team.channel.transport", "only ntfy is supported")
		}
		if owner[team.Channel.Name] {
			return refuse(ChannelTaken, "teams.team.channel.name", "a channel belongs to one team")
		}
		owner[team.Channel.Name] = true
	}
	return nil
}

// validateDescription accepts one line of at most maxDescription characters, with single spaces between words.
func validateDescription(text string) error {
	if strings.Join(strings.Fields(text), " ") != text || utf8.RuneCountInString(text) > maxDescription {
		return refuse(Invalid, "teams.team.description", "expected one line of at most 200 characters")
	}
	return nil
}

// CheckTeams refuses an app record that names a team the registry does not hold, and an instance whose `allows`
// grants one: v2 labels namespaces with the team and resolves grants against it, so a team nobody registered is a
// label with no description and no channel.
func CheckTeams(registry TeamRegistry, apps []AppSpec, instances []InstanceSpec) error {
	for _, app := range apps {
		if _, registered := registry.Teams[app.Team]; !registered {
			return refuse(NoTeam, "app.team", "the app names a team that is not registered")
		}
	}
	for _, spec := range instances {
		for _, name := range spec.Allows {
			team, granted := strings.CutPrefix(name, "team:")
			if _, registered := registry.Teams[team]; granted && !registered {
				return refuse(NoTeam, "allows", "the instance allows a team that is not registered")
			}
		}
	}
	return nil
}

// ChangeTeams applies one change to the registry, against every app and instance record: a team that a record
// names or grants is neither renamed nor removed, because neither moves the records — `edit` does. A change that
// leaves the registry as it was is not refused: it writes nothing new.
func ChangeTeams(registry TeamRegistry, change TeamChange, apps []AppSpec, instances []InstanceSpec) (TeamRegistry, error) {
	if err := registry.validate(); err != nil {
		return TeamRegistry{}, err
	}
	if !namePattern.MatchString(change.Team) {
		return TeamRegistry{}, refuse(Invalid, "team", "expected a team name")
	}
	teams := maps.Clone(registry.Teams)
	if teams == nil {
		teams = map[string]Team{}
	}
	_, registered := teams[change.Team]
	switch {
	case change.Action == AddTeam && registered:
		return TeamRegistry{}, refuse(TeamExists, "team", "the team is already registered")
	case change.Action == AddTeam:
		teams[change.Team] = Team{Description: change.Description}
	case !registered:
		return TeamRegistry{}, refuse(NoTeam, "team", "the team is not registered")
	default:
		if err := changeTeam(teams, change, apps, instances); err != nil {
			return TeamRegistry{}, err
		}
	}
	changed := TeamRegistry{SchemaVersion: registry.SchemaVersion, Teams: teams}
	return changed, changed.validate()
}

// changeTeam applies a change to a registered team in place.
func changeTeam(teams map[string]Team, change TeamChange, apps []AppSpec, instances []InstanceSpec) error {
	team := teams[change.Team]
	switch change.Action {
	case DescribeTeam:
		team.Description = change.Description
	case RenameTeam:
		return renamed(teams, change, apps, instances)
	case RemoveTeam:
		if err := removable(team, change.Team, apps, instances); err != nil {
			return err
		}
		delete(teams, change.Team)
		return nil
	case GiveChannel:
		name := change.Team
		if team.Channel != nil {
			name = team.Channel.Name
		}
		team.Channel = &Channel{Name: name, Transport: change.Transport}
	case DropChannel:
		if team.Channel == nil {
			return refuse(Invalid, "channel", "the team has no channel")
		}
		team.Channel = nil
	default:
		return refuse(Unsupported, "action", "unknown team change")
	}
	teams[change.Team] = team
	return nil
}

// renamed moves a team that nothing names to its new name, with its description and channel.
func renamed(teams map[string]Team, change TeamChange, apps []AppSpec, instances []InstanceSpec) error {
	if !namePattern.MatchString(change.Rename) {
		return refuse(Invalid, "rename", "expected a team name")
	}
	if change.Rename == change.Team {
		return nil
	}
	if _, taken := teams[change.Rename]; taken {
		return refuse(TeamExists, "rename", "a rename cannot merge two teams: move the apps with edit --set team=")
	}
	if err := unused(change.Team, apps, instances); err != nil {
		return err
	}
	teams[change.Rename] = teams[change.Team]
	delete(teams, change.Team)
	return nil
}

// removable is whether a team may go: nothing names it, and its channel went first, so removing a team never
// deletes a Secret as a side effect.
func removable(team Team, name string, apps []AppSpec, instances []InstanceSpec) error {
	if err := unused(name, apps, instances); err != nil {
		return err
	}
	if team.Channel != nil {
		return refuse(TeamInUse, "channel", "the team still has a channel: remove it first")
	}
	return nil
}

// unused refuses a team that an app record names or an instance's `allows` grants.
func unused(name string, apps []AppSpec, instances []InstanceSpec) error {
	for _, app := range apps {
		if app.Team == name {
			return refuse(TeamInUse, "team", "an app record names the team: move it first with edit --set team=")
		}
	}
	for _, spec := range instances {
		if slices.Contains(spec.Allows, "team:"+name) {
			return refuse(TeamInUse, "team", "an instance allows the team: remove the grant first with edit --set allows=")
		}
	}
	return nil
}

// Channels is every registered channel, in channel-name order.
func (r TeamRegistry) Channels() []Channel {
	found := []Channel{}
	for _, team := range r.Teams {
		if team.Channel != nil {
			found = append(found, *team.Channel)
		}
	}
	slices.SortFunc(found, func(a, b Channel) int { return strings.Compare(a.Name, b.Name) })
	return found
}

// ChannelSecretPath is where, under the platform folder, a channel's encrypted Secret lives.
func ChannelSecretPath(name string) string {
	return ChannelsFolder + "/" + channelPrefix + name + ".sops.yaml"
}

// RenderNotify renders what the notification service is told from the registry: the routes — each channel with its
// transport and each team's channel, names only — and the kustomization that lists every channel's Secret.
func RenderNotify(registry TeamRegistry) (ResourceSet, error) {
	if err := registry.validate(); err != nil {
		return ResourceSet{}, err
	}
	channels, routes, listed := manifest{}, manifest{}, []any{}
	for _, name := range slices.Sorted(maps.Keys(registry.Teams)) {
		if channel := registry.Teams[name].Channel; channel != nil {
			channels[channel.Name] = channel.Transport
			routes[name] = []any{channel.Name}
		}
	}
	for _, channel := range registry.Channels() {
		listed = append(listed, channelPrefix+channel.Name+".sops.yaml")
	}
	// The service reads the key as JSON; the shape is the one it has always read.
	document := manifest{"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": manifest{"name": routesObjectName, "namespace": notifyNamespace},
		"data":     manifest{routesKey: string(encodeManifest(manifest{"channels": channels, "teams": routes}))}}
	kustomization := manifest{"apiVersion": "kustomize.config.k8s.io/v1beta1", "kind": "Kustomization",
		"resources": listed}
	return ResourceSet{
		Files: []File{{NotifyFolder + "/routes.json", encodeManifest(document)},
			{ChannelsFolder + "/kustomization.yaml", encodeManifest(kustomization)}},
		Inventory: []Claim{{Object{"", "ConfigMap", notifyNamespace, routesObjectName}, teamsOwner}},
	}, nil
}

// ChannelSecret is one channel's Secret before encryption: its name and transport in plain metadata, so a reader
// can tell which channel a file is without decrypting it, and the address as its one value. The address is checked
// here and never appears in a refusal.
func ChannelSecret(channel Channel, address string) ([]byte, error) {
	if !namePattern.MatchString(channel.Name) || channel.Transport != ntfyTransport {
		return nil, refuse(Invalid, "channel", "expected a channel name and the ntfy transport")
	}
	if !ntfyAddress.MatchString(address) {
		return nil, refuse(Invalid, "address",
			"an ntfy address is an HTTPS topic URL without a query or fragment, such as https://ntfy.sh/<topic>")
	}
	document := manifest{"apiVersion": "v1", "kind": "Secret",
		"metadata": manifest{"name": channelPrefix + channel.Name, "namespace": notifyNamespace,
			"labels": manifest{transportLabel: channel.Transport}},
		"type": "Opaque", "stringData": manifest{channelAddressKey: address}}
	return encodeManifest(document), nil
}

// CheckEncryptedAddress is what v2 can say about a channel's ciphertext: it holds an encrypted value and not the
// address.
func CheckEncryptedAddress(document []byte, address string) error {
	if address == "" || bytes.Contains(document, []byte(address)) {
		return refuse(Invalid, "channel.secret", "the ciphertext holds the plaintext address")
	}
	if !bytes.Contains(document, []byte("ENC[")) {
		return refuse(Invalid, "channel.secret", "the ciphertext holds no encrypted value")
	}
	return nil
}
