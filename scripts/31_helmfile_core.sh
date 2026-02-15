#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

if [[ "$DELETE" == true ]]; then
  log_info "Destroying core ClusterIssuers (phase=core-issuers)"
  helmfile_cmd -l phase=core-issuers destroy >/dev/null 2>&1 || true
  log_info "Destroying core platform Helm releases (phase=core)"
  helmfile_cmd -l phase=core destroy >/dev/null 2>&1 || true
  exit 0
fi

log_info "Syncing core platform Helm releases (phase=core)"
helmfile_cmd -l phase=core sync

# Issuer creation relies on CRDs existing and webhook CA injection, both of which can lag
# right after install due to API registration and cainjector leader election.
if ! wait_crd_established clusterissuers.cert-manager.io "${TIMEOUT_SECS:-300}"; then
  die "cert-manager CRD clusterissuers.cert-manager.io not established; retry later or inspect cert-manager installation"
fi
if ! wait_webhook_cabundle cert-manager-webhook "${TIMEOUT_SECS:-300}"; then
  log_warn "Webhook CA bundle not ready; restarting webhook/cainjector and retrying"
  kubectl -n cert-manager rollout restart deploy/cert-manager-webhook deploy/cert-manager-cainjector >/dev/null 2>&1 || true
  kubectl -n cert-manager rollout status deploy/cert-manager-webhook --timeout=${TIMEOUT_SECS:-300}s
  kubectl -n cert-manager rollout status deploy/cert-manager-cainjector --timeout=${TIMEOUT_SECS:-300}s
  if ! wait_webhook_cabundle cert-manager-webhook "${TIMEOUT_SECS:-300}"; then
    die "cert-manager webhook CA bundle still not ready; retry later or inspect cert-manager-webhook/cainjector"
  fi
fi

log_info "Syncing core ClusterIssuers (phase=core-issuers)"
# Helm refuses to install a chart that renders ClusterIssuer objects if those issuers
# already exist without Helm ownership metadata. On existing clusters, adopt them.
release="platform-issuers"
release_ns="kube-system"
case "${CLUSTER_ISSUER:-selfsigned}" in
  letsencrypt) issuers=(letsencrypt letsencrypt-staging letsencrypt-prod) ;;
  selfsigned|*) issuers=(selfsigned) ;;
esac
for name in "${issuers[@]}"; do
  adopt_helm_ownership clusterissuer "$name" "$release" "$release_ns"
done
helmfile_cmd -l phase=core-issuers sync

log_info "Core Helm releases synced"
