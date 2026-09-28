# Current state

What has been verified on the live cluster, and when: current facts first, then evidence by change, oldest first. Paths not yet exercised are noted in each section. Never record Secret values, kubeconfig contents or private keys here.

## Cluster now

Checked read-only on 28 September 2026 at `97faeeb`:

- Host: Arch Linux x86-64; one Ready node, `arch` at `192.168.1.200`, k3s `v1.34.4+k3s1`, containerd `2.1.5-k3s1`; 31 GiB RAM (22 GiB available); root filesystem 239 GiB, 42 GiB used.
- Flux `v2.8.1`; all 12 Kustomizations Ready: the two roots (`cluster-sources`, `cluster-stack`), `infra-base`, `infra-cert-manager`, `infra-issuers`, `infra-traefik`, `platform-oauth2-proxy`, `platform-clickstack`, `platform-otel`, `platform-reloader`, `app-hello-staging` and `app-hello-prod`.
- Eight HelmReleases Ready: cert-manager `v1.19.3`, oauth2-proxy-shared `10.1.3`, ClickStack `1.1.1`, both OTel collectors `0.145.0`, Reloader `2.2.17`, and both `hello` instances on app-template `5.2.1`.
- Four Certificates Ready: `hello` (staging and prod), oauth2-proxy and ClickStack.
- Three `local-path` volumes: ClickHouse data (20 GiB) and ClickHouse logs (5 GiB) with `Delete`; MongoDB (10 GiB) with `Retain`, set by a live patch rather than Git.

## Baseline and immediate repairs (P0)

The first inventory, on 27 September 2026 at revision `91e88935` (PR01 then landed as `2bae8d0`), found six Flux Kustomizations, all four volumes with `Delete` reclaim, no host backup jobs, NetworkPolicies only in `flux-system`, and four faults. Each is fixed:

- **P0a teardown:** `make teardown` deleted both root Flux units, whose pruning could cascade through namespaces, Helm releases and `Delete` volumes, and removed the GitRepository the reinstall needed. Both `teardown` and `reinstall` now refuse before any cluster command.
- **P0b age key:** the key existed only on the host. An encrypted copy is on USB, and a controlled recovery from it decrypted all three Secrets tracked at the time.
- **P0c ingestion key:** `logging/hyperdx-secret.HYPERDX_API_KEY` was 48 bytes after one decode; decoding again gave the 36-byte team key, and both collectors logged repeated HTTP 401 errors. The verifier decoded twice and could print keys on mismatch. The SOPS source now holds one base64 layer and the verifier compares bytes once without printing. Verified live on 27 September 2026: after `make runtime-inputs-refresh-otel`, no 401 errors, fresh logs and metrics in ClickHouse, and `make verify-platform` passed.
- **P0d sign-in:** oauth2-proxy admitted any Google account (`email-domain: "*"`) with the cookie on `.homelab.swhurl.com`. It now admits only an authenticated-emails list (`sam@swhurl.com`) and overrides the chart default `email_domains = ["*"]`; deployed 27 September 2026. The approved account signs in (proxy `AuthSuccess`). A non-approved account was refused, but the proxy logged no callback for it, so the refusal probably came from Google (for example, consent-screen test-user limits) rather than the list. The list is enforced by the rendered chart arguments and `make test-safety`.

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

Not yet exercised: a public-exposure instance on a domain outside `homelab.swhurl.com`, and Reloader restarting a generated app (fixtures skip the Reloader watch-list edit; Reloader itself is covered by `make reloader-test`).

## Example app migration (PR05)

28 September 2026, commits `0c219bf` → `2f6eb31`:

1. `hello-staging` and `hello-prod` were generated (unprivileged nginx 1.27 pinned by digest, UID 101, port 8080, no service-account token, read-only root) and deployed beside the old example on `staging-hello-next` / `hello-next`. Both redirected to sign-in and served a page byte-identical (SHA-256) to the old `nginx:1.25` page.
2. Staging cutover (`1b8ae28`): one commit pruned the old staging resources and moved `staging-hello` to the new instance. A 2-second probe loop saw one failed request (Traefik default certificate) at 07:35:39 between the old and new Let's Encrypt certificates; all other requests got the sign-in redirect.
3. Production cutover (`9b47d28`): the same for `hello`; two failed probes (about 4 s) at 07:40:13.
4. Retirement (`2f6eb31`): `homelab-app-example` and `homelab-tenants` removed; the orphaned `apps-staging`/`apps-prod` namespaces, which held only the old certificate Secrets, were deleted by hand.

Isolation check: a throwaway instance with a non-existent image tag stayed failing (`make app-status` reported `ImagePullBackOff … not found`) while both `hello` units reconciled the latest revision, stayed 1/1 ready and kept serving. Deleting that broken instance waited for its in-flight Helm install to hit the 5-minute timeout before the HelmRelease finalizer released the namespace.

A signed-in browser session on both instances was checked later the same day by the operator over HTTPS; over plain HTTP it failed until the redirect fix below.

## HTTP to HTTPS redirect

Found 28 September 2026: plain-HTTP requests were never redirected (signing in over `http://` returned 403 because oauth2-proxy's cookies are `Secure`). k3s runs Traefik 3.6.7 / chart 38.0.2, which ignores the `ports.web.redirectTo` key added in May. Fixed at `094d21a` with `ports.web.redirections.entryPoint`; Traefik now renders `--entryPoints.web.http.redirections.entryPoint.{to=:443,scheme=https,permanent=true}`. `http://hello`, `http://staging-hello` and `http://clickstack` return 301 to the same HTTPS URL with path and query kept. A throwaway `letsencrypt-staging` Certificate for `acme-check.homelab.swhurl.com` was issued through the redirect (HTTP-01 still works). `make verify-platform` now checks the redirect.

## Secret conventions (PR07b)

28 September 2026: `make secrets-check` decrypted all four tracked SOPS Secrets locally with no errors and one warning: `observability/clickstack-runtime-inputs.CLICKSTACK_API_KEY` is double base64-encoded. In-memory comparison (no values printed) showed the live `clickstack-app-secrets.api-key` equals the once-decoded 48-byte text and that the MongoDB team key equals neither form, so the bootstrap key is independent of ingestion and nothing is currently broken. Left unchanged pending a planned ClickStack restart. The other Secrets are single-encoded (`HYPERDX_API_KEY` 36 bytes; oauth2-proxy keys) or `stringData` (fixture).

## Operator tooling in Python (phases 0–6)

28 September 2026: `make verify-platform`, `make verify-config` and `make app-status|app-logs|app-reconcile|app-check` now run from the `tools/swhurl` package; the bash scripts were deleted. On the live cluster the Python output was byte-identical to the bash output for `verify-platform`, `app-status` (both `hello` instances) and `app-logs`, with exit codes matching. Failure paths are covered offline: 18 verifier failure cases and the app commands via `FakeRunner`, plus the end-to-end secrecy contract test through fake `kubectl`; deliberately broken versions of the verifier (accepting a doubly encoded key, ignoring not-Ready units, printing the key) each failed the tests.

Phase 2 (same day): `platform-certs-*` now edits `platform-settings` through `swhurl platform-certs` (only issuers defined in Git; the one line is changed and the file re-parsed before writing) and `wait-runtime-inputs-otel` through `swhurl wait-secret-key`. Their messages matched the old recipes; a staging/prod round trip left the file byte-identical to Git. `install`, the teardown refusal, the collector restart and `host-dns` were rewritten as one- or two-line recipes with identical dry-run and refusal output; the Makefile has no shell loops, `if` blocks, `sed`, `grep` or `jq` left. Live: `make install` ran verify-config → flux-reconcile → verify-platform and passed; `make runtime-inputs-refresh-otel` waited for the Secret, restarted both collectors and passed verification; `wait-secret-key` timed out after 3 s on a missing Secret without printing any value.

Phase 3 (same day): `make backup-clickstack-mongodb` and `make restore-test-clickstack-mongodb` run from `swhurl.recovery`; `mongodump | age` and `age -d | mongorestore` stream through `Runner.pipe`. On the live cluster their output matched the bash scripts (timestamps and prune counts aside), files stayed `600` in a `700` directory, no `.partial` remained, the metadata checksum matched, and the restore test passed on both a new Python-made backup and an older bash-made one. It refused a pre-existing unlabelled `recovery-test` namespace without touching it. Offline tests cover failure cleanup, private permissions, that the dump never reaches disk or output, and restore mismatches; removing permissions, partial cleanup or namespace cleanup from the code each failed them.

Phase 4 (same day): `make suspend|resume|destroy-data` run from `swhurl.lifecycle` (no more `jq`). Against the live cluster its output and exit codes matched the bash script for dry-run suspend/resume, the mounted-claim refusal and every malformed target; two differences are deliberate (kubectl's raw `NotFound` line is no longer echoed, and a missing PV is a clean exit-2 refusal). `make lifecycle-test`, pointed at the new implementation, passed including a confirmed `destroy-data`. Removing the mounted-pod check, the `CONFIRM` check or the reclaim-policy step each failed the unit tests.

Phases 5–6 (same day): `make lifecycle-test`, `reloader-test` and `app-template-test` run from `swhurl/livetests` with cleanup guaranteed by an exit stack and namespace deletion guarded by labels (the app-template cleanup now requires the fixture's `platform.swhurl.com/app` label; the bash version deleted unconditionally). All three passed live with the same check lines as before, and afterwards no test namespaces, units, PVs or labelled objects remained and every Flux unit was Ready. The remaining bash is `shellcheck`-clean (0.11.0) and linted in CI.

## Cleanup step 1

28 September 2026: tooling-only cleanup (plan section 0, step 1). No deployed manifest changed. Offline: 124 unit tests pass, `validate-repo` passes (links and 14 render paths), `app-policy` passes for all five instances including the new `env-drift` comparison of `hello` staging/prod, ruff and shellcheck are clean. The drift tests show that allowed differences (replicas, image tag/digest, resources, issuer) pass and that removing sign-in, changing the image repository, adding an ingress path, enabling the service-account token or adding a file each fail. Live: `make verify-platform`, `app-status`/`app-check` for both `hello` instances, `make app-template-test` and `make lifecycle-test` passed with no leftovers; `make install` and `make wait-runtime-inputs-otel` passed through the rewritten Makefile, and `TIMEOUT_SECS=2` on a missing Secret exited 1 after 2 s. Commit `db8b68d` contained only the `config.env` deletion, so `main` failed CI for one run until `ebfa7a6`.

## Base domain setting (cleanup #1)

28 September 2026, commits `add1da4` (settings only, reconciled first so no unit could substitute an empty value) and `9949eb2`. Platform manifests now take every hostname, the cookie domain and the redirect allowlist from `BASE_DOMAIN`; `OAUTH_HOST` was removed. Before pushing, every unit's rendered output with emulated Flux substitution was byte-identical to the previous revision. Live after reconciling `homelab-auth`, `-clickstack` and `-minio` at `9949eb2`: Helm revisions unchanged (oauth2-proxy-shared 4, clickstack 3, minio 1) and the same oauth2-proxy pod, so nothing upgraded or restarted; oauth2-proxy still runs with `--cookie-domain=.homelab.swhurl.com`, `--whitelist-domain=.homelab.swhurl.com` and the same redirect URL; `https://hello` returned 302 to sign-in and `https://clickstack` 200; `make verify-platform` passed. A test mutation writing a ClickStack host literally failed `make test-safety`.

## MinIO removal (cleanup #12)

28 September 2026, commits `62f0e7c` and `9c2c018`. Before push 1 the MinIO volume held only `.minio.sys` (120 KiB). After `62f0e7c` (unit emptied) Flux uninstalled the release: its pods, claim, 20 GiB `Delete` volume, both ingresses and Certificates were gone, leaving only the two TLS Secrets. After `9c2c018` the `storage` namespace (with those Secrets), the `homelab-minio` unit and the `minio` HelmRepository were gone; 12 Kustomizations, 8 HelmReleases and 4 Certificates Ready, 3 volumes. `make verify-platform` passed, both `hello` instances Ready with valid TLS, and `https://minio.homelab.swhurl.com` now gets Traefik's default certificate and 404.

## Names and layout (cleanup step 4)

28 September 2026. Verbs and file moves (`129b980`) changed no manifest. Canary: `homelab-reloader` was suspended through Git (`9d9c7bb`), which held `homelab-flux-stack` in its health check until the 20-minute timeout: the unit was suspended while waiting for `homelab-cluster-base` to reach the new revision, so its status froze at Ready False ("dependency ... revision is not up to date"); `81b6ccf` then replaced it with `platform-reloader` at `platform/reloader`, which adopted the HelmRelease. Main cutover: `44ddd85` made the two `hello` units `Orphan` (app units otherwise uninstall when deleted), then `73f9a39` replaced the other nine non-root units with `infra-base`, `infra-cert-manager`, `infra-issuers`, `infra-traefik`, `platform-oauth2-proxy`, `platform-clickstack`, `platform-otel`, `app-hello-staging` and `app-hello-prod`, and moved their paths under `infra/`, `platform/` and `apps/`. Before pushing, each new path rendered byte-identically to its old path, and a MongoDB backup was taken. Live afterwards: all 12 units Ready at `73f9a39`; the 10 non-root units hold the same 25 objects as before, none in two units; Helm revisions, all pod UIDs and all volume UIDs unchanged, so nothing was recreated or restarted; namespaces carry the new unit labels; `make verify-platform` passed, both `hello` instances Ready with valid TLS, `https://hello` returned 302 to sign-in and `https://clickstack` 200.

Roots (`82ff1fb`): `make flux-bootstrap` created `cluster-sources` and `cluster-stack` beside the old roots; once Ready their inventories matched the old ones exactly (8 and 10 objects). The old `homelab-flux-sources` and `homelab-flux-stack` (confirmed `Orphan` live) were then deleted with `kubectl delete`. Afterwards the 10 child units kept their UIDs and carry the `cluster-stack` label, the Git and Helm sources the `cluster-sources` label; Helm revisions and pod and volume UIDs unchanged; `make flux-reconcile` and `make verify-platform` passed; both `hello` hosts returned 302 to sign-in and ClickStack 200. This also exercised deleting real shared Flux units.

Suspension and the stack health check, 17:37: `make suspend TARGET=kustomization/platform-reloader` on the Ready unit raised its generation to 3 and the controller still recorded `observedGeneration` 3; a forced `cluster-stack` reconcile then passed its health check in 42 ms, and the stack's apply left `suspend: true` in place. So a unit suspended while Ready does not block the stack; the canary stall came from suspending a unit that was not Ready. `make resume` restored it; all 12 units Ready afterwards. Not tested: suspending a unit that is failing.

## Simplification

28 September 2026, commits `9a233ff` (docs only) and `c27bc8e`. `make reconcile UNIT=platform-otel` applied `c27bc8e` on the live cluster, `make verify-platform` passed and all 12 units stayed Ready. The removed OTel refresh targets were a fallback for Reloader, which restarted the collectors in the dummy-key test; a real credential rotation through Reloader is still unexercised.

Live tests after the renames and simplification, 28 September 2026 at `550b2b3`: `make live-test-app-template` (three generated fixtures deployed through `app-*` units, Secret decrypted by the app's own unit, sign-in redirect, digest-pinned prod, `local-path-retain` claim written), `make live-test-lifecycle` (suspend/resume, uninstall keeping protected data, `destroy-data` refusal then deletion, `Orphan` deletion), `make live-test-reloader` and `make live-test-restore-mongodb` (backup `20260928T172127Z`: counts and ingestion key match) all passed; `make verify-platform` passed, all 12 units Ready, no test namespaces, units or volumes left behind.

## Bootstrap rehearsal

28 September 2026, on a throwaway k3d cluster (k3s `v1.34.4+k3s1`, the live version) on the same host, rootful Podman, API bound to `127.0.0.1`, its own kubeconfig. It pulled branch `rehearsal/bootstrap` (`d9238d3`): `main` at `67bf465` except that the Git source points at the branch, every certificate uses `selfsigned` and the Let's Encrypt issuers are left out, so nothing contacted Let's Encrypt. Following `docs/bootstrap.md`: step 1 checks passed; `flux install` (Flux `v2.8.1`), the `sops-age` Secret and `make flux-bootstrap` created the roots; all 12 units and 8 HelmReleases were Ready about 3 minutes later in dependency order (issuers after cert-manager, apps after `infra-base`); `make install` then failed exactly as documented, on the ingestion key (no team yet) and the MongoDB volume's reclaim policy. Instead of a first login, the backup `20260928T172127Z` was restored with the procedure in `docs/operations.md` (first run): 8 documents, matching the metadata; after the `Retain` patch `make verify-platform` passed, the collectors logged one HTTP 401 while the app restarted and none afterwards, and ClickHouse received current logs and metrics. Through Traefik: both `hello` hosts redirected to Google sign-in, ClickStack returned 200, HTTP redirected to HTTPS, and all four Certificates were Ready. Not rehearsed: DNS, router forwarding, Let's Encrypt, a first login without a backup.

## Still to verify before live changes

- The k3s datastore type, an off-host backup destination and schedule, and restore on a separate machine.
- Router forwarding and public DNS records (not inspected; external reachability is shown only by the operator's own browser use).
- Workloads outside Git, image publication workflows, and the Mac model service and its network path.
