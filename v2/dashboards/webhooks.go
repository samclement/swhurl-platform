package dashboards

import (
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The two reserved webhooks: the platform's ntfy topic, and the notify service, which every app alert calls.
const (
	PlatformWebhook = "ntfy: platform"
	NotifyWebhook   = "swhurl notify"
)

// notifyAlertURL is where the notify service takes an alert.
const notifyAlertURL = "http://notify.notify.svc.cluster.local:8080/v1/alert"

// Webhook is a HyperDX webhook as its external API states it. ID is the server's; a request body leaves it out.
type Webhook struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name"`
	Service     string `json:"service"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Body        string `json:"body"`
}

// ErrPlatformAddress is a platform topic address that is not an HTTPS topic URL. It never repeats the address:
// the address is the credential.
var ErrPlatformAddress = errors.New("the platform ntfy address is not an HTTPS topic URL without a query or fragment")

// Destinations is the two reserved webhooks, platform first, from the platform topic's address and the secret the
// notify service asks every alert for (a HyperDX webhook cannot carry a header or a token that rotates). Their
// bodies are HyperDX templates: HyperDX JSON-escapes each variable before filling one in. Both bodies, and so both
// webhooks, hold a credential.
func Destinations(platformAddress string, alertSecret []byte) ([]Webhook, error) {
	server, topic, ok := topicOf(platformAddress)
	if !ok {
		return nil, ErrPlatformAddress
	}
	if len(alertSecret) == 0 || !utf8.Valid(alertSecret) {
		return nil, errors.New("notify/notify-alerts holds no secret")
	}
	// The condition sets high priority on firing and normal priority on recovery. It comes before another field:
	// "{{/if}}}" is parsed by Handlebars as a triple-brace close, even though the last "}" is JSON.
	platform := `{"priority":{{#if (eq state "ALERT")}}4{{else}}3{{/if}},"topic":` + quoted(topic) +
		`,"title":"ClickStack: {{title}}","message":"{{body}}","click":"{{link}}"}`
	notify := `{"secret":` + quoted(string(alertSecret)) +
		`,"title":"{{title}}","body":"{{body}}","link":"{{link}}","state":"{{state}}"}`
	return []Webhook{
		{Name: PlatformWebhook, Service: "generic", URL: server + "/",
			Description: "ClickStack alerts to the platform ntfy topic", Body: platform},
		{Name: NotifyWebhook, Service: "generic", URL: notifyAlertURL,
			Description: "Default app alerts, to the notify service and on to the app's team", Body: notify},
	}, nil
}

// topicOf is an ntfy address's server and topic: https://<host>/<topic>, with no user, query or fragment and one
// path segment (surrounding slashes aside).
func topicOf(address string) (string, string, bool) {
	rest, ok := strings.CutPrefix(address, "https://")
	if !ok || strings.ContainsAny(rest, "?#") {
		return "", "", false
	}
	host, path, _ := strings.Cut(rest, "/")
	topic := strings.Trim(path, "/")
	if host == "" || strings.Contains(host, "@") || topic == "" || strings.Contains(topic, "/") {
		return "", "", false
	}
	return "https://" + host, topic, true
}

// quoted is a JSON string as Python's json.dumps writes it (ASCII only), so a body the Python job wrote is the
// same bytes and is not rewritten.
func quoted(value string) string {
	var out strings.Builder
	out.WriteByte('"')
	for _, r := range value {
		out.WriteString(escaped(r))
	}
	out.WriteByte('"')
	return out.String()
}

// shortEscapes is the characters json.dumps writes as a two-character escape.
var shortEscapes = map[rune]string{'"': `\"`, '\\': `\\`, '\n': `\n`, '\r': `\r`, '\t': `\t`, '\b': `\b`, '\f': `\f`}

func escaped(r rune) string {
	if short, ok := shortEscapes[r]; ok {
		return short
	}
	if r >= 0x20 && r < 0x7f {
		return string(r)
	}
	if r > 0xffff {
		r -= 0x10000
		return unicodeEscape(0xd800+(r>>10)) + unicodeEscape(0xdc00+(r&0x3ff))
	}
	return unicodeEscape(r)
}

func unicodeEscape(r rune) string {
	hex := strconv.FormatInt(int64(r), 16)
	return `\u` + strings.Repeat("0", 4-len(hex)) + hex
}

// WebhookStep is one change to a reserved webhook, or none (Keep).
type WebhookStep struct {
	Action  Action
	ID      string  // the live webhook's, for Keep and Update
	Webhook Webhook // the definition
}

// PlanWebhooks is how to bring both reserved webhooks to their definitions, each repaired in place to keep its id.
// Both names are checked before either is planned: a name listed twice, a webhook of that name for another service,
// or one without an id stops the plan, so neither is written. Webhooks of other names are never considered.
func PlanWebhooks(live []Webhook, wanted []Webhook) ([]WebhookStep, error) {
	found := map[string]*Webhook{}
	for _, want := range wanted {
		matched, err := reserved(live, want)
		if err != nil {
			return nil, err
		}
		found[want.Name] = matched
	}
	steps := make([]WebhookStep, 0, len(wanted))
	for _, want := range wanted {
		current := found[want.Name]
		switch {
		case current == nil:
			steps = append(steps, WebhookStep{Action: Create, Webhook: want})
		case sameWebhook(*current, want):
			steps = append(steps, WebhookStep{Action: Keep, ID: current.ID, Webhook: want})
		default:
			steps = append(steps, WebhookStep{Action: Update, ID: current.ID, Webhook: want})
		}
	}
	return steps, nil
}

// reserved is the one live webhook of a reserved name, nil when there is none, or why it cannot be repaired.
func reserved(live []Webhook, want Webhook) (*Webhook, error) {
	var matched *Webhook
	for i := range live {
		if live[i].Name != want.Name {
			continue
		}
		if matched != nil || live[i].Service != want.Service {
			return nil, errors.New("ClickStack webhook " + want.Name + ": duplicate name or conflicting service; no change made")
		}
		matched = &live[i]
	}
	if matched != nil && matched.ID == "" {
		return nil, errors.New("ClickStack webhook " + want.Name + " has no id; no change made")
	}
	return matched, nil
}

func sameWebhook(live, want Webhook) bool {
	live.ID = ""
	return live == want
}
