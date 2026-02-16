# Contracts (Config, Tooling, Delete)

This repo is optimized for a small k3s platform, but the orchestration rules are generic:

- Prefer declarative state (Helmfile + charts, or Kustomize) over bash.
- Use bash only for: (1) preparing external inputs (Secrets/ConfigMaps), (2) cleanup that Helm cannot do (CRDs/finalizers), and (3) host bootstrap (optional).

## Environment Contract

### Layering / precedence

All scripts load configuration via `scripts/00_lib.sh` with this precedence (later wins):

1) `config.env` (committed, non-secret defaults)
2) `profiles/local.env` (optional local overrides)
3) `profiles/secrets.env` (gitignored secrets)
4) `$PROFILE_FILE` (set by `./run.sh --profile ...`; highest precedence)

If `PROFILE_EXCLUSIVE=true`, then only `config.env` + `$PROFILE_FILE` are loaded.

### Export behavior

`scripts/00_lib.sh` uses `set -a` while sourcing so variables are exported to child processes.
This is required so `helmfile` Go templates can read the environment.

### Required variables

The source of truth for required inputs is `scripts/94_verify_config_inputs.sh`.

Verification toggles:

- `FEAT_VERIFY=true|false`: run core verification gates (`94`, `91`, `92`) in `./run.sh`.
- `FEAT_VERIFY_DEEP=true|false`: run extra diagnostics/consistency checks (`90`, `93`, `95`, `96`) in `./run.sh`.

## Tool Contract (How Each Tool Uses Env Vars)

### Helmfile

- File: `helmfile.yaml.gotmpl`
- Environments: `environments/common.yaml.gotmpl` + `environments/*.yaml`
- Env vars are read in templates via `{{ env "VAR" }}` and/or derived into `.Environment.Values`.

Helmfile is the declarative control plane for Helm-based installs. Releases are grouped with
Helmfile labels so orchestration can target phases:

- `phase=core` (cert-manager + ingress-nginx)
- `phase=platform` (oauth2-proxy, clickstack, otel collectors, minio)

Additionally, thin wrapper scripts that operate on a single release use:

- `component=<id>`: stable selector for `sync_release` / `destroy_release` wrappers in `scripts/00_lib.sh`.

### Helm

Helm does not substitute environment variables in values by default. In this repo Helm is
invoked via Helmfile; chart configuration comes from rendered YAML in `infra/values/*`.

### kubectl

`kubectl` reads cluster access via `KUBECONFIG` or `~/.kube/config`.

Important dry-run behavior:

- `kubectl apply --dry-run=server` hits the API server and runs admission (including validating webhooks).
- There is no supported “skip admission” flag for server dry-run. If webhook-free validation is required,
  use `--dry-run=client`.

### Kustomize (Optional)

Kustomize is not used by the default pipeline anymore; it’s kept optional for app teams who prefer raw manifests.

Kustomize does not consume arbitrary env vars by default. If Kustomize resources require runtime values, the supported patterns are:

- Make them Helmfile/Helm-managed (preferred for platform components).
- Or keep them Kustomize-only for apps and inject values via explicit script logic.

This repo does not ship a Kustomize manifest tree by default (no `infra/manifests/`). If you add your own
`kustomization.yaml` files, validate them explicitly with `kubectl kustomize` in CI.

## Delete Contract

Helm does not uninstall CRDs in a reliable/portable way. This repo’s delete contract is:

1) Remove workloads first (apps, then platform services), so PVCs/namespaces can terminate cleanly.
2) Perform teardown sweeps (managed secrets/namespaces, platform CRDs).
3) Remove Cilium last, so helper pods used for PVC cleanup can still run.
4) Verify the cluster is clean via `scripts/98_verify_teardown_clean.sh`.

Delete scope:

- `DELETE_SCOPE=managed` (default): sweep only resources in namespaces this repo manages.
- `DELETE_SCOPE=dedicated-cluster`: opt-in, more aggressive cleanup for dedicated clusters.
