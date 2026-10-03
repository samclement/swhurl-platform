# AGENTS.md

GitOps source for a live single-node k3s homelab. Flux applies `main`; changes reach the cluster only through Git or the documented `make` targets.

## Rules

- Update documentation in the same change as behaviour. Each topic has one canonical page (map below); link, don't copy. Describe what is, not what was.
- Validate before every commit: `make check`. For script, Makefile or layout changes also run `for f in host/*.sh tests/fixtures/*.sh; do bash -n "$f"; done` and the `DRY_RUN=true` variants CI runs (see `.github/workflows/validate.yml`).
- Commit to `main` after validation, push, `make flux-reconcile`, verify on the cluster (`make verify-platform`, `make app-status`), then record dated evidence in `docs/current-state.md`.
- Confirm with the user before hard-to-reverse live actions: deleting data (`make destroy-data`, namespaces, PVs), rotating real credentials, changing where backups go, or anything under `flux-system`. `make app-repo` and the console's **Start a new app** create a real public GitHub repository and package; only the user can delete them (`gh` lacks `delete_repo`), so name throwaway ones `swhurl-try-<n>` and list them for deletion afterwards.
- New operator logic goes in the `tools/swhurl` package, calling external tools only through `Runner` and tested with `FakeRunner` ([contributing](docs/contributing.md#operator-tooling)); keep short glue and host scripts as bash.
- Never print Secret values. Compare by bytes or hashes (`make check-secrets`, `make verify-platform`). `age.agekey` stays out of Git.
- Scripted `kubectl` needs `export KUBECONFIG=$HOME/.kube/config`; the k3s wrapper otherwise reads `/etc/rancher/k3s/k3s.yaml`.

## Working with the operator (draft)

- **Explain concretely.** Lead with an example; show before and after for renames and moves; draw a flow for anything crossing systems; say what an offered next step produces ("draft files, nothing applied"). After a change the operator can see, end with a "Test it yourself" block.
- **Verify like a user.** Check every entry point (http and https, signed in and out), prove scripts stop on failure, and read release notes and propose an upgrade path before merging a major version.
- **Act without asking** on warnings and doc gaps within the current change, offline-only fixes, and the next step of an approved outline. Stop for the confirm-first list above, a real choice between options, or scope growth.
- **Hand off commands precisely.** Label each block "run with `!`" or "run in your own terminal" (`sudo`, interactive logins; `!` has no terminal). Say what you will check afterwards, and watch long waits in the background instead of asking to be prompted.
- **Skills:** `decision-brief` before recommending a costly or undecided approach, `new-component-checklist` when adding anything that runs, stores data or holds a credential, `phase-handoff` for multi-step work.

## Where things are documented

| Topic | Canonical page |
| --- | --- |
| Everyday loop and task index | `README.md` |
| Bare host to running platform | `docs/bootstrap.md` |
| Health, Secrets, certificate mode, lifecycle, chart updates, backups, troubleshooting | `docs/operations.md` |
| Every `make` target | `docs/commands.md` |
| Shared services, settings, keys, known issues | `docs/services.md` |
| App lifecycle: template, create, access, secrets, telemetry, deploy, dependency updates, promote, operate, remove; app policy | `docs/apps.md` |
| Web console: use, deploy, token, protection | `docs/console.md` |
| Flux units, dependencies, ownership, deletion, how changes reach the cluster | `docs/architecture.md` |
| Validation, change checklist, docs and diagram conventions | `docs/contributing.md` |
| Dated live evidence and unexercised paths | `docs/current-state.md` |
| Remaining planned work | `docs/plan.md` section 0 |

## Commands

- Do: `make flux-reconcile`, `make verify-platform`, `make app-*`, `make check-secrets`, `make clickstack-bootstrap` and `make clickstack-dashboards` (idempotent), and the throwaway live tests (`live-test-lifecycle`, `live-test-reloader`, `live-test-app-template`, `live-test-restore-mongodb`). Push fixture changes before running live tests: they reconcile from Git.
- Don't: delete Flux units or namespaces as a reset (there is no teardown), or `kubectl apply` resources that Flux owns.
- `platform-certs-*`, `app-new`, `app-promote`, `app-scale`, `app-remove` and `console-image` only edit files: commit and push before reconciling. After changing `tools/`, `images/console/` or the lock file, the publish run deploys the console itself with a bot commit to `main` ([console](docs/console.md#deploy-a-new-console)): `git pull --rebase` before the next push; `make console-image` is the manual fallback. `make verify-platform` warns until the new image runs.
- `host-dns` and `host-backup` install system units with `sudo`: the operator runs them in their own terminal. The units run scripts from this checkout, so only template changes need a reinstall.
- Root units in `clusters/home/flux-system/kustomizations.yaml` are not reconciled by Flux: apply changes with `make flux-bootstrap`.
- Flux controllers come from `make flux-install` (`FLUX_VERSION` in `tools/swhurl/flux.py`, patches in `clusters/home/flux-system/install`); never plain `flux install`, which drops the patches.

## Lessons not obvious from the code

- **Flux substitution** consumes unescaped `${...}` in any manifest of a unit with `postBuild.substituteFrom`; escape literal references there as `$${...}` (`make check-repo` fails on an unresolved one). Units without substitution, such as `platform-otel` with its `${env:...}` collector references, need no escaping.
- **Double base64** has caused one outage: `data` values are encoded exactly once; prefer `stringData`.
- **Traefik 3 (k3s chart 38)** ignores `ports.web.redirectTo`; the redirect is `ports.web.redirections.entryPoint`. The chart renders the redirect target as `:443`.
- **oauth2-proxy ForwardAuth** needs `upstream=static://202`, `skip-provider-button=true` and the middleware pointing at `http://oauth2-proxy-shared.ingress.svc.cluster.local/` (not `/oauth2/auth`) so unauthenticated browsers get a followable 302. `set-xauthrequest: true` makes it return `X-Auth-Request-Email`, which the console needs (a test with a hand-set header cannot show it is missing; only a real signed-in request can). `email_domains = []` must stay in `config.configFile` or the chart's `*` default admits any Google account. Switching to GitHub: `provider: github`, drop `oidc-issuer-url`, keep the callback URL.
- **Moving resources between Flux units:** never change the old unit's path in the same commit as the cutover; the old unit must be `Orphan` or suspended.
- **`flux reconcile` does not preempt** an in-flight `wait: true` reconciliation; new requests queue until its health checks finish or time out, and `lastAttemptedRevision` can look stale meanwhile.
- **Deleting an app mid-install** waits for the Helm action timeout (3 minutes for app instances) before the finalizer releases the namespace.
- **ClickStack:** `hyperdx.config` changes do not restart the app (no checksum; Reloader does not watch `observability`): bump the `platform.swhurl.com/config-revision` pod annotation with them. Cold image pulls can exceed Helm's default wait (the HelmRelease sets `spec.timeout`). HyperDX registration stays open until a team exists and the team's ingestion key is random: `make clickstack-bootstrap` sets both from SOPS; the UI can rotate the key away from Git. Chart 3.x ignores `hyperdx.frontendUrl` (use `hyperdx.config.FRONTEND_URL`). ClickHouse renames a system log table to `<name>_N` when its definition changes. On ClickHouse 25.7 `system.query_log` has no `query_parameters`: match `query_id` against `clickstack-app` logs; `BAD_QUERY_PARAMETER (457)` with `nan` shows there as `param_HYPERDX_PARAM_*=nan`.
- **ClickHouse load** swings about 3x from hour to hour (merge time cycles over 3 to 4 hours), and cutting rows does not cut merge CPU: judge a change over a day against a baseline, not one window.
- **systemd unit templates** (`host/templates/systemd/`): `Exec*=` lines expand `$` and `%` themselves; write `$$` and `%%` for the shell. A `+` prefix runs that line as root despite `User=`.
- **Route 53** returns a wildcard record's name as `\052.homelab.swhurl.com.`; compare after mapping `\052` to `*`.
- **NetworkPolicy and HTTP-01:** cert-manager runs its challenge solver pods in the Ingress's namespace (port 8089); a policy selecting every pod there (`podSelector: {}`) blocks issuance with a 502. Select the workload's pods only.
- **OTel node metrics** on k3s use host networking and the kubelet at `127.0.0.1:10250`.
- **Reloader** only acts in namespaces listed in its HelmRelease; add the namespace before opting a workload in (`make app-new --secret-keys` does).
- **`nginx-unprivileged`** listens on IPv4 only with a read-only root; use `127.0.0.1` inside the pod.
- **Managed label domain** is `platform.swhurl.com/*`; throwaway test namespaces carry `platform.swhurl.com/<test>=true` and scripts refuse to touch namespaces without it.
- **Stack templates** (`swhurl-app-template-*`, Copier): Renovate cannot see versions inside `*.jinja` files, so keep versions in plain files (the Kotlin `build.gradle.kts` reads answers from `gradle.properties` instead). A new Copier question needs a line in the template's Template workflow matrix (from a third question on, defaults plus each choice alone plus all together; `make check-templates` covers every combination here). Docker mounts `--tmpfs /tmp` noexec, unlike podman: anything that unpacks and loads a native library from `/tmp` (the SQLite JDBC driver) passes locally and fails in CI.
- `showboat` is not installed globally; use `uvx showboat ...`.
