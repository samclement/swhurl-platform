# Current state

Observed on 27 September 2026 from the local host and read-only Kubernetes queries. The original cluster inventory was taken at repository revision `91e889353f656577b48efc6f85d9f1b4427852f7`; PR01 was subsequently committed to main as `2bae8d0` and its CI passed. Recheck the applied revision before changing ownership, storage or routing. This is an observation, not a backup or restore record.

## Host and cluster

- Arch Linux x86-64; `k3s` systemd service active and enabled.
- One Ready control-plane node, `arch` at `192.168.1.200`, running k3s `v1.34.4+k3s1` and containerd `2.1.5-k3s1`.
- Host has 31 GiB RAM, about 23 GiB available at inspection. The root filesystem is 239 GiB, about 169 GiB free.
- Flux CLI and controller label report `v2.8.1`. All six Flux Kustomizations were Ready at `main@sha1:91e88935`.
- Helm releases Ready: cert-manager `v1.19.3`, oauth2-proxy-shared `10.1.3`, ClickStack `1.1.1`, both OTel collectors `0.145.0`, and MinIO `5.4.0`.

## Ingress, identity and storage

- Packaged k3s Traefik and metrics-server Deployments were Ready. Traefik exposed HTTP NodePort `31514` and HTTPS NodePort `30313`.
- Three ClusterIssuers and six existing Certificates were Ready. Ingresses exist for staging and production hello-web, shared oauth2-proxy, ClickStack, MinIO, and its console.
- Default StorageClass is `local-path` with reclaim policy `Delete`. Four bound claims/PVs use it: ClickHouse data (20 GiB), ClickHouse logs (5 GiB), MongoDB (10 GiB), and MinIO (20 GiB). Their PV reclaim policies are also `Delete`.
- `flux-system/sops-age` exists, and a local `age.agekey` file exists. Their presence does not establish that either key is backed up.
- No Kubernetes CronJobs were found. NetworkPolicies were present only in `flux-system`. Neither result proves the absence of host-managed backups or network enforcement outside Kubernetes.

## Repository checks

- Active render entrypoints, individual tracked shell syntax checks, `make verify-config`, and install/teardown dry-runs passed during the PR 01 review.
- The prior CI workflow referenced deleted `platform-services/runtime-inputs`. PR 01 replaces its path list with discovery from active Flux `spec.path` values plus the two bootstrap paths.
- `scripts/verify-platform.sh` can print plaintext key values after a mismatch and currently decodes the OTel ingestion key twice. Avoid shared logs from that command or a normal `make install` until it is fixed.

## Immediate follow-up findings

These were the findings at observation time. The P0 repairs below are fixed in Git; live confirmation is noted per item.


- `make teardown` deletes `homelab-flux-stack` and `homelab-flux-sources`; both roots and their children use pruning. This can cascade through namespaces and Helm releases. The four current PVs have reclaim policy `Delete`, Removing the GitRepository also left the reinstall sequence unable to reconcile its first source without bootstrap. **Fixed (P0a):** both targets are now disabled and fail before any cluster command.
- Shared oauth2-proxy currently permits `email-domain: "*"` and sets its cookie domain to `.homelab.swhurl.com`. **Fixed in Git (P0d):** sign-in is restricted to an authenticated-emails list (`sam@swhurl.com`) and the chart default `email_domains = ["*"]` is overridden; deployed on 27 September 2026. The approved account signs in (proxy `AuthSuccess`); the operator reported a non-approved account was refused. The proxy logs recorded no callback for that attempt, so the refusal may have come from Google (for example, OAuth consent-screen test-user limits) rather than the email list. The proxy-side list is enforced by rendered chart arguments and `make test-safety`. A separate domain for public or untrusted apps is still required before expanding exposure.
- A read-only byte comparison found `logging/hyperdx-secret.HYPERDX_API_KEY` is 48 bytes after one Kubernetes `.data` decode. Decoding those bytes again yields 36 bytes matching ClickStack's live team ingestion key. Recent logs from both OTel collector workloads contain repeated HTTP 401 token/scheme failures. This confirms a live ingestion mismatch. **Fixed in Git (P0c):** the SOPS source now holds exactly one base64 layer and the verifier decodes once without printing values; verified live on 27 September 2026: after `make runtime-inputs-refresh-otel`, collector logs show no 401 errors, ClickHouse receives fresh logs and metrics, and `make verify-platform` passes. No key values were printed or saved in this document.

## Recovery (PR08a)

Checked 27 September 2026:

- No host backup jobs exist (only the dynamic DNS and keyring timers). k3s runs without `/etc/rancher/k3s/config.yaml`; its datastore was not inspected (needs root).
- All four PVs are `local-path` on the root disk (`/var/lib/rancher/k3s/storage`) with `Delete` reclaim.
- Classification: ClickStack MongoDB `hyperdx` is irreplaceable configuration (1 team holding the ingestion key, 1 user, 1 connection, 4 sources; other collections empty). ClickHouse is expendable telemetry. MinIO holds no buckets, so it is currently expendable.
- `make backup-clickstack-mongodb` produced an age-encrypted archive, and `make restore-test-clickstack-mongodb` restored it into a disposable namespace: checksum, collection counts, and the restored team ingestion key matched the restored Git Secret. Negative checks (altered counts, corrupted archive) failed as expected. Live workloads were unchanged.
- Not yet done: an off-host destination (backups are manual and local only by decision; each run prunes to 7 daily + 4 weekly), a backup schedule, restore on a separate machine, and running HyperDX against restored data.
- ClickHouse system log tables had no TTL and held about 18 GiB since March 2026 (`trace_log` 6.3 GiB, `metric_log` and `query_log` 3.4 GiB each); local-path does not enforce the 20 Gi claim.
- PR02a applied 27 September 2026 at `28723fe`: after the ClickHouse restart all ten system log tables carry a 7-day TTL; the renamed `*_0` tables (about 18 GiB) were dropped, and root-disk use fell from 58 GiB to 40 GiB. The MongoDB PV (`pvc-cf0665d9-…`) was patched to `Retain`; all three ClickStack PVCs carry `helm.sh/resource-policy: keep`; `local-path-retain` exists. `make verify-platform` passes, including the new retention checks, and telemetry kept flowing with no collector export errors.

## Lifecycle (PR02b)

Applied 27 September 2026 at `d3658b4`. The five shared Flux units report `deletionPolicy: Orphan` (the two roots were re-applied with `kubectl apply -f clusters/home/flux-system/kustomizations.yaml`); `homelab-app-example` keeps `MirrorPrune`; `observability` carries `kustomize.toolkit.fluxcd.io/prune: disabled`. All units returned to Ready.

`make lifecycle-test` passed on the disposable fixture: suspend kept the workload running and resume reconciled; deleting the app unit pruned the workload but kept the prune-protected namespace, claim and data; `destroy-data` refused without `CONFIRM`, then removed the claim and PV, and the local-path provisioner logged deletion of the host directory; deleting an `Orphan` unit left its workload running. A first run caught `destroy-data` correctly refusing while the uninstalled pod was still terminating; the test now waits for it. A dry-run `destroy-data` against `observability/clickstack-mongodb` refuses because the claim is mounted. Deleting a real shared unit was not exercised.

## PR03 capability split

Cut over 27 September 2026 at `2f50de1`. `homelab-infrastructure` and `homelab-platform` (both `Orphan`, suspended first) were replaced by `homelab-cluster-base`, `-cert-manager`, `-issuers`, `-traefik`, `-minio`, `-auth`, `-clickstack` and `-otel`. Before the cutover, the new unit paths rendered exactly the old units' 22 inventory entries with no resource in two units.

Before/after comparison: the six Helm release revisions, 13 platform/app pod UIDs, 4 PVs and 13 namespaces were identical (nothing upgraded, uninstalled, restarted or recreated); the only inventory change was the unit objects themselves. Resources now carry their new owner (for example `observability/clickstack` → `homelab-clickstack`, `observability` namespace → `homelab-cluster-base`). All 12 units were Ready; `make verify-platform` passed; `hello` still redirected to sign-in, ClickStack served, and telemetry kept arriving. The unused `infrastructure/overlays/home` and `platform-services/overlays/home` were deleted in a follow-up commit.

Not exercised live: a fresh bootstrap (issuer ordering is by construction and checked by `make test-safety`) and an app deploy during a real ClickStack/MinIO outage (the example app has no dependency path to them).

## Reloader (PR07a)

Deployed 28 September 2026 at `9c719fe` as `platform-system/reloader` (chart 2.2.17). It logs watching only `ingress`, `logging` and its own `platform-system`; no ClusterRole or ClusterRoleBinding exists. Adding the opt-in annotations upgraded the oauth2-proxy and OTel releases without restarting any pod.

- `make reloader-test` passed: an opted-in workload in `logging` restarted after its Secret changed; an identical unannotated workload and an opted-in workload in an unwatched namespace did not.
- Real Secret check without changing credentials: adding and then removing a dummy key on `logging/hyperdx-secret` made Reloader restart both OTel collectors (logged for each change); `oauth2-proxy-shared` was not restarted. Afterwards the Secret held only `HYPERDX_API_KEY`, collectors showed no 401/export errors, ClickHouse received fresh logs, `homelab-otel` reconciled cleanly and `make verify-platform` passed.
- Not exercised: a real credential rotation of `oauth2-proxy-shared-secret` or the ingestion key. `make runtime-inputs-refresh-otel` remains as a fallback.

## App contract (PR04)

Shipped 28 September 2026 at `21d4e50`: HelmRepository `bjw-s` (Ready), `make app-new`, `make app-policy` and three generated fixtures. CI ran all 22 tests with Helm (none skipped) and `make app-policy` passed for the 3 fixtures.

`make app-template-test` passed on the cluster: all three fixture units reached Ready through Flux; the worker had no Service or Ingress; the web app's SOPS Secret was decrypted by its own app unit and injected into the container; the web pod ran non-root without a service-account token; `https://smoke-web.homelab.swhurl.com` redirected to Google sign-in; the prod fixture's claim bound on `local-path-retain`, data was written, and its image was digest-pinned. Cleanup removed all fixture units, namespaces and PVs.

Not yet exercised: a public-exposure instance on a domain outside `homelab.swhurl.com`, and Reloader restarting a generated app (fixtures skip the Reloader watch-list edit; Reloader itself is covered by `make reloader-test`). The example app is still raw manifests until PR05.

## Example app migration (PR05)

28 September 2026, commits `0c219bf` → `2f6eb31`:

1. `hello-staging` and `hello-prod` were generated (unprivileged nginx 1.27 pinned by digest, UID 101, port 8080, no service-account token, read-only root) and deployed beside the old example on `staging-hello-next` / `hello-next`. Both redirected to sign-in and served a page byte-identical (SHA-256) to the old `nginx:1.25` page.
2. Staging cutover (`1b8ae28`): one commit pruned the old staging resources and moved `staging-hello` to the new instance. A 2-second probe loop saw one failed request (Traefik default certificate) at 07:35:39 between the old and new Let's Encrypt certificates; all other requests got the sign-in redirect.
3. Production cutover (`9b47d28`): the same for `hello`; two failed probes (about 4 s) at 07:40:13.
4. Retirement (`2f6eb31`): `homelab-app-example` and `homelab-tenants` removed; the orphaned `apps-staging`/`apps-prod` namespaces, which held only the old certificate Secrets, were deleted by hand.

Isolation check: a throwaway instance with a non-existent image tag stayed failing (`make app-status` reported `ImagePullBackOff … not found`) while both `hello` units reconciled the latest revision, stayed 1/1 ready and kept serving. Deleting that broken instance waited for its in-flight Helm install to hit the 5-minute timeout before the HelmRelease finalizer released the namespace.

Not exercised: a real signed-in browser session on the new instances after cutover (the redirect to sign-in was checked, not the page behind it).

## HTTP to HTTPS redirect

Found 28 September 2026: plain-HTTP requests were never redirected (signing in over `http://` returned 403 because oauth2-proxy's cookies are `Secure`). k3s runs Traefik 3.6.7 / chart 38.0.2, which ignores the `ports.web.redirectTo` key added in May. Fixed at `094d21a` with `ports.web.redirections.entryPoint`; Traefik now renders `--entryPoints.web.http.redirections.entryPoint.{to=:443,scheme=https,permanent=true}`. `http://hello`, `http://staging-hello` and `http://clickstack` return 301 to the same HTTPS URL with path and query kept. A throwaway `letsencrypt-staging` Certificate for `acme-check.homelab.swhurl.com` was issued through the redirect (HTTP-01 still works). `make verify-platform` now checks the redirect.

## Secret conventions (PR07b)

28 September 2026: `make secrets-check` decrypted all four tracked SOPS Secrets locally with no errors and one warning: `observability/clickstack-runtime-inputs.CLICKSTACK_API_KEY` is double base64-encoded. In-memory comparison (no values printed) showed the live `clickstack-app-secrets.api-key` equals the once-decoded 48-byte text and that the MongoDB team key equals neither form, so the bootstrap key is independent of ingestion and nothing is currently broken. Left unchanged pending a planned ClickStack restart. The other Secrets are single-encoded (`HYPERDX_API_KEY` 36 bytes; oauth2-proxy keys) or `stringData` (fixture).

## Operator tooling in Python (phases 0–3)

28 September 2026: `make verify-platform`, `make verify-config` and `make app-status|app-logs|app-reconcile|app-check` now run from the `tools/swhurl` package; the bash scripts were deleted. On the live cluster the Python output was byte-identical to the bash output for `verify-platform`, `app-status` (both `hello` instances) and `app-logs`, with exit codes matching. Failure paths are covered offline: 18 verifier failure cases and the app commands via `FakeRunner`, plus the end-to-end secrecy contract test through fake `kubectl`; deliberately broken versions of the verifier (accepting a doubly encoded key, ignoring not-Ready units, printing the key) each failed the tests.

Phase 2 (same day): `platform-certs-*` now edits `platform-settings` through `swhurl platform-certs` (only issuers defined in Git; the one line is changed and the file re-parsed before writing) and `wait-runtime-inputs-otel` through `swhurl wait-secret-key`. Their messages matched the old recipes; a staging/prod round trip left the file byte-identical to Git. `install`, the teardown refusal, the collector restart and `host-dns` were rewritten as one- or two-line recipes with identical dry-run and refusal output; the Makefile has no shell loops, `if` blocks, `sed`, `grep` or `jq` left. Live: `make install` ran verify-config → flux-reconcile → verify-platform and passed; `make runtime-inputs-refresh-otel` waited for the Secret, restarted both collectors and passed verification; `wait-secret-key` timed out after 3 s on a missing Secret without printing any value.

Phase 3 (same day): `make backup-clickstack-mongodb` and `make restore-test-clickstack-mongodb` run from `swhurl.recovery`; `mongodump | age` and `age -d | mongorestore` stream through `Runner.pipe`. On the live cluster their output matched the bash scripts (timestamps and prune counts aside), files stayed `600` in a `700` directory, no `.partial` remained, the metadata checksum matched, and the restore test passed on both a new Python-made backup and an older bash-made one. It refused a pre-existing unlabelled `recovery-test` namespace without touching it. Offline tests cover failure cleanup, private permissions, that the dump never reaches disk or output, and restore mismatches; removing permissions, partial cleanup or namespace cleanup from the code each failed them.

## Still to verify before live changes

- k3s datastore type, an off-host backup destination and schedule, and restore on a separate machine.
- Flux resource inventories, Helm ownership details, and the deletion effects of current pruning and namespace ownership.
- Router forwarding, public DNS, and external reachability.
- Workloads outside Git, image publication workflows, and the Mac model service/network path.

## Finding classification

- Confirmed repository defects: the former CI path list included a deleted directory; the former shell check passed multiple files to one `bash -n` call; infrastructure and platform-services docs named a deleted central runtime Secret; the live verifier can print key material on failure and masks the double encoding; the Makefile comment conflated the ClickStack bootstrap and ingestion keys. PR01 repaired the CI path, shell check, stale documentation, and Makefile comment. The verifier and live Secret remain to be fixed.
- Runtime questions: backup destination and restore evidence, datastore configuration, Flux inventories and deletion ownership, public router/DNS, the approved identity list and existing-session behavior, and external workloads or services remain unverified.
- Desired improvements: shared offline validation is included in PR 01. Safe lifecycle operations, independent reconciliation, automatic secret rollout, and app scaffolding remain later implementation work.

On this host, export `KUBECONFIG=$HOME/.kube/config` for scripted `kubectl` and Flux checks. The `/usr/local/bin/kubectl` k3s wrapper can otherwise select the root-owned kubeconfig in non-interactive shells. Do not export Secrets, kubeconfig contents or private keys into this document.
