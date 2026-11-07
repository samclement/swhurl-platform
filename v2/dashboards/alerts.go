package dashboards

import (
	"errors"
	"sort"
	"strconv"
	"strings"
)

// AlertPrefix starts the name of every alert the platform keeps, swhurl:<app>:<preset>: the notify service reads the
// app from it, and an alert without it is never changed.
const AlertPrefix = "swhurl:"

// alertsOff in an app's settings switches every alert, or one preset, off.
const alertsOff = "none"

const maxThreshold = 1_000_000

// preset is one default alert: the dashboard tile it watches, the threshold it fires at in one interval, and the
// interval.
type preset struct {
	key, tile string
	threshold int
	interval  string
}

var presets = []preset{
	{"error-logs", "Error logs", 5, "5m"},
	{"errors", "Errors", 5, "5m"},
}

// Alert is a HyperDX tile alert as its external API states it. ID is the server's; a request body leaves it out.
type Alert struct {
	ID            string   `json:"id,omitempty"`
	Name          string   `json:"name"`
	Tags          []string `json:"tags"`
	Source        string   `json:"source"`
	DashboardID   string   `json:"dashboardId"`
	TileID        string   `json:"tileId"`
	Threshold     int      `json:"threshold"`
	ThresholdType string   `json:"thresholdType"`
	Interval      string   `json:"interval"`
	Channel       Channel  `json:"channel"`
}

// Channel is where an alert goes: a webhook, by id.
type Channel struct {
	Type      string `json:"type"`
	WebhookID string `json:"webhookId"`
}

// Thresholds is each preset's threshold under an app's settings, 0 for one switched off. "" is the defaults and
// "none" switches every alert off; otherwise "<preset>=<1..1000000|none>", comma separated. An error names what is
// wrong.
func Thresholds(spec string) (map[string]int, error) {
	found := map[string]int{}
	for _, p := range presets {
		found[p.key] = p.threshold
	}
	spec = strings.TrimSpace(spec)
	if spec == alertsOff {
		for key := range found {
			found[key] = 0
		}
		return found, nil
	}
	for _, part := range strings.Split(spec, ",") {
		if part = strings.TrimSpace(part); part == "" {
			continue
		}
		key, value, _ := strings.Cut(part, "=")
		threshold, err := thresholdOf(key, value, found)
		if err != nil {
			return nil, err
		}
		found[key] = threshold
	}
	return found, nil
}

func thresholdOf(key, value string, known map[string]int) (int, error) {
	if _, ok := known[key]; !ok {
		return 0, errors.New(strconv.Quote(key) + " is not an alert: choose from error-logs, errors")
	}
	if value == alertsOff {
		return 0, nil
	}
	number, err := strconv.Atoi(value)
	if err != nil || strings.TrimLeft(value, "0123456789") != "" || number < 1 || number > maxThreshold {
		return 0, errors.New(key + "=" + strconv.Quote(value) + ": give a number from 1 to 1000000, or none")
	}
	return number, nil
}

// lenient is an app's thresholds, or the defaults when its settings do not parse: a bad value on the cluster never
// leaves an app without alerts.
func lenient(spec string) map[string]int {
	found, err := Thresholds(spec)
	if err != nil {
		found, _ = Thresholds("")
	}
	return found
}

// AlertName is the name of an app's alert for one preset.
func AlertName(app, presetKey string) string { return AlertPrefix + app + ":" + presetKey }

// wantedAlerts is the alerts an app should have on its live dashboard, whose tiles carry the server's ids: one per
// preset the app has not switched off whose tile is there.
func wantedAlerts(app App, live Dashboard, webhook string) []Alert {
	tiles := map[string]string{}
	for _, tile := range live.Tiles {
		tiles[tile.Name] = tile.ID
	}
	thresholds := lenient(app.Alerts)
	wanted := []Alert{}
	for _, p := range presets {
		if tiles[p.tile] == "" || thresholds[p.key] == 0 {
			continue
		}
		wanted = append(wanted, Alert{Name: AlertName(app.Name, p.key), Tags: []string{Tag, app.Name}, Source: "tile",
			DashboardID: live.ID, TileID: tiles[p.tile], Threshold: thresholds[p.key], ThresholdType: "above",
			Interval: p.interval, Channel: Channel{Type: "webhook", WebhookID: webhook}})
	}
	return wanted
}

// sameAlert is whether a live alert already states everything the wanted one does.
func sameAlert(live, wanted Alert) bool {
	return live.Name == wanted.Name && strings.Join(live.Tags, "\x00") == strings.Join(wanted.Tags, "\x00") &&
		live.Source == wanted.Source && live.DashboardID == wanted.DashboardID && live.TileID == wanted.TileID &&
		live.Threshold == wanted.Threshold && live.ThresholdType == wanted.ThresholdType &&
		live.Interval == wanted.Interval && live.Channel == wanted.Channel
}

// AlertStep is one change to the platform's alerts, or none (Keep).
type AlertStep struct {
	Action Action
	ID     string // the live alert's, for Update and Delete
	Alert  Alert  // the wanted alert, for Create, Update and Keep; the live one for Delete
}

// PlanAlerts is how to bring the platform's alerts to the default ones of each app whose dashboard is listed:
// create the missing ones, update in place those that differ (a rebuilt dashboard has new tile ids; an app changed
// its thresholds) and, with prune, delete the platform's other alerts (an app gone, or one that switched an alert
// off). Two live alerts of one name, or one without an id, stop the plan: deleting either could remove the one a
// person relies on. So does an app's dashboard listed without tiles, which would otherwise delete its alerts.
func PlanAlerts(apps []App, live []Dashboard, existing []Alert, webhook string, prune bool) ([]AlertStep, error) {
	ours, err := ourAlerts(existing)
	if err != nil {
		return nil, err
	}
	boards := map[string]Dashboard{}
	for _, board := range live {
		boards[board.Name] = board
	}
	steps := []AlertStep{}
	for _, app := range apps {
		board, ok := boards[app.DashboardName()]
		if !ok {
			continue
		}
		if len(board.Tiles) == 0 { // listed in a shape that did not decode: unknown, not an app without tiles
			return nil, errors.New("ClickStack lists dashboard " + board.Name + " without tiles; no change made")
		}
		for _, want := range wantedAlerts(app, board, webhook) {
			steps = append(steps, alertStep(want, ours))
			delete(ours, want.Name)
		}
	}
	return append(steps, staleAlerts(ours, prune)...), nil
}

func ourAlerts(existing []Alert) (map[string]Alert, error) {
	ours := map[string]Alert{}
	for _, alert := range existing {
		if !strings.HasPrefix(alert.Name, AlertPrefix) {
			continue
		}
		if _, twice := ours[alert.Name]; twice || alert.ID == "" {
			return nil, errors.New("ClickStack lists alert " + alert.Name + " twice or without an id; no change made")
		}
		ours[alert.Name] = alert
	}
	return ours, nil
}

func alertStep(want Alert, ours map[string]Alert) AlertStep {
	found, ok := ours[want.Name]
	switch {
	case !ok:
		return AlertStep{Action: Create, Alert: want}
	case sameAlert(found, want):
		return AlertStep{Action: Keep, ID: found.ID, Alert: want}
	}
	return AlertStep{Action: Update, ID: found.ID, Alert: want}
}

func staleAlerts(ours map[string]Alert, prune bool) []AlertStep {
	if !prune {
		return nil
	}
	steps := []AlertStep{}
	for _, alert := range ours {
		steps = append(steps, AlertStep{Action: Delete, ID: alert.ID, Alert: alert})
	}
	sort.Slice(steps, func(i, j int) bool { return steps[i].Alert.Name < steps[j].Alert.Name })
	return steps
}
