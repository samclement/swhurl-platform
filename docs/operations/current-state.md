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
- Shared oauth2-proxy currently permits `email-domain: "*"` and sets its cookie domain to `.homelab.swhurl.com`. **Fixed in Git (P0d):** sign-in is restricted to an authenticated-emails list (`sam@swhurl.com`) and the chart default `email_domains = ["*"]` is overridden; deployed and verified live for the approved account on 27 September 2026; a rejected-account test remains. A separate domain for public or untrusted apps is still required before expanding exposure.
- A read-only byte comparison found `logging/hyperdx-secret.HYPERDX_API_KEY` is 48 bytes after one Kubernetes `.data` decode. Decoding those bytes again yields 36 bytes matching ClickStack's live team ingestion key. Recent logs from both OTel collector workloads contain repeated HTTP 401 token/scheme failures. This confirms a live ingestion mismatch. **Fixed in Git (P0c):** the SOPS source now holds exactly one base64 layer and the verifier decodes once without printing values; verified live on 27 September 2026: after `make runtime-inputs-refresh-otel`, collector logs show no 401 errors, ClickHouse receives fresh logs and metrics, and `make verify-platform` passes. No key values were printed or saved in this document.

## Still to verify before live changes

- k3s server configuration and datastore type, host backup jobs, off-host backup destination, and a tested restore of state and decryption material.
- Flux resource inventories, Helm ownership details, and the deletion effects of current pruning and namespace ownership.
- Router forwarding, public DNS, rejection of non-approved identities, and external reachability.
- Workloads outside Git, image publication workflows, and the Mac model service/network path.

## Finding classification

- Confirmed repository defects: the former CI path list included a deleted directory; the former shell check passed multiple files to one `bash -n` call; infrastructure and platform-services docs named a deleted central runtime Secret; the live verifier can print key material on failure and masks the double encoding; the Makefile comment conflated the ClickStack bootstrap and ingestion keys. PR01 repaired the CI path, shell check, stale documentation, and Makefile comment. The verifier and live Secret remain to be fixed.
- Runtime questions: backup destination and restore evidence, datastore configuration, Flux inventories and deletion ownership, public router/DNS, the approved identity list and existing-session behavior, and external workloads or services remain unverified.
- Desired improvements: shared offline validation is included in PR 01. Safe lifecycle operations, independent reconciliation, automatic secret rollout, and app scaffolding remain later implementation work.

On this host, export `KUBECONFIG=$HOME/.kube/config` for scripted `kubectl` and Flux checks. The `/usr/local/bin/kubectl` k3s wrapper can otherwise select the root-owned kubeconfig in non-interactive shells. Do not export Secrets, kubeconfig contents or private keys into this document.
