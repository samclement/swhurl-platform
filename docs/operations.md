# Operations

Day-to-day work on the live cluster. Every `make` target is listed in [commands](commands.md). Export `KUBECONFIG=$HOME/.kube/config` first: on this host `kubectl` is the k3s wrapper and non-interactive shells otherwise read `/etc/rancher/k3s/k3s.yaml`.

## Check health

```bash
make verify-platform              # every Flux unit Ready, HTTPS redirect, ingestion key, retention
flux get kustomizations           # one line per Flux unit
make app-status APP=hello ENV=prod
```

A unit showing `DependencyNotReady` is waiting for another unit; fix that one first ([dependencies](architecture.md#flux-units)).

## Secrets

Each Secret is a SOPS-encrypted Kubernetes Secret beside its consumer: platform Secrets in `platform-services/<service>/base/*.sops.yaml`, app Secrets in `tenants/apps/<app>/<env>/secret.sops.yaml`. SOPS encrypts only `data`/`stringData`, and the consuming Flux unit decrypts in-cluster with `flux-system/sops-age`. There is no password; you need the age private key (`SOPS_AGE_KEY_FILE=./age.agekey`).

Rules:

- **Write new values as `stringData`** (plain text inside the encrypted file). Flux applies it correctly; generated app stubs use it.
- **In `data`, base64-encode exactly once.** A doubly encoded ingestion key silently dropped all telemetry once. The existing platform Secrets use `data`; do not convert them in passing, because a changed value restarts consumers.
- **Never print values.** Check with `make secrets-check` (decrypts in memory; fails on empty or `REPLACE_ME` values, warns on probable double encoding) and `make verify-platform` (compares the ingestion key by bytes).

Rotate a value:

```bash
sops platform-services/oauth2-proxy/base/secret-oauth2-proxy-shared.sops.yaml
make secrets-check
git commit -am "secrets: rotate oauth2-proxy client secret" && git push
make runtime-inputs-sync          # or make flux-reconcile / make app-reconcile
```

Reloader then restarts the workloads that opted in: oauth2-proxy, both OTel collectors, and generated apps with Secrets. Anything else that reads the Secret needs a manual `kubectl rollout restart`. For the collectors, `make runtime-inputs-refresh-otel` does the restart itself and then verifies. Which Secret belongs to which service: [services](services.md).

## Certificate mode

`CERT_ISSUER` in `platform-settings` selects the Let's Encrypt issuer for platform hosts (sign-in, ClickStack, MinIO). Apps choose their own issuer.

```bash
make platform-certs-staging       # or platform-certs-prod; edits the file only
git commit -am "platform: CERT_ISSUER=letsencrypt-staging" && git push && make flux-reconcile
kubectl get ingress -A -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,ISSUER:.metadata.annotations.cert-manager\\.io/cluster-issuer
```

## Lifecycle

Deploy, update and uninstall through Git. Removing an app instance's unit from `clusters/home/kustomization.yaml` uninstalls it; namespaces and claims annotated `kustomize.toolkit.fluxcd.io/prune: disabled` stay. For what Git cannot express:

| Command | Effect | Data |
| --- | --- | --- |
| `make suspend TARGET=kustomization/<name>` | Stop applying Git changes to a unit | Kept; workloads keep running |
| `make suspend TARGET=helmrelease/<ns>/<name>` | Freeze one Helm release (suspending a unit does not) | Kept |
| `make resume TARGET=...` | Reconcile from Git again | Kept |
| `make destroy-data TARGET=pvc/<ns>/<name> CONFIRM=pvc/<ns>/<name>` | Delete an unused claim, its volume and host directory | **Destroyed** |
| `make destroy-data TARGET=pv/<name> CONFIRM=pv/<name>` | Delete a `Released` retained volume and its directory | **Destroyed** |

`destroy-data` refuses while a pod mounts the claim or a Helm release or Flux unit still manages it, because they would recreate it. `DRY_RUN=true` runs only the checks.

Deleting a shared Flux unit by mistake is safe: shared units use `deletionPolicy: Orphan`, so their resources keep running unmanaged. Re-create a deleted root unit with `make flux-bootstrap`. There is no whole-platform reset; `make teardown` and `make reinstall` refuse to run.

## Backups and recovery

| Data | Class | Where it survives |
| --- | --- | --- |
| Manifests and encrypted Secrets | Irreplaceable | GitHub |
| age private key | Irreplaceable | Encrypted off-host copy (location kept outside Git) |
| ClickStack MongoDB (team, ingestion key, users, sources) | Irreplaceable | `make backup-clickstack-mongodb`; volume `Retain`, claim kept on Helm uninstall |
| ClickHouse telemetry | Expendable | Expires after 30 days; ClickHouse's own logs after 7 |
| MinIO | Expendable today (no buckets) | Reclassify before storing data |

```bash
make backup-clickstack-mongodb         # encrypted dump to ~/.local/state/swhurl-platform/backups
make restore-test-clickstack-mongodb   # restores the latest into a throwaway namespace and checks it
```

The backup streams `mongodump` through `age`, so no plaintext touches disk, and writes an archive plus metadata (checksum, version, counts). Each run keeps the newest backup for each of the last 7 days that have one and each of the last 4 weeks (`KEEP_DAILY`, `KEEP_WEEKLY`, `PRUNE=false`). **Backups are manual and stay on this host** until an off-host destination is chosen: copy the directory to the USB drive regularly.

The restore test passes only if the checksum, collection counts and restored ingestion key (against the Git Secret) all match; it never touches live workloads.

The MongoDB volume's `Retain` policy is a live patch; a re-created claim loses it and `make verify-platform` then fails. Re-apply with the two commands in [bootstrap step 6](bootstrap.md#6-protect-data-then-verify).

Restore into the live service (not yet exercised): stop `observability/clickstack-app`, pipe `age -d -i age.agekey <archive>` into `kubectl -n observability exec -i deploy/clickstack-mongodb -- mongorestore --archive --gzip --drop`, then start the app and run `make verify-platform`.

## Prove behaviour on the cluster

Each of these creates throwaway resources, checks them and removes them:

| Command | Proves |
| --- | --- |
| `make lifecycle-test` | Suspend/resume, uninstall keeping protected data, `destroy-data`, `Orphan` deletion |
| `make reloader-test` | Reloader restarts only opted-in workloads in watched namespaces |
| `make app-template-test` | Generated apps deploy: worker without route, signed-in web app with a decrypted Secret, persistent prod app |
| `make restore-test-clickstack-mongodb` | The latest backup restores |

Fixtures come from the pushed Git revision: push changes to `tests/fixtures/` first.

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| 403 after signing in, only on `http://` | No HTTP→HTTPS redirect, so the `Secure` sign-in cookie is not sent. Check `make verify-platform`'s Ingress line; the redirect is `ports.web.redirections` in `helmchartconfig-traefik.yaml` (the old `redirectTo` key is ignored by Traefik 3). |
| `redirect_uri_mismatch` from Google | The OAuth client's allowed redirect URI differs from `https://oauth.<BASE_DOMAIN>/oauth2/callback`. |
| Collector logs show HTTP 401 `scheme or token does not match` | `HYPERDX_API_KEY` differs from the ClickStack team key or is encoded twice. `make verify-platform` says which; fix the Secret ([Secrets](#secrets)). |
| A signed-in route shows Traefik's default certificate for a few seconds | Normal while cert-manager issues a certificate for a host that just moved. |
| `flux reconcile` seems ignored | A previous revision is still running health checks (up to the unit's timeout); new requests queue behind it. |
| Deleting a broken app takes about 5 minutes | Its Helm install is still in progress; the finalizer waits for the Helm timeout. |
| A certificate stays not Ready on a new host | DNS has not propagated or Let's Encrypt cannot reach port 80; check `kubectl get challenges -A` and the router forward. |
| A new host doesn't resolve | Multi-label names are not covered by the wildcard; add them to `DYNAMIC_DNS_RECORDS` and re-run `make host-dns`. |
| ClickHouse has `*_log_0` tables after a restart | ClickHouse renamed system log tables whose definition changed. Check, then drop them. |
