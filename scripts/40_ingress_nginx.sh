#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

if [[ "$DELETE" == true ]]; then
  log_info "Uninstalling ingress-nginx"
  helm uninstall ingress-nginx -n ingress || true
  exit 0
fi

SERVICE_ARGS=()
if [[ "${K8S_PROVIDER:-k3s}" == "k3s" ]]; then
  # Use k3s' KlipperLB to expose 80/443 on the host for simplicity
  SERVICE_ARGS+=("--set" "controller.service.type=LoadBalancer")
else
  # kind or other: prefer NodePort with explicit ports to avoid collisions
  SERVICE_ARGS+=("--set" "controller.service.type=NodePort")
  SERVICE_ARGS+=("--set" "controller.service.nodePorts.http=31514")
  SERVICE_ARGS+=("--set" "controller.service.nodePorts.https=30313")
fi

helm_upsert ingress-nginx ingress-nginx/ingress-nginx ingress \
  --set controller.replicaCount=1 \
  --set controller.ingressClassResource.default=true \
  "${SERVICE_ARGS[@]}"

wait_deploy ingress ingress-nginx-controller
log_info "ingress-nginx installed"
