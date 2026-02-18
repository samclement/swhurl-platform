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
