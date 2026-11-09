package pages

import (
	"slices"
	"strconv"
	"strings"
)

// MapNode is one app of a map: its box and where it sits.
type MapNode struct {
	Row                *AppRow
	X, Y               int
	TextX, NameY, SubY int
	Label              string // what the box says under the app's name
}

// MapEdge is one line of a map: a call or an event, its SVG path and the apps it joins.
type MapEdge struct{ Kind, Path, From, To string }

// AppMap is the apps of one environment as boxes, with a line for every call and every event subscription.
type AppMap struct {
	Nodes     []MapNode
	Edges     []MapEdge
	Width     int
	Height    int
	BoxWidth  int
	BoxHeight int
}

// box is how a map draws one app: its size and the gaps between them.
const (
	mapWidth, mapHeight, mapGapX, mapGapY, mapMargin = 210, 56, 110, 20, 12
)

// mapLink is one relationship a map draws: a call or an event, between two apps of its environment.
type mapLink struct{ kind, source, target string }

// AppMapOf lays the apps out left to right in the direction requests and events travel: an app sits one column right
// of the furthest app that calls it or whose events it receives.
func AppMapOf(rows []AppRow, env string) AppMap {
	here, order := mapApps(rows, env)
	links := mapLinks(here, order)
	columns := mapColumns(order, links)
	position, filled := mapPositions(order, links, columns)
	edges, lowest := mapEdges(links, position, columns)
	return mapDrawn(here, order, position, filled, edges, lowest)
}

// mapApps is the environment's apps, in the order the rows came.
func mapApps(rows []AppRow, env string) (map[string]*AppRow, []string) {
	here := map[string]*AppRow{}
	order := []string{}
	for i := range rows {
		if rows[i].Instance.Env == env {
			here[rows[i].Instance.App] = &rows[i]
			order = append(order, rows[i].Instance.App)
		}
	}
	return here, order
}

// mapLinks is every call and every event subscription between apps of the environment, calls first.
func mapLinks(here map[string]*AppRow, order []string) []mapLink {
	links := []mapLink{}
	for _, app := range order {
		for _, callee := range here[app].Connections().Calls {
			if _, known := here[callee]; known {
				links = append(links, mapLink{"call", app, callee})
			}
		}
	}
	for _, app := range order {
		for _, name := range here[app].Connections().Subscribes {
			publisher, _, _ := strings.Cut(name, ".")
			if _, known := here[publisher]; known {
				links = append(links, mapLink{"event", publisher, app})
			}
		}
	}
	return links
}

// mapColumns is each app's column: one right of the furthest app that calls it or whose events it receives.
func mapColumns(order []string, links []mapLink) map[string]int {
	sources := map[string][]string{}
	for _, one := range links {
		sources[one.target] = append(sources[one.target], one.source)
	}
	columns := map[string]int{}
	var column func(string, []string) int
	column = func(app string, seen []string) int {
		if found, known := columns[app]; known {
			return found
		}
		deepest := -1
		for _, source := range sources[app] {
			if !slices.Contains(seen, source) {
				deepest = max(deepest, column(source, append(seen, app)))
			}
		}
		columns[app] = deepest + 1
		return columns[app]
	}
	for _, app := range order {
		column(app, nil)
	}
	return columns
}

// mapPositions places the connected apps first, so the apps nothing links to gather at the bottom of the first
// column, and says how many rows each column holds.
func mapPositions(order []string, links []mapLink, columns map[string]int) (map[string][2]int, map[int]int) {
	sources := map[string]bool{}
	for _, one := range links {
		sources[one.source], sources[one.target] = true, true
	}
	connected := func(app string) bool { return sources[app] }
	apps := slices.Clone(order)
	slices.SortStableFunc(apps, func(a, b string) int {
		if first, second := !connected(a), !connected(b); first != second {
			if first {
				return 1
			}
			return -1
		}
		return strings.Compare(a, b)
	})
	filled := map[int]int{}
	position := map[string][2]int{}
	for _, app := range apps {
		level := columns[app]
		position[app] = [2]int{mapMargin + level*(mapWidth+mapGapX), mapMargin + filled[level]*(mapHeight+mapGapY)}
		filled[level]++
	}
	return position, filled
}

// mapEdges is one path per relationship: a call and an event between the same two apps stay apart, and a line that
// skips a column is sent under the boxes between rather than behind them. lowest is how far down a line reaches.
func mapEdges(links []mapLink, position map[string][2]int, columns map[string]int) ([]MapEdge, int) {
	edges := []MapEdge{}
	lowest := 0
	for _, one := range links {
		start, end := position[one.source], position[one.target]
		offset := 10
		if one.kind == "call" {
			offset = -8
		}
		from := [2]int{start[0] + mapWidth, start[1] + mapHeight/2 + offset}
		to := [2]int{end[0], end[1] + mapHeight/2 + offset}
		bend := max(40, abs(to[0]-from[0])/2)
		dip := 0
		if skipped := abs(columns[one.target]-columns[one.source]) - 1; skipped > 0 {
			dip = mapHeight/2 + 14 + 10*skipped
			if one.kind == "event" {
				dip += 8
			}
		}
		lowest = max(lowest, max(from[1], to[1])+dip)
		edges = append(edges, MapEdge{Kind: one.kind, From: one.source, To: one.target,
			Path: "M " + strconv.Itoa(from[0]) + " " + strconv.Itoa(from[1]) +
				" C " + strconv.Itoa(from[0]+bend) + " " + strconv.Itoa(from[1]+dip*2) + ", " +
				strconv.Itoa(to[0]-bend) + " " + strconv.Itoa(to[1]+dip*2) + ", " +
				strconv.Itoa(to[0]) + " " + strconv.Itoa(to[1])})
	}
	return edges, lowest
}

// mapDrawn is the finished map: its boxes, and the area that holds them.
func mapDrawn(here map[string]*AppRow, order []string, position map[string][2]int, filled map[int]int,
	edges []MapEdge, lowest int) AppMap {
	apps := slices.Clone(order)
	slices.Sort(apps)
	nodes := make([]MapNode, 0, len(position))
	for _, app := range apps {
		row := here[app]
		x, y := position[app][0], position[app][1]
		nodes = append(nodes, MapNode{Row: row, X: x, Y: y, TextX: x + 12, NameY: y + 23, SubY: y + 43,
			Label: mapLabel(row)})
	}
	rowsInMap, columnsUsed := 1, 0
	for level, count := range filled {
		rowsInMap = max(rowsInMap, count)
		columnsUsed = max(columnsUsed, level)
	}
	return AppMap{Nodes: nodes, Edges: edges,
		Width:    mapMargin*2 + (columnsUsed+1)*(mapWidth+mapGapX) - mapGapX,
		Height:   max(mapMargin*2+rowsInMap*(mapHeight+mapGapY)-mapGapY, lowest+mapMargin),
		BoxWidth: mapWidth, BoxHeight: mapHeight}
}

// mapLabel is what a map's box says under the app's name.
func mapLabel(row *AppRow) string {
	label := row.Team()
	if label == "" {
		label = "no team"
	}
	label += " · " + row.State.Label()
	if row.Connections().Address == "" {
		label += " · worker"
	}
	return label
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
