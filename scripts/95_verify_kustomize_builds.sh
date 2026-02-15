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

manifests_root="$SCRIPT_DIR/../infra/manifests"
if [[ ! -d "$manifests_root" ]]; then
  ok "No kustomizations found (infra/manifests missing; this repo is Helmfile/local-chart driven)"
  ok "Kustomize build verification passed"
  exit 0
fi

mapfile -t targets < <(find "$manifests_root" -name kustomization.yaml -printf '%h\n' 2>/dev/null | sort)
if [[ "${#targets[@]}" -eq 0 ]]; then
  ok "No kustomizations found (this repo is Helmfile/local-chart driven)"
  ok "Kustomize build verification passed"
  exit 0
fi
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
