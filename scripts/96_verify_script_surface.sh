#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done
if [[ "$DELETE" == true ]]; then
  log_info "Script surface check is apply-only; skipping in delete mode"
  exit 0
fi

ok(){ printf "[OK] %s\n" "$1"; }
bad(){ printf "[BAD] %s\n" "$1"; fail=1; }

fail=0
printf "== Script Surface Verification ==\n"

for s in 26_cilium.sh 30_cert_manager.sh 40_ingress_nginx.sh 45_oauth2_proxy.sh 50_clickstack.sh 51_otel_k8s.sh 70_minio.sh; do
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

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

echo
ok "Script surface verification passed"
