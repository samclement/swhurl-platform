# Platform telemetry: progress, remaining slices and the next gate

Status: proposed
Gate: the operator approves slice 1 and answers D1 and D2
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

**Next:** slice 1, once the operator approves it. Each slice below lists its files, so its session reads
those and nothing more.

## Where it stands

| Capability | State | Evidence |
| --- | --- | --- |
| Platform pod logs parsed into ClickStack (all namespaces, JSON and named formats) | done | [baseline](current-state.md#platform-telemetry-baseline-11-november-2026) |
| Node, pod and container metrics per platform service | done | [baseline](current-state.md#platform-telemetry-baseline-11-november-2026) |
| HyperDX's own traces and metrics (10% sampled) | done | [baseline](current-state.md#platform-telemetry-baseline-11-november-2026) |
| Request telemetry at the ingress: `access.format: json` is set but access logs are off, and no OTLP export | partial | [baseline](current-state.md#platform-telemetry-baseline-11-november-2026) |
| Console and notify telemetry: plain-text and timing lines only, no per-request or per-job line, no spans | partial | [baseline](current-state.md#platform-telemetry-baseline-11-november-2026) |
| Service metrics: Flux, cert-manager, oauth2-proxy and Reloader expose Prometheus that nothing scrapes; NATS has no exporter | partial | [baseline](current-state.md#platform-telemetry-baseline-11-november-2026) |
| Generated dashboards: apps only (`App: <app>`, web and worker); none for platform services | partial | [apps](apps.md#dashboards) |
| Scheduled-job dashboard row: [apps](apps.md#dashboards) describes it, but the Go reconciler refuses a CronJob (`v2/dashboards/apps.go`, `kind`) | partial | [baseline](current-state.md#platform-telemetry-baseline-11-november-2026) |

## Remaining work

Ordered. Each slice is one session and one commit (or a short series), checked offline, then applied and
verified live, with a dated `current-state.md` entry. Files are where to start, not a limit.

**S1. Requests at the edge (Traefik): configuration only.**
- Change: in `infra/traefik/helmchartconfig.yaml`, turn on access logs, and OTLP tracing and metrics to the
  node collector (`HOST_IP:4318`, the apps' path; `platform/otel/helmrelease-daemonset.yaml` already
  receives it). Set a trace sample rate (D3). Read the chart's values at the k3s-pinned version first;
  Traefik 3 keys differ from older docs.
- Check: Traefik becomes the parent span of app requests. An app's web tiles count `SpanKind = 'Server'` in its
  own namespace, so they shouldn't double count. Confirm the worker `ParentSpanId = ''` tiles are
  unaffected.
- Verify: add an access-log fixture to `tests/fixtures/logs.json` and run `make check-otel`, then `make check`.
  Live: `host/telemetry-inventory.sh 1` shows `kube-system/traefik` access lines and spans, and a console
  page load appears as a Traefik span.
- Docs: [services](services.md#clickstack-and-otel) (what Traefik sends), [current state](current-state.md).

**S2. `Platform: <service>` dashboards in the reconciler (`v2/dashboards`, `v2/orchestration/dashboards.go`).**
- Meaning: a platform service is an entry in a fixed Go catalogue: name, namespace, container selector, kind
  (`web`, `worker` or `job`) and, for `web`, the Traefik router or service its request tiles filter on. A
  test checks the catalogue against `platform/*` and `infra/*`, so a new unit can't be left out silently.
- Ownership: the existing pass and `clickstack.API` adapter. Tag `swhurl-platform`. The app pass never reads,
  writes or prunes that tag. The platform pass deletes only a tagged dashboard named exactly
  `Platform: <x>` whose `x` the catalogue no longer has. An empty catalogue is a failure, never "delete
  everything" (the same rule as `ErrNoRecords`).
- Tiles: reuse `Definition`'s tiles, filtered by namespace. Web request tiles read Traefik's spans from S1.
  The `job` kind restores the scheduled-job row (runs, failures, duration, last runs) for the backup and
  console CronJobs, which also settles D4.
- Verify: Go tests beside `dashboards_test.go` and `apps_test.go` (catalogue, prune, idempotent `Same`),
  `make check-v2`, `make check`; live `DRY_RUN=true make clickstack-dashboards`, then the CronJob's next run
  and `make verify-platform`.
- Docs: [apps](apps.md#dashboards) or a services section for the platform family, [v2 status](v2-status.md)
  (the reconciler's row), [console](console.md) if it links them.

**S3. The console's and notify's own telemetry (per D2).** With D2's recommended option: one structured JSON
line per HTTP request (method, route pattern, status, `duration_ms`, never a token or form value) and one per
job (kind, outcome, `duration_ms`), via stdlib `log/slog` in `v2/cmd/swhurl-v2` (console, `notify-serve`).
The collector's JSON parser takes them as they are. The console dashboard gains job tiles. Verify with a Go
test of the line shape and redaction, `make check-v2` and a live page load and job seen in ClickStack.

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
| D1 | Approve S1 and S2 as the first slices? | Yes: S1 is configuration only and lights up every service behind the ingress | S1 |
| D2 | Console telemetry: (1) structured JSON lines, (2) a stdlib OTLP/HTTP trace exporter in v2 (a new external-effect adapter, separate approval), (3) an OTel Go SDK exception to stdlib-only | (1); revisit (2) if a console trace is ever needed | S3 |
| D3 | Traefik trace sample rate | 100% while the volume is small (apps send ≈4,700 spans an hour); lower if S4's cost gate says so | S1 |
| D4 | Scheduled-job row: restore it in Go (S2) or correct the apps page now | Restore in S2; the backup jobs need it | S2 |
| D5 | NATS exporter sidecar (one more container in `messaging`) | Yes, with a keep-list | S4 |
| D6 | Platform alert names: `swhurl:platform/<service>:<preset>` or a separate prefix | `swhurl:platform/<service>:<preset>`, one parser | S5 |

## Deferred features

| Feature | v2 today | Reopen when |
| --- | --- | --- |
| Spans inside the console (handler, GitHub and Kubernetes calls) | None; S3 gives request and job lines | A console slowness can't be explained from the lines (D2 option 2) |
| Squid metrics for the egress proxy | Access lines (`squid-access`) only | Egress volume or latency needs a graph |
| HyperDX's own scrape of ClickHouse | Pushed over OpAMP; Git can't change it ([services](services.md#clickstack-and-otel)) | The chart exposes it |
| An outside availability monitor | None; nothing in the cluster can see the cluster down | A public route matters to someone outside |
| Telemetry backups | None; telemetry is expendable ([plan](plan.md)) | Telemetry becomes evidence that must survive |
| Per-pod collector authentication | The node collector trusts every pod | An untrusted workload runs on the node |

## Durable decisions

- Telemetry enters through the node collector's OTLP port or its log files; no service holds the ingestion key.
- v2 stays stdlib-only: no OpenTelemetry Go SDK without a separate approval (D2).
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
