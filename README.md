# Swhurl Platform (k3s-only)

This repo provides a k3s-focused, declarative platform setup: Cilium CNI, cert-manager, ingress-nginx, oauth2-proxy, ClickStack (ClickHouse + HyperDX + OTel Collector), and MinIO. Scripts are thin orchestrators around Helm + manifests in `infra/`.

## Quick Start

1. Configure non-secrets in `config.env`.
2. Configure secrets in `profiles/secrets.env` (gitignored, see `profiles/secrets.example.env`).
3. Print the plan: `./scripts/02_print_plan.sh`
4. Apply: `./run.sh`

Docs:
- Phase runbook: `docs/runbook.md`

## How This Repo Is Structured

The platform is run in explicit phases so you can apply/verify/debug in stages. The orchestrator (`run.sh`) does not discover scripts dynamically; it runs an explicit plan.

To print the plan without executing:

```bash
./scripts/02_print_plan.sh
./scripts/02_print_plan.sh --delete
```

## Phases (Install)

This is the top-level structure the repo follows (each phase has its own verification gates). The concrete script mapping is in `docs/runbook.md`.

1. Prerequisites & verify
2. DNS & Network Reachability & verify
3. Basic Kubernetes Cluster (kubeconfig) & verify
4. Environment (profiles/secrets) & verification
5. Cluster Deps (Helm, namespaces, Cilium) & verification
6. Cluster Platform Services (cert-manager, ingress, oauth, clickstack, otel, minio) & verification
7. Test application & verification
8. Cluster verification suite

Run everything:

```bash
./run.sh
```

Use a profile:

```bash
./run.sh --profile profiles/minimal.env
```

## Key Flags and Inputs

## How Environment Variables Flow (kubectl, Helm, Helmfile, Kustomize)

This repo uses shell-sourced config (`config.env` + profiles) and relies on **exported** environment variables to drive Helmfile templating.

1) `./run.sh` and `./scripts/*`
- `./run.sh` sources `config.env` and an optional `--profile FILE` for orchestration decisions (feature flags, plan).
- Each script then sources `scripts/00_lib.sh`, which sources `config.env` and either:
  - `$PROFILE_FILE` (when `--profile` is used), or
  - `profiles/secrets.env` (fallback), or
  - `profiles/local.env` (fallback)
- `scripts/00_lib.sh` uses `set -a` while sourcing so variables are **exported** for child processes (notably `helmfile`).

2) `helmfile` (templating and values)
- Helmfile templates read environment variables via Go templating: `{{ env "VAR" }}`.
- This repo’s Helmfile is `helmfile.yaml.gotmpl` and its environment values come from:
  - `environments/common.yaml.gotmpl` (derives `.Environment.Values` from exported env vars)
  - plus `environments/default.yaml` or `environments/minimal.yaml`
- Feature flags are applied twice:
  - Orchestration: `run.sh` decides which scripts to run.
  - Declarative state: Helmfile uses `.Environment.Values.features.*` to set `installed:` on releases.

If you run Helmfile manually, you must export variables yourself, e.g.:
```bash
set -a
source ./config.env
source ./profiles/secrets.env
set +a
helmfile -f helmfile.yaml.gotmpl -e "${HELMFILE_ENV:-default}" diff
```

3) `helm` (charts)
- Helm itself does not substitute env vars into chart values automatically.
- In this repo, Helm is invoked via Helmfile; chart configuration comes from the rendered YAML in `infra/values/*`.

4) `kubectl` (apply and context)
- `kubectl` reads `KUBECONFIG` (or `~/.kube/config`) for cluster access.
- `kubectl apply --dry-run=server` talks to the API server and **runs admission** (including validating webhooks). You can’t “skip admission hooks” in server dry-run; if you need webhook-free validation, use client dry-run.

5) `kustomize` (raw manifests)
- Kustomize does not read arbitrary environment variables by default.
- This repo uses Kustomize for mostly-static resources under `infra/manifests/*`.
- When a manifest needs a runtime value, the pattern is either:
  - Use Helmfile templating/values for Helm-managed resources, or
  - Build with `kubectl kustomize ...` and then inject via `envsubst` in a script (e.g. `scripts/35_issuer.sh` for `ACME_EMAIL`).

Environment layering
- `config.env`: non-secret defaults (committed).
- `profiles/secrets.env`: secrets (gitignored).
- `--profile FILE`: additional overrides (exported to child scripts as `PROFILE_FILE`).

ACME / Let’s Encrypt
- Default is staging: `LETSENCRYPT_ENV=staging`
- `scripts/35_issuer.sh` ensures:
  - `letsencrypt-staging` and `letsencrypt-prod` exist
  - `letsencrypt` is an alias issuer that points to the selected env (so most ingresses can keep `cert-manager.io/cluster-issuer: letsencrypt`)

k3s bootstrap (optional)
- Cluster provisioning is not the default workflow. If you want the repo to install k3s on the local host:
  - Set `FEAT_BOOTSTRAP_K3S=true`
  - The plan includes `scripts/10_install_k3s_cilium_minimal.sh` + `scripts/11_cluster_k3s.sh`

Verification toggles
- `FEAT_VERIFY=true|false` controls whether the verification suite runs during `./run.sh`.

Helmfile drift checks (`scripts/92_verify_helmfile_diff.sh`)
- Requires the `helm-diff` plugin:
  - `helm plugin install https://github.com/databus23/helm-diff`
- Server-side dry-run uses the API server and will run admission webhooks. If your cluster rejects dry-run due to validating webhooks (common with ingress duplicate host/path), you can skip server dry-run and still validate render sanity:
  - `HELMFILE_SERVER_DRY_RUN=false ./scripts/92_verify_helmfile_diff.sh`

## Teardown (Delete)

```bash
./run.sh --delete
```

Delete ordering is intentionally strict:
- Platform services are removed first.
- `scripts/99_teardown.sh` sweeps managed namespaces, non-k3s-native secrets, and platform CRDs.
- Cilium is deleted last (`scripts/26_cilium.sh --delete`), so k3s/local-path helper pods can still run during PVC/namespace cleanup.
- `scripts/98_verify_delete_clean.sh` runs last and fails the delete if anything remains.

Delete scope (shared clusters)
- By default, deletion only sweeps Secrets in the namespaces this repo manages.
- For a dedicated cluster wipe, opt in explicitly:
  - `DELETE_SCOPE=dedicated-cluster ./run.sh --delete`

k3s uninstall is manual unless `K3S_UNINSTALL=true`:

```bash
sudo /usr/local/bin/k3s-uninstall.sh
```

## Layout

```
infra/
  manifests/
  values/
profiles/
scripts/
config.env
run.sh
```
