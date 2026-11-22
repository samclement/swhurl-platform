package notify

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

var (
	// alertName is how an app's alert is named, swhurl:<app>:<preset>: how the service knows which app it is about.
	alertName = regexp.MustCompile(`swhurl:([a-z][a-z0-9-]*):([a-z][a-z0-9-]*)`)
	// alertGroup is the namespace an alert fired for, from the "Group:" line HyperDX writes for a tile grouped by
	// namespace.
	alertGroup = regexp.MustCompile(`k8s\.namespace\.name'\):([a-z0-9-]+)`)
)

// presetLabel is how a notification names each default alert; any other preset keeps its own name.
func presetLabel(preset string) string {
	switch preset {
	case "error-logs":
		return "error logs"
	case "errors":
		return "failed requests or runs"
	}
	return preset
}

// stateWord is how a notification says whether its alert fires or has recovered.
func stateWord(firing bool) string {
	if firing {
		return "alert"
	}
	return "recovered"
}

// Alert is a ClickStack webhook's body, which proves itself with the shared secret it carries.
type Alert struct{ secret, title, body, link, state string }

// DecodeAlert reads a POST /v1/alert body: one JSON object whose secret, title, body, link and state, where given,
// are strings. Any other field is ignored.
func DecodeAlert(body []byte) (Alert, error) {
	doc, ok := object(body)
	var text map[string]string
	if ok {
		text, ok = texts(doc, "secret", "title", "body", "link", "state")
	}
	if !ok {
		return Alert{}, &Refusal{400, "the body must be one JSON object of strings"}
	}
	return Alert{text["secret"], text["title"], text["body"], text["link"], text["state"]}, nil
}

var secretRequired = &Refusal{401, "the alert webhook secret is required"}

// Signed refuses an alert that carries no secret, before the cluster's is read.
func (a Alert) Signed() error {
	if a.secret == "" {
		return secretRequired
	}
	return nil
}

// Request is the alert as a request for the outbox, once its secret matches expected (compared in constant time):
// the app from the alert's name and the environment from the namespace its group names, a title in the platform's
// words, and HyperDX's own detail without its "Group:" line.
func (a Alert) Request(expected string) (Request, error) {
	if a.secret == "" || subtle.ConstantTimeCompare([]byte(a.secret), []byte(expected)) != 1 {
		return Request{}, secretRequired
	}
	named, group := alertName.FindStringSubmatch(a.title), alertGroup.FindStringSubmatch(a.body)
	var instance Instance
	ok := named != nil && group != nil
	if ok {
		instance, ok = OfNamespace(group[1])
	}
	if !ok || instance.App != named[1] {
		return Request{}, &Refusal{400,
			"not an alert of an app: its name is swhurl:<app>:<preset> and its group the app's namespace"}
	}
	preset, firing := named[2], a.state == "ALERT"
	link := a.link
	if !strings.HasPrefix(link, "https://") || length(link) > maxLink {
		link = ""
	}
	digest := sha256.Sum256([]byte(a.body))
	key := fmt.Sprintf("alert:%s:%s:%s:%s:%s", instance.App, instance.Env, preset, a.state, hex.EncodeToString(digest[:])[:16])
	title := fmt.Sprintf("%s/%s %s: %s", instance.App, instance.Env, stateWord(firing), presetLabel(preset))
	return Request{instance, key, Notice{title, truncated(a.detail(), MaxMessage), firing, link}}, nil
}

// detail is the alert body's lines that say something, without the "Group:" line the app was read from.
func (a Alert) detail() string {
	var kept []string
	for _, line := range lines(a.body) {
		if strings.TrimFunc(line, isSpace) != "" && !strings.HasPrefix(line, "Group:") {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}
