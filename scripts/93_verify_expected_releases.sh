#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done
if [[ "$DELETE" == true ]]; then
  log_info "Inventory check is apply-only; skipping in delete mode"
  exit 0
fi

ensure_context

say() { printf "\n== %s ==\n" "$1"; }
ok() { printf "[OK] %s\n" "$1"; }
bad() { printf "[BAD] %s\n" "$1"; fail=1; }
warn() { printf "[WARN] %s\n" "$1"; }

manifest_kind_names() {
  local target_kind="$1" file="$2"
  awk -v target="$target_kind" '
    $1 == "kind:" && $2 == target { in_obj=1; next }
    in_obj && $1 == "name:" { print $2; in_obj=0 }
  ' "$file"
}

is_flux_inventory_available() {
  kubectl get crd kustomizations.kustomize.toolkit.fluxcd.io >/dev/null 2>&1 || return 1
  kubectl -n flux-system get kustomization homelab-flux-stack >/dev/null 2>&1 || return 1
}

verify_flux_inventory() {
  local strict_extras="$1"

  say "Required Flux Kustomizations"

  mapfile -t expected_flux < <(
    {
      printf '%s\n' "homelab-flux-stack"
      manifest_kind_names "Kustomization" "$SCRIPT_DIR/../cluster/overlays/homelab/flux/stack-kustomizations.yaml"
    } | sort -u
  )

  mapfile -t actual_flux < <(
    kubectl -n flux-system get kustomization -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null | sort -u
  )

  local item
  for item in "${expected_flux[@]}"; do
    if ! kubectl -n flux-system get kustomization "$item" >/dev/null 2>&1; then
      bad "Kustomization missing: ${item}"
      continue
    fi

    local ready_status reason message
    ready_status="$(kubectl -n flux-system get kustomization "$item" -o jsonpath='{range .status.conditions[?(@.type=="Ready")]}{.status}{end}' 2>/dev/null || true)"
    reason="$(kubectl -n flux-system get kustomization "$item" -o jsonpath='{range .status.conditions[?(@.type=="Ready")]}{.reason}{end}' 2>/dev/null || true)"
    message="$(kubectl -n flux-system get kustomization "$item" -o jsonpath='{range .status.conditions[?(@.type=="Ready")]}{.message}{end}' 2>/dev/null || true)"
    if [[ "$ready_status" == "True" ]]; then
      ok "Kustomization ready: ${item}"
    else
      bad "Kustomization not ready: ${item} (status=${ready_status:-<empty>} reason=${reason:-<none>} message=${message:-<none>})"
    fi
  done

  say "Required Flux Sources"
  mapfile -t expected_git_sources < <(
    manifest_kind_names "GitRepository" "$SCRIPT_DIR/../cluster/flux/sources/gitrepositories.yaml" | sort -u
  )
  for item in "${expected_git_sources[@]}"; do
    if ! kubectl -n flux-system get gitrepository "$item" >/dev/null 2>&1; then
      bad "GitRepository missing: ${item}"
      continue
    fi
    local ready_status reason message
    ready_status="$(kubectl -n flux-system get gitrepository "$item" -o jsonpath='{range .status.conditions[?(@.type=="Ready")]}{.status}{end}' 2>/dev/null || true)"
    reason="$(kubectl -n flux-system get gitrepository "$item" -o jsonpath='{range .status.conditions[?(@.type=="Ready")]}{.reason}{end}' 2>/dev/null || true)"
    message="$(kubectl -n flux-system get gitrepository "$item" -o jsonpath='{range .status.conditions[?(@.type=="Ready")]}{.message}{end}' 2>/dev/null || true)"
    if [[ "$ready_status" == "True" ]]; then
      ok "GitRepository ready: ${item}"
    else
      bad "GitRepository not ready: ${item} (status=${ready_status:-<empty>} reason=${reason:-<none>} message=${message:-<none>})"
    fi
  done

  if [[ "$strict_extras" == "true" ]]; then
    say "Unexpected Flux Kustomizations"
    local release_scope="${VERIFY_RELEASE_SCOPE:-platform}" # platform|cluster
    case "$release_scope" in
      platform|cluster) ;;
      *) bad "VERIFY_RELEASE_SCOPE must be one of: platform, cluster (got: ${release_scope})" ;;
    esac

    local -a allow_patterns=()
    if [[ -n "${VERIFY_RELEASE_ALLOWLIST:-}" ]]; then
      IFS=',' read -r -a allow_patterns <<< "${VERIFY_RELEASE_ALLOWLIST}"
    fi

    local extras_found=0
    for item in "${actual_flux[@]}"; do
      if printf '%s\n' "${expected_flux[@]}" | grep -qx "$item"; then
        continue
      fi
      if [[ "$release_scope" == "platform" && "$item" != homelab-* ]]; then
        continue
      fi
      if [[ "${#allow_patterns[@]}" -gt 0 ]] && name_matches_any_pattern "$item" "${allow_patterns[@]}"; then
        warn "Allowlisted extra kustomization: ${item}"
        continue
      fi
      bad "Unexpected kustomization: ${item}"
      extras_found=1
    done
    if [[ "$extras_found" -eq 0 ]]; then
      ok "No unexpected kustomizations (scope: ${release_scope})"
    fi
  else
    warn "Skipping unexpected kustomization checks (VERIFY_RELEASE_STRICT_EXTRAS=false)"
  fi
}

verify_helm_inventory() {
  local strict_extras="$1"
  need_cmd helm

  say "Required Helm Releases"
  mapfile -t expected < <(verify_expected_releases)
  mapfile -t actual < <(helm list -A --no-headers 2>/dev/null | awk '{print $2"/"$1}' | sort -u)

  local item
  for item in "${expected[@]}"; do
    if printf '%s\n' "${actual[@]}" | grep -qx "$item"; then
      ok "$item"
    else
      bad "$item"
    fi
  done

  if [[ "$strict_extras" == "true" ]]; then
    say "Unexpected Helm Releases"
    local release_scope="${VERIFY_RELEASE_SCOPE:-platform}" # platform|cluster
    case "$release_scope" in
      platform|cluster) ;;
      *) bad "VERIFY_RELEASE_SCOPE must be one of: platform, cluster (got: ${release_scope})" ;;
    esac

    local -a allow_patterns=("${VERIFY_RELEASE_ALLOWLIST_DEFAULT[@]}")
    if [[ -n "${VERIFY_RELEASE_ALLOWLIST:-}" ]]; then
      IFS=',' read -r -a extra_allow <<< "${VERIFY_RELEASE_ALLOWLIST}"
      local p
      for p in "${extra_allow[@]}"; do
        [[ -n "$p" ]] || continue
        allow_patterns+=("$p")
      done
    fi

    local extras_found=0
    for item in "${actual[@]}"; do
      if printf '%s\n' "${expected[@]}" | grep -qx "$item"; then
        continue
      fi
      if [[ "$release_scope" == "platform" ]] && ! is_release_in_platform_scope "$item"; then
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
      ok "No unexpected releases (scope: ${release_scope})"
    fi
  else
    warn "Skipping unexpected release checks (VERIFY_RELEASE_STRICT_EXTRAS=false)"
  fi
}

fail=0

STRICT_EXTRAS="${VERIFY_RELEASE_STRICT_EXTRAS:-false}"
if [[ "$STRICT_EXTRAS" != "true" && "$STRICT_EXTRAS" != "false" ]]; then
  bad "VERIFY_RELEASE_STRICT_EXTRAS must be true or false (got: ${STRICT_EXTRAS})"
fi

MODE="${VERIFY_INVENTORY_MODE:-auto}" # auto|flux|helm
case "$MODE" in
  auto|flux|helm) ;;
  *) bad "VERIFY_INVENTORY_MODE must be one of: auto, flux, helm (got: ${MODE})" ;;
esac

resolved_mode="$MODE"
if [[ "$MODE" == "auto" ]]; then
  if is_flux_inventory_available; then
    resolved_mode="flux"
  else
    resolved_mode="helm"
  fi
fi

say "Inventory Mode"
ok "resolved mode: ${resolved_mode}"

if [[ "$resolved_mode" == "flux" ]]; then
  if ! is_flux_inventory_available; then
    bad "Flux inventory mode selected but Flux stack resources are unavailable"
  else
    verify_flux_inventory "$STRICT_EXTRAS"
  fi
else
  verify_helm_inventory "$STRICT_EXTRAS"
fi

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

echo
ok "Inventory verification passed"
