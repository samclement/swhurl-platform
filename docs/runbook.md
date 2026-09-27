# Platform Runbook (Flux-First)

This repo is operated through Flux GitOps with Makefile-first orchestration.

## Manual k3s prerequisite

Install k3s manually before Flux bootstrap. Keep packaged `traefik` and `metrics-server` enabled:

```bash
curl -sfL https://get.k3s.io | sudo INSTALL_K3S_EXEC="server" sh -
sudo cp /etc/rancher/k3s/k3s.yaml "$HOME/.kube/config"
sudo chown "$(id -u):$(id -g)" "$HOME/.kube/config"
chmod 600 "$HOME/.kube/config"
kubectl -n kube-system get deploy traefik metrics-server
```

## Standard Operations

Preferred day-to-day entrypoints:
- `make validate-repo` for local repository validation
- `make flux-reconcile` after committing and pushing Git changes
- `make verify-config` for local config contracts

The installed host's observed baseline and remaining live checks are in [current state](operations/current-state.md). `make teardown` and `make reinstall` are disabled. The current persistent volumes have `Delete` reclaim policy; the former root deletion could cascade into volume loss.

### Bootstrap

```bash
flux check --pre
flux install --namespace flux-system
kubectl -n flux-system create secret generic sops-age \
  --from-file=age.agekey=./age.agekey \
  --dry-run=client -o yaml | kubectl apply -f -
make flux-bootstrap
```

Behavior:
- Flux installation is manual (outside repo scripts).
- `make flux-bootstrap` applies `clusters/home/flux-system` bootstrap manifests only.
- If `homelab-infrastructure` fails with `no matches for kind "ClusterIssuer" in version "cert-manager.io/v1"`, install cert-manager CRDs once and rerun reconcile:

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.19.3/cert-manager.crds.yaml
make flux-reconcile
```

### Reconcile

```bash
make flux-reconcile
```

Behavior:
- Reconciles the repository source, bootstrap sources layer, and stack layer from Git; platform runtime SOPS Secrets are applied by `homelab-platform` inside the stack.
- Reconciles `swhurl-platform` source, `homelab-flux-sources`, then `homelab-flux-stack`.

### Full apply (`make install`)

```bash
make install
```

Flow:
1. `make verify-config` (when `FEAT_VERIFY=true`)
2. `make flux-reconcile`
3. `make verify-platform` (when `FEAT_VERIFY=true`)

The verifier decodes Kubernetes Secret data once, checks against the unique ClickStack team ingestion key, and never prints the values. A byte match does not prove collector delivery: check fresh telemetry after restarting collectors. It fails if MongoDB is unavailable or there is no unique team key.

### Lifecycle guard

`make teardown` and `make reinstall` exit nonzero before any cluster command. Their `DRY_RUN=true` variants report that they are disabled and exit successfully. There is no force bypass.

Routine updates use commit, push, and `make flux-reconcile`. The former teardown deleted root Flux Kustomizations, which could cascade through namespaces, Helm releases and PVs with `Delete` reclaim policy. It also removed the GitRepository required by install. Do not reproduce those deletions manually as a reset.

Suspend/resume, per-instance uninstall and explicit data destruction remain PR02 work. They require the ownership map in [architecture](architecture.md#current-reconciliation-ownership), disposable-scope deletion checks and the stateful restore in [Recovery](#recovery) before live handover.

## Recovery

What must survive a host loss, and where it is:

| Data | Class | Recovery source |
| --- | --- | --- |
| Manifests and SOPS Secrets | Irreplaceable | GitHub (`origin`) |
| age private key (`age.agekey`) | Irreplaceable | Encrypted off-host copy (P0b); location kept outside Git |
| ClickStack MongoDB `hyperdx` (team, ingestion key, user, sources, connection) | Irreplaceable config | `make backup-clickstack-mongodb` archives |
| ClickHouse telemetry | Expendable | Not backed up |
| MinIO (`storage/minio`) | Expendable today (no buckets) | Reclassify before storing data in it |

Back up ClickStack MongoDB:

```bash
make backup-clickstack-mongodb
```

This streams `mongodump --db hyperdx --archive --gzip` through `age` to the recipient in `.sops.yaml`, so the dump is never written in plaintext. It writes `clickstack-mongodb-<UTC>.archive.gz.age` and a `.json` metadata file (checksum, MongoDB version, collection counts) to `~/.local/state/swhurl-platform/backups` (override with `BACKUP_DIR`). **Backups are local only**: copy both files off-host yourself. There is no schedule yet.

Prove a backup restores:

```bash
make restore-test-clickstack-mongodb
```

This creates the disposable namespace `recovery-test` (labelled `platform.swhurl.com/recovery-test=true`), starts a throwaway MongoDB of the backed-up version, decrypts and restores the latest archive with `age.agekey` (override with `AGE_KEY_FILE`, `BACKUP_FILE`), and restores `hyperdx-secret` from Git with the same key. It passes only if the archive checksum, restored collection counts and the restored team ingestion key all match. It deletes the namespace afterwards (`KEEP=true` keeps it), never touches live workloads and prints no key material. It refuses to reuse an existing `recovery-test` namespace.

To restore into the live service after data loss, restore with `mongorestore --archive --gzip --drop` into `observability/clickstack-mongodb` using the same decrypt pipe, then restart `observability/clickstack-app`. This live path has not been exercised.

## Host Dynamic DNS (Optional)

Host DNS automation is a standalone entrypoint:

```bash
make host-dns
make host-dns-delete
```

Direct script usage:

```bash
./host/dynamic-dns.sh [--dry-run] [--delete]
```

## Active Flux Dependency Chain

Parent level:
- `homelab-flux-sources -> homelab-flux-stack`

Cluster level (`clusters/home/*.yaml`):
- `homelab-infrastructure -> homelab-platform -> homelab-tenants -> homelab-app-example`

Layer composition:
- `homelab-infrastructure` points to `infrastructure/overlays/home`.
- `homelab-platform` points to `platform-services/overlays/home`.
- `homelab-tenants` points to `tenants/app-envs` (tenant env namespaces only).
- `homelab-app-example` points to `tenants/apps/example` (sample app staging+prod overlays).
- Platform cert issuer intent is post-build substitution from `flux-system/platform-settings` (`CERT_ISSUER`).

## Runtime Inputs

Runtime Secret manifests are Git-managed, SOPS-encrypted, and co-located with the platform service that consumes them:
- `platform-services/oauth2-proxy/base/secret-oauth2-proxy-shared.sops.yaml` -> `ingress/oauth2-proxy-shared-secret`
- `platform-services/clickstack/base/secret-clickstack-runtime-inputs.sops.yaml` -> `observability/clickstack-runtime-inputs`
- `platform-services/otel/base/secret-hyperdx.sops.yaml` -> `logging/hyperdx-secret`

Non-secret runtime settings are Git-managed in:
- `clusters/home/flux-system/sources/configmap-platform-settings.yaml` (`CERT_ISSUER`, `OAUTH_HOST`)

SOPS key material:
- There is no SOPS password to remember.
- Local edits require the age private key matching `.sops.yaml`, either in `~/.config/sops/age/keys.txt` or via `SOPS_AGE_KEY_FILE=./age.agekey`.
- Flux decrypts in-cluster with the same private key material stored in `flux-system/sops-age`.
- Keep `age.agekey` backed up and out of Git.
- The `age:` value in `.sops.yaml` is the public recipient derived from the private key; generate a new key with `age-keygen -o age.agekey` or print the public recipient for an existing key with `age-keygen -y age.agekey`.

Runtime input value origins:
- The `ENC[...]` values in `platform-services/*/base/*.sops.yaml` are generated by SOPS when saving plaintext edits.
- oauth2-proxy `client-id` and `client-secret` come from the configured OAuth/OIDC provider app.
- oauth2-proxy `cookie-secret` is locally generated, for example with `openssl rand -base64 32`.
- `CLICKSTACK_API_KEY` is stored in `platform-services/clickstack/base/secret-clickstack-runtime-inputs.sops.yaml`; this is the ClickStack chart bootstrap/app API key passed into `hyperdx.apiKey`.
- The ClickStack chart renders that same value into `observability/clickstack-app-secrets.api-key` for `clickstack-app` and the ClickStack-managed internal collector.
- `HYPERDX_API_KEY` is stored in `platform-services/otel/base/secret-hyperdx.sops.yaml`; this is the external OTel collector ingestion key copied from the ClickStack admin UI after first-login setup.
- `CLICKSTACK_API_KEY` and `HYPERDX_API_KEY` may be temporarily equal during bootstrap if seeded that way, but the expected steady state is separate values.

Secret boundary:
- Keep shared platform runtime Secrets next to the platform service that consumes them.
- Keep app-only secrets with the app (`tenants/apps/<app>/.../secret-*.sops.yaml`).
- App onboarding example: `docs/runbooks/onboard-app-with-sops-secrets.md`

After editing encrypted runtime inputs in Git:

```bash
git add platform-services/*/base/*.sops.yaml
git commit -m "runtime-inputs: update platform secrets"
git push
make runtime-inputs-sync
```

Note:
- `logging/hyperdx-secret` value changes do not hot-reload into already-running `otel-k8s-*` collectors because `secretKeyRef` env vars are read at container start.
- `make runtime-inputs-refresh-otel` now waits for `hyperdx-secret` propagation before collector restart to avoid stale-token rollouts.
- For ClickStack key rotations, prefer:

```bash
make runtime-inputs-refresh-otel
```

## ClickStack First-Login and OTel Ingestion Key

The initial ClickStack install uses `CLICKSTACK_API_KEY` as the chart/app API key. After the UI is available, create the separate ingestion key used by the standalone OTel collectors:

1. Open `https://clickstack.homelab.swhurl.com` and complete first team/user setup.
2. In ClickStack admin, create or copy the Ingestion API key for OTel collectors.
3. Copy the ingestion key.
4. Update the Git-managed OTel SOPS target Secret:

```bash
SOPS_AGE_KEY_FILE=./age.agekey sops platform-services/otel/base/secret-hyperdx.sops.yaml
```

Set `data.HYPERDX_API_KEY` to the new value and save.

5. Commit, push, and apply:

```bash
git add platform-services/otel/base/secret-hyperdx.sops.yaml
git commit -m "runtime-inputs: rotate clickstack ingestion key"
git push
make runtime-inputs-refresh-otel
```

6. Verify the rendered Secret exists without printing the secret value:

```bash
dst="$(kubectl -n logging get secret hyperdx-secret -o jsonpath='{.data.HYPERDX_API_KEY}')"
test -n "$dst" && echo "OK: ingestion key is present in logging/hyperdx-secret"
```

## Gotchas

1. k3s prerequisite: use default networking (`flannel`) with packaged `traefik` + `metrics-server` enabled.
2. Runtime inputs are Git-managed via SOPS: commit + push encrypted changes before `make runtime-inputs-sync` (or `make flux-reconcile`).
3. DNS wildcard scope: `*.homelab.swhurl.com` matches one-label hosts only; multi-label names need explicit records (or deeper wildcard). Add explicit hosts to `DYNAMIC_DNS_RECORDS` in `config.env`.
4. Dynamic DNS timer config: after changing `DYNAMIC_DNS_RECORDS`, `AWS_ZONE_ID`, or `AWS_PROFILE`, rerun `make host-dns` so `/etc/swhurl-platform/dynamic-dns.env` is regenerated for systemd.
5. cert-manager issuance timing: first reconcile can fail until DNS propagates and ACME HTTP-01 checks can reach ingress.
6. ClickStack ingestion timing: OTLP ingestion is not fully active until initial team setup completes in UI.
7. OTel collector key reload: after key rotation, restart collectors (or use `make runtime-inputs-refresh-otel`) because `secretKeyRef` env values do not hot-reload in running pods.

## Verification

Core checks:
- `make validate-repo` (cluster-free shell, Secret format, active render path, substitution and schema checks; see [Infrastructure](INFRASTRUCTURE.md#repository-validation) for pinned prerequisites)
- `make verify-config` (config inputs)
- `make verify-platform` (Flux kustomization health + OTel ingestion Secret presence)
- `make verify` (both)

Architecture chart generation:
- C4 source files: `docs/charts/c4/*.d2`
- Render command: `make charts-generate`
- Output path: `docs/charts/c4/rendered/*.svg`

## Promotion / Profiles

- Infrastructure/platform cert issuer mode is Git-managed in:
  - `clusters/home/flux-system/sources/configmap-platform-settings.yaml`
  - `CERT_ISSUER=letsencrypt-staging|letsencrypt-prod`
- Sample app path is fixed via `clusters/home/app-example.yaml`:
  - `./tenants/apps/example`
- Example app staging/prod overlays both use `letsencrypt-prod`.
- Provider selection is controlled by composition entries in `infrastructure/overlays/home/kustomization.yaml`.

## Native k3s Defaults

Active `home` composition assumes:
- k3s default CNI (`flannel`)
- k3s packaged `traefik`
- k3s packaged `metrics-server`
- Traefik NodePorts are pinned declaratively through k3s `HelmChartConfig` in `infrastructure/ingress-traefik/base/helmchartconfig-traefik.yaml`:
  - HTTP `80 -> 31514`
  - HTTPS `443 -> 30313`

Legacy provider manifests were removed from this repo; `infrastructure/overlays/home` now targets only active paths.
