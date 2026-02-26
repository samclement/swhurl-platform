# Swhurl Platform: Linear Code Walkthrough

*2026-02-26T10:59:01Z by Showboat 0.6.1*
<!-- showboat-id: 1fd7f1f4-7805-4b43-8afa-e1e37b9c554b -->

This walkthrough follows the code in execution order, from `./run.sh` through each script it invokes, then into Helmfile values, local charts, verification, and delete behavior.

The goal is to explain how the platform converges a k3s cluster declaratively while keeping imperative bash focused on orchestration, safety checks, and cleanup edge-cases.

> Status (2026-02-26): this walkthrough is currently stale versus the active migration state.
> Use `README.md`, `docs/runbook.md`, and `docs/target-tree-and-migration-checklist.md` as the source of truth until this file is fully regenerated.

```bash
pwd && rg --files | sort | sed -n '1,80p'
```

```output
/home/sam/ghq/github.com/samclement/swhurl-platform
AGENTS.md
charts/apps-hello/Chart.yaml
charts/apps-hello/templates/certificate.yaml
charts/apps-hello/templates/deployment.yaml
charts/apps-hello/templates/_helpers.tpl
charts/apps-hello/templates/ingress.yaml
charts/apps-hello/templates/service.yaml
charts/apps-hello/values.yaml
charts/platform-issuers/Chart.yaml
charts/platform-issuers/templates/clusterissuer-letsencrypt.yaml
charts/platform-issuers/templates/clusterissuer-selfsigned.yaml
charts/platform-issuers/templates/_helpers.tpl
charts/platform-issuers/values.yaml
charts/platform-namespaces/Chart.yaml
charts/platform-namespaces/templates/namespaces.yaml
charts/platform-namespaces/values.yaml
config.env
docs/add-feature-checklist.md
docs/architecture.d2
docs/architecture.svg
docs/contracts.md
docs/migration-plan-local-charts.md
docs/runbook.md
docs/verification-maintainability-plan.md
environments/common.yaml.gotmpl
environments/default.yaml
environments/minimal.yaml
helmfile.yaml.gotmpl
infra/values/apps-hello-helmfile.yaml.gotmpl
infra/values/cert-manager-helmfile.yaml.gotmpl
infra/values/cilium-helmfile.yaml.gotmpl
infra/values/clickstack-helmfile.yaml.gotmpl
infra/values/ingress-nginx-logging.yaml
infra/values/minio-helmfile.yaml.gotmpl
infra/values/oauth2-proxy-helmfile.yaml.gotmpl
infra/values/otel-k8s-daemonset.yaml.gotmpl
infra/values/otel-k8s-deployment.yaml.gotmpl
infra/values/platform-issuers-helmfile.yaml.gotmpl
profiles/local.env
profiles/minimal.env
profiles/secrets.example.env
README.md
run.sh
scripts/00_feature_registry_lib.sh
scripts/00_lib.sh
scripts/00_verify_contract_lib.sh
scripts/01_check_prereqs.sh
scripts/02_print_plan.sh
scripts/15_verify_cluster_access.sh
scripts/20_reconcile_platform_namespaces.sh
scripts/25_prepare_helm_repositories.sh
scripts/26_manage_cilium_lifecycle.sh
scripts/29_prepare_platform_runtime_inputs.sh
scripts/30_manage_cert_manager_cleanup.sh
scripts/31_sync_helmfile_phase_core.sh
scripts/36_sync_helmfile_phase_platform.sh
scripts/75_manage_sample_app_lifecycle.sh
scripts/90_verify_runtime_smoke.sh
scripts/91_verify_platform_state.sh
scripts/92_verify_helmfile_drift.sh
scripts/93_verify_expected_releases.sh
scripts/94_verify_config_inputs.sh
scripts/95_capture_cluster_diagnostics.sh
scripts/96_verify_orchestrator_contract.sh
scripts/98_verify_teardown_clean.sh
scripts/99_execute_teardown.sh
scripts/aws-dns-updater.sh
scripts/manual_configure_route53_dns_updater.sh
scripts/manual_install_k3s_minimal.sh
walkthrough.md
```

## 1) Configuration and environment model

Everything flows from exported environment variables. `config.env` defines defaults, `profiles/*.env` overrides them, and `scripts/00_lib.sh` loads and exports values so Helmfile templates can read them via `env`.

Feature flags are centralized in `scripts/00_feature_registry_lib.sh`, and cross-script invariants live in `scripts/00_verify_contract_lib.sh`.

```bash
sed -n '1,120p' config.env
```

```output
# Base configuration for local platform scripts

# Domain / TLS
# Register these subdomains in Route53 (or use a wildcard A record)
SWHURL_SUBDOMAINS="homelab oauth.homelab hello.homelab clickstack.homelab hubble.homelab minio.homelab minio-console.homelab"
SWHURL_SUBDOMAIN=
BASE_DOMAIN=homelab.swhurl.com
CLUSTER_ISSUER=letsencrypt  # selfsigned | letsencrypt
LETSENCRYPT_ENV=staging     # staging | prod
# Put ACME_EMAIL in a secrets profile instead of committing it
ACME_EMAIL=

# OAuth2 Proxy (optional) — move secrets to profiles/secrets.env
OIDC_ISSUER=
OIDC_CLIENT_ID=
OIDC_CLIENT_SECRET=
OAUTH_COOKIE_SECRET=
OAUTH_HOST=oauth.${BASE_DOMAIN}

# Observability
HUBBLE_HOST=hubble.${BASE_DOMAIN}
CLICKSTACK_HOST=clickstack.${BASE_DOMAIN}
CLICKSTACK_API_KEY=
CLICKSTACK_INGESTION_KEY=
CLICKSTACK_OTEL_ENDPOINT=http://clickstack-otel-collector.observability.svc.cluster.local:4318

# Storage
MINIO_HOST=minio.${BASE_DOMAIN}
MINIO_CONSOLE_HOST=minio-console.${BASE_DOMAIN}
MINIO_ROOT_USER=minioadmin
MINIO_ROOT_PASSWORD=

# Host bootstrap
# k3s installation is intentionally not part of the platform orchestrator.
# Use scripts/manual_install_k3s_minimal.sh for local host install.

# Skip verification scripts in apply runs (useful for quick iteration).
FEAT_VERIFY=true
# Extra verification/diagnostics (smoke/inventory/context/surface) are opt-in.
FEAT_VERIFY_DEEP=false

FEAT_OAUTH2_PROXY=true
FEAT_CILIUM=true
FEAT_CLICKSTACK=true
FEAT_OTEL_K8S=true
FEAT_MINIO=true
## Service mesh
# Linkerd/Istio scripts were removed to keep the platform focused and reduce maintenance.

# Runtime
TIMEOUT_SECS=300
HELMFILE_ENV=default
```

```bash
sed -n '1,140p' scripts/00_lib.sh
```

```output
#!/usr/bin/env bash
set -Eeuo pipefail

# Common helpers for platform scripts

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# Shared feature registry.
# shellcheck disable=SC1091
source "$SCRIPT_DIR/00_feature_registry_lib.sh"

# Shared verification/teardown contract.
# shellcheck disable=SC1091
source "$SCRIPT_DIR/00_verify_contract_lib.sh"

# Load config and profile. Helmfile templates use env-vars via `env "FOO"`,
# so we need to export loaded values, not just set shell variables.
set -a
if [[ -f "$ROOT_DIR/config.env" ]]; then
  # shellcheck disable=SC1090
  source "$ROOT_DIR/config.env"
fi

# Profile layering:
# - By default: config.env -> profiles/local.env -> profiles/secrets.env -> PROFILE_FILE (highest precedence)
# - Opt out (standalone profile): PROFILE_EXCLUSIVE=true uses only config.env -> PROFILE_FILE
PROFILE_EXCLUSIVE="${PROFILE_EXCLUSIVE:-false}"
if [[ "$PROFILE_EXCLUSIVE" != "true" && "$PROFILE_EXCLUSIVE" != "false" ]]; then
  die "PROFILE_EXCLUSIVE must be true or false (got: $PROFILE_EXCLUSIVE)"
fi

if [[ "$PROFILE_EXCLUSIVE" == "false" && -f "$ROOT_DIR/profiles/local.env" ]]; then
  # shellcheck disable=SC1090
  source "$ROOT_DIR/profiles/local.env"
fi
if [[ "$PROFILE_EXCLUSIVE" == "false" && -f "$ROOT_DIR/profiles/secrets.env" ]]; then
  # shellcheck disable=SC1090
  source "$ROOT_DIR/profiles/secrets.env"
fi
if [[ -n "${PROFILE_FILE:-}" && -f "$PROFILE_FILE" ]]; then
  # shellcheck disable=SC1090
  source "$PROFILE_FILE"
fi
set +a

log_info() { printf "[INFO] %s\n" "$*"; }
log_warn() { printf "[WARN] %s\n" "$*"; }
log_error() { printf "[ERROR] %s\n" "$*" >&2; }
die() { log_error "$*"; exit 1; }

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "Missing required command: $1"
}

ensure_context() {
  need_cmd kubectl
  # Robust reachability check that works across kubectl versions
  kubectl get --raw=/version >/dev/null 2>&1 || die "kubectl cannot reach a cluster; ensure kubeconfig is set"
}

kubectl_ns() {
  local ns="$1"
  kubectl get ns "$ns" >/dev/null 2>&1 || kubectl create namespace "$ns" >/dev/null
}

wait_deploy() {
  local ns="$1" name="$2" timeout="${3:-${TIMEOUT_SECS:-300}}"
  kubectl -n "$ns" rollout status deploy/"$name" --timeout="${timeout}s"
}

wait_ds() {
  local ns="$1" name="$2" timeout="${3:-${TIMEOUT_SECS:-300}}"
  kubectl -n "$ns" rollout status ds/"$name" --timeout="${timeout}s"
}

wait_webhook_cabundle() {
  local name="$1" timeout="${2:-${TIMEOUT_SECS:-300}}"
  local start now ca elapsed last_log exists ca_len leader
  start=$(date +%s)
  last_log=0
  log_info "Waiting for webhook '${name}' CA bundle to be injected by cert-manager-cainjector (often delayed by leader election) (timeout: ${timeout}s)"
  while true; do
    exists=false
    ca=""
    if kubectl get validatingwebhookconfiguration "$name" >/dev/null 2>&1; then
      exists=true
      ca=$(kubectl get validatingwebhookconfiguration "$name" -o jsonpath='{.webhooks[0].clientConfig.caBundle}' 2>/dev/null || true)
      if [[ -n "$ca" ]]; then
        log_info "Webhook '${name}' CA bundle populated"
        return 0
      fi
    fi
    now=$(date +%s)
    elapsed=$(( now - start ))
    # Emit periodic status so this doesn't look like a hang.
    if (( elapsed - last_log >= 10 )); then
      ca_len=0
      if [[ -n "${ca:-}" ]]; then
        ca_len=${#ca}
      fi
      leader="$(kubectl -n kube-system get lease cert-manager-cainjector-leader-election -o jsonpath='{.spec.holderIdentity}' 2>/dev/null || true)"
      if [[ -z "$leader" ]]; then
        leader="(none)"
      fi
      log_info "Still waiting: webhookConfigPresent=${exists} caBundleLen=${ca_len} cainjectorLeader=${leader} elapsed=${elapsed}s"
      last_log=$elapsed
    fi
    if (( elapsed >= timeout )); then
      return 1
    fi
    sleep 2
  done
}

wait_crd_established() {
  local crd="$1" timeout="${2:-${TIMEOUT_SECS:-300}}"
  local start now elapsed last_log status
  start=$(date +%s)
  last_log=0
  log_info "Waiting for CRD '${crd}' to be Established (timeout: ${timeout}s)"
  while true; do
    status="$(kubectl get crd "$crd" -o jsonpath='{range .status.conditions[?(@.type=="Established")]}{.status}{end}' 2>/dev/null || true)"
    if [[ "$status" == "True" ]]; then
      log_info "CRD '${crd}' is Established"
      return 0
    fi
    now=$(date +%s)
    elapsed=$(( now - start ))
    if (( elapsed - last_log >= 10 )); then
      if kubectl get crd "$crd" >/dev/null 2>&1; then
        log_info "Still waiting: crdPresent=true establishedStatus=${status:-<empty>} elapsed=${elapsed}s"
      else
        log_info "Still waiting: crdPresent=false elapsed=${elapsed}s"
      fi
      last_log=$elapsed
    fi
    if (( elapsed >= timeout )); then
      return 1
    fi
```

```bash
sed -n '1,260p' scripts/00_verify_contract_lib.sh
```

```output
#!/usr/bin/env bash

# Helper library (not a runnable phase step).
# Sourced by scripts/00_lib.sh to provide shared verification/teardown contracts.
# Depends on scripts/00_feature_registry_lib.sh.

if [[ -z "${FEATURE_REGISTRY_LOADED:-}" ]]; then
  _VERIFY_SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  # shellcheck disable=SC1091
  source "$_VERIFY_SCRIPT_DIR/00_feature_registry_lib.sh"
  unset _VERIFY_SCRIPT_DIR
fi

if [[ "${VERIFY_CONTRACT_LOADED:-}" == "1" ]]; then
  return 0 2>/dev/null || exit 0
fi
readonly VERIFY_CONTRACT_LOADED="1"

# Shared verification and teardown expectations.
# Feature-specific metadata is sourced from scripts/00_feature_registry_lib.sh.

# Ingress runtime verification contract.
readonly VERIFY_INGRESS_SERVICE_TYPE="NodePort"
readonly VERIFY_INGRESS_NODEPORT_HTTP="31514"
readonly VERIFY_INGRESS_NODEPORT_HTTPS="30313"
readonly VERIFY_SAMPLE_INGRESS_HOST_PREFIX="hello"

# Helmfile drift ignore contract.
readonly -a VERIFY_HELMFILE_IGNORED_RESOURCE_HEADERS=(
  "kube-system, cilium-ca, Secret (v1) has changed:"
  "kube-system, hubble-relay-client-certs, Secret (v1) has changed:"
  "kube-system, hubble-server-certs, Secret (v1) has changed:"
)

# Teardown/delete-clean contract.
readonly -a PLATFORM_MANAGED_NAMESPACES=(apps cert-manager ingress logging observability platform-system storage)
readonly PLATFORM_CRD_NAME_REGEX='cert-manager\.io|acme\.cert-manager\.io|\.cilium\.io$'
readonly -a VERIFY_RELEASE_ALLOWLIST_DEFAULT=(
  "apps/hello-web"
)

# During teardown (before Cilium delete), keep Cilium helm release metadata.
readonly -a VERIFY_K3S_ALLOWED_SECRETS_PRE_CILIUM=(
  "k3s-serving"
  "*.node-password.k3s"
  "bootstrap-token-*"
  "sh.helm.release.v1.cilium.*"
)

# After full delete, Cilium release metadata should also be gone.
readonly -a VERIFY_K3S_ALLOWED_SECRETS_POST_CILIUM=(
  "k3s-serving"
  "*.node-password.k3s"
  "bootstrap-token-*"
)

# Config input contract.
readonly -a VERIFY_REQUIRED_BASE_VARS=(BASE_DOMAIN CLUSTER_ISSUER)
readonly VERIFY_REQUIRED_TIMEOUT_VAR="TIMEOUT_SECS"
readonly -a VERIFY_ALLOWED_LETSENCRYPT_ENVS=(staging prod production)
readonly -a VERIFY_BASE_EFFECTIVE_NON_SECRET_VARS=(
  BASE_DOMAIN
  CLUSTER_ISSUER
  LETSENCRYPT_ENV
)

name_matches_any_pattern() {
  local value="$1"; shift
  local pattern
  for pattern in "$@"; do
    [[ "$value" == $pattern ]] && return 0
  done
  return 1
}

is_platform_managed_namespace() {
  local ns="$1"
  local item
  for item in "${PLATFORM_MANAGED_NAMESPACES[@]}"; do
    [[ "$item" == "$ns" ]] && return 0
  done
  return 1
}

is_release_in_platform_scope() {
  local release_ref="$1"
  local ns="${release_ref%%/*}"
  [[ -n "$ns" ]] || return 1
  [[ "$ns" == "kube-system" ]] && return 0
  is_platform_managed_namespace "$ns"
}

is_allowed_k3s_secret_for_teardown() {
  local ns="$1" name="$2"
  [[ "$ns" == "kube-system" ]] || return 1
  name_matches_any_pattern "$name" "${VERIFY_K3S_ALLOWED_SECRETS_PRE_CILIUM[@]}"
}

is_allowed_k3s_secret_for_verify() {
  local ns="$1" name="$2"
  [[ "$ns" == "kube-system" ]] || return 1
  name_matches_any_pattern "$name" "${VERIFY_K3S_ALLOWED_SECRETS_POST_CILIUM[@]}"
}

is_allowed_letsencrypt_env() {
  local value="$1"
  name_matches_any_pattern "$value" "${VERIFY_ALLOWED_LETSENCRYPT_ENVS[@]}"
}

verify_oauth_auth_url() {
  local oauth_host="$1"
  printf 'https://%s/oauth2/auth' "$oauth_host"
}

verify_oauth_auth_signin() {
  local oauth_host="$1"
  printf 'https://%s/oauth2/start?rd=$scheme://$host$request_uri' "$oauth_host"
}

verify_expected_letsencrypt_server() {
  local le_env="${1:-staging}"
  case "$le_env" in
    prod|production) printf '%s' "https://acme-v02.api.letsencrypt.org/directory" ;;
    *) printf '%s' "https://acme-staging-v02.api.letsencrypt.org/directory" ;;
  esac
}

verify_expected_releases() {
  local -A seen=()
  local -a expected=(
    "kube-system/platform-namespaces"
    "cert-manager/cert-manager"
    "kube-system/platform-issuers"
    "ingress/ingress-nginx"
  )
  local key release
  for key in "${FEATURE_KEYS[@]}"; do
    feature_is_enabled "$key" || continue
    while IFS= read -r release; do
      [[ -n "$release" ]] || continue
      if [[ -z "${seen[$release]+x}" ]]; then
        expected+=("$release")
        seen["$release"]=1
      fi
    done < <(feature_expected_releases "$key")
  done
  printf '%s\n' "${expected[@]}"
}

verify_required_vars_for_enabled_features() {
  local -A seen=()
  local key var
  for key in "${FEATURE_KEYS[@]}"; do
    feature_is_enabled "$key" || continue
    while IFS= read -r var; do
      [[ -n "$var" ]] || continue
      [[ -n "${seen[$var]+x}" ]] && continue
      seen["$var"]=1
      printf '%s\n' "$var"
    done < <(feature_required_vars "$key")
  done
}

verify_effective_non_secret_vars() {
  local -A seen=()
  local key var

  for var in "${VERIFY_BASE_EFFECTIVE_NON_SECRET_VARS[@]}"; do
    [[ -n "${seen[$var]+x}" ]] && continue
    seen["$var"]=1
    printf '%s\n' "$var"
  done

  for key in "${FEATURE_KEYS[@]}"; do
    feature_is_enabled "$key" || continue
    while IFS= read -r var; do
      [[ -n "$var" ]] || continue
      [[ -n "${seen[$var]+x}" ]] && continue
      seen["$var"]=1
      printf '%s\n' "$var"
    done < <(feature_effective_non_secret_vars "$key")
  done
}
```

```bash
sed -n '1,220p' scripts/00_feature_registry_lib.sh
```

```output
#!/usr/bin/env bash

# Helper library (not a runnable phase step).
# Canonical feature registry for flags, required vars, and expected releases.

if [[ "${FEATURE_REGISTRY_LOADED:-}" == "1" ]]; then
  return 0 2>/dev/null || exit 0
fi
readonly FEATURE_REGISTRY_LOADED="1"

readonly -a FEATURE_KEYS=(
  cilium
  oauth2_proxy
  clickstack
  otel_k8s
  minio
)

readonly -A FEATURE_FLAGS=(
  [cilium]="FEAT_CILIUM"
  [oauth2_proxy]="FEAT_OAUTH2_PROXY"
  [clickstack]="FEAT_CLICKSTACK"
  [otel_k8s]="FEAT_OTEL_K8S"
  [minio]="FEAT_MINIO"
)

readonly -A FEATURE_REQUIRED_VARS=(
  [cilium]="HUBBLE_HOST"
  [oauth2_proxy]="OAUTH_HOST OIDC_ISSUER OIDC_CLIENT_ID OIDC_CLIENT_SECRET"
  [clickstack]="CLICKSTACK_HOST CLICKSTACK_API_KEY"
  [otel_k8s]="CLICKSTACK_OTEL_ENDPOINT CLICKSTACK_INGESTION_KEY"
  [minio]="MINIO_HOST MINIO_CONSOLE_HOST MINIO_ROOT_PASSWORD"
)

readonly -A FEATURE_EFFECTIVE_NON_SECRET_VARS=(
  [cilium]="HUBBLE_HOST"
  [oauth2_proxy]="OAUTH_HOST"
  [clickstack]="CLICKSTACK_HOST"
  [otel_k8s]="CLICKSTACK_OTEL_ENDPOINT"
  [minio]="MINIO_HOST MINIO_CONSOLE_HOST"
)

readonly -A FEATURE_EXPECTED_RELEASES=(
  [cilium]="kube-system/cilium"
  [oauth2_proxy]="ingress/oauth2-proxy"
  [clickstack]="observability/clickstack"
  [otel_k8s]="logging/otel-k8s-daemonset logging/otel-k8s-cluster"
  [minio]="storage/minio"
)

feature_registry_keys() {
  printf '%s\n' "${FEATURE_KEYS[@]}"
}

feature_registry_flags() {
  local key
  for key in "${FEATURE_KEYS[@]}"; do
    printf '%s\n' "${FEATURE_FLAGS[$key]}"
  done
}

feature_flag_var() {
  local key="$1"
  printf '%s' "${FEATURE_FLAGS[$key]:-}"
}

feature_is_enabled() {
  local key="$1"
  local flag
  flag="$(feature_flag_var "$key")"
  [[ -n "$flag" ]] || return 1
  [[ "${!flag:-true}" == "true" ]]
}

feature_required_vars() {
  local key="$1"
  local vars="${FEATURE_REQUIRED_VARS[$key]:-}"
  local v
  for v in $vars; do
    printf '%s\n' "$v"
  done
}

feature_effective_non_secret_vars() {
  local key="$1"
  local vars="${FEATURE_EFFECTIVE_NON_SECRET_VARS[$key]:-}"
  local v
  for v in $vars; do
    printf '%s\n' "$v"
  done
}

feature_expected_releases() {
  local key="$1"
  local releases="${FEATURE_EXPECTED_RELEASES[$key]:-}"
  local r
  for r in $releases; do
    printf '%s\n' "$r"
  done
}
```

```bash
sed -n '1,220p' run.sh
```

```output
#!/usr/bin/env bash
set -Eeuo pipefail

# Phase-based orchestrator for platform scripts.
# - Explicit ordering and dependencies (no implicit script discovery)
# - --delete runs reverse phases with deterministic finalizers
# - --only can filter by step number (NN) or script basename

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

DELETE_MODE=false
DRY_RUN=false
ONLY_FILTER=""
PROFILE_FILE=""

usage() {
  cat <<USAGE
Usage: ./run.sh [--profile FILE] [--only N[,N...]] [--dry-run] [--delete]

Options:
  --profile FILE  Load additional env vars (overrides config.env)
  --only LIST     Comma-separated step numbers or script basenames to run
  --dry-run       Print plan without executing
  --delete        Run teardown (reverse order), passing --delete to steps

Env:
  ONLY                 Same as --only (overridden by CLI flag)
  FEAT_VERIFY           If false, skip verification scripts in apply runs (default true)
  FEAT_VERIFY_DEEP      If true, run extra/diagnostic verification scripts (default false)

Manual prereqs:
  DNS registration is not part of the orchestrator plan. If you use .swhurl.com
  domains and want automatic Route53 updates, run: ./scripts/manual_configure_route53_dns_updater.sh
  Host bootstrap (k3s install) is also manual. See README.
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --delete) DELETE_MODE=true; shift ;;
    --dry-run) DRY_RUN=true; shift ;;
    --only) ONLY_FILTER="$2"; shift 2 ;;
    --profile) PROFILE_FILE="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown arg: $1" >&2; usage; exit 1 ;;
  esac
done

# Load env with the same layering semantics as scripts/00_lib.sh, so the plan
# is computed from the same effective configuration the scripts will use:
#   config.env -> profiles/local.env -> profiles/secrets.env -> --profile (highest precedence)
#
# Opt out (standalone profile): PROFILE_EXCLUSIVE=true uses only config.env -> --profile
PROFILE_EXCLUSIVE="${PROFILE_EXCLUSIVE:-false}"
if [[ "$PROFILE_EXCLUSIVE" != "true" && "$PROFILE_EXCLUSIVE" != "false" ]]; then
  echo "[ERROR] PROFILE_EXCLUSIVE must be true or false (got: $PROFILE_EXCLUSIVE)" >&2
  exit 1
fi

set -a
if [[ -f "config.env" ]]; then
  # shellcheck disable=SC1091
  source "config.env"
fi
if [[ "$PROFILE_EXCLUSIVE" == "false" && -f "profiles/local.env" ]]; then
  # shellcheck disable=SC1091
  source "profiles/local.env"
fi
if [[ "$PROFILE_EXCLUSIVE" == "false" && -f "profiles/secrets.env" ]]; then
  # shellcheck disable=SC1091
  source "profiles/secrets.env"
fi
if [[ -n "$PROFILE_FILE" ]]; then
  export PROFILE_FILE
  # shellcheck disable=SC1090
  source "$PROFILE_FILE"
fi
set +a

# Allow ONLY from env if not given via flag.
ONLY_FILTER="${ONLY_FILTER:-${ONLY:-}}"

step_path() { printf "scripts/%s\n" "$1"; }

filter_steps() {
  local -n in_arr=$1 out_arr=$2
  local only="$3"
  if [[ -z "$only" ]]; then
    out_arr=("${in_arr[@]}")
    return
  fi
  IFS=',' read -r -a tokens <<< "$only"
  out_arr=()
  for s in "${in_arr[@]}"; do
    local base num t
    base=$(basename "$s")
    num=${base%%_*}
    for t in "${tokens[@]}"; do
      if [[ "$t" == "$num" || "$t" == "$base" ]]; then
        out_arr+=("$s")
        break
      fi
    done
  done
}

add_step() {
  local -n _arr="$1"
  local s="$2"
  [[ -f "$s" ]] || return 0
  _arr+=("$s")
}

add_step_if() {
  local arr_name="$1"
  local cond="$2" s="$3"
  [[ "$cond" == "true" ]] || return 0
  add_step "$arr_name" "$s"
}

FEAT_VERIFY="${FEAT_VERIFY:-true}"
FEAT_VERIFY_DEEP="${FEAT_VERIFY_DEEP:-false}"
if [[ "$FEAT_VERIFY" != "true" && "$FEAT_VERIFY" != "false" ]]; then
  echo "[ERROR] FEAT_VERIFY must be true or false (got: $FEAT_VERIFY)" >&2
  exit 1
fi
if [[ "$FEAT_VERIFY_DEEP" != "true" && "$FEAT_VERIFY_DEEP" != "false" ]]; then
  echo "[ERROR] FEAT_VERIFY_DEEP must be true or false (got: $FEAT_VERIFY_DEEP)" >&2
  exit 1
fi
if [[ "$FEAT_VERIFY" != "true" ]]; then
  FEAT_VERIFY_DEEP="false"
fi

build_apply_plan() {
  local -n out_arr=$1
  out_arr=()

  # 1) Prerequisites
  add_step out_arr "$(step_path 01_check_prereqs.sh)"

  # 2) Cluster Access (kubeconfig)
  add_step out_arr "$(step_path 15_verify_cluster_access.sh)"

  # 3) Environment & Config Contract
  add_step_if out_arr "$FEAT_VERIFY" "$(step_path 94_verify_config_inputs.sh)"

  # 4) Cluster Dependencies
  add_step out_arr "$(step_path 25_prepare_helm_repositories.sh)"
  add_step out_arr "$(step_path 20_reconcile_platform_namespaces.sh)"
  add_step_if out_arr "${FEAT_CILIUM:-true}" "$(step_path 26_manage_cilium_lifecycle.sh)"

  # 5) Platform Services
  add_step out_arr "$(step_path 31_sync_helmfile_phase_core.sh)"
  add_step out_arr "$(step_path 29_prepare_platform_runtime_inputs.sh)"
  add_step out_arr "$(step_path 36_sync_helmfile_phase_platform.sh)"
  # Service mesh scripts removed (Linkerd/Istio) to keep platform focused and reduce surface area.

  # 6) Test Application
  add_step out_arr "$(step_path 75_manage_sample_app_lifecycle.sh)"

  # 7) Verification
  # Core verification gates (default)
  add_step_if out_arr "$FEAT_VERIFY" "$(step_path 91_verify_platform_state.sh)"
  add_step_if out_arr "$FEAT_VERIFY" "$(step_path 92_verify_helmfile_drift.sh)"
  # Extra verification/diagnostics (opt-in)
  add_step_if out_arr "$FEAT_VERIFY_DEEP" "$(step_path 90_verify_runtime_smoke.sh)"
  add_step_if out_arr "$FEAT_VERIFY_DEEP" "$(step_path 93_verify_expected_releases.sh)"
  add_step_if out_arr "$FEAT_VERIFY_DEEP" "$(step_path 95_capture_cluster_diagnostics.sh)"
  add_step_if out_arr "$FEAT_VERIFY_DEEP" "$(step_path 96_verify_orchestrator_contract.sh)"
}

build_delete_plan() {
  local -n out_arr=$1
  out_arr=()

  # Preflight: require kube context (do not pass --delete).
  add_step out_arr "$(step_path 15_verify_cluster_access.sh)"

  # Reverse platform components (apps -> services -> finalizers).
  add_step out_arr "$(step_path 75_manage_sample_app_lifecycle.sh)"

  add_step out_arr "$(step_path 36_sync_helmfile_phase_platform.sh)"
  add_step out_arr "$(step_path 29_prepare_platform_runtime_inputs.sh)"
  add_step out_arr "$(step_path 31_sync_helmfile_phase_core.sh)"
  add_step out_arr "$(step_path 30_manage_cert_manager_cleanup.sh)"
  # Service mesh scripts removed (Linkerd/Istio) to keep platform focused and reduce surface area.

  # Remove the namespaces Helm release record (namespaces themselves are deleted in 99_execute_teardown.sh).
  add_step out_arr "$(step_path 20_reconcile_platform_namespaces.sh)"

  # Deterministic finalizers: teardown -> cilium -> verify.
  add_step out_arr "$(step_path 99_execute_teardown.sh)"
  add_step_if out_arr "${FEAT_CILIUM:-true}" "$(step_path 26_manage_cilium_lifecycle.sh)"
  add_step out_arr "$(step_path 98_verify_teardown_clean.sh)"
}

print_plan() {
  local -n steps=$1
  echo "Plan:"

  # Phase headings are informational only; script order is the source of truth.
  if [[ "$DELETE_MODE" != true ]]; then
    echo "  - 1) Prerequisites & verify"
    echo "  - 2) Basic Kubernetes Cluster (kubeconfig)"
    echo "  - 3) Environment (profiles/secrets) & verification"
    echo "  - 4) Cluster deps (helm/cilium) & verification"
    echo "  - 5) Platform services & verification"
    echo "  - 6) Test application & verification"
    echo "  - 7) Cluster verification suite"
  else
    echo "  - Delete (reverse phases; cilium last)"
  fi

  for s in "${steps[@]}"; do
    local base
    base=$(basename "$s")
    if [[ "$DELETE_MODE" == true && "$base" != "15_verify_cluster_access.sh" ]]; then
      printf "  - %s (delete)\n" "$base"
```

```bash
sed -n '220,420p' run.sh
```

```output
      printf "  - %s (delete)\n" "$base"
    else
      printf "  - %s\n" "$base"
    fi
  done
}

run_step() {
  local s="$1"
  local base
  base=$(basename "$s")

  if [[ ! -x "$s" ]]; then
    echo "[skip] $base (not executable or missing)"
    return 0
  fi

  # Feature gates for direct --only execution and readability.
  case "$base" in
    26_manage_cilium_lifecycle.sh)
      [[ "${FEAT_CILIUM:-true}" == "true" || "$DELETE_MODE" == true ]] || { echo "[skip] $base (FEAT_CILIUM=false)"; return 0; } ;;
    29_prepare_platform_runtime_inputs.sh)
      # Contains internal feature gates for individual resources.
      ;;
    31_sync_helmfile_phase_core.sh|36_sync_helmfile_phase_platform.sh)
      ;;
    91_verify_platform_state.sh|92_verify_helmfile_drift.sh|94_verify_config_inputs.sh)
      [[ "$DELETE_MODE" == false && "$FEAT_VERIFY" == "true" ]] || { echo "[skip] $base (FEAT_VERIFY=false or delete mode)"; return 0; } ;;
    90_verify_runtime_smoke.sh|93_verify_expected_releases.sh|95_capture_cluster_diagnostics.sh|96_verify_orchestrator_contract.sh)
      [[ "$DELETE_MODE" == false && "$FEAT_VERIFY_DEEP" == "true" ]] || { echo "[skip] $base (FEAT_VERIFY_DEEP=false or delete mode)"; return 0; } ;;
    98_verify_teardown_clean.sh|99_execute_teardown.sh)
      [[ "$DELETE_MODE" == true ]] || { echo "[skip] $base (delete-only)"; return 0; } ;;
  esac

  if [[ "$DELETE_MODE" == true ]]; then
    if [[ "$base" == "15_verify_cluster_access.sh" ]]; then
      echo "[run] $base"
      "$s"
    else
      echo "[run] $base --delete"
      "$s" --delete
    fi
  else
    echo "[run] $base"
    "$s"
  fi
}

if [[ "$DELETE_MODE" == true ]]; then
  build_delete_plan ALL_STEPS
else
  build_apply_plan ALL_STEPS
fi

filter_steps ALL_STEPS SELECTED_STEPS "$ONLY_FILTER"

print_plan SELECTED_STEPS

if [[ "$DRY_RUN" == true ]]; then
  echo "Dry run: exiting without executing."
  exit 0
fi

for s in "${SELECTED_STEPS[@]}"; do
  run_step "$s"
done

echo "Done."
```

## 2) Orchestrator entrypoint (`run.sh`)

`run.sh` is the control plane for script execution. It does four critical things:
1. Parses mode/filters (`--delete`, `--dry-run`, `--only`, `--profile`).
2. Loads env with the same layering as `00_lib.sh` so planning matches execution.
3. Builds an explicit ordered plan (`build_apply_plan` or `build_delete_plan`).
4. Executes each selected script, forwarding `--delete` where appropriate.

`./scripts/02_print_plan.sh` is a thin wrapper over `./run.sh --dry-run`.

A dry-run illustrates exactly which scripts will execute for the current feature flags.

```bash
sed -n '1,80p' scripts/02_print_plan.sh && echo && ./run.sh --dry-run
```

```output
#!/usr/bin/env bash
set -Eeuo pipefail

# Convenience wrapper to print the current phase plan (and delete plan).
# Keep logic centralized in run.sh so docs and automation don't drift.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

exec ./run.sh --dry-run "$@"


Plan:
  - 1) Prerequisites & verify
  - 2) Basic Kubernetes Cluster (kubeconfig)
  - 3) Environment (profiles/secrets) & verification
  - 4) Cluster deps (helm/cilium) & verification
  - 5) Platform services & verification
  - 6) Test application & verification
  - 7) Cluster verification suite
  - 01_check_prereqs.sh
  - 15_verify_cluster_access.sh
  - 94_verify_config_inputs.sh
  - 25_prepare_helm_repositories.sh
  - 20_reconcile_platform_namespaces.sh
  - 26_manage_cilium_lifecycle.sh
  - 31_sync_helmfile_phase_core.sh
  - 29_prepare_platform_runtime_inputs.sh
  - 36_sync_helmfile_phase_platform.sh
  - 75_manage_sample_app_lifecycle.sh
  - 91_verify_platform_state.sh
  - 92_verify_helmfile_drift.sh
Dry run: exiting without executing.
```

## 3) Apply path, phase by phase

### Phase 1-3: prerequisites, cluster reachability, config contract

- `01_check_prereqs.sh` ensures required CLIs exist.
- `15_verify_cluster_access.sh` confirms the active kube context can reach the API.
- `94_verify_config_inputs.sh` enforces base + feature-specific required variables before any cluster mutation.

```bash
sed -n '1,200p' scripts/15_verify_cluster_access.sh
```

```output
#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

CTX="$(kubectl config current-context)"

if kubectl config get-contexts "$CTX" >/dev/null 2>&1; then
  log_info "Using kubectl context: $CTX"
  kubectl config use-context "$CTX" >/dev/null
else
  log_warn "Context $CTX not found; keeping current context"
fi

ensure_context
log_info "Kube context verified"
```

```bash
sed -n '1,240p' scripts/94_verify_config_inputs.sh
```

```output
#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done
if [[ "$DELETE" == true ]]; then
  log_info "Config contract check is apply-only; skipping in delete mode"
  exit 0
fi

ok(){ printf "[OK] %s\n" "$1"; }
bad(){ printf "[BAD] %s\n" "$1"; fail=1; }
need(){ local k="$1"; local v="${!k:-}"; [[ -n "$v" ]] && ok "$k is set" || bad "$k is set"; }

fail=0
printf "== Config Contract ==\n"
for key in "${VERIFY_REQUIRED_BASE_VARS[@]}"; do
  need "$key"
done
[[ "${!VERIFY_REQUIRED_TIMEOUT_VAR:-}" =~ ^[0-9]+$ ]] && ok "${VERIFY_REQUIRED_TIMEOUT_VAR} is numeric" || bad "${VERIFY_REQUIRED_TIMEOUT_VAR} is numeric"

if [[ "${CLUSTER_ISSUER:-}" == "letsencrypt" ]]; then
  need ACME_EMAIL
  if is_allowed_letsencrypt_env "${LETSENCRYPT_ENV:-staging}"; then
    ok "LETSENCRYPT_ENV is valid"
  else
    bad "LETSENCRYPT_ENV must be staging|prod|production"
  fi
fi

printf "\n== Feature Contracts ==\n"
while IFS= read -r key; do
  [[ -n "$key" ]] || continue
  need "$key"
done < <(verify_required_vars_for_enabled_features)

printf "\n== Effective (non-secret) ==\n"
while IFS= read -r key; do
  [[ -n "$key" ]] || continue
  printf "%s=%s\n" "$key" "${!key:-}"
done < <(verify_effective_non_secret_vars)

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

echo
ok "Config contract verification passed"
```

```bash
sed -n '1,200p' scripts/01_check_prereqs.sh
```

```output
#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

log_info "Checking prerequisites"

# Required
need_cmd kubectl
need_cmd helm
need_cmd helmfile
need_cmd curl
need_cmd rg
need_cmd envsubst
need_cmd base64
need_cmd hexdump

# Optional
command -v jq >/dev/null 2>&1 || log_warn "jq not found (optional)"
command -v yq >/dev/null 2>&1 || log_warn "yq not found (optional)"
command -v sops >/dev/null 2>&1 || log_warn "sops not found (optional for secrets)"
command -v age >/dev/null 2>&1 || log_warn "age not found (optional for secrets)"

log_info "All checks passed (or warned)."
```

### Phase 4: cluster dependencies (Helm repos, namespaces, Cilium)

- `25_prepare_helm_repositories.sh` adds only repos needed by enabled features.
- `20_reconcile_platform_namespaces.sh` adopts pre-existing namespaces into Helm ownership, then syncs local chart `platform-namespaces`.
- `26_manage_cilium_lifecycle.sh` handles Cilium install/delete imperatively around Helmfile (flannel guard, namespace adoption, force cleanup fallback).

```bash
sed -n '1,240p' scripts/20_reconcile_platform_namespaces.sh
```

```output
#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

ensure_context

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done
if [[ "$DELETE" == true ]]; then
  # This removes the Helm release record without deleting namespaces (the chart marks
  # Namespace resources with helm.sh/resource-policy=keep). scripts/99_execute_teardown.sh
  # is responsible for deleting namespaces in a deterministic, gated way.
  log_info "Destroying platform-namespaces Helm release (namespaces are deleted by scripts/99_execute_teardown.sh)"
  helmfile_cmd -l component=platform-namespaces destroy >/dev/null 2>&1 || true
  exit 0
fi

need_cmd helmfile

# Helm refuses to install a chart that renders Namespace objects if those namespaces
# already exist without Helm ownership metadata. On existing clusters, adopt them.
release="platform-namespaces"
release_ns="kube-system"
namespaces=(platform-system ingress cert-manager logging observability storage apps)
for ns in "${namespaces[@]}"; do
  adopt_helm_ownership ns "$ns" "$release" "$release_ns"
done

# Namespaces are managed declaratively via a local Helm chart so the platform can
# rely on them existing before applying secrets/config.
helmfile_cmd -l component=platform-namespaces sync
log_info "Namespaces ensured (helmfile: component=platform-namespaces)"
```

```bash
sed -n '1,220p' scripts/25_prepare_helm_repositories.sh
```

```output
#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

need_cmd helm

add_repo() {
  local name="$1" url="$2" required="${3:-true}"
  local tries=0 max_tries="${HELM_REPO_RETRIES:-3}"
  while true; do
    # --force-update makes reruns idempotent (updates URL if repo already exists).
    if helm repo add "$name" "$url" --force-update >/dev/null 2>&1; then
      return 0
    fi
    tries=$((tries + 1))
    if (( tries >= max_tries )); then
      if [[ "$required" == "true" ]]; then
        die "Failed to add Helm repo '${name}' (${url}) after ${max_tries} attempts"
      fi
      log_warn "Failed to add optional Helm repo '${name}' (${url}); continuing"
      return 0
    fi
    sleep 2
  done
}

# Only add repos needed for enabled features so transient repo outages don't
# block unrelated installs.
add_repo jetstack https://charts.jetstack.io true
add_repo ingress-nginx https://kubernetes.github.io/ingress-nginx true

if [[ "${FEAT_OAUTH2_PROXY:-true}" == "true" ]]; then
  add_repo oauth2-proxy https://oauth2-proxy.github.io/manifests true
fi
if [[ "${FEAT_CILIUM:-true}" == "true" ]]; then
  add_repo cilium https://helm.cilium.io/ true
fi
if [[ "${FEAT_CLICKSTACK:-true}" == "true" ]]; then
  add_repo clickstack https://clickhouse.github.io/ClickStack-helm-charts true
fi
if [[ "${FEAT_OTEL_K8S:-true}" == "true" ]]; then
  add_repo open-telemetry https://open-telemetry.github.io/opentelemetry-helm-charts true
fi
if [[ "${FEAT_MINIO:-true}" == "true" ]]; then
  add_repo minio https://charts.min.io/ true
fi

helm repo update >/dev/null 2>&1 || true
log_info "Helm repositories added/updated"
```

```bash
sed -n '1,320p' scripts/26_manage_cilium_lifecycle.sh
```

```output
#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

if [[ "${FEAT_CILIUM:-true}" != "true" && "$DELETE" != true ]]; then
  log_info "FEAT_CILIUM=false; skipping install"
  exit 0
fi

if [[ "$DELETE" == true ]]; then
  log_info "Uninstalling cilium"
  destroy_release cilium >/dev/null 2>&1 || true
  log_info "Attempting force cleanup of labeled cilium resources"
  kubectl -n kube-system delete ds cilium cilium-envoy --ignore-not-found >/dev/null 2>&1 || true
  kubectl -n kube-system delete deploy cilium-operator hubble-relay hubble-ui --ignore-not-found >/dev/null 2>&1 || true
  kubectl -n kube-system delete svc cilium-envoy hubble-peer hubble-relay hubble-ui --ignore-not-found >/dev/null 2>&1 || true

  # Hubble UI TLS secret is created by cert-manager (ingress-shim) and may not be removed
  # by Helm uninstall when cert-manager/CRDs are deleted earlier in the teardown sequence.
  kubectl -n kube-system delete secret hubble-ui-tls --ignore-not-found >/dev/null 2>&1 || true

  kubectl -n kube-system delete deploy,ds,svc,cm,secret,sa,role,rolebinding \
    -l app.kubernetes.io/part-of=cilium --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete clusterrole,clusterrolebinding \
    -l app.kubernetes.io/part-of=cilium --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete ciliumidentity,ciliumendpoint,ciliumnode,ciliumnetworkpolicy,ciliumclusterwidenetworkpolicy,ciliumcidrgroup,ciliuml2announcementpolicy,ciliumloadbalancerippool,ciliumnodeconfig,ciliumpodippool \
    --all --ignore-not-found >/dev/null 2>&1 || true
  if [[ "${CILIUM_DELETE_CRDS:-true}" == "true" ]]; then
    crds="$(kubectl get crd -o name 2>/dev/null | rg '\.cilium\.io$' || true)"
    if [[ -n "$crds" ]]; then
      log_info "Deleting Cilium CRDs"
      # shellcheck disable=SC2086
      kubectl delete $crds --ignore-not-found || true
    fi
  fi
  kubectl -n kube-system wait --for=delete pod -l app.kubernetes.io/part-of=cilium --timeout=60s >/dev/null 2>&1 || true
  leftover_pods="$(kubectl -n kube-system get pod -l app.kubernetes.io/part-of=cilium -o name 2>/dev/null || true)"
  if [[ -n "$leftover_pods" ]]; then
    log_warn "Force deleting stuck Cilium pods"
    # shellcheck disable=SC2086
    kubectl -n kube-system delete $leftover_pods --force --grace-period=0 --ignore-not-found >/dev/null 2>&1 || true
    kubectl -n kube-system wait --for=delete pod -l app.kubernetes.io/part-of=cilium --timeout=30s >/dev/null 2>&1 || true
  fi

  # cilium-secrets is created/owned by Cilium. Remove it as part of Cilium teardown so
  # reruns converge cleanly.
  kubectl delete ns cilium-secrets --ignore-not-found >/dev/null 2>&1 || true
  exit 0
fi

if [[ "${CILIUM_SKIP_FLANNEL_CHECK:-false}" != "true" ]]; then
  flannel_backend=$(kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.annotations.flannel\.alpha\.coreos\.com/backend-type}{"\n"}{end}' | grep -v '^$' | head -n 1 || true)
  if [[ -n "$flannel_backend" ]]; then
    log_error "Detected flannel backend on nodes (${flannel_backend})."
    log_error "Cilium requires k3s with flannel disabled: --flannel-backend=none --disable-network-policy"
    log_error "Reinstall k3s, then rerun this step. Set CILIUM_SKIP_FLANNEL_CHECK=true to override."
    exit 1
  fi
fi

# The Cilium chart manages `cilium-secrets` and Helm will refuse to install if the
# namespace already exists without matching ownership metadata.
if kubectl get ns cilium-secrets >/dev/null 2>&1; then
  # If a previous run removed cilium-secrets, it may be Terminating briefly.
  if [[ -n "$(kubectl get ns cilium-secrets -o jsonpath='{.metadata.deletionTimestamp}' 2>/dev/null || true)" ]]; then
    log_info "Waiting for namespace cilium-secrets to finish terminating"
    kubectl wait --for=delete ns/cilium-secrets --timeout=120s >/dev/null 2>&1 || true
  fi
  adopt_helm_ownership ns cilium-secrets cilium kube-system
fi

sync_release cilium

wait_ds kube-system cilium
wait_deploy kube-system cilium-operator

if kubectl -n kube-system get deploy hubble-relay >/dev/null 2>&1; then
  wait_deploy kube-system hubble-relay
fi
if kubectl -n kube-system get deploy hubble-ui >/dev/null 2>&1; then
  wait_deploy kube-system hubble-ui
fi
log_info "cilium installed"
```

### Phase 5: core and platform services

`31_sync_helmfile_phase_core.sh` installs `phase=core` releases, waits for cert-manager webhook readiness, then installs `phase=core-issuers`.

`29_prepare_platform_runtime_inputs.sh` creates Secrets/ConfigMaps consumed by charts (`oauth2-proxy-secret`, `hyperdx-secret`, `otel-config-vars`, `minio-creds`) and labels them as platform-managed.

`36_sync_helmfile_phase_platform.sh` syncs all `phase=platform` releases.

`30_manage_cert_manager_cleanup.sh` is primarily used on delete for finalizer/CRD cleanup.

```bash
sed -n '1,320p' scripts/30_manage_cert_manager_cleanup.sh
```
```output
#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

if [[ "$DELETE" == true ]]; then
  log_info "Uninstalling cert-manager"
  destroy_release cert-manager >/dev/null 2>&1 || true
  # If cert-manager controllers are already gone, ACME resources can stay
  # stuck on finalizers and block CRD deletion. Clear and delete instances first.
  for r in \
    certificates.cert-manager.io \
    certificaterequests.cert-manager.io \
    orders.acme.cert-manager.io \
    challenges.acme.cert-manager.io \
    issuers.cert-manager.io \
    clusterissuers.cert-manager.io
  do
    mapfile -t objs < <(kubectl get "$r" -A -o name 2>/dev/null || true)
    for obj in "${objs[@]}"; do
      [[ -z "$obj" ]] && continue
      kubectl patch "$obj" --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true
      kubectl delete "$obj" --ignore-not-found --wait=false >/dev/null 2>&1 || true
    done
  done
  if [[ "${CM_DELETE_CRDS:-true}" == "true" ]]; then
    crds="$(kubectl get crd -o name 2>/dev/null | rg 'cert-manager\.io|acme\.cert-manager\.io' || true)"
    if [[ -n "$crds" ]]; then
      log_info "Deleting cert-manager CRDs"
      # shellcheck disable=SC2086
      kubectl delete $crds --ignore-not-found --wait=false || true
    fi
  fi
  exit 0
fi

sync_release cert-manager

kubectl -n cert-manager wait --for=condition=Available deploy/cert-manager --timeout=${TIMEOUT_SECS:-300}s
kubectl -n cert-manager wait --for=condition=Available deploy/cert-manager-webhook --timeout=${TIMEOUT_SECS:-300}s
kubectl -n cert-manager wait --for=condition=Available deploy/cert-manager-cainjector --timeout=${TIMEOUT_SECS:-300}s

# Ensure webhook CA bundle is injected before proceeding to issuer creation
if ! wait_webhook_cabundle cert-manager-webhook "${TIMEOUT_SECS:-300}"; then
  log_warn "Webhook CA bundle not ready; restarting webhook/cainjector and retrying"
  kubectl -n cert-manager rollout restart deploy/cert-manager-webhook deploy/cert-manager-cainjector >/dev/null 2>&1 || true
  kubectl -n cert-manager rollout status deploy/cert-manager-webhook --timeout=${TIMEOUT_SECS:-300}s
  kubectl -n cert-manager rollout status deploy/cert-manager-cainjector --timeout=${TIMEOUT_SECS:-300}s
  if ! wait_webhook_cabundle cert-manager-webhook "${TIMEOUT_SECS:-300}"; then
    die "cert-manager webhook CA bundle still not ready; retry later or inspect cert-manager-webhook"
  fi
fi

log_info "cert-manager installed"
```

```bash
sed -n '1,320p' scripts/29_prepare_platform_runtime_inputs.sh
```

```output
#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

is_managed() {
  local ns="$1" kind="$2" name="$3"
  kubectl -n "$ns" get "$kind" "$name" -o jsonpath='{.metadata.labels.platform\.swhurl\.io/managed}' 2>/dev/null | rg -q '^true$'
}

delete_if_managed() {
  local ns="$1" kind="$2" name="$3"
  if kubectl -n "$ns" get "$kind" "$name" >/dev/null 2>&1; then
    if is_managed "$ns" "$kind" "$name"; then
      log_info "Deleting managed ${ns}/${kind}/${name}"
      kubectl -n "$ns" delete "$kind" "$name" --ignore-not-found >/dev/null 2>&1 || true
    else
      log_warn "Skipping delete of ${ns}/${kind}/${name} (missing label platform.swhurl.io/managed=true)"
    fi
  fi
}

if [[ "$DELETE" == true ]]; then
  log_info "Deleting platform config resources (secrets/configmaps)"
  delete_if_managed ingress secret oauth2-proxy-secret
  delete_if_managed logging secret hyperdx-secret
  delete_if_managed logging configmap otel-config-vars
  delete_if_managed storage secret minio-creds
  exit 0
fi

# oauth2-proxy secret (required by oauth2-proxy Helm release)
if [[ "${FEAT_OAUTH2_PROXY:-true}" == "true" ]]; then
  [[ -n "${OIDC_ISSUER:-}" ]] || die "OIDC_ISSUER is required when FEAT_OAUTH2_PROXY=true"
  [[ -n "${OIDC_CLIENT_ID:-}" ]] || die "OIDC_CLIENT_ID is required when FEAT_OAUTH2_PROXY=true"
  [[ -n "${OIDC_CLIENT_SECRET:-}" ]] || die "OIDC_CLIENT_SECRET is required when FEAT_OAUTH2_PROXY=true"

  kubectl_ns ingress

  log_info "Ensuring ingress/Secret oauth2-proxy-secret (OIDC client + cookie secret)"

  # oauth2-proxy expects a cookie secret that is exactly 16, 24, or 32 bytes.
  # Create-once semantics:
  # - If OAUTH_COOKIE_SECRET is set: enforce it and update the Secret.
  # - Else: do not mutate an existing Secret (avoids auth outages on rerun).
  GEN_COOKIE_SECRET() { hexdump -v -e '/1 "%02X"' -n 16 /dev/urandom; }
  if kubectl -n ingress get secret oauth2-proxy-secret >/dev/null 2>&1; then
    if [[ -n "${OAUTH_COOKIE_SECRET:-}" ]]; then
      kubectl -n ingress create secret generic oauth2-proxy-secret \
        --from-literal=client-id="${OIDC_CLIENT_ID}" \
        --from-literal=client-secret="${OIDC_CLIENT_SECRET}" \
        --from-literal=cookie-secret="${OAUTH_COOKIE_SECRET}" \
        --dry-run=client -o yaml | kubectl apply -f -
    else
      log_info "oauth2-proxy-secret already exists and OAUTH_COOKIE_SECRET is unset; leaving cookie secret unchanged"
    fi
  else
    COOKIE_SECRET_VAL="${OAUTH_COOKIE_SECRET:-$(GEN_COOKIE_SECRET)}"
    kubectl -n ingress create secret generic oauth2-proxy-secret \
      --from-literal=client-id="${OIDC_CLIENT_ID}" \
      --from-literal=client-secret="${OIDC_CLIENT_SECRET}" \
      --from-literal=cookie-secret="${COOKIE_SECRET_VAL}" \
      --dry-run=client -o yaml | kubectl apply -f -
  fi

  label_managed ingress secret oauth2-proxy-secret

  # Verify secret exists and cookie-secret length is valid (16/24/32)
  for i in {1..10}; do
    if kubectl -n ingress get secret oauth2-proxy-secret >/dev/null 2>&1; then
      LEN=$(kubectl -n ingress get secret oauth2-proxy-secret -o jsonpath='{.data.cookie-secret}' | base64 -d | wc -c | tr -d '[:space:]')
      if [[ "$LEN" == "16" || "$LEN" == "24" || "$LEN" == "32" ]]; then
        break
      fi
    fi
    sleep 1
  done
  LEN=$(kubectl -n ingress get secret oauth2-proxy-secret -o jsonpath='{.data.cookie-secret}' | base64 -d | wc -c | tr -d '[:space:]' || echo 0)
  if [[ "$LEN" != "16" && "$LEN" != "24" && "$LEN" != "32" ]]; then
    die "oauth2-proxy-secret not created or invalid cookie-secret length ($LEN)"
  fi
fi

# Kubernetes OTel collectors config (required by otel-k8s Helm releases)
if [[ "${FEAT_OTEL_K8S:-true}" == "true" ]]; then
  kubectl_ns logging

  OTLP_ENDPOINT="${CLICKSTACK_OTEL_ENDPOINT:-http://clickstack-otel-collector.observability.svc.cluster.local:4318}"
  INGESTION_KEY="${CLICKSTACK_INGESTION_KEY:-}"
  [[ -n "$INGESTION_KEY" ]] || die "CLICKSTACK_INGESTION_KEY is required when FEAT_OTEL_K8S=true"

  log_info "Ensuring logging/Secret hyperdx-secret (HYPERDX_API_KEY from CLICKSTACK_INGESTION_KEY)"
  kubectl -n logging create secret generic hyperdx-secret \
    --from-literal=HYPERDX_API_KEY="$INGESTION_KEY" \
    --dry-run=client -o yaml | kubectl apply -f -

  log_info "Ensuring logging/ConfigMap otel-config-vars (HYPERDX_OTLP_ENDPOINT)"
  kubectl -n logging create configmap otel-config-vars \
    --from-literal=HYPERDX_OTLP_ENDPOINT="$OTLP_ENDPOINT" \
    --dry-run=client -o yaml | kubectl apply -f -

  label_managed logging secret hyperdx-secret
  label_managed logging configmap otel-config-vars
fi

# MinIO credentials secret (required by MinIO Helm release)
if [[ "${FEAT_MINIO:-true}" == "true" ]]; then
  kubectl_ns storage
  [[ -n "${MINIO_ROOT_USER:-}" ]] || die "MINIO_ROOT_USER is required when FEAT_MINIO=true"
  [[ -n "${MINIO_ROOT_PASSWORD:-}" ]] || die "MINIO_ROOT_PASSWORD is required when FEAT_MINIO=true"

  log_info "Ensuring storage/Secret minio-creds (existingSecret for MinIO chart)"
  kubectl -n storage create secret generic minio-creds \
    --from-literal=rootUser="${MINIO_ROOT_USER}" \
    --from-literal=rootPassword="${MINIO_ROOT_PASSWORD}" \
    --dry-run=client -o yaml | kubectl apply -f -

  label_managed storage secret minio-creds
fi

log_info "Platform config resources ensured"
```

```bash
sed -n '1,200p' scripts/36_sync_helmfile_phase_platform.sh
```

```output
#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

if [[ "$DELETE" == true ]]; then
  log_info "Destroying platform Helm releases (phase=platform)"
  helmfile_cmd -l phase=platform destroy >/dev/null 2>&1 || true
  exit 0
fi

log_info "Syncing platform Helm releases (phase=platform)"
helmfile_cmd -l phase=platform sync
log_info "Platform Helm releases synced"

```

### Phase 6: sample app

`75_manage_sample_app_lifecycle.sh` converges the local `apps-hello` chart and adopts existing Deployment/Service/Ingress (and Certificate when CRD exists) to avoid ownership conflicts.

```bash
sed -n '1,220p' scripts/75_manage_sample_app_lifecycle.sh
```

```output
#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

if [[ "$DELETE" == true ]]; then
  log_info "Destroying sample app (helmfile: component=apps-hello)"
  helmfile_cmd -l component=apps-hello destroy >/dev/null 2>&1 || true
  exit 0
fi

need_cmd helmfile

log_info "Syncing sample app (helmfile: component=apps-hello)"
release="hello-web"
release_ns="apps"
for kind in deploy svc ingress; do
  adopt_helm_ownership "$kind" hello-web "$release" "$release_ns" "$release_ns"
done
if kubectl api-resources --api-group=cert-manager.io -o name 2>/dev/null | rg -q '(^|[.])certificates([.]|$)'; then
  if kubectl -n "$release_ns" get certificate hello-web >/dev/null 2>&1; then
    adopt_helm_ownership certificate hello-web "$release" "$release_ns" "$release_ns"
  fi
fi
helmfile_cmd -l component=apps-hello sync

HOST="$(kubectl -n apps get ingress hello-web -o jsonpath='{.spec.rules[0].host}' 2>/dev/null || true)"
if [[ -n "$HOST" ]]; then
  log_info "Sample app deployed at https://${HOST}"
else
  log_info "Sample app deployed"
fi
```

### Phase 7: verification gates

Default verification (`FEAT_VERIFY=true`):
- `91_verify_platform_state.sh`: compares live cluster state to expected config and suggests rerun scripts.
- `92_verify_helmfile_drift.sh`: lint/template/dry-run/diff gate with ignore rules for known Cilium cert churn.

Optional deep verification (`FEAT_VERIFY_DEEP=true`):
- `90_verify_runtime_smoke.sh`
- `93_verify_expected_releases.sh`
- `95_capture_cluster_diagnostics.sh`
- `96_verify_orchestrator_contract.sh`

```bash
sed -n '1,320p' scripts/92_verify_helmfile_drift.sh
```

```output
#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done
if [[ "$DELETE" == true ]]; then
  log_info "Helmfile validation is apply-only; skipping in delete mode"
  exit 0
fi

ensure_context
need_cmd helmfile
need_cmd helm

# helmfile diff relies on the helm-diff plugin. Fail fast with clear guidance.
if ! helm diff --help >/dev/null 2>&1; then
  die "helm diff plugin not found. Install via: helm plugin install https://github.com/databus23/helm-diff"
fi

log_info "Verifying Helmfile environment '${HELMFILE_ENV:-default}'"
log_info "Assuming Helm repos are already configured (run scripts/25_prepare_helm_repositories.sh first)"

log_info "Running helmfile lint"
helmfile_cmd lint

rendered="$(mktemp)"
trap 'rm -f "$rendered"' EXIT

log_info "Rendering desired manifests (helmfile template)"
helmfile_cmd template > "$rendered"

SERVER_DRY_RUN="${HELMFILE_SERVER_DRY_RUN:-true}" # true|false
if [[ "$SERVER_DRY_RUN" != "true" && "$SERVER_DRY_RUN" != "false" ]]; then
  die "HELMFILE_SERVER_DRY_RUN must be true or false (got: ${SERVER_DRY_RUN})"
fi

if [[ "$SERVER_DRY_RUN" == "false" ]]; then
  log_info "Skipping server dry-run (HELMFILE_SERVER_DRY_RUN=false); running client dry-run only"
  kubectl apply --dry-run=client -f "$rendered" >/dev/null
  log_info "Template/dry-run validation passed"
else
  log_info "Validating rendered manifests against live API (kubectl dry-run=server)"
srv_err="$(mktemp)"
cli_err="$(mktemp)"
if ! kubectl apply --dry-run=server -f "$rendered" >/dev/null 2>"$srv_err"; then
  log_warn "Server dry-run failed; trying client dry-run fallback"
  # Print a short excerpt so failures are actionable without dumping huge output.
  lines="${HELMFILE_SERVER_DRY_RUN_DEBUG_LINES:-60}"
  if [[ -s "$srv_err" ]]; then
    log_warn "Server dry-run error excerpt (matching lines):"
    rg -n '^(Error from server|error:|Error:)' "$srv_err" | head -n 50 >&2 || true
    log_warn "Server dry-run stderr tail (non-warning, last ${lines} lines):"
    rg -v '^Warning:' "$srv_err" | tail -n "$lines" >&2 || true
  fi
  if ! kubectl apply --dry-run=client -f "$rendered" >/dev/null 2>"$cli_err"; then
    if [[ -s "$cli_err" ]]; then
      log_error "Client dry-run also failed (matching lines):"
      rg -n '^(Error from server|error:|Error:)' "$cli_err" | head -n 50 >&2 || true
      log_error "Client dry-run stderr tail (non-warning, last ${lines} lines):"
      rg -v '^Warning:' "$cli_err" | tail -n "$lines" >&2 || true
    fi
    exit 1
  fi
else
  # Server dry-run may emit warnings on stderr; keep quiet by default.
  if [[ "${HELMFILE_SERVER_DRY_RUN_PRINT_WARNINGS:-false}" == "true" && -s "$srv_err" ]]; then
    lines="${HELMFILE_SERVER_DRY_RUN_DEBUG_LINES:-60}"
    log_warn "Server dry-run warnings excerpt (first ${lines} lines):"
    sed -n "1,${lines}p" "$srv_err" >&2 || true
  fi
fi
rm -f "$srv_err" "$cli_err" || true

log_info "Template/dry-run validation passed"
fi

# Drift check: helmfile diff should be the final gate for "declarative == live".
# We keep suppression knobs to avoid known non-actionable noise (e.g. some Secret churn).
log_info "Running helmfile diff (drift check)"
diff_out="$(mktemp)"
diff_clean="$(mktemp)"
trap 'rm -f "$rendered" "$diff_out" "$diff_clean"' EXIT

DIFF_CONTEXT="${HELMFILE_DIFF_CONTEXT:-3}"
DIFF_SUPPRESS_SECRETS="${HELMFILE_DIFF_SUPPRESS_SECRETS:-true}"
DIFF_SUPPRESS_OBJECTS="${HELMFILE_DIFF_SUPPRESS_OBJECTS:-}" # comma-separated kinds, e.g. Secret
DIFF_SUPPRESS_LINE_REGEX="${HELMFILE_DIFF_SUPPRESS_OUTPUT_LINE_REGEX:-}" # comma-separated regexes

diff_args=(diff --detailed-exitcode --context "$DIFF_CONTEXT" --skip-deps)
if [[ "$DIFF_SUPPRESS_SECRETS" == "true" ]]; then
  diff_args+=(--suppress-secrets)
fi

IFS=',' read -r -a suppress_objs <<< "$DIFF_SUPPRESS_OBJECTS"
for o in "${suppress_objs[@]}"; do
  [[ -n "$o" ]] || continue
  diff_args+=(--suppress "$o")
done

IFS=',' read -r -a suppress_lines <<< "$DIFF_SUPPRESS_LINE_REGEX"
for r in "${suppress_lines[@]}"; do
  [[ -n "$r" ]] || continue
  diff_args+=(--suppress-output-line-regex "$r")
done

set +e
helmfile_cmd "${diff_args[@]}" >"$diff_out" 2>&1
rc=$?
set -e

# Strip ANSI color sequences so parsing/printing is stable.
sed -r 's/\x1B\[[0-9;]*[[:alpha:]]//g' "$diff_out" >"$diff_clean" || cp "$diff_out" "$diff_clean"

if [[ "$rc" -eq 0 ]]; then
  log_info "No Helmfile drift detected"
  exit 0
fi
if [[ "$rc" -ne 2 ]]; then
  log_error "helmfile diff failed (exit=$rc)"
  sed -n '1,200p' "$diff_clean" >&2 || true
  exit "$rc"
fi

# Exit 2 means "diff exists". Treat known non-actionable churn as ignorable, but fail on anything else.
#
# Known non-actionable churn:
# - Cilium rotates these TLS/CA secrets and helm-diff will always show changes (with secret content suppressed).
# Resource headers look like:
#   <ns>, <name>, <Kind> (<apiVersion>) has changed:
resource_headers="$(rg -n '^[^,]+, [^,]+, .* has (changed|been added|been removed):$' "$diff_clean" | sed -E 's/^[0-9]+://g' || true)"

# Filter out ignored resource headers.
actionable_headers="$resource_headers"
for h in "${VERIFY_HELMFILE_IGNORED_RESOURCE_HEADERS[@]}"; do
  actionable_headers="$(printf "%s\n" "$actionable_headers" | rg -v -F "$h" || true)"
done
actionable_headers="$(printf "%s\n" "$actionable_headers" | rg -v '^$' || true)"

# Also treat "diff exists but all output suppressed/boilerplate" as non-actionable.
boilerplate_stripped="$(rg -v '^(Comparing release=|Affected releases are:|Identified at least one change|$)' "$diff_clean" || true)"

if [[ -z "$boilerplate_stripped" ]]; then
  log_info "Helmfile reported changes but output was fully suppressed/boilerplate-only"
  exit 0
fi

if [[ -z "$resource_headers" ]]; then
  log_error "Helmfile reported drift but resource header parsing failed; refusing to auto-ignore"
  sed -n '1,200p' "$diff_clean" >&2 || true
  exit 1
fi

if [[ -z "$actionable_headers" ]]; then
  log_info "Helmfile reported changes but they are fully ignorable/noise-only"
  log_info "Ignored drift: Cilium CA/Hubble cert secret churn"
  exit 0
fi

log_error "Helmfile drift detected"
sed -n '1,200p' "$diff_clean" >&2 || true
exit 1
```

```bash
sed -n '1,320p' scripts/96_verify_orchestrator_contract.sh
```

```output
#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done
if [[ "$DELETE" == true ]]; then
  log_info "Orchestrator contract check is apply-only; skipping in delete mode"
  exit 0
fi

ok(){ printf "[OK] %s\n" "$1"; }
bad(){ printf "[BAD] %s\n" "$1"; fail=1; }

fail=0
printf "== Orchestrator Contract Verification ==\n"

run="$SCRIPT_DIR/../run.sh"
root="$SCRIPT_DIR/.."
config_file="$root/config.env"
helmfile_file="$root/helmfile.yaml.gotmpl"

# Verify the supported default pipeline uses the Helmfile phase scripts.
for s in 31_sync_helmfile_phase_core.sh 29_prepare_platform_runtime_inputs.sh 36_sync_helmfile_phase_platform.sh; do
  p="$SCRIPT_DIR/$s"
  if [[ -f "$p" ]]; then
    ok "$s: present"
  else
    bad "$s: present"
  fi
done

if rg -q '31_sync_helmfile_phase_core\.sh' "$run" && rg -q '29_prepare_platform_runtime_inputs\.sh' "$run" && rg -q '36_sync_helmfile_phase_platform\.sh' "$run"; then
  ok "run.sh: phase scripts wired into plan"
else
  bad "run.sh: phase scripts wired into plan"
fi

if rg -q 'helmfile_cmd -l phase=core (sync|destroy)' "$SCRIPT_DIR/31_sync_helmfile_phase_core.sh"; then
  ok "31_sync_helmfile_phase_core.sh: uses helmfile_cmd with phase=core label selection"
else
  bad "31_sync_helmfile_phase_core.sh: uses helmfile_cmd with phase=core label selection"
fi
if rg -q 'helmfile_cmd -l phase=platform (sync|destroy)' "$SCRIPT_DIR/36_sync_helmfile_phase_platform.sh"; then
  ok "36_sync_helmfile_phase_platform.sh: uses helmfile_cmd with phase=platform label selection"
else
  bad "36_sync_helmfile_phase_platform.sh: uses helmfile_cmd with phase=platform label selection"
fi

# Release-specific scripts should keep using shared Helmfile helpers if/when they exist.
for s in 26_manage_cilium_lifecycle.sh 30_manage_cert_manager_cleanup.sh; do
  p="$SCRIPT_DIR/$s"
  if rg -q 'sync_release ' "$p"; then
    ok "$s: sync_release path present"
  else
    bad "$s: sync_release path present"
  fi
  if rg -q 'destroy_release ' "$p"; then
    ok "$s: destroy_release path present"
  else
    bad "$s: destroy_release path present"
  fi
done

printf "\n== Feature Registry Contracts ==\n"

allowed_non_feature_flags=(FEAT_VERIFY FEAT_VERIFY_DEEP)
mapfile -t registry_flags < <(feature_registry_flags | rg -v '^$' | sort -u)
if [[ "${#registry_flags[@]}" -eq 0 ]]; then
  bad "feature registry exposes FEAT_* flags"
else
  ok "feature registry exposes FEAT_* flags"
fi

# Every FEAT_* in config/profiles should be represented by the registry
# unless it is an orchestration-only toggle.
config_sources=("$config_file")
for p in "$root"/profiles/*.env; do
  [[ -f "$p" ]] || continue
  config_sources+=("$p")
done

mapfile -t declared_feat_flags < <(
  awk -F= '/^[[:space:]]*FEAT_[A-Z0-9_]+[[:space:]]*=/{gsub(/[[:space:]]/,"",$1); print $1}' "${config_sources[@]}" | sort -u
)
for flag in "${declared_feat_flags[@]}"; do
  if name_matches_any_pattern "$flag" "${registry_flags[@]}"; then
    continue
  fi
  if name_matches_any_pattern "$flag" "${allowed_non_feature_flags[@]}"; then
    continue
  fi
  bad "${flag}: declared in config/profiles but missing from feature registry"
done
ok "All feature FEAT_* flags in config/profiles are covered by the registry"

# Ensure config.env carries defaults for each registered feature flag.
mapfile -t config_defaults < <(
  awk -F= '/^[[:space:]]*FEAT_[A-Z0-9_]+[[:space:]]*=/{gsub(/[[:space:]]/,"",$1); print $1}' "$config_file" | sort -u
)
for flag in "${registry_flags[@]}"; do
  if name_matches_any_pattern "$flag" "${config_defaults[@]}"; then
    ok "${flag}: default present in config.env"
  else
    bad "${flag}: missing default in config.env"
  fi
done

# Parse release refs from helmfile (<namespace>/<name>).
mapfile -t helmfile_releases < <(
  awk '
    /^[[:space:]]*-[[:space:]]name:[[:space:]]*/{
      name=$0
      sub(/^[[:space:]]*-[[:space:]]name:[[:space:]]*/,"",name)
      gsub(/"/,"",name)
      next
    }
    /^[[:space:]]*namespace:[[:space:]]*/{
      ns=$0
      sub(/^[[:space:]]*namespace:[[:space:]]*/,"",ns)
      gsub(/"/,"",ns)
      if(name!=""){print ns "/" name; name=""}
    }
  ' "$helmfile_file" | sort -u
)

for key in "${FEATURE_KEYS[@]}"; do
  mapfile -t expected_releases < <(feature_expected_releases "$key")
  if [[ "${#expected_releases[@]}" -eq 0 ]]; then
    bad "feature '${key}' has no expected release mapping"
  fi
  for rel in "${expected_releases[@]}"; do
    if printf '%s\n' "${helmfile_releases[@]}" | grep -qx "$rel"; then
      ok "feature '${key}' release mapped in helmfile: ${rel}"
    else
      bad "feature '${key}' release missing from helmfile: ${rel}"
    fi
  done
done

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

echo
ok "Orchestrator contract verification passed"
```

```bash
sed -n '1,260p' scripts/93_verify_expected_releases.sh
```

```output
#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done
if [[ "$DELETE" == true ]]; then
  log_info "Release inventory check is apply-only; skipping in delete mode"
  exit 0
fi

ensure_context
need_cmd helm

say() { printf "\n== %s ==\n" "$1"; }
ok() { printf "[OK] %s\n" "$1"; }
bad() { printf "[BAD] %s\n" "$1"; fail=1; }
warn() { printf "[WARN] %s\n" "$1"; }

fail=0
say "Required Releases"

mapfile -t expected < <(verify_expected_releases)

mapfile -t actual < <(helm list -A --no-headers 2>/dev/null | awk '{print $2"/"$1}' | sort -u)

for item in "${expected[@]}"; do
  if printf '%s\n' "${actual[@]}" | grep -qx "$item"; then
    ok "$item"
  else
    bad "$item"
  fi
done

STRICT_EXTRAS="${VERIFY_RELEASE_STRICT_EXTRAS:-false}"
if [[ "$STRICT_EXTRAS" != "true" && "$STRICT_EXTRAS" != "false" ]]; then
  bad "VERIFY_RELEASE_STRICT_EXTRAS must be true or false (got: ${STRICT_EXTRAS})"
fi

if [[ "$STRICT_EXTRAS" == "true" ]]; then
  say "Unexpected Releases"
  RELEASE_SCOPE="${VERIFY_RELEASE_SCOPE:-platform}" # platform|cluster
  case "$RELEASE_SCOPE" in
    platform|cluster) ;;
    *) bad "VERIFY_RELEASE_SCOPE must be one of: platform, cluster (got: ${RELEASE_SCOPE})" ;;
  esac

  allow_patterns=("${VERIFY_RELEASE_ALLOWLIST_DEFAULT[@]}")
  if [[ -n "${VERIFY_RELEASE_ALLOWLIST:-}" ]]; then
    IFS=',' read -r -a extra_allow <<< "${VERIFY_RELEASE_ALLOWLIST}"
    for p in "${extra_allow[@]}"; do
      [[ -n "$p" ]] || continue
      allow_patterns+=("$p")
    done
  fi

  extras_found=0
  for item in "${actual[@]}"; do
    if printf '%s\n' "${expected[@]}" | grep -qx "$item"; then
      continue
    fi
    if [[ "$RELEASE_SCOPE" == "platform" ]] && ! is_release_in_platform_scope "$item"; then
      continue
    fi
    if name_matches_any_pattern "$item" "${allow_patterns[@]}"; then
      warn "Allowlisted extra release: ${item}"
      continue
    fi
    bad "Unexpected release: ${item}"
    extras_found=1
  done
  if [[ "$extras_found" -eq 0 ]]; then
    ok "No unexpected releases (scope: ${RELEASE_SCOPE})"
  fi
else
  warn "Skipping unexpected release checks (VERIFY_RELEASE_STRICT_EXTRAS=false)"
fi

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

echo
ok "Release inventory verification passed"
```

```bash
sed -n '1,260p' scripts/91_verify_platform_state.sh
```

```output
#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

ensure_context

fail=0
declare -a SUGGEST=()

say() { printf "\n== %s ==\n" "$1"; }
ok() { printf "[OK] %s\n" "$1"; }
warn() { printf "[WARN] %s\n" "$1"; }
mismatch() { printf "[MISMATCH] %s\n" "$1"; fail=1; }

add_suggest() {
  local s="$1"
  for e in "${SUGGEST[@]:-}"; do
    [[ "$e" == "$s" ]] && return 0
  done
  SUGGEST+=("$s")
}

check_eq() {
  local label="$1" expected="$2" actual="$3" suggest="$4"
  if [[ "$expected" == "$actual" ]]; then
    ok "$label: $actual"
  else
    mismatch "$label: expected=$expected actual=$actual"
    [[ -n "$suggest" ]] && add_suggest "$suggest"
  fi
}

say "ClusterIssuer"
case "${CLUSTER_ISSUER:-selfsigned}" in
  letsencrypt)
    if [[ -z "${ACME_EMAIL:-}" ]]; then
      warn "ACME_EMAIL is empty; cannot validate letsencrypt email"
    elif kubectl get clusterissuer letsencrypt >/dev/null 2>&1; then
      actual_email=$(kubectl get clusterissuer letsencrypt -o jsonpath='{.spec.acme.email}')
      check_eq "letsencrypt.email" "${ACME_EMAIL}" "$actual_email" "scripts/31_sync_helmfile_phase_core.sh"
      expected_server="$(verify_expected_letsencrypt_server "${LETSENCRYPT_ENV:-staging}")"
      actual_server=$(kubectl get clusterissuer letsencrypt -o jsonpath='{.spec.acme.server}')
      check_eq "letsencrypt.server" "${expected_server}" "$actual_server" "scripts/31_sync_helmfile_phase_core.sh"
      if kubectl get clusterissuer letsencrypt-staging >/dev/null 2>&1; then
        ok "letsencrypt-staging ClusterIssuer present"
      else
        mismatch "ClusterIssuer letsencrypt-staging not found"
        add_suggest "scripts/31_sync_helmfile_phase_core.sh"
      fi
      if kubectl get clusterissuer letsencrypt-prod >/dev/null 2>&1; then
        ok "letsencrypt-prod ClusterIssuer present"
      else
        mismatch "ClusterIssuer letsencrypt-prod not found"
        add_suggest "scripts/31_sync_helmfile_phase_core.sh"
      fi
    else
      mismatch "ClusterIssuer letsencrypt not found"
      add_suggest "scripts/31_sync_helmfile_phase_core.sh"
    fi
    ;;
  selfsigned)
    if kubectl get clusterissuer selfsigned >/dev/null 2>&1; then
      ok "selfsigned ClusterIssuer present"
    else
      mismatch "ClusterIssuer selfsigned not found"
      add_suggest "scripts/31_sync_helmfile_phase_core.sh"
    fi
    ;;
  *)
    warn "Unknown CLUSTER_ISSUER=${CLUSTER_ISSUER}"
    ;;
esac

say "Cilium"
if feature_is_enabled cilium; then
  if kubectl -n kube-system get ds cilium >/dev/null 2>&1; then
    ok "cilium daemonset present"
  else
    mismatch "cilium daemonset not found"
    add_suggest "scripts/26_manage_cilium_lifecycle.sh"
  fi
  if kubectl -n kube-system get deploy cilium-operator >/dev/null 2>&1; then
    ok "cilium-operator deployment present"
  else
    mismatch "cilium-operator deployment not found"
    add_suggest "scripts/26_manage_cilium_lifecycle.sh"
  fi
else
  ok "$(feature_flag_var cilium)=false; skipping"
fi

say "Hubble"
if feature_is_enabled cilium; then
  if kubectl -n kube-system get deploy hubble-relay >/dev/null 2>&1; then
    ok "hubble-relay deployment present"
  else
    mismatch "hubble-relay deployment not found"
    add_suggest "scripts/26_manage_cilium_lifecycle.sh"
  fi
  if kubectl -n kube-system get deploy hubble-ui >/dev/null 2>&1; then
    ok "hubble-ui deployment present"
  else
    mismatch "hubble-ui deployment not found"
    add_suggest "scripts/26_manage_cilium_lifecycle.sh"
  fi
  if kubectl -n kube-system get ingress hubble-ui >/dev/null 2>&1; then
    actual_host=$(kubectl -n kube-system get ingress hubble-ui -o jsonpath='{.spec.rules[0].host}')
    actual_issuer=$(kubectl -n kube-system get ingress hubble-ui -o jsonpath='{.metadata.annotations.cert-manager\.io/cluster-issuer}')
    check_eq "hubble-ui.host" "${HUBBLE_HOST:-}" "$actual_host" "scripts/26_manage_cilium_lifecycle.sh"
    check_eq "hubble-ui.issuer" "${CLUSTER_ISSUER:-}" "$actual_issuer" "scripts/26_manage_cilium_lifecycle.sh"
    if feature_is_enabled oauth2_proxy; then
      expected_auth_url="$(verify_oauth_auth_url "${OAUTH_HOST}")"
      expected_auth_signin="$(verify_oauth_auth_signin "${OAUTH_HOST}")"
      actual_auth_url=$(kubectl -n kube-system get ingress hubble-ui -o jsonpath='{.metadata.annotations.nginx\.ingress\.kubernetes\.io/auth-url}')
      actual_auth_signin=$(kubectl -n kube-system get ingress hubble-ui -o jsonpath='{.metadata.annotations.nginx\.ingress\.kubernetes\.io/auth-signin}')
      check_eq "hubble-ui.auth-url" "${expected_auth_url}" "$actual_auth_url" "scripts/26_manage_cilium_lifecycle.sh"
      check_eq "hubble-ui.auth-signin" "${expected_auth_signin}" "$actual_auth_signin" "scripts/26_manage_cilium_lifecycle.sh"
    else
      ok "$(feature_flag_var oauth2_proxy)=false; skipping hubble-ui auth annotation checks"
    fi
  else
    mismatch "hubble-ui ingress not found"
    add_suggest "scripts/26_manage_cilium_lifecycle.sh"
  fi
else
  ok "$(feature_flag_var cilium)=false; skipping"
fi

say "ingress-nginx"
if kubectl -n ingress get svc ingress-nginx-controller >/dev/null 2>&1; then
  actual_svc_type=$(kubectl -n ingress get svc ingress-nginx-controller -o jsonpath='{.spec.type}')
  check_eq "service.type" "$VERIFY_INGRESS_SERVICE_TYPE" "$actual_svc_type" "scripts/31_sync_helmfile_phase_core.sh"
  actual_http_np=$(kubectl -n ingress get svc ingress-nginx-controller -o jsonpath='{.spec.ports[?(@.name=="http")].nodePort}')
  actual_https_np=$(kubectl -n ingress get svc ingress-nginx-controller -o jsonpath='{.spec.ports[?(@.name=="https")].nodePort}')
  check_eq "nodePort.http" "$VERIFY_INGRESS_NODEPORT_HTTP" "$actual_http_np" "scripts/31_sync_helmfile_phase_core.sh"
  check_eq "nodePort.https" "$VERIFY_INGRESS_NODEPORT_HTTPS" "$actual_https_np" "scripts/31_sync_helmfile_phase_core.sh"
else
  mismatch "ingress-nginx service not found"
  add_suggest "scripts/31_sync_helmfile_phase_core.sh"
fi

if kubectl -n ingress get cm ingress-nginx-controller >/dev/null 2>&1; then
  actual_log=$(kubectl -n ingress get cm ingress-nginx-controller -o jsonpath='{.data.log-format-upstream}')
  if [[ -n "$actual_log" ]]; then
    ok "log-format-upstream present"
  else
    mismatch "log-format-upstream missing"
    add_suggest "scripts/31_sync_helmfile_phase_core.sh"
  fi
else
  mismatch "ingress-nginx configmap not found"
  add_suggest "scripts/31_sync_helmfile_phase_core.sh"
fi

if kubectl get ingressclass nginx >/dev/null 2>&1; then
  actual_default=$(kubectl get ingressclass nginx -o jsonpath='{.metadata.annotations.ingressclass\.kubernetes\.io/is-default-class}')
  check_eq "ingressclass.default" "true" "$actual_default" "scripts/31_sync_helmfile_phase_core.sh"
else
  mismatch "ingressclass nginx not found"
  add_suggest "scripts/31_sync_helmfile_phase_core.sh"
fi

say "oauth2-proxy"
if feature_is_enabled oauth2_proxy; then
  if kubectl -n ingress get secret oauth2-proxy-secret >/dev/null 2>&1; then
    ok "oauth2-proxy-secret present"
  else
    mismatch "oauth2-proxy-secret missing"
    add_suggest "scripts/29_prepare_platform_runtime_inputs.sh"
  fi
  if kubectl -n ingress get ingress oauth2-proxy >/dev/null 2>&1; then
    actual_host=$(kubectl -n ingress get ingress oauth2-proxy -o jsonpath='{.spec.rules[0].host}')
    actual_issuer=$(kubectl -n ingress get ingress oauth2-proxy -o jsonpath='{.metadata.annotations.cert-manager\.io/cluster-issuer}')
    check_eq "oauth2-proxy.host" "${OAUTH_HOST:-}" "$actual_host" "scripts/36_sync_helmfile_phase_platform.sh"
    check_eq "oauth2-proxy.issuer" "${CLUSTER_ISSUER:-}" "$actual_issuer" "scripts/36_sync_helmfile_phase_platform.sh"
  else
    mismatch "oauth2-proxy ingress not found"
    add_suggest "scripts/36_sync_helmfile_phase_platform.sh"
  fi
else
  ok "$(feature_flag_var oauth2_proxy)=false; skipping"
fi

say "ClickStack"
if feature_is_enabled clickstack; then
  if kubectl -n observability get ingress clickstack-app-ingress >/dev/null 2>&1; then
    actual_host=$(kubectl -n observability get ingress clickstack-app-ingress -o jsonpath='{.spec.rules[0].host}')
    actual_issuer=$(kubectl -n observability get ingress clickstack-app-ingress -o jsonpath='{.metadata.annotations.cert-manager\.io/cluster-issuer}')
    check_eq "clickstack.host" "${CLICKSTACK_HOST:-}" "$actual_host" "scripts/36_sync_helmfile_phase_platform.sh"
    check_eq "clickstack.issuer" "${CLUSTER_ISSUER:-}" "$actual_issuer" "scripts/36_sync_helmfile_phase_platform.sh"
    if feature_is_enabled oauth2_proxy; then
      expected_auth_url="$(verify_oauth_auth_url "${OAUTH_HOST}")"
      expected_auth_signin="$(verify_oauth_auth_signin "${OAUTH_HOST}")"
      actual_auth_url=$(kubectl -n observability get ingress clickstack-app-ingress -o jsonpath='{.metadata.annotations.nginx\.ingress\.kubernetes\.io/auth-url}')
      actual_auth_signin=$(kubectl -n observability get ingress clickstack-app-ingress -o jsonpath='{.metadata.annotations.nginx\.ingress\.kubernetes\.io/auth-signin}')
      check_eq "clickstack.auth-url" "${expected_auth_url}" "$actual_auth_url" "scripts/36_sync_helmfile_phase_platform.sh"
      check_eq "clickstack.auth-signin" "${expected_auth_signin}" "$actual_auth_signin" "scripts/36_sync_helmfile_phase_platform.sh"
    fi
  else
    mismatch "clickstack ingress not found"
    add_suggest "scripts/36_sync_helmfile_phase_platform.sh"
  fi
  if kubectl -n observability get deploy clickstack-app >/dev/null 2>&1; then
    ok "clickstack app deployment present"
  else
    mismatch "clickstack app deployment not found"
    add_suggest "scripts/36_sync_helmfile_phase_platform.sh"
  fi
  if kubectl -n observability get deploy clickstack-otel-collector >/dev/null 2>&1; then
    ok "clickstack otel collector deployment present"
  else
    mismatch "clickstack otel collector deployment not found"
    add_suggest "scripts/36_sync_helmfile_phase_platform.sh"
  fi
  if kubectl -n observability get deploy clickstack-clickhouse >/dev/null 2>&1; then
    ok "clickstack clickhouse deployment present"
  else
    mismatch "clickstack clickhouse deployment not found"
    add_suggest "scripts/36_sync_helmfile_phase_platform.sh"
  fi
else
  ok "$(feature_flag_var clickstack)=false; skipping"
fi

say "Kubernetes OTel Collectors"
if feature_is_enabled otel_k8s; then
  if kubectl -n logging get ds -l app.kubernetes.io/instance=otel-k8s-daemonset >/dev/null 2>&1; then
    ok "otel-k8s daemonset release present"
  else
    mismatch "otel-k8s daemonset release not found"
    add_suggest "scripts/36_sync_helmfile_phase_platform.sh"
  fi
  if kubectl -n logging get deploy -l app.kubernetes.io/instance=otel-k8s-cluster >/dev/null 2>&1; then
    ok "otel-k8s cluster deployment release present"
  else
    mismatch "otel-k8s cluster deployment release not found"
    add_suggest "scripts/36_sync_helmfile_phase_platform.sh"
  fi
  if kubectl -n logging get secret hyperdx-secret >/dev/null 2>&1; then
    ok "hyperdx-secret present"
  else
    mismatch "hyperdx-secret missing"
    add_suggest "scripts/29_prepare_platform_runtime_inputs.sh"
  fi
  if kubectl -n logging get configmap otel-config-vars >/dev/null 2>&1; then
    ok "otel-config-vars configmap present"
  else
    mismatch "otel-config-vars configmap missing"
    add_suggest "scripts/29_prepare_platform_runtime_inputs.sh"
  fi
  if kubectl -n logging get secret hyperdx-secret >/dev/null 2>&1 && kubectl -n observability get deploy clickstack-otel-collector >/dev/null 2>&1; then
    sender_token="$(kubectl -n logging get secret hyperdx-secret -o jsonpath='{.data.HYPERDX_API_KEY}' 2>/dev/null | base64 -d || true)"
    receiver_token="$(
      kubectl -n observability exec deploy/clickstack-otel-collector -- sh -lc \
        "sed -n '40,60p' /etc/otel/supervisor-data/effective.yaml | sed -n 's/^[[:space:]]*-[[:space:]]*//p' | head -n1" \
        2>/dev/null || true
    )"
    if [[ -n "$sender_token" && -n "$receiver_token" && "$sender_token" != "$receiver_token" ]]; then
      mismatch "otel token mismatch: logging/hyperdx-secret does not match clickstack receiver token"
```

```bash
sed -n '1,220p' scripts/95_capture_cluster_diagnostics.sh
```

```output
#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

ensure_context

timestamp="$(date +%Y%m%d-%H%M%S)"
OUT_DIR="${1:-./artifacts/cluster-diagnostics-${timestamp}}"
mkdir -p "$OUT_DIR"

log_info "Cluster info"
kubectl cluster-info >"$OUT_DIR/cluster-info.txt" 2>&1 || true
kubectl get ns >"$OUT_DIR/namespaces.txt" 2>&1 || true

log_info "Events (last 1h)"
kubectl get events --all-namespaces --sort-by=.lastTimestamp --field-selector=type!=Normal -A | tail -n 200 >"$OUT_DIR/non-normal-events.txt" 2>&1 || true

log_info "Installed releases"
helm list -A >"$OUT_DIR/helm-releases.txt" 2>&1 || true

log_info "Diagnostics written to $OUT_DIR"
```

```bash
sed -n '1,220p' scripts/90_verify_runtime_smoke.sh
```

```output
#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

ensure_context

fail=0
bad() { log_error "$1"; fail=1; }
ok() { log_info "$1"; }

log_info "Smoke tests: node readiness"
kubectl get nodes -o wide
total_nodes="$(kubectl get nodes --no-headers 2>/dev/null | wc -l | tr -d '[:space:]')"
ready_nodes="$(kubectl get nodes --no-headers 2>/dev/null | awk '$2 ~ /Ready/ {c++} END {print c+0}')"
if [[ "$total_nodes" == "0" ]]; then
  bad "No nodes found in cluster"
elif [[ "$ready_nodes" != "$total_nodes" ]]; then
  bad "Not all nodes are Ready (${ready_nodes}/${total_nodes})"
else
  ok "All nodes Ready (${ready_nodes}/${total_nodes})"
fi

log_info "Smoke tests: ingress NodePort wiring"
if kubectl -n ingress get svc ingress-nginx-controller >/dev/null 2>&1; then
  svc_type="$(kubectl -n ingress get svc ingress-nginx-controller -o jsonpath='{.spec.type}')"
  https_np="$(kubectl -n ingress get svc ingress-nginx-controller -o jsonpath='{.spec.ports[?(@.name=="https")].nodePort}')"
  if [[ "$svc_type" != "$VERIFY_INGRESS_SERVICE_TYPE" ]]; then
    bad "ingress-nginx service.type mismatch (expected ${VERIFY_INGRESS_SERVICE_TYPE}, got ${svc_type:-<empty>})"
  elif [[ "$https_np" == "$VERIFY_INGRESS_NODEPORT_HTTPS" ]]; then
    ok "ingress-nginx HTTPS NodePort is ${VERIFY_INGRESS_NODEPORT_HTTPS}"
  else
    bad "ingress-nginx HTTPS NodePort mismatch (expected ${VERIFY_INGRESS_NODEPORT_HTTPS}, got ${https_np:-<empty>})"
  fi
else
  bad "ingress-nginx service not found"
fi

# End-to-end reachability test through ingress-nginx NodePort.
if command -v curl >/dev/null 2>&1; then
  host="${VERIFY_SAMPLE_INGRESS_HOST_PREFIX}.${BASE_DOMAIN}"
  log_info "Smoke tests: HTTPS NodePort ${VERIFY_INGRESS_NODEPORT_HTTPS} -> Host: ${host}"
  set +e
  code="$(curl -sk -o /dev/null -w "%{http_code}" -H "Host: ${host}" "https://127.0.0.1:${VERIFY_INGRESS_NODEPORT_HTTPS}/")"
  set -e
  if [[ "$code" =~ ^[234][0-9][0-9]$ ]]; then
    ok "Ingress HTTPS smoke check returned HTTP ${code}"
  else
    bad "Ingress HTTPS smoke check returned HTTP ${code:-<empty>}"
  fi
else
  log_warn "curl not found; skipping ingress HTTPS smoke check"
fi

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

log_info "Smoke tests passed"
```

## 4) Delete path and teardown safeguards

Delete mode runs a reverse plan and adds hard gates before Cilium teardown:
- `99_execute_teardown.sh` sweeps managed resources, deletes namespaces, blocks on leftover PVC/namespaces, then prunes platform CRDs.
- `26_manage_cilium_lifecycle.sh --delete` force-cleans Cilium resources if needed.
- `98_verify_teardown_clean.sh` asserts no Helm releases, managed namespaces, platform CRDs, or Cilium leftovers remain.

```bash
sed -n '1,320p' scripts/99_execute_teardown.sh
```

```output
#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

if [[ "$DELETE" != true ]]; then
  log_info "Final teardown is delete-only; skipping in apply mode"
  exit 0
fi

ensure_context

managed_namespaces=("${PLATFORM_MANAGED_NAMESPACES[@]}")
NAMESPACE_DELETE_TIMEOUT_SECS="${NAMESPACE_DELETE_TIMEOUT_SECS:-180}"
DELETE_SCOPE="${DELETE_SCOPE:-managed}" # managed | dedicated-cluster

case "$DELETE_SCOPE" in
  managed|dedicated-cluster) ;;
  *) die "DELETE_SCOPE must be one of: managed, dedicated-cluster (got: ${DELETE_SCOPE})" ;;
esac

if [[ "$DELETE_SCOPE" == "dedicated-cluster" ]]; then
  log_warn "DELETE_SCOPE=dedicated-cluster: sweeping secrets cluster-wide (unsafe on shared clusters)"
  secret_rows="$(kubectl get secret -A -o jsonpath='{range .items[*]}{.metadata.namespace}{"/"}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)"
  while IFS= read -r row; do
    [[ -z "$row" ]] && continue
    ns="${row%%/*}"
    name="${row#*/}"
    if is_allowed_k3s_secret_for_teardown "$ns" "$name"; then
      continue
    fi
    kubectl -n "$ns" delete secret "$name" --ignore-not-found >/dev/null 2>&1 || true
  done <<< "$secret_rows"
else
  log_info "Sweeping platform secrets in managed namespaces only (DELETE_SCOPE=managed)"
  for ns in "${managed_namespaces[@]}"; do
    kubectl get ns "$ns" >/dev/null 2>&1 || continue
    kubectl -n "$ns" delete secret -l platform.swhurl.io/managed=true --ignore-not-found >/dev/null 2>&1 || true
  done
fi

log_info "Deleting managed namespaces: ${managed_namespaces[*]}"
for ns in "${managed_namespaces[@]}"; do
  kubectl delete ns "$ns" --ignore-not-found >/dev/null 2>&1 || true
done

log_info "Waiting for managed namespaces to terminate (${NAMESPACE_DELETE_TIMEOUT_SECS}s)"
for ns in "${managed_namespaces[@]}"; do
  kubectl wait --for=delete ns/"$ns" --timeout="${NAMESPACE_DELETE_TIMEOUT_SECS}s" >/dev/null 2>&1 || true
done

leftover_pvcs=()
for ns in "${managed_namespaces[@]}"; do
  rows="$(kubectl -n "$ns" get pvc -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)"
  while IFS= read -r pvc; do
    [[ -z "$pvc" ]] && continue
    leftover_pvcs+=("${ns}/${pvc}")
  done <<< "$rows"
done

ns_left=()
for ns in "${managed_namespaces[@]}"; do
  if kubectl get ns "$ns" >/dev/null 2>&1; then
    ns_left+=("$ns")
  fi
done

if [[ "${#leftover_pvcs[@]}" -gt 0 ]]; then
  log_error "PVCs still present after teardown wait: ${leftover_pvcs[*]}"
fi
if [[ "${#ns_left[@]}" -gt 0 ]]; then
  log_error "Namespaces still present after wait: ${ns_left[*]}"
fi

if [[ "${#leftover_pvcs[@]}" -gt 0 || "${#ns_left[@]}" -gt 0 ]]; then
  die "Refusing to continue delete while non-k3s workloads are still terminating. Resolve stuck PVC/namespace teardown before deleting Cilium."
fi

log_info "Deleting platform CRDs (cert-manager/acme/cilium)"
crds="$(kubectl get crd -o name 2>/dev/null | rg "$PLATFORM_CRD_NAME_REGEX" || true)"
if [[ -n "$crds" ]]; then
  # shellcheck disable=SC2086
  kubectl delete $crds --ignore-not-found >/dev/null 2>&1 || true
fi

log_info "Final teardown: k3s uninstall is not automatic."
log_info "Manual: sudo /usr/local/bin/k3s-uninstall.sh (server)"
if [[ "${K3S_UNINSTALL:-false}" == "true" ]]; then
  if command -v sudo >/dev/null 2>&1 && [[ -x "/usr/local/bin/k3s-uninstall.sh" ]]; then
    log_info "Running k3s-uninstall.sh via sudo (K3S_UNINSTALL=true)"
    sudo /usr/local/bin/k3s-uninstall.sh || true
  else
    log_warn "sudo or /usr/local/bin/k3s-uninstall.sh not available; skipping"
  fi
fi

log_info "Teardown complete"
```

```bash
sed -n '1,280p' scripts/98_verify_teardown_clean.sh
```

```output
#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

if [[ "$DELETE" != true ]]; then
  log_info "Delete-clean verification only applies in --delete mode; skipping"
  exit 0
fi

ensure_context

fail=0
bad() { printf "[BAD] %s\n" "$1"; fail=1; }
ok() { printf "[OK] %s\n" "$1"; }
DELETE_SCOPE="${DELETE_SCOPE:-managed}" # managed | dedicated-cluster

case "$DELETE_SCOPE" in
  managed|dedicated-cluster) ;;
  *) bad "DELETE_SCOPE must be one of: managed, dedicated-cluster (got: ${DELETE_SCOPE})" ;;
esac

# 1) No Helm releases should remain.
if [[ "$(helm list -A -q | wc -l | tr -d '[:space:]')" == "0" ]]; then
  ok "No Helm releases remain"
else
  bad "Helm releases still present"
  helm list -A || true
fi

# 2) Managed namespaces should be gone.
managed_namespaces=("${PLATFORM_MANAGED_NAMESPACES[@]}")
ns_left=()
for ns in "${managed_namespaces[@]}"; do
  if kubectl get ns "$ns" >/dev/null 2>&1; then
    ns_left+=("$ns")
  fi
done
if [[ "${#ns_left[@]}" -eq 0 ]]; then
  ok "Managed namespaces removed"
else
  bad "Managed namespaces still present: ${ns_left[*]}"
fi

# 2b) Cilium-owned namespace should be removed after cilium delete.
if kubectl get ns cilium-secrets >/dev/null 2>&1; then
  bad "Cilium namespace still present: cilium-secrets"
else
  ok "Cilium namespace removed: cilium-secrets"
fi

# 3) Cilium/cert-manager CRDs should be gone.
if kubectl get crd -o name | rg -q "$PLATFORM_CRD_NAME_REGEX"; then
  bad "Platform CRDs still present"
  kubectl get crd -o name | rg "$PLATFORM_CRD_NAME_REGEX" || true
else
  ok "Platform CRDs removed"
fi

# 4) Non-k3s-native secrets should be gone.
secret_rows=$(kubectl get secret -A -o jsonpath='{range .items[*]}{.metadata.namespace}{"/"}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)
non_native=()
while IFS= read -r row; do
  [[ -z "$row" ]] && continue
  ns="${row%%/*}"
  name="${row#*/}"

  if [[ "$DELETE_SCOPE" == "managed" ]]; then
    # kube-system is normally excluded in managed scope, but this secret is created by the
    # platform (Cilium Hubble UI + cert-manager ingress-shim) and should be deleted.
    if [[ "$ns" == "kube-system" && "$name" == "hubble-ui-tls" ]]; then
      non_native+=("$row")
      continue
    fi

    # Only enforce cleanup expectations for platform-managed secrets in managed namespaces.
    is_platform_managed_namespace "$ns" || continue
    if ! kubectl -n "$ns" get secret "$name" -o jsonpath='{.metadata.labels.platform\.swhurl\.io/managed}' 2>/dev/null | rg -q '^true$'; then
      continue
    fi
  else
    # dedicated-cluster: enforce cluster-wide cleanup (unsafe on shared clusters).
    if is_allowed_k3s_secret_for_verify "$ns" "$name"; then
      continue
    fi
  fi

  # everything else is treated as leftover
  non_native+=("$row")
done <<< "$secret_rows"

if [[ "${#non_native[@]}" -eq 0 ]]; then
  ok "No platform-managed secrets remain (scope: ${DELETE_SCOPE})"
else
  bad "Non-k3s-native secrets still present: ${non_native[*]}"
fi

# 5) No Cilium/Hubble resources should remain in kube-system.
kubectl -n kube-system wait --for=delete pod -l app.kubernetes.io/part-of=cilium --timeout=60s >/dev/null 2>&1 || true
cilium_left="$(kubectl -n kube-system get all -l app.kubernetes.io/part-of=cilium -o name 2>/dev/null || true)"
if [[ -z "$cilium_left" ]]; then
  ok "No Cilium resources remain in kube-system"
else
  bad "Cilium resources still present in kube-system"
  printf "%s\n" "$cilium_left"
fi

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

ok "Delete-clean verification passed"
```

```bash
./run.sh --dry-run --delete
```

```output
Plan:
  - Delete (reverse phases; cilium last)
  - 15_verify_cluster_access.sh
  - 75_manage_sample_app_lifecycle.sh (delete)
  - 36_sync_helmfile_phase_platform.sh (delete)
  - 29_prepare_platform_runtime_inputs.sh (delete)
  - 31_sync_helmfile_phase_core.sh (delete)
  - 30_manage_cert_manager_cleanup.sh (delete)
  - 20_reconcile_platform_namespaces.sh (delete)
  - 99_execute_teardown.sh (delete)
  - 26_manage_cilium_lifecycle.sh (delete)
  - 98_verify_teardown_clean.sh (delete)
Dry run: exiting without executing.
```

```bash
sed -n '1,260p' helmfile.yaml.gotmpl
```

```output
helmDefaults:
  wait: true
  waitForJobs: true
  timeout: {{ env "TIMEOUT_SECS" | default "300" }}
  createNamespace: true
  historyMax: 10

environments:
  default:
    values:
      - environments/common.yaml.gotmpl
      - environments/default.yaml
  minimal:
    values:
      - environments/common.yaml.gotmpl
      - environments/minimal.yaml

---

releases:
  - name: platform-namespaces
    namespace: kube-system
    chart: ./charts/platform-namespaces
    installed: true
    labels:
      app: platform-namespaces
      tier: bootstrap
      component: platform-namespaces

  - name: cilium
    namespace: kube-system
    chart: cilium/cilium
    version: 1.19.0
    installed: {{ .Environment.Values.features.cilium }}
    labels:
      app: cilium
      tier: network
      component: cilium
    values:
      - infra/values/cilium-helmfile.yaml.gotmpl

  - name: cert-manager
    namespace: cert-manager
    chart: jetstack/cert-manager
    version: v1.19.3
    needs:
      - kube-system/platform-namespaces
    labels:
      app: cert-manager
      tier: security
      phase: core
      component: cert-manager
    values:
      - infra/values/cert-manager-helmfile.yaml.gotmpl

  - name: platform-issuers
    namespace: kube-system
    chart: ./charts/platform-issuers
    installed: true
    needs:
      - cert-manager/cert-manager
    labels:
      app: platform-issuers
      tier: security
      phase: core-issuers
      component: platform-issuers
    values:
      - infra/values/platform-issuers-helmfile.yaml.gotmpl

  - name: ingress-nginx
    namespace: ingress
    chart: ingress-nginx/ingress-nginx
    version: 4.14.3
    needs:
      - cert-manager/cert-manager
    labels:
      app: ingress-nginx
      tier: ingress
      phase: core
      component: ingress-nginx
    values:
      - infra/values/ingress-nginx-logging.yaml

  - name: oauth2-proxy
    namespace: ingress
    chart: oauth2-proxy/oauth2-proxy
    version: 10.1.3
    installed: {{ .Environment.Values.features.oauth2Proxy }}
    needs:
      - ingress/ingress-nginx
      - cert-manager/cert-manager
    labels:
      app: oauth2-proxy
      tier: ingress
      phase: platform
      component: oauth2-proxy
    values:
      - infra/values/oauth2-proxy-helmfile.yaml.gotmpl

  - name: clickstack
    namespace: observability
    chart: clickstack/clickstack
    version: 1.1.1
    installed: {{ .Environment.Values.features.clickstack }}
    needs:
      - ingress/ingress-nginx
      - cert-manager/cert-manager
    labels:
      app: clickstack
      tier: observability
      phase: platform
      component: clickstack
    values:
      - infra/values/clickstack-helmfile.yaml.gotmpl

  - name: otel-k8s-daemonset
    namespace: logging
    chart: open-telemetry/opentelemetry-collector
    version: 0.145.0
    installed: {{ .Environment.Values.features.otelK8s }}
    needs:
      - observability/clickstack
    labels:
      app: otel-k8s-daemonset
      tier: observability
      phase: platform
      component: otel-k8s-daemonset
    values:
      - infra/values/otel-k8s-daemonset.yaml.gotmpl

  - name: otel-k8s-cluster
    namespace: logging
    chart: open-telemetry/opentelemetry-collector
    version: 0.145.0
    installed: {{ .Environment.Values.features.otelK8s }}
    needs:
      - observability/clickstack
      - logging/otel-k8s-daemonset
    labels:
      app: otel-k8s-cluster
      tier: observability
      phase: platform
      component: otel-k8s-cluster
    values:
      - infra/values/otel-k8s-deployment.yaml.gotmpl

  - name: minio
    namespace: storage
    chart: minio/minio
    version: 5.4.0
    installed: {{ .Environment.Values.features.minio }}
    needs:
      - ingress/ingress-nginx
      - cert-manager/cert-manager
    labels:
      app: minio
      tier: storage
      phase: platform
      component: minio
    values:
      - infra/values/minio-helmfile.yaml.gotmpl

  - name: hello-web
    namespace: apps
    chart: ./charts/apps-hello
    installed: true
    needs:
      - ingress/ingress-nginx
      - kube-system/platform-issuers
    labels:
      app: hello-web
      tier: apps
      component: apps-hello
    values:
      - infra/values/apps-hello-helmfile.yaml.gotmpl
```

## 5) Declarative state: Helmfile environments and releases

`helmfile.yaml.gotmpl` defines all releases. It uses labels (`component=...`, `phase=...`) that scripts target for sync/destroy.

`environments/common.yaml.gotmpl` maps exported env vars into `.Environment.Values`, and `environments/default.yaml` / `minimal.yaml` provide profile-specific toggles.

```bash
sed -n '1,220p' environments/common.yaml.gotmpl && echo && sed -n '1,80p' environments/default.yaml && echo && sed -n '1,120p' environments/minimal.yaml
```

```output
baseDomain: {{ env "BASE_DOMAIN" | default "127.0.0.1.nip.io" | quote }}
issuer: {{ env "CLUSTER_ISSUER" | default "selfsigned" | quote }}
timeoutSecs: {{ env "TIMEOUT_SECS" | default "300" }}
features:
  cilium: {{ eq (env "FEAT_CILIUM" | default "true") "true" }}
  oauth2Proxy: {{ eq (env "FEAT_OAUTH2_PROXY" | default "true") "true" }}
  clickstack: {{ eq (env "FEAT_CLICKSTACK" | default "true") "true" }}
  otelK8s: {{ eq (env "FEAT_OTEL_K8S" | default "true") "true" }}
  minio: {{ eq (env "FEAT_MINIO" | default "true") "true" }}
computed:
  oauthHost: {{ env "OAUTH_HOST" | default (printf "oauth.%s" (env "BASE_DOMAIN" | default "127.0.0.1.nip.io")) | quote }}
  hubbleHost: {{ env "HUBBLE_HOST" | default (printf "hubble.%s" (env "BASE_DOMAIN" | default "127.0.0.1.nip.io")) | quote }}
  clickstackHost: {{ env "CLICKSTACK_HOST" | default (printf "clickstack.%s" (env "BASE_DOMAIN" | default "127.0.0.1.nip.io")) | quote }}
  minioHost: {{ env "MINIO_HOST" | default (printf "minio.%s" (env "BASE_DOMAIN" | default "127.0.0.1.nip.io")) | quote }}
  minioConsoleHost: {{ env "MINIO_CONSOLE_HOST" | default (printf "minio-console.%s" (env "BASE_DOMAIN" | default "127.0.0.1.nip.io")) | quote }}
  clickstackOtelEndpoint: {{ env "CLICKSTACK_OTEL_ENDPOINT" | default "http://clickstack-otel-collector.observability.svc.cluster.local:4318" | quote }}

profileName: default

profileName: minimal
features:
  oauth2Proxy: false
  minio: false
```

```bash
sed -n '1,220p' infra/values/cilium-helmfile.yaml.gotmpl && echo && sed -n '1,220p' infra/values/ingress-nginx-logging.yaml && echo && sed -n '1,220p' infra/values/oauth2-proxy-helmfile.yaml.gotmpl
```

```output
kubeProxyReplacement: "false"
operator:
  replicas: 1
hubble:
  enabled: true
  relay:
    enabled: true
  ui:
    enabled: true
    ingress:
      enabled: true
      className: nginx
      annotations:
        cert-manager.io/cluster-issuer: {{ .Environment.Values.issuer | quote }}
        nginx.ingress.kubernetes.io/auth-url: {{ printf "https://%s/oauth2/auth" .Environment.Values.computed.oauthHost | quote }}
        nginx.ingress.kubernetes.io/auth-signin: {{ printf "https://%s/oauth2/start?rd=$scheme://$host$request_uri" .Environment.Values.computed.oauthHost | quote }}
      hosts:
        - {{ .Environment.Values.computed.hubbleHost | quote }}
      tls:
        - secretName: hubble-ui-tls
          hosts:
            - {{ .Environment.Values.computed.hubbleHost | quote }}

controller:
  replicaCount: 1
  ingressClassResource:
    default: true
  service:
    type: NodePort
    nodePorts:
      http: 31514
      https: 30313
  config:
    # Escape JSON so log fields are valid JSON
    log-format-escape-json: "true"
    # Structured upstream log format (JSON). Includes HTTP and k8s routing metadata.
    log-format-upstream: >-
      {"time":"$time_iso8601","remote_addr":"$remote_addr","x_forward_for":"$proxy_add_x_forwarded_for","request_id":"$req_id","remote_user":"$remote_user","bytes_sent":$bytes_sent,"request_time":$request_time,"status":$status,"host":"$host","uri":"$uri","request":"$request","request_length":$request_length,"method":"$request_method","user_agent":"$http_user_agent","referer":"$http_referer","upstream_addr":"$upstream_addr","upstream_response_time":$upstream_response_time,"upstream_status":$upstream_status,"namespace":"$namespace","ingress":"$ingress_name","service":"$service_name"}

config:
  existingSecret: oauth2-proxy-secret

extraArgs:
  provider: oidc
  oidc-issuer-url: {{ env "OIDC_ISSUER" | quote }}
  redirect-url: {{ env "OAUTH_REDIRECT_URL" | default (printf "https://%s/oauth2/callback" .Environment.Values.computed.oauthHost) | quote }}
  email-domain: "*"
  cookie-domain: {{ printf ".%s" .Environment.Values.baseDomain | quote }}
  whitelist-domain: {{ printf ".%s" .Environment.Values.baseDomain | quote }}
  standard-logging: true
  request-logging: true
  auth-logging: true
  silence-ping-logging: true

ingress:
  enabled: true
  className: nginx
  annotations:
    cert-manager.io/cluster-issuer: {{ .Environment.Values.issuer | quote }}
  hosts:
    - {{ .Environment.Values.computed.oauthHost | quote }}
  tls:
    - secretName: oauth2-proxy-tls
      hosts:
        - {{ .Environment.Values.computed.oauthHost | quote }}
```

```bash
sed -n '1,280p' infra/values/clickstack-helmfile.yaml.gotmpl && echo && sed -n '1,260p' infra/values/minio-helmfile.yaml.gotmpl
```

```output
global:
  storageClassName: local-path

clickhouse:
  persistence:
    enabled: true
    dataSize: 20Gi
    logSize: 5Gi
  prometheus:
    enabled: false

mongodb:
  persistence:
    enabled: true
    dataSize: 10Gi

hyperdx:
  apiKey: {{ env "CLICKSTACK_API_KEY" | quote }}
  frontendUrl: {{ printf "https://%s" .Environment.Values.computed.clickstackHost | quote }}
  ingress:
    enabled: true
    ingressClassName: nginx
    host: {{ .Environment.Values.computed.clickstackHost | quote }}
    path: "/(.*)"
    pathType: ImplementationSpecific
    annotations:
      cert-manager.io/cluster-issuer: {{ .Environment.Values.issuer | quote }}
{{- if .Environment.Values.features.oauth2Proxy }}
      nginx.ingress.kubernetes.io/auth-url: {{ printf "https://%s/oauth2/auth" .Environment.Values.computed.oauthHost | quote }}
      nginx.ingress.kubernetes.io/auth-signin: {{ printf "https://%s/oauth2/start?rd=$scheme://$host$request_uri" .Environment.Values.computed.oauthHost | quote }}
      nginx.ingress.kubernetes.io/auth-response-headers: "X-Auth-Request-User,X-Auth-Request-Email,Authorization"
{{- end }}
    tls:
      enabled: true
      secretName: clickstack-tls

otel:
  enabled: true

image:
  repository: docker.io/minio/minio

mode: standalone
resources:
  requests:
    memory: 512Mi
replicas: 1
persistence:
  enabled: true
  size: 20Gi

existingSecret: minio-creds

ingress:
  enabled: true
  ingressClassName: nginx
  annotations:
    cert-manager.io/cluster-issuer: {{ .Environment.Values.issuer | quote }}
  path: /
  hosts:
    - {{ .Environment.Values.computed.minioHost | quote }}
  tls:
    - secretName: minio-tls
      hosts:
        - {{ .Environment.Values.computed.minioHost | quote }}

consoleIngress:
  enabled: true
  ingressClassName: nginx
  annotations:
    cert-manager.io/cluster-issuer: {{ .Environment.Values.issuer | quote }}
  path: /
  hosts:
    - {{ .Environment.Values.computed.minioConsoleHost | quote }}
  tls:
    - secretName: minio-console-tls
      hosts:
        - {{ .Environment.Values.computed.minioConsoleHost | quote }}
```

```bash
sed -n '1,260p' infra/values/platform-issuers-helmfile.yaml.gotmpl && echo && sed -n '1,280p' infra/values/apps-hello-helmfile.yaml.gotmpl
```

```output
labels:
  platform.swhurl.io/managed: "true"

# Keep the existing external contract:
# - CLUSTER_ISSUER=selfsigned -> install only selfsigned
# - CLUSTER_ISSUER=letsencrypt -> install letsencrypt-staging + letsencrypt-prod + letsencrypt alias (selected by LETSENCRYPT_ENV)
mode: {{ .Environment.Values.issuer | quote }}

letsencrypt:
  # Only used when mode=letsencrypt
  email: {{ env "ACME_EMAIL" | default "" | quote }}
  ingressClass: nginx
  selectedEnv: {{ env "LETSENCRYPT_ENV" | default "staging" | quote }}
  createAlias: true

nameOverride: hello-web

labels:
  platform.swhurl.io/managed: "true"

image:
  repository: {{ env "HELLO_IMAGE_REPO" | default "docker.io/nginx" | quote }}
  tag: {{ env "HELLO_IMAGE_TAG" | default "1.25-alpine" | quote }}

service:
  port: 80

ingress:
  enabled: true
  className: nginx
  host: {{ env "APP_HOST" | default (printf "hello.%s" .Environment.Values.baseDomain) | quote }}
  tlsSecretName: {{ env "TLS_SECRET" | default "hello-web-tls" | quote }}
  annotations:
    nginx.ingress.kubernetes.io/ssl-redirect: "true"
    nginx.ingress.kubernetes.io/force-ssl-redirect: "true"

tls:
  enabled: true
  issuerName: {{ .Environment.Values.issuer | quote }}
  dnsNames:
    - {{ env "APP_HOST" | default (printf "hello.%s" .Environment.Values.baseDomain) | quote }}

auth:
  enabled: {{ if ne (env "APP_AUTH_ENABLED" | default "") "" }}{{ eq (env "APP_AUTH_ENABLED") "true" }}{{ else }}{{ .Environment.Values.features.oauth2Proxy }}{{ end }}
  oauthHost: {{ .Environment.Values.computed.oauthHost | quote }}
```

```bash
sed -n '1,260p' infra/values/otel-k8s-daemonset.yaml.gotmpl && echo && sed -n '1,260p' infra/values/otel-k8s-deployment.yaml.gotmpl
```

```output
mode: daemonset

image:
  repository: otel/opentelemetry-collector-k8s

presets:
  logsCollection:
    enabled: true
    includeCollectorLogs: true
  kubeletMetrics:
    enabled: true
  hostMetrics:
    enabled: true
  kubernetesAttributes:
    enabled: true

resources:
  requests:
    cpu: 100m
    memory: 256Mi
  limits:
    cpu: 500m
    memory: 512Mi

config:
  exporters:
    otlphttp:
      endpoint: ${env:HYPERDX_OTLP_ENDPOINT}
      headers:
        authorization: ${env:HYPERDX_API_KEY}

  service:
    pipelines:
      logs:
        receivers: [otlp, filelog]
        processors: [memory_limiter, k8sattributes, batch]
        exporters: [otlphttp]
      metrics:
        receivers: [kubeletstats, hostmetrics]
        processors: [memory_limiter, k8sattributes, batch]
        exporters: [otlphttp]

extraEnvs:
  - name: HYPERDX_API_KEY
    valueFrom:
      secretKeyRef:
        name: hyperdx-secret
        key: HYPERDX_API_KEY
  - name: HYPERDX_OTLP_ENDPOINT
    valueFrom:
      configMapKeyRef:
        name: otel-config-vars
        key: HYPERDX_OTLP_ENDPOINT

clusterRole:
  create: true

mode: deployment

image:
  repository: otel/opentelemetry-collector-k8s

presets:
  kubernetesEvents:
    enabled: true
  clusterMetrics:
    enabled: true
  kubernetesAttributes:
    enabled: true

resources:
  requests:
    cpu: 100m
    memory: 256Mi
  limits:
    cpu: 500m
    memory: 512Mi

config:
  exporters:
    otlphttp:
      endpoint: ${env:HYPERDX_OTLP_ENDPOINT}
      headers:
        authorization: ${env:HYPERDX_API_KEY}

  service:
    pipelines:
      logs:
        receivers: [k8sobjects]
        processors: [memory_limiter, k8sattributes, batch]
        exporters: [otlphttp]
      metrics:
        receivers: [k8s_cluster]
        processors: [memory_limiter, k8sattributes, batch]
        exporters: [otlphttp]

extraEnvs:
  - name: HYPERDX_API_KEY
    valueFrom:
      secretKeyRef:
        name: hyperdx-secret
        key: HYPERDX_API_KEY
  - name: HYPERDX_OTLP_ENDPOINT
    valueFrom:
      configMapKeyRef:
        name: otel-config-vars
        key: HYPERDX_OTLP_ENDPOINT

clusterRole:
  create: true
```

## 6) Component values and local charts

The scripts orchestrate, but behavior is mostly defined in `infra/values/*.yaml*` and local charts under `charts/`.

Key patterns:
- ingress hosts/TLS issuers are templated from `BASE_DOMAIN`/`CLUSTER_ISSUER`.
- OAuth annotations are injected declaratively when `oauth2Proxy` feature is enabled.
- OTel collectors read endpoint and API key from runtime ConfigMap/Secret created by `29_prepare_platform_runtime_inputs.sh`.

Local charts in this repo provide stable declarative ownership for resources that are easy to keep under version control (namespaces, issuers, sample app).

```bash
sed -n '1,220p' charts/platform-namespaces/values.yaml && echo && sed -n '1,240p' charts/platform-namespaces/templates/namespaces.yaml
```

```output
namespaces:
  - platform-system
  - ingress
  - cert-manager
  - logging
  - observability
  - storage
  - apps
  # NOTE: cilium-secrets is owned by the Cilium chart. Do not manage it here or
  # Helm will fail to install Cilium on clusters where this chart ran first.

labels:
  platform.swhurl.io/managed: "true"

{{- $labels := .Values.labels | default dict -}}
{{- range $i, $ns := .Values.namespaces -}}
{{- if gt $i 0 }}
---
{{- end }}
apiVersion: v1
kind: Namespace
metadata:
  name: {{ $ns | quote }}
  annotations:
    # Prevent accidental early namespace deletion (e.g. if this release is destroyed out of order).
    # Cluster teardown is handled explicitly by scripts/99_execute_teardown.sh.
    "helm.sh/resource-policy": keep
  labels:
{{- range $k, $v := $labels }}
    {{ $k }}: {{ $v | quote }}
{{- end }}
{{- end -}}
```

```bash
sed -n '1,260p' charts/apps-hello/templates/deployment.yaml && echo && sed -n '1,260p' charts/apps-hello/templates/service.yaml && echo && sed -n '1,320p' charts/apps-hello/templates/ingress.yaml && echo && sed -n '1,260p' charts/apps-hello/templates/certificate.yaml
```

```output
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "apps-hello.name" . }}
  labels:
{{ include "apps-hello.labels" . | indent 4 }}
spec:
  replicas: {{ .Values.replicaCount | default 1 }}
  selector:
    matchLabels:
      app.kubernetes.io/name: {{ include "apps-hello.name" . | quote }}
      app.kubernetes.io/instance: {{ .Release.Name | quote }}
  template:
    metadata:
      labels:
        app.kubernetes.io/name: {{ include "apps-hello.name" . | quote }}
        app.kubernetes.io/instance: {{ .Release.Name | quote }}
    spec:
      containers:
        - name: web
          image: {{ printf "%s:%s" (.Values.image.repository | default "docker.io/nginx") (.Values.image.tag | default "latest") | quote }}
          ports:
            - name: http
              containerPort: 80

apiVersion: v1
kind: Service
metadata:
  name: {{ include "apps-hello.name" . }}
  labels:
{{ include "apps-hello.labels" . | indent 4 }}
spec:
  selector:
    app.kubernetes.io/name: {{ include "apps-hello.name" . | quote }}
    app.kubernetes.io/instance: {{ .Release.Name | quote }}
  ports:
    - name: http
      port: {{ .Values.service.port | default 80 }}
      targetPort: http

{{- if .Values.ingress.enabled -}}
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: {{ include "apps-hello.name" . }}
  labels:
{{ include "apps-hello.labels" . | indent 4 }}
  annotations:
    kubernetes.io/ingress.class: {{ .Values.ingress.className | default "nginx" | quote }}
{{- range $k, $v := (.Values.ingress.annotations | default dict) }}
    {{ $k }}: {{ $v | quote }}
{{- end }}
{{- if .Values.auth.enabled }}
    nginx.ingress.kubernetes.io/auth-url: {{ printf "https://%s/oauth2/auth" .Values.auth.oauthHost | quote }}
    nginx.ingress.kubernetes.io/auth-signin: {{ printf "https://%s/oauth2/start?rd=$scheme://$host$request_uri" .Values.auth.oauthHost | quote }}
{{- end }}
spec:
  ingressClassName: {{ .Values.ingress.className | default "nginx" | quote }}
  tls:
    - hosts:
        - {{ .Values.ingress.host | quote }}
      secretName: {{ .Values.ingress.tlsSecretName | default (printf "%s-tls" (include "apps-hello.name" .)) | quote }}
  rules:
    - host: {{ .Values.ingress.host | quote }}
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: {{ include "apps-hello.name" . }}
                port:
                  number: {{ .Values.service.port | default 80 }}
{{- end -}}


{{- if .Values.tls.enabled -}}
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: {{ include "apps-hello.name" . }}
  labels:
{{ include "apps-hello.labels" . | indent 4 }}
spec:
  secretName: {{ .Values.ingress.tlsSecretName | default (printf "%s-tls" (include "apps-hello.name" .)) | quote }}
  issuerRef:
    name: {{ .Values.tls.issuerName | default "selfsigned" | quote }}
    kind: ClusterIssuer
  dnsNames:
{{- range $d := (.Values.tls.dnsNames | default (list .Values.ingress.host)) }}
    - {{ $d | quote }}
{{- end }}
{{- end -}}

```

```bash
sed -n '1,220p' charts/platform-issuers/values.yaml && echo && sed -n '1,280p' charts/platform-issuers/templates/clusterissuer-selfsigned.yaml && echo && sed -n '1,340p' charts/platform-issuers/templates/clusterissuer-letsencrypt.yaml
```

```output
# Controls which issuers to render.
mode: selfsigned # selfsigned | letsencrypt

labels:
  platform.swhurl.io/managed: "true"

selfsigned:
  name: selfsigned

letsencrypt:
  # The two explicit issuers are always rendered in letsencrypt mode.
  stagingName: letsencrypt-staging
  prodName: letsencrypt-prod
  selectedEnv: staging # staging | prod
  aliasName: letsencrypt
  createAlias: true

  email: ""
  ingressClass: nginx

  # Secrets where cert-manager stores the ACME account private keys.
  # Keep these stable so switching env doesn't create unnecessary churn.
  stagingAccountKeySecretName: acme-account-key-staging
  prodAccountKeySecretName: acme-account-key-prod
  aliasAccountKeySecretName: acme-account-key

{{- if eq (.Values.mode | default "selfsigned") "selfsigned" -}}
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: {{ .Values.selfsigned.name | default "selfsigned" | quote }}
  labels:
{{ include "platform-issuers.labels" . | indent 4 }}
spec:
  selfSigned: {}
{{- end -}}


{{- if eq (.Values.mode | default "selfsigned") "letsencrypt" -}}
{{- $email := required "letsencrypt.email is required when mode=letsencrypt" .Values.letsencrypt.email -}}
{{- $class := .Values.letsencrypt.ingressClass | default "nginx" -}}

apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: {{ .Values.letsencrypt.stagingName | default "letsencrypt-staging" | quote }}
  labels:
{{ include "platform-issuers.labels" . | indent 4 }}
spec:
  acme:
    email: {{ $email | quote }}
    server: https://acme-staging-v02.api.letsencrypt.org/directory
    privateKeySecretRef:
      name: {{ .Values.letsencrypt.stagingAccountKeySecretName | default "acme-account-key-staging" | quote }}
    solvers:
      - http01:
          ingress:
            class: {{ $class | quote }}
---
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: {{ .Values.letsencrypt.prodName | default "letsencrypt-prod" | quote }}
  labels:
{{ include "platform-issuers.labels" . | indent 4 }}
spec:
  acme:
    email: {{ $email | quote }}
    server: https://acme-v02.api.letsencrypt.org/directory
    privateKeySecretRef:
      name: {{ .Values.letsencrypt.prodAccountKeySecretName | default "acme-account-key-prod" | quote }}
    solvers:
      - http01:
          ingress:
            class: {{ $class | quote }}

{{- if (.Values.letsencrypt.createAlias | default true) }}
---
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: {{ .Values.letsencrypt.aliasName | default "letsencrypt" | quote }}
  labels:
{{ include "platform-issuers.labels" . | indent 4 }}
spec:
  acme:
    email: {{ $email | quote }}
    server: {{ include "platform-issuers.letsencrypt.server" . | quote }}
    privateKeySecretRef:
      name: {{ .Values.letsencrypt.aliasAccountKeySecretName | default "acme-account-key" | quote }}
    solvers:
      - http01:
          ingress:
            class: {{ $class | quote }}
{{- end }}

{{- end -}}
```

## 7) Manual lifecycle scripts (outside default `run.sh` plan)

Two helper scripts are intentionally manual:
- `manual_install_k3s_minimal.sh` installs k3s with flannel/traefik disabled, preparing for Cilium.
- `manual_configure_route53_dns_updater.sh` installs/removes a systemd timer + helper script that UPSERTs `<subdomain>.swhurl.com` Route53 records.

```bash
sed -n '1,340p' scripts/manual_configure_route53_dns_updater.sh
```

```output
#!/usr/bin/env bash
set -Eeuo pipefail

# Idempotent DNS registration for one or more <subdomain>.swhurl.com names
# using a local aws-dns-updater script and systemd service/timer.
# - Linux + systemd only. On macOS or non-systemd, this is a no-op.
# - Supports multiple subdomains via SWHURL_SUBDOMAINS (space/comma-separated).
# - Backwards compatible with SWHURL_SUBDOMAIN (single value).

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Use the standard env layering logic (config.env -> local.env -> secrets.env -> PROFILE_FILE)
# so DNS inputs behave consistently with the rest of the repo.
# shellcheck disable=SC1090
source "$SCRIPT_DIR/00_lib.sh"

log() { printf "[%s] %s\n" "$(date +'%Y-%m-%dT%H:%M:%S%z')" "$*"; }
warn() { log "WARN: $*"; }
die() { log "ERROR: $*" >&2; exit 1; }

DELETE_MODE=false
for arg in "$@"; do
  case "$arg" in
    --delete) DELETE_MODE=true ;;
    --help|-h)
      cat <<USAGE
Usage: $(basename "$0") [--delete]
  Install or remove a systemd service/timer that updates
  one or more <subdomain>.swhurl.com Route53 A records.

Env:
  SWHURL_SUBDOMAINS   Space or comma-separated subdomains (e.g. "homelab oauth.homelab clickstack.homelab hubble.homelab")
  SWHURL_SUBDOMAIN    Back-compat single subdomain (ignored if SWHURL_SUBDOMAINS is set)
  BASE_DOMAIN         If ends with .swhurl.com, defaults will be derived when SWHURL_SUBDOMAINS is empty
  PROFILE_FILE        Optional profile file (run.sh --profile) for overrides
  PROFILE_EXCLUSIVE   If true, do not auto-load profiles/local.env or profiles/secrets.env (standalone profile)
USAGE
      exit 0
      ;;
  esac
done

OS="$(uname -s || true)"
if [[ "$OS" != "Linux" ]]; then
  log "Non-Linux host detected ($OS); skipping DNS registration."
  exit 0
fi

if ! command -v systemctl >/dev/null 2>&1; then
  log "systemd not available; skipping DNS registration."
  exit 0
fi

USER_NAME="${SUDO_USER:-$(id -un)}"
USER_HOME="$(getent passwd "$USER_NAME" | cut -d: -f6 || echo "$HOME")"
[[ -n "$USER_HOME" ]] || die "Could not determine home directory for $USER_NAME"

SERVICE_NAME="aws-dns-updater.service"
TIMER_NAME="aws-dns-updater.timer"
SERVICE_PATH="/etc/systemd/system/$SERVICE_NAME"
TIMER_PATH="/etc/systemd/system/$TIMER_NAME"

HELPER_DIR="$USER_HOME/.local/scripts"
HELPER_SCRIPT="$HELPER_DIR/aws-dns-updater.sh"
LOCAL_HELPER_SOURCE="$SCRIPT_DIR/aws-dns-updater.sh"

mkdir -p "$HELPER_DIR"

# Determine subdomain list
subdomains_raw="${SWHURL_SUBDOMAINS:-}"
if [[ -z "$subdomains_raw" ]]; then
  if [[ -n "${SWHURL_SUBDOMAIN:-}" ]]; then
    subdomains_raw="$SWHURL_SUBDOMAIN"
  elif [[ -n "${BASE_DOMAIN:-}" && "$BASE_DOMAIN" =~ \.swhurl\.com$ ]]; then
    # Derive defaults from BASE_DOMAIN=<base>.swhurl.com
    base_subdomain="${BASE_DOMAIN%.swhurl.com}"
    base_subdomain="${base_subdomain%.}"
    subdomains_raw="$base_subdomain oauth.$base_subdomain clickstack.$base_subdomain hubble.$base_subdomain minio.$base_subdomain minio-console.$base_subdomain"
    warn "SWHURL_SUBDOMAINS not set; derived defaults: $subdomains_raw"
  else
    subdomains_raw="homelab"
    warn "SWHURL_SUBDOMAINS not set; defaulting to '$subdomains_raw'"
  fi
fi

# Normalize separators to spaces and quote each for ExecStart
subdomains_raw="${subdomains_raw//,/ }"
read -r -a SUBDOMAINS <<< "$subdomains_raw"
if [[ ${#SUBDOMAINS[@]} -eq 0 ]]; then
  die "No subdomains provided or derived"
fi

quoted_subdomains=()
for s in "${SUBDOMAINS[@]}"; do
  [[ -n "$s" ]] || continue
  quoted_subdomains+=("\"$s\"")
done

desired_execstart=("ExecStart=/bin/bash $HELPER_SCRIPT" "${quoted_subdomains[@]}")
desired_execstart_line="${desired_execstart[*]}"

create_or_update_unit() {
  local path="$1"; shift
  local content="$1"; shift
  local changed=0
  if [[ -f "$path" ]]; then
    if ! diff -q <(printf "%s" "$content") "$path" >/dev/null 2>&1; then
      log "Updating $(basename "$path")"
      printf "%s" "$content" | sudo tee "$path" >/dev/null
      changed=1
    else
      log "$(basename "$path") already up-to-date"
    fi
  else
    log "Creating $(basename "$path")"
    printf "%s" "$content" | sudo tee "$path" >/dev/null
    changed=1
  fi
  return $changed
}

unit_changed=0

service_content="# Managed-By: swhurl-platform
[Unit]
Description=AWS Dynamic DNS Update Service
After=network.target

[Service]
Type=simple
$desired_execstart_line
User=$USER_NAME
Restart=on-failure
RestartSec=10s

[Install]
WantedBy=multi-user.target
"

timer_content="# Managed-By: swhurl-platform
[Unit]
Description=Run AWS Dynamic DNS Update Service every 10 minutes

[Timer]
OnBootSec=10min
OnUnitActiveSec=10min
AccuracySec=1min

[Install]
WantedBy=timers.target
"

if [[ "$DELETE_MODE" == true ]]; then
  log "Deleting DNS updater units if managed by this repo"
  needs_remove=false
  if [[ -f "$SERVICE_PATH" ]] && grep -q "^ExecStart=.*/aws-dns-updater.sh" "$SERVICE_PATH"; then
    needs_remove=true
  fi
  if [[ "$needs_remove" == true ]]; then
    sudo systemctl stop "$TIMER_NAME" "$SERVICE_NAME" || true
    sudo systemctl disable "$TIMER_NAME" "$SERVICE_NAME" >/dev/null || true
    sudo rm -f "$SERVICE_PATH" "$TIMER_PATH" || true
    sudo systemctl daemon-reload || true
    log "Removed systemd units"
  else
    log "Units not recognized as managed; nothing to delete."
  fi
  exit 0
fi

# Install/refresh helper script from this repo
if [[ ! -f "$LOCAL_HELPER_SOURCE" ]]; then
  die "Local helper script not found: $LOCAL_HELPER_SOURCE"
fi
if ! cmp -s "$LOCAL_HELPER_SOURCE" "$HELPER_SCRIPT" 2>/dev/null; then
  log "Installing helper script to $HELPER_SCRIPT"
  install -m 0755 "$LOCAL_HELPER_SOURCE" "$HELPER_SCRIPT"
else
  log "Helper script already up-to-date: $HELPER_SCRIPT"
fi

if create_or_update_unit "$SERVICE_PATH" "$service_content"; then unit_changed=1; fi
if create_or_update_unit "$TIMER_PATH" "$timer_content"; then unit_changed=1; fi

if (( unit_changed == 1 )); then
  log "Reloading systemd units"
  sudo systemctl daemon-reload
fi

# Enable and start units (idempotent)
sudo systemctl enable "$SERVICE_NAME" >/dev/null || true
sudo systemctl enable "$TIMER_NAME" >/dev/null || true

if (( unit_changed == 1 )); then
  log "(Re)starting $SERVICE_NAME and $TIMER_NAME"
  sudo systemctl restart "$SERVICE_NAME" || true
  sudo systemctl restart "$TIMER_NAME" || true
else
  sudo systemctl start "$SERVICE_NAME" || true
  sudo systemctl start "$TIMER_NAME" || true
fi

log "Configured subdomains: ${SUBDOMAINS[*]} (under swhurl.com)"
log "Service status:"
sudo systemctl --no-pager --full status "$SERVICE_NAME" | sed -n '1,15p' || true
log "Timer status:"
sudo systemctl --no-pager --full status "$TIMER_NAME" | sed -n '1,15p' || true

log "Done."
```

```bash
sed -n '1,260p' scripts/manual_install_k3s_minimal.sh
```

```output
#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

# Minimal bootstrap:
# 1) Install k3s with Traefik disabled and flannel/network-policy disabled
# 2) Configure kubeconfig and wait for node registration
#
# Cilium is installed separately by scripts/26_manage_cilium_lifecycle.sh.

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

if [[ "$DELETE" == true ]]; then
  log_info "k3s teardown is optional. To uninstall: 'sudo /usr/local/bin/k3s-uninstall.sh' (server)"
  log_info "Set K3S_UNINSTALL=true to attempt running the uninstall script with sudo."
  if [[ "${K3S_UNINSTALL:-false}" == "true" ]]; then
    if command -v sudo >/dev/null 2>&1 && [[ -x "/usr/local/bin/k3s-uninstall.sh" ]]; then
      log_info "Running k3s-uninstall.sh via sudo"
      sudo /usr/local/bin/k3s-uninstall.sh || true
    else
      log_warn "sudo or /usr/local/bin/k3s-uninstall.sh not available; skipping"
    fi
  fi
  exit 0
fi

need_cmd curl
need_cmd kubectl
need_cmd sudo

K3S_VERSION="${K3S_VERSION:-}"
WAIT_SECS="${WAIT_SECS:-900}"
log_info "Bootstrap timeout set to ${WAIT_SECS}s"

if systemctl is-active --quiet k3s; then
  log_info "k3s already active; skipping install"
else
  log_info "Installing k3s (traefik disabled, flannel disabled)"
  if [[ -n "$K3S_VERSION" ]]; then
    curl -sfL https://get.k3s.io | sudo env \
      INSTALL_K3S_VERSION="$K3S_VERSION" \
      INSTALL_K3S_EXEC="--disable traefik --flannel-backend=none --disable-network-policy" \
      sh -
  else
    curl -sfL https://get.k3s.io | sudo env \
      INSTALL_K3S_EXEC="--disable traefik --flannel-backend=none --disable-network-policy" \
      sh -
  fi
fi

log_info "Configuring kubeconfig"
mkdir -p "$HOME/.kube"
sudo cp /etc/rancher/k3s/k3s.yaml "$HOME/.kube/config"
sudo chown "$(id -u):$(id -g)" "$HOME/.kube/config"
chmod 600 "$HOME/.kube/config"
export KUBECONFIG="$HOME/.kube/config"

log_info "Waiting for node registration"
for _ in $(seq 1 60); do
  if kubectl get nodes --no-headers 2>/dev/null | grep -q .; then
    break
  fi
  sleep 2
done
if ! kubectl get nodes --no-headers 2>/dev/null | grep -q .; then
  die "No nodes registered in cluster after waiting"
fi

# Do not wait for Ready here: with flannel disabled this depends on Cilium.
log_info "k3s installed without flannel/traefik. Next step: scripts/26_manage_cilium_lifecycle.sh"
log_info "Verify kubeconfig with: scripts/15_verify_cluster_access.sh"
```

```bash
sed -n '1,260p' scripts/aws-dns-updater.sh
```

```output
#!/usr/bin/env bash
set -Eeuo pipefail

# Update one or more A records in Route53 for <subdomain>.swhurl.com
# Usage: aws-dns-updater.sh <subdomain> [<subdomain> ...]
# - Looks up current external IP once and UPSERTs each hostname
# - Accepts optional overrides via env:
#     AWS_PROFILE   (default: default)
#     AWS_ZONE_ID   (defaults to swhurl.com hosted zone)
#     AWS_NAMESERVER (defaults to an AWS authoritative NS for swhurl.com)

# Defaults specific to swhurl.com (override via env for other zones)
DEF_ZONE_ID="${DEF_ZONE_ID:-Z08316812BZVAZ9D79ZRO}"
DEF_NAMESERVER="${DEF_NAMESERVER:-ns-758.awsdns-30.net}"

ZONE_ID="${AWS_ZONE_ID:-$DEF_ZONE_ID}"
NAMESERVER="${AWS_NAMESERVER:-$DEF_NAMESERVER}"

AWS_PROFILE="${AWS_PROFILE:-default}"
export AWS_PROFILE

if [[ "$#" -lt 1 ]]; then
  echo "Usage: $0 <subdomain> [<subdomain> ...]" >&2
  exit 1
fi

# One external IP lookup for all updates
NEW_IP="$(curl -s checkip.amazonaws.com || true)"
if [[ ! $NEW_IP =~ ^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}$ ]]; then
  echo "Could not get current IP address: $NEW_IP" >&2
  exit 1
fi
echo "New IP - $NEW_IP"

export PATH=$PATH:/usr/local/bin

for subdomain in "$@"; do
  [[ -n "$subdomain" ]] || continue
  hostname="${subdomain}.swhurl.com"

  # Best-effort old IP lookup; allow empty for first-time UPSERTs
  OLD_IP="$(dig +short "$hostname" @"$NAMESERVER" | head -n1 || true)"
  if [[ -n "$OLD_IP" && ! $OLD_IP =~ ^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}$ ]]; then
    echo "Non-A record for $hostname, proceeding with UPSERT"
    OLD_IP=""
  fi

  if [[ "$NEW_IP" == "$OLD_IP" ]]; then
    echo "IP unchanged for $hostname ($OLD_IP)"
    continue
  fi

  TMP_FILE="$(mktemp /tmp/dynamic-dns.XXXXXXXX)"
  cat >"$TMP_FILE" <<EOF
{
  "Comment": "Auto updating @ $(date)",
  "Changes": [{
    "Action": "UPSERT",
    "ResourceRecordSet": {
      "ResourceRecords": [{ "Value": "$NEW_IP" }],
      "Name": "$hostname",
      "Type": "A",
      "TTL": 300
    }
  }]
}
EOF

  echo "Updating $hostname from ${OLD_IP:-<none>} to $NEW_IP"
  aws route53 change-resource-record-sets --hosted-zone-id "$ZONE_ID" --change-batch "file://$TMP_FILE"
  rm -f "$TMP_FILE"
done

```

## 8) End-to-end mental model

1. `run.sh` chooses apply/delete plan and enforces feature gates.
2. `00_lib.sh` exports layered env config; Helmfile templates consume it.
3. Apply path: prereqs -> cluster access -> config contract -> repos/namespaces/Cilium -> core Helm phase -> runtime secrets/configmaps -> platform Helm phase -> sample app -> verification.
4. Delete path: reverse service teardown -> cert-manager cleanup -> namespace release teardown -> hard teardown gate (`99`) -> Cilium delete -> clean verification (`98`).
5. Declarative desired state is in Helmfile + values + local charts; scripts are mostly orchestration, adoption, and cleanup logic.
