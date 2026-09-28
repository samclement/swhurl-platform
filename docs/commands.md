# Commands

Every `make` target, grouped by task. **Cluster** means the target reads or changes the live cluster; **Git** means it edits files for you to commit. `make help` prints a short list. Targets marked † accept `DRY_RUN=true` to print the plan (or run only read-only checks) without acting.

## Deploy and verify

| Target | Does | Touches |
| --- | --- | --- |
| `flux-reconcile` | Fetch Git, reconcile the source layer and the stack, wait | Cluster |
| `install` † | `verify-config`, `flux-reconcile`, `verify-platform` (skip verification with `FEAT_VERIFY=false`) | Cluster |
| `verify-config` | Check the required Secret files and `OAUTH_HOST` exist | Local |
| `verify-platform` | Every Flux unit Ready, HTTP→HTTPS redirect, ingestion key matches ClickStack (bytes, never printed), retention settings | Cluster (read) |
| `verify` | `verify-config` and `verify-platform` | Cluster (read) |
| `flux-bootstrap` | Apply the root units and sources in `clusters/home/flux-system` (Flux must already be installed) | Cluster |

## Apps

| Target | Does | Touches |
| --- | --- | --- |
| `app-new NAME=<app> ARGS="..."` | Generate an app instance ([apps](apps.md)) | Git |
| `app-status APP= ENV=` | Desired vs applied Git revision and image digest, replicas, route, certificate, failing containers | Cluster (read) |
| `app-logs APP= ENV=` | Recent workload logs (`FOLLOW=true`, `TAIL=N`) | Cluster (read) |
| `app-reconcile APP= ENV=` | Fetch Git and reconcile only that instance | Cluster |
| `app-check APP= ENV=` | Render one instance and check it against the app policy | Local |
| `app-policy` | Render every instance with Helm and check the app policy (CI runs this) | Local |

## Secrets and settings

| Target | Does | Touches |
| --- | --- | --- |
| `secrets-check` | Decrypt every tracked Secret in memory; fail on empty or `REPLACE_ME`, warn on probable double encoding. Needs the age key | Local |
| `runtime-inputs-sync` | Fetch Git and reconcile `homelab-auth`, `homelab-clickstack`, `homelab-otel` | Cluster |
| `runtime-inputs-refresh-otel` | `runtime-inputs-sync`, wait for `logging/hyperdx-secret`, restart collectors, `verify-platform`. Fallback: Reloader normally restarts them | Cluster |
| `otel-collectors-restart` | Restart both OTel collectors | Cluster |
| `platform-certs-staging`, `platform-certs-prod` † | Set `CERT_ISSUER` in `platform-settings` | Git |

## Lifecycle, backup and recovery

| Target | Does | Touches |
| --- | --- | --- |
| `suspend`, `resume` `TARGET=kustomization/<name>\|helmrelease/<ns>/<name>` † | `flux suspend/resume`; workloads and data untouched | Cluster |
| `destroy-data TARGET=pvc/<ns>/<name>\|pv/<name> CONFIRM=<TARGET>` † | Delete a released claim or volume and its host data ([lifecycle](operations.md#lifecycle)) | Cluster |
| `backup-clickstack-mongodb` † | Encrypted MongoDB dump to `~/.local/state/swhurl-platform/backups` (`BACKUP_DIR`), then prune to 7 daily + 4 weekly (`PRUNE=false` skips) | Cluster (read), local |
| `restore-test-clickstack-mongodb` † | Restore the latest backup into a throwaway namespace and check it (`BACKUP_FILE`, `AGE_KEY_FILE`, `KEEP=true`) | Cluster (throwaway) |
| `teardown`, `reinstall` † | Refuse to run: no whole-platform reset exists | — |

## Tests

| Target | Does | Touches |
| --- | --- | --- |
| `validate-repo` | Render every active Flux path, validate schemas, SOPS structure, shell syntax and doc links (CI runs this) | Local |
| `test-safety` | Offline unit tests: lifecycle guards, verifier, policies, generator, fixtures (CI runs this) | Local |
| `lifecycle-test` † | Prove lifecycle commands on a throwaway app | Cluster (throwaway) |
| `reloader-test` † | Prove Reloader's opt-in and namespace scope | Cluster (throwaway) |
| `app-template-test` † | Deploy the generated fixtures through Flux, check, remove | Cluster (throwaway) |

## Host and docs

| Target | Does | Touches |
| --- | --- | --- |
| `host-dns`, `host-dns-delete` † | Install or remove the Route53 dynamic DNS systemd timer (`DYNAMIC_DNS_RECORDS`, `AWS_ZONE_ID`, `AWS_PROFILE`) | Host |
| `charts-generate` | Render `docs/charts/c4/*.d2` to SVG (needs `d2`) | Git |

Settings read by the Makefile come from [`config.env`](../config.env): `DYNAMIC_DNS_RECORDS`, `FEAT_VERIFY`, `TIMEOUT_SECS` (runtime-input wait). Cluster settings live in Git, not here ([services](services.md#settings)).
