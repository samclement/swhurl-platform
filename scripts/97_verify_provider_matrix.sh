#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$SCRIPT_DIR/00_lib.sh"

need_cmd helmfile
need_cmd awk

tmp_dir="$(mktemp -d)"
cleanup() {
  rm -rf "$tmp_dir"
}
trap cleanup EXIT

failures=0

release_installed_state() {
  local release="$1" rendered="$2"
  awk -v target="$release" '
    BEGIN { found=0; name=""; installed="" }
    /^  - chart:/ {
      if (name == target) {
        print installed
        found=1
        exit 0
      }
      name=""
      installed=""
      next
    }
    /^    name:[[:space:]]+/ { name=$2; next }
    /^    installed:[[:space:]]+/ { installed=$2; next }
    END {
      if (!found && name == target) {
        print installed
        found=1
        exit 0
      }
      if (!found) {
        exit 2
      }
    }
  ' "$rendered"
}

check_release_state() {
  local case_name="$1" rendered="$2" release="$3" expected="$4"
  local actual=""
  if ! actual="$(release_installed_state "$release" "$rendered")"; then
    log_error "[$case_name] Release '$release' not found in rendered Helmfile output"
    failures=$((failures + 1))
    return 0
  fi

  if [[ "$actual" != "$expected" ]]; then
    log_error "[$case_name] Release '$release' installed=$actual (expected $expected)"
    failures=$((failures + 1))
  else
    log_info "[$case_name] Release '$release' installed=$actual (expected)"
  fi
}

run_case() {
  local case_name="$1" ingress_provider="$2" storage_provider="$3" expect_ingress="$4" expect_minio="$5"
  local rendered="$tmp_dir/${case_name}.yaml"

  log_info "Rendering provider case '$case_name' (INGRESS_PROVIDER=$ingress_provider OBJECT_STORAGE_PROVIDER=$storage_provider)"
  INGRESS_PROVIDER="$ingress_provider" OBJECT_STORAGE_PROVIDER="$storage_provider" helmfile_cmd build > "$rendered"

  check_release_state "$case_name" "$rendered" "ingress-nginx" "$expect_ingress"
  check_release_state "$case_name" "$rendered" "minio" "$expect_minio"
}

run_case "nginx-minio" "nginx" "minio" "true" "true"
run_case "traefik-minio" "traefik" "minio" "false" "true"
run_case "nginx-ceph" "nginx" "ceph" "true" "false"
run_case "traefik-ceph" "traefik" "ceph" "false" "false"

if (( failures > 0 )); then
  die "Provider matrix verification failed with ${failures} mismatch(es)"
fi

log_info "Provider matrix verification passed"
