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

APP_USE_KUSTOMIZE="${APP_USE_KUSTOMIZE:-true}"
TLS_SECRET="${TLS_SECRET:-${APP_NAME}-tls}"
ISSUER="${ISSUER:-${CLUSTER_ISSUER}}"
IMAGE="${IMAGE:-docker.io/nginx:1.25-alpine}"
AUTH_ENABLED="${APP_AUTH_ENABLED:-${FEAT_OAUTH2_PROXY:-false}}"

if [[ "$APP_USE_KUSTOMIZE" == true ]]; then
  TMPDIR=$(mktemp -d)
  trap 'rm -rf "$TMPDIR"' EXIT
  # Copy base to avoid kustomize security boundary issues
  mkdir -p "$TMPDIR/base"
  cp -r "$SCRIPT_DIR/../infra/manifests/apps/hello/base/"* "$TMPDIR/base/"

  cat > "$TMPDIR/kustomization.yaml" <<K
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: ${APP_NS}
resources:
  - ./base

configMapGenerator:
  - name: hello-params
    literals:
      - host=${HOST}
      - tlsSecret=${TLS_SECRET}
      - issuerName=${ISSUER}

replacements:
  - source: {kind: ConfigMap, name: hello-params, fieldPath: data.host}
    targets:
      - select: {kind: Ingress, name: ${APP_NAME}}
        fieldPaths: [spec.rules.0.host, spec.tls.0.hosts.0]
      - select: {kind: Certificate, name: ${APP_NAME}}
        fieldPaths: [spec.dnsNames.0]
  - source: {kind: ConfigMap, name: hello-params, fieldPath: data.tlsSecret}
    targets:
      - select: {kind: Ingress, name: ${APP_NAME}}
        fieldPaths: [spec.tls.0.secretName]
      - select: {kind: Certificate, name: ${APP_NAME}}
        fieldPaths: [spec.secretName]
  - source: {kind: ConfigMap, name: hello-params, fieldPath: data.issuerName}
    targets:
      - select: {kind: Certificate, name: ${APP_NAME}}
        fieldPaths: [spec.issuerRef.name]
K

  if [[ "$AUTH_ENABLED" == true && -n "${OAUTH_HOST:-}" ]]; then
    cat > "$TMPDIR/auth-patch.yaml" <<K
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: ${APP_NAME}
  annotations:
    nginx.ingress.kubernetes.io/auth-url: http://oauth2-proxy.ingress.svc.cluster.local/oauth2/auth
    nginx.ingress.kubernetes.io/auth-signin: https://${OAUTH_HOST}/oauth2/start?rd=\$scheme://\$host\$request_uri
K
    cat >> "$TMPDIR/kustomization.yaml" <<K

patches:
  - path: auth-patch.yaml
    target:
      kind: Ingress
      name: ${APP_NAME}
K
  elif [[ "$AUTH_ENABLED" == true ]]; then
    log_warn "APP_AUTH_ENABLED/FEAT_OAUTH2_PROXY is true but OAUTH_HOST is empty; skipping auth annotations"
  fi

  kubectl apply -k "$TMPDIR"
else
  # Legacy template path using envsubst (kept for compatibility)
  TMPDIR=$(mktemp -d)
  trap 'rm -rf "$TMPDIR"' EXIT
  cp "$SCRIPT_DIR/../infra/manifests/templates/app/"*.yaml "$TMPDIR/"
  export APP_NAME APP_NS HOST IMAGE TLS_SECRET ISSUER OAUTH_HOST
  for f in "$TMPDIR"/*.yaml; do
    envsubst '${APP_NAME} ${APP_NS} ${HOST} ${TLS_SECRET} ${ISSUER} ${IMAGE} ${OAUTH_HOST}' < "$f" > "$f.rendered"
    mv "$f.rendered" "$f"
  done
  cat > "$TMPDIR/kustomization.yaml" <<K
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - deployment.yaml
  - service.yaml
  - certificate.yaml
  - ingress-public.yaml
K
  kubectl apply -k "$TMPDIR"
fi

wait_deploy "$APP_NS" "$APP_NAME"
log_info "Sample app deployed at https://${HOST}"
