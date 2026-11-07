package dashboards

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
)

// Tag is on every dashboard and alert the platform keeps; nothing without it is ever changed.
const Tag = "swhurl-app"

const namespaceColumn = "ResourceAttributes['k8s.namespace.name']"

// chartHeight is a line chart's height in grid rows: 3 (HyperDX's usual) is too short to read one.
const chartHeight = 9

// Dashboard is a HyperDX dashboard as its external API states it. ID is the server's; a request body leaves it out.
type Dashboard struct {
	ID    string   `json:"id,omitempty"`
	Name  string   `json:"name"`
	Tags  []string `json:"tags"`
	Tiles []Tile   `json:"tiles"`
}

// Tile is one chart or search on a dashboard, placed on a 24-column grid. ID is the server's: alerts name it.
type Tile struct {
	ID     string     `json:"id,omitempty"`
	Name   string     `json:"name"`
	X      int        `json:"x"`
	Y      int        `json:"y"`
	W      int        `json:"w"`
	H      int        `json:"h"`
	Config TileConfig `json:"config"`
}

// TileConfig is what a tile shows. A line chart selects aggregations (Select); a search selects columns (Columns),
// which the API states in the same "select" field as text.
type TileConfig struct {
	DisplayType   string
	SourceID      string
	GroupBy       string
	Select        []Select
	Columns       string
	Where         string
	WhereLanguage string
}

// Select is one aggregation of a line chart.
type Select struct {
	AggFn           string  `json:"aggFn"`
	Level           float64 `json:"level,omitempty"`
	ValueExpression string  `json:"valueExpression,omitempty"`
	Where           string  `json:"where"`
	WhereLanguage   string  `json:"whereLanguage"`
	Alias           string  `json:"alias,omitempty"`
}

type tileConfigJSON struct {
	DisplayType   string          `json:"displayType"`
	SourceID      string          `json:"sourceId"`
	GroupBy       string          `json:"groupBy,omitempty"`
	Select        json.RawMessage `json:"select,omitempty"`
	Where         string          `json:"where,omitempty"`
	WhereLanguage string          `json:"whereLanguage,omitempty"`
}

// MarshalJSON states the config as the API takes it: "select" is the aggregations, or the columns of a search.
func (c TileConfig) MarshalJSON() ([]byte, error) {
	var selected any = c.Select
	if c.Select == nil {
		selected = c.Columns
	}
	raw, err := json.Marshal(selected)
	if err != nil {
		return nil, err
	}
	return json.Marshal(tileConfigJSON{c.DisplayType, c.SourceID, c.GroupBy, raw, c.Where, c.WhereLanguage})
}

// UnmarshalJSON reads the fields the platform states and drops the rest, such as the defaults the server adds. A
// field of another shape is left empty, so the tile differs from its definition and is rewritten, never refused.
func (c *TileConfig) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		*c = TileConfig{}
		return nil
	}
	*c = TileConfig{DisplayType: text(fields, "displayType"), SourceID: text(fields, "sourceId"),
		GroupBy: text(fields, "groupBy"), Where: text(fields, "where"), WhereLanguage: text(fields, "whereLanguage")}
	selected := bytes.TrimSpace(fields["select"])
	if json.Unmarshal(selected, &c.Select) != nil {
		c.Select = nil
	}
	if c.Select == nil {
		c.Columns = text(fields, "select")
	}
	return nil
}

// text is a field's string value, or "" when it is absent or not a string.
func text(fields map[string]json.RawMessage, key string) string {
	var value string
	if json.Unmarshal(fields[key], &value) != nil {
		return ""
	}
	return value
}

// Definition is the dashboard an app should have, its tiles reading the trace and log sources given. Every tile
// filters on the app's namespaces, not its service name, so an app without an OpenTelemetry SDK still gets its log
// tiles, and each line splits by environment. A web app counts its server spans as requests; a worker its root
// spans as runs.
func Definition(app App, traces, logs string) Dashboard {
	mine := namespaceColumn + " IN (" + app.namespaces() + ")"
	spans, noun := mine+" AND ParentSpanId = ''", "Runs"
	if app.Kind == Web {
		spans, noun = mine+" AND SpanKind = 'Server'", "Requests"
	}
	tiles := []Tile{
		line(noun+" per minute", 0, 0, 8, traces, count(spans, strings.ToLower(noun))),
		line("Errors", 8, 0, 8, traces, count(spans+" AND StatusCode = 'Error'", "errors")),
		line("p95 duration (ms)", 16, 0, 8, traces, Select{AggFn: "quantile", Level: 0.95,
			ValueExpression: "Duration / 1e6", Where: spans, WhereLanguage: "sql", Alias: "p95 ms"}),
		line("Error logs", 0, chartHeight, 12, logs, count(mine+" AND lower(SeverityText) IN ('error', 'fatal')", "errors")),
		line("Log lines", 12, chartHeight, 12, logs, count(mine, "lines")),
		{Name: "Latest logs", X: 0, Y: 2 * chartHeight, W: 24, H: 6, Config: TileConfig{DisplayType: "search",
			SourceID: logs, WhereLanguage: "sql", Where: mine,
			Columns: "Timestamp, " + namespaceColumn + " AS environment, SeverityText, Body"}},
	}
	return Dashboard{Name: app.DashboardName(), Tags: []string{Tag}, Tiles: tiles}
}

func line(name string, x, y, w int, source string, selected Select) Tile {
	return Tile{Name: name, X: x, Y: y, W: w, H: chartHeight, Config: TileConfig{DisplayType: "line",
		SourceID: source, GroupBy: namespaceColumn, Select: []Select{selected}}}
}

func count(where, alias string) Select {
	return Select{AggFn: "count", Where: where, WhereLanguage: "sql", Alias: alias}
}

// Same is whether a live dashboard already is its definition: the same name, tags in any order, and tiles in order
// with the same placement and config. The server's tile ids and the defaults it adds are not differences.
func Same(live, wanted Dashboard) bool {
	return live.Name == wanted.Name && sameTags(live.Tags, wanted.Tags) && slices.EqualFunc(live.Tiles, wanted.Tiles, sameTile)
}

func sameTags(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func sameTile(a, b Tile) bool {
	return shapeOf(a) == shapeOf(b) && slices.Equal(a.Config.Select, b.Config.Select)
}

// tileShape is a tile's comparable facts: all but its server id and its aggregations.
type tileShape struct {
	Name                                                          string
	X, Y, W, H                                                    int
	DisplayType, SourceID, GroupBy, Columns, Where, WhereLanguage string
}

func shapeOf(t Tile) tileShape {
	c := t.Config
	return tileShape{t.Name, t.X, t.Y, t.W, t.H, c.DisplayType, c.SourceID, c.GroupBy, c.Columns, c.Where, c.WhereLanguage}
}
