#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done
if [[ "$DELETE" == true ]]; then
  log_info "Kustomize build check is apply-only; skipping in delete mode"
  exit 0
fi

need_cmd kubectl

ok(){ printf "[OK] %s\n" "$1"; }
bad(){ printf "[BAD] %s\n" "$1"; fail=1; }

fail=0
printf "== Kustomize Build Verification ==\n"

mapfile -t targets < <(find "$SCRIPT_DIR/../infra/manifests" -name kustomization.yaml -printf '%h\n' | sort)
for t in "${targets[@]}"; do
  if kubectl kustomize "$t" >/dev/null 2>&1; then
    ok "$t"
  else
    bad "$t"
  fi
done

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

echo
ok "Kustomize build verification passed"
