# Platform telemetry: progress, remaining slices and the next gate

Status: in progress
Gate: slice 4's selective scrape, with its day-long cost baseline (D5)
Uncommitted: none

**The goal: every platform service is observable in ClickStack the way an app is.** Its logs, its requests
(rate, errors, latency) and the metrics that say whether it works reach ClickStack. Each service gets a
generated `Platform: <service>` dashboard from the same reconciler that makes `App: <app>` dashboards. The
failures nobody watches today (expiring certificates, a dashboard or backup job that stopped, failed console
actions) alert through `swhurl notify`. The services are the console and its jobs, notify, Traefik,
oauth2-proxy, Flux, cert-manager, NATS, Reloader, the egress proxy, the backup jobs and HyperDX itself.

This page is the one place this work's progress, slices and decisions live. Dated evidence is in
[current state](current-state.md); the registry is [workstreams](workstreams.md); the contracts it builds on
are in [services](services.md#clickstack-and-otel) and [apps](apps.md#dashboards). Rows are overwritten in
place; history is Git.

## Resume here

Before reading anything else in a new session:

1. Read this page only. It is the map: start from **Next** below, and don't re-run the gap analysis.
2. `git log --oneline -5 -- docs/platform-telemetry-status.md` and `git status --short` show what moved since
   this page was last written.
3. Open a linked page by its heading only when the slice needs it; send broad greps to a subagent with this
   page as its brief.
4. For numbers, run `host/telemetry-inventory.sh [hours]`. It is read-only and prints logs, spans and
   metric names per source and rows per hour. Never paste ad hoc ClickHouse queries into the conversation.
5. One slice per session, opened with `/slice`. If the slice touches `v2/`, read the v2
   [design principles](iteration-2-plan.md#design-principles) and update [v2 status](v2-status.md) as well.

**Next:** slice 4 (a selective metrics scrape), with its day-long cost baseline; D5 decides the NATS exporter.
Each slice below lists its files, so its session reads those and nothing more.

## Where it stands

| Capability | State | Evidence |
| --- | --- | --- |
| Platform pod logs parsed into ClickStack (all namespaces, JSON and named formats) | done | [baseline](current-state.md#platform-telemetry-baseline-11-november-2026) |
| Node, pod and container metrics per platform service | done | [baseline](current-state.md#platform-telemetry-baseline-11-november-2026) |
| HyperDX's own traces and metrics (10% sampled) | done | [baseline](current-state.md#platform-telemetry-baseline-11-november-2026) |
| Request telemetry at the ingress: one JSON access line and one OTLP span per request, 100% sampled | done | [record](current-state.md#traefiks-requests-reach-clickstack-13-november-2026) |
| Console and notify telemetry: one JSON line per request (method, route pattern, status, duration) and per job state change, and the console's own job tiles | done | [record](current-state.md#console-and-notify-telemetry-14-november-2026) |
| The console's own traces and metrics (S3b): a span per request, continuing Traefik's trace, with its reads inside; request and job counters and durations | done | [record](current-state.md#console-traces-and-metrics-14-november-2026) |
| Service metrics: Flux, cert-manager, oauth2-proxy and Reloader expose Prometheus that nothing scrapes; NATS has no exporter | partial | [baseline](current-state.md#platform-telemetry-baseline-11-november-2026) |
| Generated dashboards: `App: <app>` for apps, and `Platform: <service>` for the thirteen catalogued platform services, including their scheduled jobs | done | [record](current-state.md#platform-dashboards-13-november-2026) |
| Scheduled-job row: a platform job's dashboard counts the log line each completed run writes; an app cannot be a scheduled job, and [apps](apps.md#dashboards) now says so | done | [record](current-state.md#platform-dashboards-13-november-2026) |

## Remaining work

Ordered. Each slice is one session and one commit (or a short series), checked offline, then applied and
verified live, with a dated `current-state.md` entry. Files are where to start, not a limit.

**S4. A selective metrics scrape.** A `prometheus` receiver on the cluster collector
(`platform/otel/helmrelease-cluster.yaml`), each target with a keep-list:
- cert-manager: certificate expiry and readiness
- Flux: reconcile duration and readiness
- oauth2-proxy: requests by status
- NATS: needs its exporter enabled in `platform/nats/helmrelease-nats.yaml` (D5)

Admit the scrape in each target's NetworkPolicy: `flux-system/allow-scraping` already allows it,
`messaging/nats` likely does not. **Cost gate:** record `host/telemetry-inventory.sh 24` rows per hour
before and a full day after. Judge ClickHouse CPU over a day, not one window
([lesson](services.md#clickstack-and-otel)). Add the metric tiles to the S2 dashboards.

**S5. Platform alerts.** Presets on the platform dashboards: certificate expiring within 14 days, a CronJob
with no success within its window, error logs, 5xx at the edge. Alerts route through `swhurl notify` to the
platform topic. Notify's name contract is `swhurl:<app>:<preset>`, with the environment taken from the
namespace. A platform owner needs a reserved form (D6) in `v2/dashboards/alerts.go`, notify's parser and
its tests. Afterwards, update the uncovered boundaries in [services](services.md) that S5 closes.

### Open decisions

| # | Question | Recommendation | Blocks |
| --- | --- | --- | --- |
| D5 | NATS exporter sidecar (one more container in `messaging`) | Yes, with a keep-list | S4 |
| D6 | Platform alert names: `swhurl:platform/<service>:<preset>` or a separate prefix | `swhurl:platform/<service>:<preset>`, one parser | S5 |

## Deferred features

| Feature | v2 today | Reopen when |
| --- | --- | --- |
| Squid metrics for the egress proxy | Access lines (`squid-access`) only | Egress volume or latency needs a graph |
| HyperDX's own scrape of ClickHouse | Pushed over OpAMP; Git can't change it ([services](services.md#clickstack-and-otel)) | The chart exposes it |
| An outside availability monitor | None; nothing in the cluster can see the cluster down | A public route matters to someone outside |
| Telemetry backups | None; telemetry is expendable ([plan](plan.md)) | Telemetry becomes evidence that must survive |
| Per-pod collector authentication | The node collector trusts every pod | An untrusted workload runs on the node |

## Durable decisions

- Telemetry enters through the node collector's OTLP port or its log files; no service holds the ingestion key.
- Traefik's access logs and spans reach the node collector on `$(HOST_IP):4318`, the address apps use; the host's
  address is never written in Git. Traefik's spans are 100% sampled until a day's cost says otherwise.
- Traefik's Prometheus and OTLP metrics stay off until a scrape has a keep-list and a day-long cost baseline.
- Platform dashboards come from a Go catalogue checked against `platform/*` and `infra/*`: a service behind the
  ingress counts Traefik's spans by request host, a job counts the log line of each completed run, and an empty
  catalogue fails rather than deleting every dashboard.
- `swhurl-app` and `swhurl-platform` are disjoint: each pass reads, updates and prunes only its own tag, and only
  the app pass records dashboard ids for the console and keeps the default alerts.
- v2 stays stdlib-only: no OpenTelemetry Go SDK without a separate approval (D2).
- Console and notify telemetry is structured JSON lines on stdout, written by v2 with the standard library: a request's
  method, route *pattern* (never its path, query or form value), status and duration; a job's action, target, state,
  duration and the audit text. Probes and assets write no request line, and a 5xx request or a failed job is an ERROR
  line. D2 is answered with those lines (14 November 2026); the console's traces are D2's option 2, a separate
  stdlib exporter adapter (D7 answered A, 14 November 2026): no SDK, no protobuf, and nothing sent without
  `OTEL_EXPORTER_OTLP_ENDPOINT`.
- Dashboards are generated, never hand-edited: the definition is Go, and a UI edit is overwritten on the next run.
- The platform catalogue is Go, checked against Git's units; the cluster never adds a platform dashboard.
- Tag `swhurl-platform` is the platform pass's alone; `swhurl-app` stays the app pass's alone.
- No metrics scrape without a keep-list and a day-long cost baseline from `host/telemetry-inventory.sh`.

## How this page is kept

- **At the end of every session** that worked on this, in the same commit as the work:
  1. Overwrite the capability rows the session moved (`done` or `partial`, one dated evidence link).
  2. Delete the delivered slice from Remaining work, and the open decisions it settled; move a settled rule
     to Durable decisions in one line.
  3. Rewrite **Next** in "Resume here" to say the next slice and its first action.
  4. Set `Status`, `Gate` and `Uncommitted` here and the same `State` and `Next gate` in
     [workstreams](workstreams.md). Only one workstream may be `in progress`.
  5. Add one dated [current state](current-state.md) entry with the commands run and what was seen.
- A session that stops mid-slice lists its uncommitted paths in `Uncommitted` and writes the one next action
  in **Next**, so the following session starts there without re-reading the transcript.
- A map, not a log: no session narratives or slice records. History is
  `git log -p -- docs/platform-telemetry-status.md`. Contracts stay on their owning pages and are linked.
- `make check-docs` enforces the shape, the registry match and the 250-line cap. A change to
  `host/telemetry-inventory.sh` must also change this page; a change under `v2/` also changes
  [v2 status](v2-status.md).
