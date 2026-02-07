#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

APP_NS=apps
APP_NAME=hello-web

# Derive host: allow override via APP_HOST, else use BASE_DOMAIN
HOST="${APP_HOST:-}"
if [[ -z "$HOST" ]]; then
  if [[ -n "${BASE_DOMAIN:-}" ]]; then
    HOST="hello.${BASE_DOMAIN}"
  else
    die "BASE_DOMAIN is not set and APP_HOST not provided; cannot render sample app Ingress. Set BASE_DOMAIN (e.g., 127.0.0.1.nip.io) or export APP_HOST."
  fi
fi

if [[ "$DELETE" == true ]]; then
  kubectl delete -n "$APP_NS" ingress "$APP_NAME" --ignore-not-found
  kubectl delete -n "$APP_NS" certificate "$APP_NAME" --ignore-not-found || true
  kubectl delete -n "$APP_NS" deploy "$APP_NAME" svc "$APP_NAME" --ignore-not-found
  kubectl delete ns "$APP_NS" --ignore-not-found
  exit 0
fi

kubectl_ns "$APP_NS"

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT
cp "$SCRIPT_DIR/../manifests/templates/app/"*.yaml "$TMPDIR/"

export APP_NAME APP_NS HOST IMAGE="docker.io/nginx:1.25-alpine" TLS_SECRET="${APP_NAME}-tls" ISSUER="${CLUSTER_ISSUER}" OAUTH_HOST
for f in "$TMPDIR"/*.yaml; do
  envsubst '${APP_NAME} ${APP_NS} ${HOST} ${TLS_SECRET} ${ISSUER} ${IMAGE} ${OAUTH_HOST}' < "$f" > "$f.rendered"
  mv "$f.rendered" "$f"
done

# Decide whether to use OAuth-protected ingress
USE_AUTH=false
if [[ "${FEAT_OAUTH2_PROXY:-true}" == "true" ]]; then
  # Only enable auth if oauth2-proxy is deployed and available
  if kubectl -n ingress get deploy oauth2-proxy >/dev/null 2>&1; then
    if kubectl -n ingress rollout status deploy/oauth2-proxy --timeout=5s >/dev/null 2>&1; then
      USE_AUTH=true
    fi
  fi
fi

if [[ "$USE_AUTH" == true ]]; then
  log_info "Using OAuth-protected Ingress for sample app (oauth2-proxy detected)"
else
  log_info "Using public Ingress for sample app (oauth2-proxy missing or disabled)"
fi

cat > "$TMPDIR/kustomization.yaml" <<K
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - deployment.yaml
  - service.yaml
  - certificate.yaml
  - $( [[ "$USE_AUTH" == true ]] && echo ingress-auth.yaml || echo ingress-public.yaml )
K

kubectl apply -k "$TMPDIR"

wait_deploy "$APP_NS" "$APP_NAME"
log_info "Sample app deployed at https://${HOST}"
