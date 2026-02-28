#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

platform_cert_env=""
app_env=""
app_le_env=""
dry_run=false

usage() {
  cat <<'EOF'
Usage:
  set-flux-path-modes.sh [--dry-run] [--platform-cert-env staging|prod] [--app-env staging|prod --app-le-env staging|prod]

Examples:
  set-flux-path-modes.sh --platform-cert-env prod
  set-flux-path-modes.sh --app-env staging --app-le-env prod
  set-flux-path-modes.sh --platform-cert-env staging --app-env prod --app-le-env staging
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --platform-cert-env)
      [[ $# -ge 2 ]] || { echo "--platform-cert-env requires a value" >&2; exit 1; }
      platform_cert_env="$2"
      shift 2
      ;;
    --app-env)
      [[ $# -ge 2 ]] || { echo "--app-env requires a value" >&2; exit 1; }
      app_env="$2"
      shift 2
      ;;
    --app-le-env)
      [[ $# -ge 2 ]] || { echo "--app-le-env requires a value" >&2; exit 1; }
      app_le_env="$2"
      shift 2
      ;;
    --dry-run)
      dry_run=true
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown argument: $1" >&2
      usage
      exit 1
      ;;
  esac
done

if [[ -z "$platform_cert_env" && -z "$app_env" && -z "$app_le_env" ]]; then
  echo "No mode change requested." >&2
  usage
  exit 1
fi

update_flux_path() {
  local file="$1"
  local new_path="$2"
  local current_path tmp

  current_path="$(awk '/^[[:space:]]*path:[[:space:]]*/ { print $2; exit }' "$file")"
  [[ -n "$current_path" ]] || { echo "No path field found in $file" >&2; exit 1; }

  if [[ "$current_path" == "$new_path" ]]; then
    printf '[INFO] %s already set to %s\n' "$file" "$new_path"
    return 0
  fi

  printf '[INFO] %s: %s -> %s\n' "$file" "$current_path" "$new_path"
  if [[ "$dry_run" == true ]]; then
    return 0
  fi

  tmp="$(mktemp)"
  awk -v new_path="$new_path" '
    BEGIN { updated=0 }
    /^[[:space:]]*path:[[:space:]]*/ && updated==0 {
      sub(/path:[[:space:]]*.*/, "  path: " new_path)
      updated=1
    }
    { print }
    END {
      if (updated==0) {
        exit 42
      }
    }
  ' "$file" >"$tmp"
  mv "$tmp" "$file"
}

if [[ -n "$platform_cert_env" ]]; then
  case "$platform_cert_env" in
    staging)
      infra_path="./infrastructure/overlays/home"
      platform_path="./platform-services/overlays/home"
      ;;
    prod)
      infra_path="./infrastructure/overlays/home-letsencrypt-prod"
      platform_path="./platform-services/overlays/home-letsencrypt-prod"
      ;;
    *)
      echo "platform cert env must be staging|prod (got: $platform_cert_env)" >&2
      exit 1
      ;;
  esac

  update_flux_path "$REPO_ROOT/clusters/home/infrastructure.yaml" "$infra_path"
  update_flux_path "$REPO_ROOT/clusters/home/platform.yaml" "$platform_path"
fi

if [[ -n "$app_env" || -n "$app_le_env" ]]; then
  [[ -n "$app_env" && -n "$app_le_env" ]] || {
    echo "both --app-env and --app-le-env are required for app mode changes" >&2
    exit 1
  }
  case "$app_env" in
    staging|prod) ;;
    *) echo "app env must be staging|prod (got: $app_env)" >&2; exit 1 ;;
  esac
  case "$app_le_env" in
    staging|prod) ;;
    *) echo "app le env must be staging|prod (got: $app_le_env)" >&2; exit 1 ;;
  esac

  tenants_path="./tenants/overlays/app-${app_env}-le-${app_le_env}"
  update_flux_path "$REPO_ROOT/clusters/home/tenants.yaml" "$tenants_path"
fi
