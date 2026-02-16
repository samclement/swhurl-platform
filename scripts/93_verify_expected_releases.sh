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

fail=0
say "Required Releases"

mapfile -t expected < <(verify_expected_releases)

actual="$(helm list -A --no-headers 2>/dev/null | awk '{print $2"/"$1}')"
for item in "${expected[@]}"; do
  if grep -qx "$item" <<< "$actual"; then
    ok "$item"
  else
    bad "$item"
  fi
done

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

echo
ok "Release inventory verification passed"
