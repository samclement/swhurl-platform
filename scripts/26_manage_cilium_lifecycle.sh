#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

if [[ "${FEAT_CILIUM:-true}" != "true" && "$DELETE" != true ]]; then
  log_info "FEAT_CILIUM=false; skipping install"
  exit 0
fi

if [[ "$DELETE" == true ]]; then
  log_info "Uninstalling cilium Helm release"
  helm -n kube-system uninstall cilium >/dev/null 2>&1 || true

  log_info "Attempting force cleanup of labeled cilium resources"
  kubectl -n kube-system delete ds cilium cilium-envoy --ignore-not-found >/dev/null 2>&1 || true
  kubectl -n kube-system delete deploy cilium-operator hubble-relay hubble-ui --ignore-not-found >/dev/null 2>&1 || true
  kubectl -n kube-system delete svc cilium-envoy hubble-peer hubble-relay hubble-ui --ignore-not-found >/dev/null 2>&1 || true

  kubectl -n kube-system delete secret hubble-ui-tls --ignore-not-found >/dev/null 2>&1 || true

  kubectl -n kube-system delete deploy,ds,svc,cm,secret,sa,role,rolebinding \
    -l app.kubernetes.io/part-of=cilium --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete clusterrole,clusterrolebinding \
    -l app.kubernetes.io/part-of=cilium --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete ciliumidentity,ciliumendpoint,ciliumnode,ciliumnetworkpolicy,ciliumclusterwidenetworkpolicy,ciliumcidrgroup,ciliuml2announcementpolicy,ciliumloadbalancerippool,ciliumnodeconfig,ciliumpodippool \
    --all --ignore-not-found >/dev/null 2>&1 || true

  if [[ "${CILIUM_DELETE_CRDS:-true}" == "true" ]]; then
    crds="$(kubectl get crd -o name 2>/dev/null | rg '\.cilium\.io$' || true)"
    if [[ -n "$crds" ]]; then
      log_info "Deleting Cilium CRDs"
      # shellcheck disable=SC2086
      kubectl delete $crds --ignore-not-found || true
    fi
  fi

  kubectl -n kube-system wait --for=delete pod -l app.kubernetes.io/part-of=cilium --timeout=60s >/dev/null 2>&1 || true
  leftover_pods="$(kubectl -n kube-system get pod -l app.kubernetes.io/part-of=cilium -o name 2>/dev/null || true)"
  if [[ -n "$leftover_pods" ]]; then
    log_warn "Force deleting stuck Cilium pods"
    # shellcheck disable=SC2086
    kubectl -n kube-system delete $leftover_pods --force --grace-period=0 --ignore-not-found >/dev/null 2>&1 || true
    kubectl -n kube-system wait --for=delete pod -l app.kubernetes.io/part-of=cilium --timeout=30s >/dev/null 2>&1 || true
  fi

  kubectl delete ns cilium-secrets --ignore-not-found >/dev/null 2>&1 || true
  exit 0
fi

if [[ "${CILIUM_SKIP_FLANNEL_CHECK:-false}" != "true" ]]; then
  flannel_backend=$(kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.annotations.flannel\.alpha\.coreos\.com/backend-type}{"\n"}{end}' | grep -v '^$' | head -n 1 || true)
  if [[ -n "$flannel_backend" ]]; then
    log_error "Detected flannel backend on nodes (${flannel_backend})."
    log_error "Cilium requires k3s with flannel disabled: --flannel-backend=none --disable-network-policy"
    log_error "Reinstall k3s, then rerun this step. Set CILIUM_SKIP_FLANNEL_CHECK=true to override."
    exit 1
  fi
fi

need_cmd helm
helm repo add cilium https://helm.cilium.io/ --force-update >/dev/null 2>&1 || true
helm repo update >/dev/null 2>&1 || true

if kubectl get ns cilium-secrets >/dev/null 2>&1; then
  if [[ -n "$(kubectl get ns cilium-secrets -o jsonpath='{.metadata.deletionTimestamp}' 2>/dev/null || true)" ]]; then
    log_info "Waiting for namespace cilium-secrets to finish terminating"
    kubectl wait --for=delete ns/cilium-secrets --timeout=120s >/dev/null 2>&1 || true
  fi
fi

ingress_class="nginx"
if [[ "${INGRESS_PROVIDER:-nginx}" == "traefik" ]]; then
  ingress_class="traefik"
fi

issuer="letsencrypt-staging"
hubble_host="${HUBBLE_HOST:-hubble.${BASE_DOMAIN}}"
oauth_host="${OAUTH_HOST:-oauth.${BASE_DOMAIN}}"

values_file="$(mktemp)"
trap 'rm -f "$values_file"' EXIT

cat >"$values_file" <<VALUES
kubeProxyReplacement: "false"
operator:
  replicas: 1
hubble:
  enabled: true
  relay:
    enabled: true
  ui:
    enabled: true
    ingress:
      enabled: true
      className: ${ingress_class}
      annotations:
        cert-manager.io/cluster-issuer: "${issuer}"
VALUES

if [[ "${FEAT_OAUTH2_PROXY:-true}" == "true" && "$ingress_class" == "nginx" ]]; then
  cat >>"$values_file" <<VALUES
        nginx.ingress.kubernetes.io/auth-url: "https://${oauth_host}/oauth2/auth"
        nginx.ingress.kubernetes.io/auth-signin: "https://${oauth_host}/oauth2/start?rd=\$scheme://\$host\$request_uri"
VALUES
fi

cat >>"$values_file" <<VALUES
      hosts:
        - "${hubble_host}"
      tls:
        - secretName: hubble-ui-tls
          hosts:
            - "${hubble_host}"
VALUES

helm -n kube-system upgrade --install cilium cilium/cilium \
  --version 1.19.0 \
  --create-namespace \
  --wait \
  --timeout "${TIMEOUT_SECS:-300}s" \
  -f "$values_file"

wait_ds kube-system cilium
wait_deploy kube-system cilium-operator

if kubectl -n kube-system get deploy hubble-relay >/dev/null 2>&1; then
  wait_deploy kube-system hubble-relay
fi
if kubectl -n kube-system get deploy hubble-ui >/dev/null 2>&1; then
  wait_deploy kube-system hubble-ui
fi
log_info "cilium installed"
