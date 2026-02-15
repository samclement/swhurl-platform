#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

if [[ "$DELETE" == true ]]; then
  log_info "Deleting platform config resources (secrets/configmaps)"
  kubectl -n ingress delete secret oauth2-proxy-secret --ignore-not-found >/dev/null 2>&1 || true
  kubectl -n logging delete secret hyperdx-secret --ignore-not-found >/dev/null 2>&1 || true
  kubectl -n logging delete configmap otel-config-vars --ignore-not-found >/dev/null 2>&1 || true
  kubectl -n storage delete secret minio-creds --ignore-not-found >/dev/null 2>&1 || true
  exit 0
fi

# oauth2-proxy secret (required by oauth2-proxy Helm release)
if [[ "${FEAT_OAUTH2_PROXY:-true}" == "true" ]]; then
  [[ -n "${OIDC_ISSUER:-}" ]] || die "OIDC_ISSUER is required when FEAT_OAUTH2_PROXY=true"
  [[ -n "${OIDC_CLIENT_ID:-}" ]] || die "OIDC_CLIENT_ID is required when FEAT_OAUTH2_PROXY=true"
  [[ -n "${OIDC_CLIENT_SECRET:-}" ]] || die "OIDC_CLIENT_SECRET is required when FEAT_OAUTH2_PROXY=true"

  kubectl_ns ingress

  # oauth2-proxy expects a cookie secret that is exactly 16, 24, or 32 bytes.
  # Create-once semantics:
  # - If OAUTH_COOKIE_SECRET is set: enforce it and update the Secret.
  # - Else: do not mutate an existing Secret (avoids auth outages on rerun).
  GEN_COOKIE_SECRET() { hexdump -v -e '/1 "%02X"' -n 16 /dev/urandom; }
  if kubectl -n ingress get secret oauth2-proxy-secret >/dev/null 2>&1; then
    if [[ -n "${OAUTH_COOKIE_SECRET:-}" ]]; then
      kubectl -n ingress create secret generic oauth2-proxy-secret \
        --from-literal=client-id="${OIDC_CLIENT_ID}" \
        --from-literal=client-secret="${OIDC_CLIENT_SECRET}" \
        --from-literal=cookie-secret="${OAUTH_COOKIE_SECRET}" \
        --dry-run=client -o yaml | kubectl apply -f -
    else
      log_info "oauth2-proxy-secret already exists and OAUTH_COOKIE_SECRET is unset; leaving cookie secret unchanged"
    fi
  else
    COOKIE_SECRET_VAL="${OAUTH_COOKIE_SECRET:-$(GEN_COOKIE_SECRET)}"
    kubectl -n ingress create secret generic oauth2-proxy-secret \
      --from-literal=client-id="${OIDC_CLIENT_ID}" \
      --from-literal=client-secret="${OIDC_CLIENT_SECRET}" \
      --from-literal=cookie-secret="${COOKIE_SECRET_VAL}" \
      --dry-run=client -o yaml | kubectl apply -f -
  fi

  label_managed ingress secret oauth2-proxy-secret

  # Verify secret exists and cookie-secret length is valid (16/24/32)
  for i in {1..10}; do
    if kubectl -n ingress get secret oauth2-proxy-secret >/dev/null 2>&1; then
      LEN=$(kubectl -n ingress get secret oauth2-proxy-secret -o jsonpath='{.data.cookie-secret}' | base64 -d | wc -c | tr -d '[:space:]')
      if [[ "$LEN" == "16" || "$LEN" == "24" || "$LEN" == "32" ]]; then
        break
      fi
    fi
    sleep 1
  done
  LEN=$(kubectl -n ingress get secret oauth2-proxy-secret -o jsonpath='{.data.cookie-secret}' | base64 -d | wc -c | tr -d '[:space:]' || echo 0)
  if [[ "$LEN" != "16" && "$LEN" != "24" && "$LEN" != "32" ]]; then
    die "oauth2-proxy-secret not created or invalid cookie-secret length ($LEN)"
  fi
fi

# Kubernetes OTel collectors config (required by otel-k8s Helm releases)
if [[ "${FEAT_OTEL_K8S:-true}" == "true" ]]; then
  kubectl_ns logging

  OTLP_ENDPOINT="${CLICKSTACK_OTEL_ENDPOINT:-http://clickstack-otel-collector.observability.svc.cluster.local:4318}"
  INGESTION_KEY="${CLICKSTACK_INGESTION_KEY:-}"
  [[ -n "$INGESTION_KEY" ]] || die "CLICKSTACK_INGESTION_KEY is required when FEAT_OTEL_K8S=true"

  kubectl -n logging create secret generic hyperdx-secret \
    --from-literal=HYPERDX_API_KEY="$INGESTION_KEY" \
    --dry-run=client -o yaml | kubectl apply -f -

  kubectl -n logging create configmap otel-config-vars \
    --from-literal=HYPERDX_OTLP_ENDPOINT="$OTLP_ENDPOINT" \
    --dry-run=client -o yaml | kubectl apply -f -

  label_managed logging secret hyperdx-secret
  label_managed logging configmap otel-config-vars
fi

# MinIO credentials secret (required by MinIO Helm release)
if [[ "${FEAT_MINIO:-true}" == "true" ]]; then
  kubectl_ns storage
  [[ -n "${MINIO_ROOT_USER:-}" ]] || die "MINIO_ROOT_USER is required when FEAT_MINIO=true"
  [[ -n "${MINIO_ROOT_PASSWORD:-}" ]] || die "MINIO_ROOT_PASSWORD is required when FEAT_MINIO=true"

  kubectl -n storage create secret generic minio-creds \
    --from-literal=rootUser="${MINIO_ROOT_USER}" \
    --from-literal=rootPassword="${MINIO_ROOT_PASSWORD}" \
    --dry-run=client -o yaml | kubectl apply -f -

  label_managed storage secret minio-creds
fi

log_info "Platform config resources ensured"

