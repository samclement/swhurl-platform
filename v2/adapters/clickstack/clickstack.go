// Package clickstack reaches HyperDX's external API (/api/v2) and reads the admin account's access key from its
// MongoDB. Both are reached by running a program inside ClickStack's own containers (a mongosh script, a Node
// fetch), because neither service is exposed outside the cluster; this adapter writes those programs and reads
// their answers, and a Runner supplied by the caller runs them. Credentials travel in the program only: never in
// an argument, an error or a log.
package clickstack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/samclement/swhurl-platform/v2/dashboards"
)

// Runner runs one program in a ClickStack container and returns its standard output. A transport failure or a
// nonzero exit is an error, which must not carry the program or its output: its text becomes the reason of the
// adapter's error, so a failed exec can be diagnosed.
type Runner func(ctx context.Context, program []byte) ([]byte, error)

// Opener authenticates as the admin account: it reads the account's access key from MongoDB, once.
type Opener interface {
	Open(ctx context.Context, uri, email string) (API, error)
}

// API is the external API as the platform uses it: listings are complete or an error, a write is accepted
// (2xx) or an error, and every request carries the access key Open read.
type API interface {
	Sources(ctx context.Context) ([]dashboards.Source, error)
	Webhooks(ctx context.Context) ([]dashboards.Webhook, error)
	CreateWebhook(ctx context.Context, webhook dashboards.Webhook) (string, error)
	UpdateWebhook(ctx context.Context, id string, webhook dashboards.Webhook) error
	Dashboards(ctx context.Context) ([]dashboards.Dashboard, error)
	CreateDashboard(ctx context.Context, dashboard dashboards.Dashboard) (string, error)
	UpdateDashboard(ctx context.Context, id string, dashboard dashboards.Dashboard) error
	DeleteDashboard(ctx context.Context, id string) error
	Alerts(ctx context.Context) ([]dashboards.Alert, error)
	CreateAlert(ctx context.Context, alert dashboards.Alert) error
	UpdateAlert(ctx context.Context, id string, alert dashboards.Alert) error
	DeleteAlert(ctx context.Context, id string) error
}

// ErrNoAccessKey is an admin account with no access key: it has never signed in, or bootstrap has not run.
var ErrNoAccessKey = errors.New("the ClickStack admin account has no access key; sign in once, or run make clickstack-bootstrap")

// Error is a request that failed: the operation (method and path, which hold no credential) and a fixed reason,
// such as "HTTP 503". It never carries an answer's body.
type Error struct{ Operation, Reason string }

func (e *Error) Error() string { return "clickstack: " + e.Operation + ": " + e.Reason }

// marker starts the one output line a program answers on; tools print warnings around it.
const marker = "RESULT "

const apiBase = "http://127.0.0.1:8000/api/v2"

// adminKey prints the admin account's access key, or "" when it has none.
const adminKey = `const u = db.users.findOne({email: EMAIL}, {accessKey: 1}); print("RESULT " + JSON.stringify({key: (u && u.accessKey) || ""}));`

var _ Opener = opener{}

type opener struct{ mongo, app Runner }

// New is the adapter: mongo runs a mongosh script beside ClickStack's MongoDB, app a Node module in its app.
func New(mongo, app Runner) Opener { return opener{mongo: mongo, app: app} }

// Open reads the admin's access key with one MongoDB query that reads, and writes nothing.
func (o opener) Open(ctx context.Context, uri, email string) (API, error) {
	if uri == "" || email == "" {
		return nil, &Error{"open", "credentials_required"}
	}
	program := "db = connect(" + literal(uri) + ");\nconst EMAIL = " + literal(email) + "; " + adminKey + "\n"
	out, err := o.mongo(ctx, []byte(program))
	if err != nil {
		return nil, &Error{"admin key", "exec failed: " + err.Error()}
	}
	var answer struct {
		Key *string `json:"key"`
	}
	if !result(out, &answer) || answer.Key == nil {
		return nil, &Error{"admin key", "malformed"}
	}
	if *answer.Key == "" {
		return nil, ErrNoAccessKey
	}
	return &client{app: o.app, key: *answer.Key}, nil
}

// literal is a value as a JavaScript string literal (JSON's escapes, which JavaScript reads the same).
func literal(value string) string {
	encoded, _ := json.Marshal(value) // a string always encodes
	return string(encoded)
}

// result decodes the JSON after the marker on the program's one result line.
func result(out []byte, into any) bool {
	for _, line := range bytes.Split(out, []byte("\n")) {
		if rest, ok := bytes.CutPrefix(line, []byte(marker)); ok {
			return json.Unmarshal(rest, into) == nil
		}
	}
	return false
}

type client struct {
	app Runner
	key string
}

// fetchRequest is the second argument of fetch: the key in a header, the body as JSON text.
type fetchRequest struct {
	Method   string            `json:"method"`
	Headers  map[string]string `json:"headers"`
	Redirect string            `json:"redirect"`
	Body     *string           `json:"body,omitempty"`
}

// call makes one request and returns the answer's body; any answer outside 2xx is an error naming its status.
func (c *client) call(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	operation := method + " " + path
	request := fetchRequest{Method: method, Redirect: "manual",
		Headers: map[string]string{"content-type": "application/json", "authorization": "Bearer " + c.key}}
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, &Error{operation, "unencodable"}
		}
		text := string(encoded)
		request.Body = &text
	}
	options, _ := json.Marshal(request) // strings and a map of strings always encode
	program := "const r = await fetch(" + literal(apiBase+path) + ", " + string(options) + ");\n" +
		"let body = null; try { body = await r.json(); } catch {}\n" +
		`console.log("RESULT " + JSON.stringify({status: r.status, body}));` + "\n"
	out, err := c.app(ctx, []byte(program))
	if err != nil {
		return nil, &Error{operation, "exec failed: " + err.Error()}
	}
	var answer struct {
		Status int             `json:"status"`
		Body   json.RawMessage `json:"body"`
	}
	if !result(out, &answer) {
		return nil, &Error{operation, "malformed"}
	}
	if answer.Status < 200 || answer.Status > 299 {
		return nil, &Error{operation, "HTTP " + strconv.Itoa(answer.Status)}
	}
	return answer.Body, nil
}

// list is a listing's items: {"data": [...]} with, when the server states one, meta.total. A listing that states
// more items than it holds is truncated, and is an error rather than a partial answer.
func (c *client) list(ctx context.Context, path string) ([]json.RawMessage, error) {
	body, err := c.call(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	var listing struct {
		Data *[]json.RawMessage `json:"data"`
		Meta struct {
			Total *int `json:"total"`
		} `json:"meta"`
	}
	if json.Unmarshal(body, &listing) != nil || listing.Data == nil {
		return nil, &Error{"GET " + path, "malformed"}
	}
	if listing.Meta.Total != nil && *listing.Meta.Total > len(*listing.Data) {
		return nil, &Error{"GET " + path, "truncated"}
	}
	return *listing.Data, nil
}

// fields is a listed item as its fields; an item that is not an object makes the listing malformed.
func fields(path string, raw json.RawMessage) (map[string]json.RawMessage, error) {
	var found map[string]json.RawMessage
	if json.Unmarshal(raw, &found) != nil || found == nil {
		return nil, &Error{"GET " + path, "malformed"}
	}
	return found, nil
}

// text is a field's string value, or "" when it is absent or not a string.
func text(found map[string]json.RawMessage, key string) string {
	var value string
	if json.Unmarshal(found[key], &value) != nil {
		return ""
	}
	return value
}

func (c *client) Sources(ctx context.Context) ([]dashboards.Source, error) {
	items, err := c.list(ctx, "/sources")
	if err != nil {
		return nil, err
	}
	sources := make([]dashboards.Source, 0, len(items))
	for _, item := range items {
		found, err := fields("/sources", item)
		if err != nil {
			return nil, err
		}
		from, _ := fields("/sources", found["from"])
		sources = append(sources, dashboards.Source{ID: text(found, "id"), Kind: text(found, "kind"), Table: text(from, "tableName")})
	}
	return sources, nil
}

func (c *client) Webhooks(ctx context.Context) ([]dashboards.Webhook, error) {
	items, err := c.list(ctx, "/webhooks")
	if err != nil {
		return nil, err
	}
	webhooks := make([]dashboards.Webhook, 0, len(items))
	for _, item := range items {
		found, err := fields("/webhooks", item)
		if err != nil {
			return nil, err
		}
		webhooks = append(webhooks, dashboards.Webhook{ID: text(found, "id"), Name: text(found, "name"),
			Service: text(found, "service"), URL: text(found, "url"), Description: text(found, "description"),
			Body: text(found, "body")})
	}
	return webhooks, nil
}

// created is the id in a create's answer, {"data": {"id": ...}}, or "" when it states none.
func created(body json.RawMessage) string {
	var answer struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if json.Unmarshal(body, &answer) != nil {
		return ""
	}
	return text(answer.Data, "id")
}

// CreateWebhook requires the id in the answer: the alerts that follow name it.
func (c *client) CreateWebhook(ctx context.Context, webhook dashboards.Webhook) (string, error) {
	webhook.ID = ""
	body, err := c.call(ctx, "POST", "/webhooks", webhook)
	if err != nil {
		return "", err
	}
	if id := created(body); id != "" {
		return id, nil
	}
	return "", &Error{"POST /webhooks", "created_without_id"}
}

func (c *client) UpdateWebhook(ctx context.Context, id string, webhook dashboards.Webhook) error {
	webhook.ID = ""
	_, err := c.call(ctx, "PUT", "/webhooks/"+pathID(id), webhook)
	return err
}

// Dashboards lists every dashboard. One whose fields do not decode as the platform writes them keeps its id, name
// and tags, so the platform's own is rewritten and another's is left alone.
func (c *client) Dashboards(ctx context.Context) ([]dashboards.Dashboard, error) {
	items, err := c.list(ctx, "/dashboards")
	if err != nil {
		return nil, err
	}
	boards := make([]dashboards.Dashboard, 0, len(items))
	for _, item := range items {
		found, err := fields("/dashboards", item)
		if err != nil {
			return nil, err
		}
		var board dashboards.Dashboard
		if json.Unmarshal(item, &board) != nil {
			board = dashboards.Dashboard{Tags: tags(found)}
		}
		board.ID, board.Name = text(found, "id"), text(found, "name")
		boards = append(boards, board)
	}
	return boards, nil
}

func tags(found map[string]json.RawMessage) []string {
	var listed []string
	if json.Unmarshal(found["tags"], &listed) != nil {
		return nil
	}
	return listed
}

// CreateDashboard returns the new dashboard's id, or "" when the answer states none.
func (c *client) CreateDashboard(ctx context.Context, dashboard dashboards.Dashboard) (string, error) {
	dashboard.ID = ""
	body, err := c.call(ctx, "POST", "/dashboards", dashboard)
	return created(body), err
}

func (c *client) UpdateDashboard(ctx context.Context, id string, dashboard dashboards.Dashboard) error {
	dashboard.ID = ""
	_, err := c.call(ctx, "PUT", "/dashboards/"+pathID(id), dashboard)
	return err
}

func (c *client) DeleteDashboard(ctx context.Context, id string) error {
	_, err := c.call(ctx, "DELETE", "/dashboards/"+pathID(id), nil)
	return err
}

// Alerts lists every alert. One whose fields do not decode as the platform writes them keeps its id and name, so
// the platform's own is rewritten.
func (c *client) Alerts(ctx context.Context) ([]dashboards.Alert, error) {
	items, err := c.list(ctx, "/alerts")
	if err != nil {
		return nil, err
	}
	alerts := make([]dashboards.Alert, 0, len(items))
	for _, item := range items {
		found, err := fields("/alerts", item)
		if err != nil {
			return nil, err
		}
		var alert dashboards.Alert
		if json.Unmarshal(item, &alert) != nil {
			alert = dashboards.Alert{}
		}
		alert.ID, alert.Name = text(found, "id"), text(found, "name")
		alerts = append(alerts, alert)
	}
	return alerts, nil
}

func (c *client) CreateAlert(ctx context.Context, alert dashboards.Alert) error {
	alert.ID = ""
	_, err := c.call(ctx, "POST", "/alerts", alert)
	return err
}

func (c *client) UpdateAlert(ctx context.Context, id string, alert dashboards.Alert) error {
	alert.ID = ""
	_, err := c.call(ctx, "PUT", "/alerts/"+pathID(id), alert)
	return err
}

func (c *client) DeleteAlert(ctx context.Context, id string) error {
	_, err := c.call(ctx, "DELETE", "/alerts/"+pathID(id), nil)
	return err
}

// pathID is an id as one path segment: HyperDX ids are ObjectIds, so anything that could reshape the path is
// replaced and the request fails as not found rather than reaching another object.
func pathID(id string) string {
	if id == "" || strings.ContainsAny(id, "/?#%.\\ ") {
		return "invalid-id"
	}
	return id
}
