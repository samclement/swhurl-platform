package notify

import (
	"encoding/json"
	"strings"
)

// Where the service finds its routes, its channels' addresses, the webhook's secret and its outbox: all in its own
// namespace.
const (
	Namespace      = "notify"
	RoutesName     = "notification-routes"
	RoutesKey      = "routes.json"
	AddressKey     = "address"
	TransportLabel = "platform.swhurl.com/notify-transport"
	AlertSecret    = "notify-alerts"
	AlertSecretKey = "secret"
	OutboxName     = "notify-outbox"
	OutboxKey      = "state.json"
	FieldManager   = "notify"
	// TeamLabel is the label on an app's namespace naming the team that owns it.
	TeamLabel = "platform.swhurl.com/team"
	// channelPrefix starts every channel's Secret's name: channel-<name>.
	channelPrefix = "channel-"
)

// Routes is the routes document: which channels each team's apps notify.
type Routes struct {
	Teams map[string][]string `json:"teams"`
}

// DecodeRoutes reads the routes document the routes ConfigMap holds.
func DecodeRoutes(raw string) (Routes, error) {
	var routes Routes
	err := json.Unmarshal([]byte(raw), &routes)
	return routes, err
}

// Channels are the channels of the team that owns an app's namespace: none for a namespace without a team, or a
// team the routes do not name.
func (r Routes) Channels(team string, hasTeam bool) []string {
	if !hasTeam {
		return []string{}
	}
	return append([]string{}, r.Teams[team]...)
}

// TeamOf is the team a namespace document's label names, and whether it names one.
func TeamOf(namespace []byte) (string, bool, error) {
	var doc struct {
		Metadata struct {
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(namespace, &doc); err != nil {
		return "", false, err
	}
	team, ok := doc.Metadata.Labels[TeamLabel]
	return team, ok, nil
}

// ChannelOf is the channel a Secret holds the address of, from its name channel-<name>; false for any other Secret.
func ChannelOf(secretName string) (string, bool) {
	channel, ok := strings.CutPrefix(secretName, channelPrefix)
	return channel, ok && channel != ""
}
