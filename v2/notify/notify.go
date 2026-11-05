// Package notify is the notify service's decisions: who may send, what a message or a ClickStack alert becomes,
// how many an app may send, and the outbox those requests are queued in until each channel has its copy.
//
// It is the Python service's contract (docs/services.md#notify-service), kept exactly: the same input gets the same
// status and reason, in the same order of checks, and the outbox it reads and writes is the one that service wrote.
// Lengths are counted in characters, as Python counts them. Time and the random key are arguments; nothing here
// reads the cluster or sends anything.
package notify

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// The service's bounds and identities.
const (
	// Audience is the only audience a caller's token is accepted for.
	Audience = "notify"
	// Checker is the platform sender: the notification checker, which names the app a message is about.
	Checker                               = "system:serviceaccount:console:notification-check"
	MaxBody, MaxTitle, MaxMessage, MaxKey = 16_384, 250, 3_500, 300
	maxLink                               = 500
	RateCount, RateSeconds                = 30, 3600 // messages an app instance may send, per this many seconds
)

// The reasons a refusal of the service's own effects gives (status 503).
const (
	CannotReview  = "cannot check the token with Kubernetes"
	CannotRoute   = "cannot read the team routes"
	NoAlertSecret = "the alert webhook secret is not set up"
	CannotSave    = "cannot save to the outbox"
)

// Refusal is an answer other than 202: its HTTP status and the reason the body's "error" gives.
type Refusal struct {
	Status int
	Reason string
}

func (r *Refusal) Error() string { return r.Reason }

// Unavailable is the 503 for an effect that failed: the cluster could not be read or written.
func Unavailable(reason string) *Refusal { return &Refusal{503, reason} }

var (
	tokenRequired = &Refusal{401, "a service-account token for the audience " + Audience + " is required"}
	appName       = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$`)
	appSender     = regexp.MustCompile(`^system:serviceaccount:([a-z0-9-]+):([a-z0-9-]+)$`)
)

// CheckBody refuses a request body over MaxBody bytes, before anything else is asked of it.
func CheckBody(body []byte) error {
	if len(body) > MaxBody {
		return &Refusal{413, fmt.Sprintf("a request is at most %d bytes", MaxBody)}
	}
	return nil
}

// Bearer is the token an Authorization header presents: "Bearer <token>", the scheme in any case.
func Bearer(header string) (string, error) {
	scheme, token, _ := strings.Cut(header, " ")
	token = strings.TrimFunc(token, isSpace)
	if !strings.EqualFold(scheme, "bearer") || token == "" {
		return "", tokenRequired
	}
	return token, nil
}

// Identity is who Kubernetes says a token belongs to, from its answer to a review for Audience; a token it does not
// vouch for, or vouches for another audience only, is refused.
func Identity(authenticated bool, audiences []string, username string) (string, error) {
	if !authenticated || !slices.Contains(audiences, Audience) {
		return "", tokenRequired
	}
	return username, nil
}

// Instance is an app in one environment: the pair a message is about.
type Instance struct{ App, Env string }

// Named is the instance an app and environment name, if they name one.
func Named(app, env string) (Instance, bool) {
	if !appName.MatchString(app) || (env != "staging" && env != "prod") {
		return Instance{}, false
	}
	return Instance{app, env}, true
}

// OfNamespace is the instance a namespace <app>-<env> belongs to; the app's own name may contain hyphens.
func OfNamespace(namespace string) (Instance, bool) {
	cut := strings.LastIndex(namespace, "-")
	if cut < 0 {
		return Instance{}, false
	}
	return Named(namespace[:cut], namespace[cut+1:])
}

// Namespace is where the instance runs.
func (i Instance) Namespace() string { return i.App + "-" + i.Env }

// Sender is who a message is from: an app instance's own service account, or the zero Sender for the platform's.
type Sender struct{ Instance }

// IsApp is whether the sender is an app, which sends about itself only and is rate limited.
func (s Sender) IsApp() bool { return s.App != "" }

// SenderOf applies the sender rule: the checker is the platform sender, an app's own service account (<app> in
// <app>-<env>) sends as that instance, and anyone else is refused.
func SenderOf(who string) (Sender, error) {
	if who == Checker {
		return Sender{}, nil
	}
	if found := appSender.FindStringSubmatch(who); found != nil {
		if instance, ok := OfNamespace(found[1]); ok && instance.App == found[2] {
			return Sender{instance}, nil
		}
	}
	return Sender{}, &Refusal{403, "this caller may not send notifications"}
}

// Notice is one message for a person: what every channel's copy says.
type Notice struct {
	Title, Message string
	High           bool   // a failure or an alert; anything else is normal
	Link           string // opened from the notification
}

// Request is an accepted message or alert, before it has its channels: the instance it is about, the key that
// makes a retry harmless, and the notice.
type Request struct {
	Instance
	Key    string
	Notice Notice
}

var messageFields = []string{"app", "env", "title", "message", "priority", "click", "key"}

// DecodeMessage validates a POST /v1/notify body, in the Python service's order of checks. An app's message is
// about itself (sender, from its token): its title carries the instance and, without a key of its own, it is
// given randomKey.
func DecodeMessage(body []byte, sender Sender, randomKey string) (Request, error) {
	doc, ok := object(body)
	if !ok {
		return Request{}, &Refusal{400, "the body must be one JSON object"}
	}
	if unknown := unknownFields(doc); len(unknown) > 0 {
		return Request{}, &Refusal{400, "unknown field " + strings.Join(unknown, ", ")}
	}
	text, ok := texts(doc, "app", "env", "title", "message", "click", "key")
	if !ok {
		return Request{}, &Refusal{400, "app, env, title, message, click and key are strings"}
	}
	if sender.IsApp() {
		if err := asSender(text, sender, randomKey); err != nil {
			return Request{}, err
		}
	}
	return validMessage(doc, text)
}

// asSender makes an app's message its own: about the instance its token names, and keyed and titled as that one's.
func asSender(text map[string]string, sender Sender, randomKey string) error {
	if or(text["app"], sender.App) != sender.App || or(text["env"], sender.Env) != sender.Env {
		return &Refusal{403, "an app sends about itself only"}
	}
	text["app"], text["env"] = sender.App, sender.Env
	text["key"] = fmt.Sprintf("app:%s:%s:%s", sender.App, sender.Env, or(text["key"], randomKey))
	if title := strings.TrimFunc(text["title"], isSpace); title != "" {
		text["title"] = fmt.Sprintf("%s/%s: %s", sender.App, sender.Env, title)
	} else {
		text["title"] = ""
	}
	return nil
}

// validMessage checks the message's fields in order, and is the request it states.
func validMessage(doc map[string]json.RawMessage, text map[string]string) (Request, error) {
	instance, ok := Named(text["app"], text["env"])
	if !ok {
		return Request{}, &Refusal{400, "app must be an app name and env staging or prod"}
	}
	title := strings.TrimFunc(text["title"], isSpace)
	if title == "" || length(text["title"]) > MaxTitle || length(text["message"]) > MaxMessage {
		return Request{}, &Refusal{400, fmt.Sprintf("a title of at most %d characters is required; a message is at most %d",
			MaxTitle, MaxMessage)}
	}
	priority, ok := priorityOf(doc["priority"])
	if !ok {
		return Request{}, &Refusal{400, "priority is one of normal, high"}
	}
	if err := checkLinkAndKey(text["click"], text["key"]); err != nil {
		return Request{}, err
	}
	return Request{instance, text["key"], Notice{title, text["message"], priority == "high", text["click"]}}, nil
}

// checkLinkAndKey checks the last two fields: a link is https, and the key is given and bounded.
func checkLinkAndKey(click, key string) error {
	if click != "" && !strings.HasPrefix(click, "https://") || length(click) > maxLink {
		return &Refusal{400, fmt.Sprintf("click must be an https URL of at most %d characters", maxLink)}
	}
	if key == "" || length(key) > MaxKey {
		return &Refusal{400, fmt.Sprintf("key is required (at most %d characters): the same key is delivered once", MaxKey)}
	}
	return nil
}

// priorityOf is the priority a message names: normal when it names none, and only normal or high.
func priorityOf(raw json.RawMessage) (string, bool) {
	if raw == nil {
		return "normal", true
	}
	value, ok := text(raw)
	return value, ok && (value == "normal" || value == "high")
}

// unknownFields are the fields of a message the API does not have, sorted.
func unknownFields(doc map[string]json.RawMessage) []string {
	var unknown []string
	for field := range doc {
		if !slices.Contains(messageFields, field) {
			unknown = append(unknown, field)
		}
	}
	sort.Strings(unknown)
	return unknown
}

// object is a body that is one JSON object, as its fields.
func object(body []byte) (map[string]json.RawMessage, bool) {
	var doc map[string]json.RawMessage
	if !utf8.Valid(body) || json.Unmarshal(body, &doc) != nil || doc == nil {
		return nil, false
	}
	return doc, true
}

// texts are the named fields as strings, empty where absent; false when any is present and not a string.
func texts(doc map[string]json.RawMessage, fields ...string) (map[string]string, bool) {
	found := map[string]string{}
	for _, field := range fields {
		raw, present := doc[field]
		if !present {
			found[field] = ""
			continue
		}
		value, ok := text(raw)
		if !ok {
			return nil, false
		}
		found[field] = value
	}
	return found, true
}

// text is a JSON string's value; null, a number or anything else is not one.
func text(raw json.RawMessage) (string, bool) {
	var value string
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func or(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// RateLimit is at most RateCount accepted messages per app instance in any RateSeconds, over times the caller
// supplies. It keeps no lock: its caller holds one. Its zero value is ready, and it forgets on restart, which only
// ever allows more.
type RateLimit struct{ sent map[Instance][]float64 }

// Take counts one message from the instance at now, or refuses it when the instance has sent its share.
func (l *RateLimit) Take(instance Instance, now float64) error {
	if l.sent == nil {
		l.sent = map[Instance][]float64{}
	}
	times := l.sent[instance]
	for len(times) > 0 && times[0] <= now-RateSeconds {
		times = times[1:]
	}
	if len(times) >= RateCount {
		l.sent[instance] = times
		return &Refusal{429, fmt.Sprintf("%s/%s has sent %d messages in the last hour; try again later",
			instance.App, instance.Env, RateCount)}
	}
	l.sent[instance] = append(times, now)
	return nil
}
