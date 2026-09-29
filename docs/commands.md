# Commands

Every `make` target, grouped by task. **Cluster** means the target reads or changes the live cluster; **Git** means it edits files for you to commit. `help` prints the same list from the `##` comment on each target in the Makefile; a test fails if a target is missing from this page. Targets marked † accept `DRY_RUN=true` to print the plan (or run only read-only checks) without acting.

## Deploy and verify

| Target | Does | Touches |
| --- | --- | --- |
| `flux-reconcile` | Fetch Git, reconcile the source layer and the stack, wait | Cluster |
| `install` † | `check-config`, `flux-reconcile`, `verify-platform` (`SKIP_VERIFY=1` skips the checks) | Cluster |
| `verify-platform` | Every Flux unit Ready, HTTP→HTTPS redirect, ingestion key matches ClickStack (bytes, never printed), retention settings | Cluster (read) |
| `clickstack-bootstrap` † | After a ClickStack install: register the admin from SOPS if no team exists, set the team ingestion key to `CLICKSTACK_INGESTION_KEY`; idempotent, never prints values ([services](services.md#clickstack-and-otel)) | Cluster |
| `reconcile UNIT=<name>` | Fetch Git and reconcile one Flux unit, for example after changing its Secret | Cluster |
| `flux-bootstrap` | Apply the root units and sources in `clusters/home/flux-system` (Flux must already be installed) | Cluster |

## Apps

| Target | Does | Touches |
| --- | --- | --- |
| `app-new NAME=<app> ARGS="..."` | Generate an app instance and check it against the app policy ([apps](apps.md)) | Git |
| `app-status APP= ENV=` | Desired vs applied Git revision and image digest, replicas, route, certificate, failing containers | Cluster (read) |
| `app-logs APP= ENV=` | Recent workload logs (`FOLLOW=true`, `TAIL=N`) | Cluster (read) |
| `app-reconcile APP= ENV=` | Fetch Git and reconcile only that instance | Cluster |
| `app-check APP= ENV=` | Render one instance and check it against the app policy | Local |

## Settings

| Target | Does | Touches |
| --- | --- | --- |
| `platform-certs-staging`, `platform-certs-prod` † | Set `CERT_ISSUER` in `platform-settings`: only issuers defined in Git, one line changed, comments kept | Git |

## Lifecycle, backup and recovery

| Target | Does | Touches |
| --- | --- | --- |
| `suspend`, `resume` `TARGET=kustomization/<name>\|helmrelease/<ns>/<name>` † | `flux suspend/resume`; workloads and data untouched | Cluster |
| `destroy-data TARGET=pvc/<ns>/<name>\|pv/<name> CONFIRM=<TARGET>` † | Delete a released claim or volume and its host data ([lifecycle](operations.md#lifecycle)) | Cluster |
| `backup-mongodb` † | Encrypted MongoDB dump to `~/.local/state/swhurl-platform/backups` (`BACKUP_DIR`), then prune to 7 daily + 4 weekly (`PRUNE=false` skips) | Cluster (read), local |

## Offline checks

Verbs: `check-*` never touch the cluster, `verify-*` read the live cluster, `live-test-*` create and remove throwaway resources on it.

| Target | Does | Touches |
| --- | --- | --- |
| `check` | `check-repo`, `test`, `check-apps`, `check-lint`: everything CI runs. Run before every push | Local |
| `check-repo` | Render every active Flux path, validate schemas, SOPS structure, shell syntax and doc links | Local |
| `test` | Unit tests: lifecycle guards, verifier, policies, generator, fixtures, Flux unit rules | Local |
| `check-apps` | Render every app instance with Helm and check the app policy | Local |
| `check-lint` | Ruff on the Python tooling and ShellCheck on the remaining bash (needs `uv`) | Local |
| `check-config` | The required Secret files and the `BASE_DOMAIN` and `CERT_ISSUER` settings exist | Local |
| `check-secrets` | Decrypt every tracked Secret in memory; fail on empty or `REPLACE_ME`, warn on probable double encoding. Needs the age key, so CI cannot run it | Local |

## Live tests

| Target | Does | Touches |
| --- | --- | --- |
| `live-test-lifecycle` † | Prove lifecycle commands on a throwaway app | Cluster (throwaway) |
| `live-test-reloader` † | Prove Reloader's opt-in and namespace scope | Cluster (throwaway) |
| `live-test-app-template` † | Deploy the generated fixtures through Flux, check, remove | Cluster (throwaway) |
| `live-test-restore-mongodb` † | Restore the latest backup into a throwaway namespace and check it (`BACKUP_FILE`, `AGE_KEY_FILE`, `KEEP=true`) | Cluster (throwaway) |

## Host

| Target | Does | Touches |
| --- | --- | --- |
| `host-dns`, `host-dns-delete` † | Install or remove the Route53 dynamic DNS systemd timer (`DYNAMIC_DNS_RECORDS`, `AWS_ZONE_ID`, `AWS_PROFILE`) | Host |

Environment variables the targets read: `DRY_RUN`, `SKIP_VERIFY`, and the per-target ones in the tables. Host DNS records are in [`host/dns.env`](../host/dns.env). Cluster settings live in Git, not here ([services](services.md#settings)).
