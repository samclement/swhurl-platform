package dashboards

import "strings"

// PlatformTag is on every dashboard of the platform's own services. The app pass carries Tag and never reads,
// updates or deletes a dashboard that carries only this one, and this pass changes only its own tag.
const PlatformTag = "swhurl-platform"

// PlatformPrefix names a platform service's dashboard, "Platform: console". It is also the only name this pass
// deletes: a tagged dashboard named "Platform: <x>" whose service the catalogue no longer has.
const PlatformPrefix = "Platform: "

// The platform kinds apps do not have. Job is a scheduled job, whose completed runs are the log lines one marker
// states; LogsOnly is a service that reports logs only, until its own request telemetry lands.
const (
	Job      = "job"
	LogsOnly = "logs"
)

// edgeSpans is Traefik's own spans in its namespace: every request that reached the cluster through the ingress,
// whatever it was for.
const edgeSpans = namespaceColumn + " = 'kube-system' AND SpanKind = 'Server'"

// edgeFailed is how a request Traefik answered itself fails: Traefik sets no span status for a 4xx, so the status
// code is the evidence. Three-digit codes compare as text.
const edgeFailed = "SpanAttributes['http.response.status_code'] >= '500'"

// Service is one platform service as its dashboard needs it. Name is the dashboard's subject; Namespace and
// Container select its logs, and its own spans when it reports them.
type Service struct {
	Name       string
	Kind       string
	Namespace  string
	Container  string // "" for every container in the namespace
	HostPrefix string // a web service behind the ingress: the start of the request host Traefik sees
	RunMarker  string // a job: a SQL predicate on Body that marks one completed run
	PodPattern string // a job whose pods live in many namespaces: a LIKE pattern on the pod name, instead of Namespace
	// RequestMarker is the SQL predicate on a service's logs that marks one request's line, for a service that
	// reports no spans: its request tiles then count those lines instead.
	RequestMarker string
	// JobMarker is the SQL predicate on a service's logs that marks one of its own jobs' lines (a start and an end
	// both carry it): its job tiles count the ends, and time them.
	JobMarker string
}

// DashboardName is the name of the service's dashboard, and how a dashboard is known to be its.
func (s Service) DashboardName() string { return PlatformPrefix + s.Name }

// logWhere is the service's logs: by pod name for a job that runs in every app's namespace, else its namespace and
// container.
func (s Service) logWhere() string {
	if s.PodPattern != "" {
		return "ResourceAttributes['k8s.pod.name'] LIKE '" + s.PodPattern + "'"
	}
	where := namespaceColumn + " = '" + s.Namespace + "'"
	if s.Container != "" {
		where += " AND ResourceAttributes['k8s.container.name'] = '" + s.Container + "'"
	}
	return where
}

// spanWhere is the trace filter of a service that reports requests or runs, the word its first tiles count, and how
// a failed one of those is stated in that source.
func (s Service) spanWhere() (where, noun, failed string, ok bool) {
	namespace := namespaceColumn + " = '" + s.Namespace + "'"
	switch s.Kind {
	case Web:
		if s.HostPrefix != "" {
			return edgeSpans + " AND SpanAttributes['server.address'] LIKE '" + s.HostPrefix + "%'", "Requests",
				edgeFailed, true
		}
		return namespace + " AND SpanKind = 'Server'", "Requests", "StatusCode = 'Error'", true
	case Worker:
		return namespace + " AND ParentSpanId = ''", "Runs", "StatusCode = 'Error'", true
	}
	return "", "", "", false
}

// PlatformDefinition is the dashboard a platform service should have, its tiles reading the trace and log sources
// given. Requests of a service behind the ingress are Traefik's spans filtered by the request host, so a service
// needs no OpenTelemetry SDK of its own; a job counts the log line that marks each completed run.
func PlatformDefinition(s Service, traces, logs string) Dashboard {
	mine, errorLogs := s.logWhere(), s.logWhere()+" AND lower(SeverityText) IN ('error', 'fatal')"
	tiles, row := []Tile{}, 0
	switch where, noun, failed, spans := s.spanWhere(); {
	case s.RequestMarker != "":
		tiles = append(tiles, requestLineTiles(mine, s.RequestMarker, logs, row)...)
		row++
	case spans:
		tiles = append(tiles,
			line(noun+" per minute", 0, row*chartHeight, 8, traces, count(where, strings.ToLower(noun))),
			line("Errors", 8, row*chartHeight, 8, traces, count(where+" AND "+failed, "errors")),
			line("p95 duration (ms)", 16, row*chartHeight, 8, traces, Select{AggFn: "quantile", Level: 0.95,
				ValueExpression: "Duration / 1e6", Where: where, WhereLanguage: "sql", Alias: "p95 ms"}),
		)
		row++
	}
	if s.JobMarker != "" {
		tiles = append(tiles, jobLineTiles(mine, s.JobMarker, logs, row)...)
		row++
	}
	if s.Kind == Job {
		// A run that fails writes no marker line, so the error lines are the failures; the marker counts the runs.
		tiles = append(tiles,
			line("Runs per minute", 0, row*chartHeight, 8, logs, count(mine+" AND "+s.RunMarker, "runs")),
			line("Error logs", 8, row*chartHeight, 8, logs, count(errorLogs, "errors")),
			line("Log lines", 16, row*chartHeight, 8, logs, count(mine, "lines")),
		)
		row++
	}
	tiles = append(tiles,
		line("Error logs", 0, row*chartHeight, 12, logs, count(errorLogs, "errors")),
		line("Log lines", 12, row*chartHeight, 12, logs, count(mine, "lines")))
	row++
	tiles = append(tiles, Tile{Name: "Latest logs", X: 0, Y: row * chartHeight, W: 24, H: 6,
		Config: TileConfig{DisplayType: "search", SourceID: logs, WhereLanguage: "sql", Where: mine,
			Columns: "Timestamp, SeverityText, Body"}})
	return Dashboard{Name: s.DashboardName(), Tags: []string{PlatformTag}, Tiles: tiles}
}

// requestLineTiles chart a service's own request lines: how many, how many failed and how slow. A failed request is
// a 5xx, which is the service's; three-digit codes compare as text, as they do for the edge's spans.
func requestLineTiles(mine, marker, logs string, row int) []Tile {
	y := row * chartHeight
	where := mine + " AND " + marker
	return []Tile{
		line("Requests per minute", 0, y, 8, logs, count(where, "requests")),
		line("Failed requests", 8, y, 8, logs, count(where+" AND LogAttributes['status'] >= '500'", "failed")),
		line("p95 duration (ms)", 16, y, 8, logs, Select{AggFn: "quantile", Level: 0.95,
			ValueExpression: "toFloat64OrZero(LogAttributes['duration_ms'])", Where: where,
			WhereLanguage: "sql", Alias: "p95 ms"}),
	}
}

// jobLineTiles chart a service's own jobs from the line each start and end writes: a start is not a run, and a job
// that was cut short by a pod's replacement counts as failed.
func jobLineTiles(mine, marker, logs string, row int) []Tile {
	y := row * chartHeight
	finished := mine + " AND " + marker + " AND LogAttributes['state'] != 'started'"
	return []Tile{
		line("Jobs per minute", 0, y, 8, logs, count(finished, "jobs")),
		line("Failed jobs", 8, y, 8, logs,
			count(finished+" AND LogAttributes['state'] IN ('failed', 'interrupted')", "failed")),
		line("p95 job duration (ms)", 16, y, 8, logs, Select{AggFn: "quantile", Level: 0.95,
			ValueExpression: "toFloat64OrZero(LogAttributes['duration_ms'])", Where: finished,
			WhereLanguage: "sql", Alias: "p95 ms"}),
	}
}

// PlatformServices is the fixed catalogue: one entry per service that runs a workload of its own. A unit that runs
// none (namespaces, ConfigMaps, alert rules) is named in the test's noWorkload instead, so a new unit cannot be
// missing from both without a test failing. Sorted by name, which is the order the pass reports them in.
func PlatformServices() []Service {
	return []Service{
		// Every SQLite instance's backup job and ClickStack MongoDB's, in their own namespaces: one line names what
		// was published, and a manual run publishes the same way.
		{Name: "backup", Kind: Job, PodPattern: "%-backup-%", RunMarker: "Body LIKE 'Published %'"},
		{Name: "cert-manager", Kind: LogsOnly, Namespace: "cert-manager"},
		{Name: "clickstack", Kind: Web, Namespace: "observability", Container: "app", HostPrefix: "clickstack."},
		{Name: "console", Kind: Web, Namespace: "console", Container: "main", HostPrefix: "console.",
			JobMarker: "LogAttributes['event'] = 'job'"},
		{Name: "console-dashboards", Kind: Job, Namespace: "console", Container: "main",
			RunMarker: "Body LIKE 'ClickStack dashboard pass completed%'"},
		{Name: "console-notifications", Kind: Job, Namespace: "console", Container: "main",
			RunMarker: "Body LIKE 'Notification check completed%'"},
		{Name: "egress", Kind: LogsOnly, Namespace: "egress", Container: "main"},
		{Name: "flux-controllers", Kind: LogsOnly, Namespace: "flux-system"},
		{Name: "nats", Kind: LogsOnly, Namespace: "messaging", Container: "nats"},
		{Name: "notify", Kind: LogsOnly, Namespace: "notify", Container: "main",
			RequestMarker: "LogAttributes['event'] = 'request'"},
		{Name: "oauth2-proxy", Kind: Web, Namespace: "ingress", Container: "oauth2-proxy", HostPrefix: "oauth."},
		{Name: "reloader", Kind: LogsOnly, Namespace: "platform-system"},
		// Traefik reports the requests it serves itself, so its own spans are its requests.
		{Name: "traefik", Kind: Web, Namespace: "kube-system", Container: "traefik"},
	}
}
