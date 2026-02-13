#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

ensure_context

fail=0
declare -a SUGGEST=()

say() { printf "\n== %s ==\n" "$1"; }
ok() { printf "[OK] %s\n" "$1"; }
warn() { printf "[WARN] %s\n" "$1"; }
mismatch() { printf "[MISMATCH] %s\n" "$1"; fail=1; }

add_suggest() {
  local s="$1"
  for e in "${SUGGEST[@]:-}"; do
    [[ "$e" == "$s" ]] && return 0
  done
  SUGGEST+=("$s")
}

check_eq() {
  local label="$1" expected="$2" actual="$3" suggest="$4"
  if [[ "$expected" == "$actual" ]]; then
    ok "$label: $actual"
  else
    mismatch "$label: expected=$expected actual=$actual"
    [[ -n "$suggest" ]] && add_suggest "$suggest"
  fi
}

say "ClusterIssuer"
case "${CLUSTER_ISSUER:-selfsigned}" in
  letsencrypt)
    if [[ -z "${ACME_EMAIL:-}" ]]; then
      warn "ACME_EMAIL is empty; cannot validate letsencrypt email"
    elif kubectl get clusterissuer letsencrypt >/dev/null 2>&1; then
      actual_email=$(kubectl get clusterissuer letsencrypt -o jsonpath='{.spec.acme.email}')
      check_eq "letsencrypt.email" "${ACME_EMAIL}" "$actual_email" "scripts/35_issuer.sh"
    else
      mismatch "ClusterIssuer letsencrypt not found"
      add_suggest "scripts/35_issuer.sh"
    fi
    ;;
  selfsigned)
    if kubectl get clusterissuer selfsigned >/dev/null 2>&1; then
      ok "selfsigned ClusterIssuer present"
    else
      mismatch "ClusterIssuer selfsigned not found"
      add_suggest "scripts/35_issuer.sh"
    fi
    ;;
  *)
    warn "Unknown CLUSTER_ISSUER=${CLUSTER_ISSUER}"
    ;;
esac

say "Cilium"
if [[ "${FEAT_CILIUM:-true}" == "true" ]]; then
  if kubectl -n kube-system get ds cilium >/dev/null 2>&1; then
    ok "cilium daemonset present"
  else
    mismatch "cilium daemonset not found"
    add_suggest "scripts/26_cilium.sh"
  fi
  if kubectl -n kube-system get deploy cilium-operator >/dev/null 2>&1; then
    ok "cilium-operator deployment present"
  else
    mismatch "cilium-operator deployment not found"
    add_suggest "scripts/26_cilium.sh"
  fi
else
  ok "FEAT_CILIUM=false; skipping"
fi

say "Hubble"
if [[ "${FEAT_CILIUM:-true}" == "true" ]]; then
  if kubectl -n kube-system get deploy hubble-relay >/dev/null 2>&1; then
    ok "hubble-relay deployment present"
  else
    mismatch "hubble-relay deployment not found"
    add_suggest "scripts/26_cilium.sh"
  fi
  if kubectl -n kube-system get deploy hubble-ui >/dev/null 2>&1; then
    ok "hubble-ui deployment present"
  else
    mismatch "hubble-ui deployment not found"
    add_suggest "scripts/26_cilium.sh"
  fi
  if kubectl -n kube-system get ingress hubble-ui >/dev/null 2>&1; then
    actual_host=$(kubectl -n kube-system get ingress hubble-ui -o jsonpath='{.spec.rules[0].host}')
    actual_issuer=$(kubectl -n kube-system get ingress hubble-ui -o jsonpath='{.metadata.annotations.cert-manager\.io/cluster-issuer}')
    check_eq "hubble-ui.host" "${HUBBLE_HOST:-}" "$actual_host" "scripts/26_cilium.sh"
    check_eq "hubble-ui.issuer" "${CLUSTER_ISSUER:-}" "$actual_issuer" "scripts/26_cilium.sh"
    expected_auth_url="https://${OAUTH_HOST}/oauth2/auth"
    expected_auth_signin="https://${OAUTH_HOST}/oauth2/start?rd=\$scheme://\$host\$request_uri"
    actual_auth_url=$(kubectl -n kube-system get ingress hubble-ui -o jsonpath='{.metadata.annotations.nginx\.ingress\.kubernetes\.io/auth-url}')
    actual_auth_signin=$(kubectl -n kube-system get ingress hubble-ui -o jsonpath='{.metadata.annotations.nginx\.ingress\.kubernetes\.io/auth-signin}')
    check_eq "hubble-ui.auth-url" "${expected_auth_url}" "$actual_auth_url" "scripts/26_cilium.sh"
    check_eq "hubble-ui.auth-signin" "${expected_auth_signin}" "$actual_auth_signin" "scripts/26_cilium.sh"
  else
    mismatch "hubble-ui ingress not found"
    add_suggest "scripts/26_cilium.sh"
  fi
else
  ok "FEAT_CILIUM=false; skipping"
fi

say "ingress-nginx"
if kubectl -n ingress get svc ingress-nginx-controller >/dev/null 2>&1; then
  actual_svc_type=$(kubectl -n ingress get svc ingress-nginx-controller -o jsonpath='{.spec.type}')
  check_eq "service.type" "NodePort" "$actual_svc_type" "scripts/40_ingress_nginx.sh"
  actual_http_np=$(kubectl -n ingress get svc ingress-nginx-controller -o jsonpath='{.spec.ports[?(@.name=="http")].nodePort}')
  actual_https_np=$(kubectl -n ingress get svc ingress-nginx-controller -o jsonpath='{.spec.ports[?(@.name=="https")].nodePort}')
  check_eq "nodePort.http" "31514" "$actual_http_np" "scripts/40_ingress_nginx.sh"
  check_eq "nodePort.https" "30313" "$actual_https_np" "scripts/40_ingress_nginx.sh"
else
  mismatch "ingress-nginx service not found"
  add_suggest "scripts/40_ingress_nginx.sh"
fi

if kubectl -n ingress get cm ingress-nginx-controller >/dev/null 2>&1; then
  actual_log=$(kubectl -n ingress get cm ingress-nginx-controller -o jsonpath='{.data.log-format-upstream}')
  if [[ -n "$actual_log" ]]; then
    ok "log-format-upstream present"
  else
    mismatch "log-format-upstream missing"
    add_suggest "scripts/40_ingress_nginx.sh"
  fi
else
  mismatch "ingress-nginx configmap not found"
  add_suggest "scripts/40_ingress_nginx.sh"
fi

if kubectl get ingressclass nginx >/dev/null 2>&1; then
  actual_default=$(kubectl get ingressclass nginx -o jsonpath='{.metadata.annotations.ingressclass\.kubernetes\.io/is-default-class}')
  check_eq "ingressclass.default" "true" "$actual_default" "scripts/40_ingress_nginx.sh"
else
  mismatch "ingressclass nginx not found"
  add_suggest "scripts/40_ingress_nginx.sh"
fi

say "oauth2-proxy"
if [[ "${FEAT_OAUTH2_PROXY:-true}" == "true" ]]; then
  if kubectl -n ingress get ingress oauth2-proxy >/dev/null 2>&1; then
    actual_host=$(kubectl -n ingress get ingress oauth2-proxy -o jsonpath='{.spec.rules[0].host}')
    actual_issuer=$(kubectl -n ingress get ingress oauth2-proxy -o jsonpath='{.metadata.annotations.cert-manager\.io/cluster-issuer}')
    check_eq "oauth2-proxy.host" "${OAUTH_HOST:-}" "$actual_host" "scripts/45_oauth2_proxy.sh"
    check_eq "oauth2-proxy.issuer" "${CLUSTER_ISSUER:-}" "$actual_issuer" "scripts/45_oauth2_proxy.sh"
  else
    mismatch "oauth2-proxy ingress not found"
    add_suggest "scripts/45_oauth2_proxy.sh"
  fi
else
  ok "FEAT_OAUTH2_PROXY=false; skipping"
fi

say "Grafana"
if [[ "${FEAT_OBS:-true}" == "true" ]]; then
  if kubectl -n observability get ingress monitoring-grafana >/dev/null 2>&1; then
    actual_host=$(kubectl -n observability get ingress monitoring-grafana -o jsonpath='{.spec.rules[0].host}')
    actual_issuer=$(kubectl -n observability get ingress monitoring-grafana -o jsonpath='{.metadata.annotations.cert-manager\.io/cluster-issuer}')
    check_eq "grafana.host" "${GRAFANA_HOST:-}" "$actual_host" "scripts/60_prom_grafana.sh"
    check_eq "grafana.issuer" "${CLUSTER_ISSUER:-}" "$actual_issuer" "scripts/60_prom_grafana.sh"
  else
    mismatch "grafana ingress not found"
    add_suggest "scripts/60_prom_grafana.sh"
  fi
else
  ok "FEAT_OBS=false; skipping"
fi

say "MinIO"
if [[ "${FEAT_MINIO:-true}" == "true" ]]; then
  if kubectl -n storage get ingress minio >/dev/null 2>&1; then
    actual_host=$(kubectl -n storage get ingress minio -o jsonpath='{.spec.rules[0].host}')
    actual_issuer=$(kubectl -n storage get ingress minio -o jsonpath='{.metadata.annotations.cert-manager\.io/cluster-issuer}')
    check_eq "minio.host" "${MINIO_HOST:-}" "$actual_host" "scripts/70_minio.sh"
    check_eq "minio.issuer" "${CLUSTER_ISSUER:-}" "$actual_issuer" "scripts/70_minio.sh"
  else
    mismatch "minio ingress not found"
    add_suggest "scripts/70_minio.sh"
  fi
  if kubectl -n storage get ingress minio-console >/dev/null 2>&1; then
    actual_host=$(kubectl -n storage get ingress minio-console -o jsonpath='{.spec.rules[0].host}')
    actual_issuer=$(kubectl -n storage get ingress minio-console -o jsonpath='{.metadata.annotations.cert-manager\.io/cluster-issuer}')
    check_eq "minio-console.host" "${MINIO_CONSOLE_HOST:-}" "$actual_host" "scripts/70_minio.sh"
    check_eq "minio-console.issuer" "${CLUSTER_ISSUER:-}" "$actual_issuer" "scripts/70_minio.sh"
  else
    mismatch "minio-console ingress not found"
    add_suggest "scripts/70_minio.sh"
  fi
else
  ok "FEAT_MINIO=false; skipping"
fi

say "Fluent Bit"
if [[ "${FEAT_LOGGING:-true}" == "true" ]]; then
  if kubectl -n logging get cm fluent-bit >/dev/null 2>&1; then
    actual_fb=$(kubectl -n logging get cm fluent-bit -o jsonpath='{.data.fluent-bit\.conf}')
    if echo "$actual_fb" | rg -q "Name loki"; then
      ok "fluent-bit outputs include loki"
    else
      mismatch "fluent-bit outputs missing loki"
      add_suggest "scripts/50_logging_fluentbit.sh"
    fi
  else
    mismatch "fluent-bit configmap not found"
    add_suggest "scripts/50_logging_fluentbit.sh"
  fi
else
  ok "FEAT_LOGGING=false; skipping"
fi

if [[ "$fail" -eq 1 ]]; then
  printf "\nValidation failed.\n"
  if [[ ${#SUGGEST[@]} -gt 0 ]]; then
    printf "Suggested re-runs:\n"
    for s in "${SUGGEST[@]}"; do
      printf "  - %s\n" "$s"
    done
  fi
  exit 1
fi

printf "\nValidation passed.\n"
