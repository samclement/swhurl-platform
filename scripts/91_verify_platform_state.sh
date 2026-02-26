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
expected_platform_issuer="${PLATFORM_CLUSTER_ISSUER:-${CLUSTER_ISSUER:-letsencrypt-staging}}"
if is_allowed_cluster_issuer "$expected_platform_issuer"; then
  ok "PLATFORM_CLUSTER_ISSUER resolved to ${expected_platform_issuer}"
else
  mismatch "PLATFORM_CLUSTER_ISSUER invalid: ${expected_platform_issuer}"
  add_suggest "scripts/94_verify_config_inputs.sh"
fi

if kubectl get clusterissuer selfsigned >/dev/null 2>&1; then
  ok "selfsigned ClusterIssuer present"
else
  mismatch "ClusterIssuer selfsigned not found"
  add_suggest "scripts/31_sync_helmfile_phase_core.sh"
fi

if [[ -z "${ACME_EMAIL:-}" ]]; then
  warn "ACME_EMAIL is empty; cannot validate letsencrypt issuer emails"
fi

for issuer_name in letsencrypt-staging letsencrypt-prod letsencrypt; do
  if kubectl get clusterissuer "$issuer_name" >/dev/null 2>&1; then
    ok "${issuer_name} ClusterIssuer present"
  else
    mismatch "ClusterIssuer ${issuer_name} not found"
    add_suggest "scripts/31_sync_helmfile_phase_core.sh"
  fi
done

if [[ -n "${ACME_EMAIL:-}" ]]; then
  for issuer_name in letsencrypt-staging letsencrypt-prod letsencrypt; do
    if kubectl get clusterissuer "$issuer_name" >/dev/null 2>&1; then
      actual_email=$(kubectl get clusterissuer "$issuer_name" -o jsonpath='{.spec.acme.email}')
      check_eq "${issuer_name}.email" "${ACME_EMAIL}" "$actual_email" "scripts/31_sync_helmfile_phase_core.sh"
    fi
  done
fi

expected_staging_server="$(verify_expected_letsencrypt_server staging)"
expected_prod_server="$(verify_expected_letsencrypt_server prod)"
expected_alias_server="$(verify_expected_letsencrypt_server alias "${LETSENCRYPT_ENV:-staging}")"
if kubectl get clusterissuer letsencrypt-staging >/dev/null 2>&1; then
  actual_server=$(kubectl get clusterissuer letsencrypt-staging -o jsonpath='{.spec.acme.server}')
  check_eq "letsencrypt-staging.server" "${expected_staging_server}" "$actual_server" "scripts/31_sync_helmfile_phase_core.sh"
fi
if kubectl get clusterissuer letsencrypt-prod >/dev/null 2>&1; then
  actual_server=$(kubectl get clusterissuer letsencrypt-prod -o jsonpath='{.spec.acme.server}')
  check_eq "letsencrypt-prod.server" "${expected_prod_server}" "$actual_server" "scripts/31_sync_helmfile_phase_core.sh"
fi
if kubectl get clusterissuer letsencrypt >/dev/null 2>&1; then
  actual_server=$(kubectl get clusterissuer letsencrypt -o jsonpath='{.spec.acme.server}')
  check_eq "letsencrypt.server" "${expected_alias_server}" "$actual_server" "scripts/31_sync_helmfile_phase_core.sh"
fi

say "Cilium"
if feature_is_enabled cilium; then
  if kubectl -n kube-system get ds cilium >/dev/null 2>&1; then
    ok "cilium daemonset present"
  else
    mismatch "cilium daemonset not found"
    add_suggest "scripts/26_manage_cilium_lifecycle.sh"
  fi
  if kubectl -n kube-system get deploy cilium-operator >/dev/null 2>&1; then
    ok "cilium-operator deployment present"
  else
    mismatch "cilium-operator deployment not found"
    add_suggest "scripts/26_manage_cilium_lifecycle.sh"
  fi
else
  ok "$(feature_flag_var cilium)=false; skipping"
fi

say "Hubble"
if feature_is_enabled cilium; then
  if kubectl -n kube-system get deploy hubble-relay >/dev/null 2>&1; then
    ok "hubble-relay deployment present"
  else
    mismatch "hubble-relay deployment not found"
    add_suggest "scripts/26_manage_cilium_lifecycle.sh"
  fi
  if kubectl -n kube-system get deploy hubble-ui >/dev/null 2>&1; then
    ok "hubble-ui deployment present"
  else
    mismatch "hubble-ui deployment not found"
    add_suggest "scripts/26_manage_cilium_lifecycle.sh"
  fi
  if kubectl -n kube-system get ingress hubble-ui >/dev/null 2>&1; then
    actual_host=$(kubectl -n kube-system get ingress hubble-ui -o jsonpath='{.spec.rules[0].host}')
    actual_issuer=$(kubectl -n kube-system get ingress hubble-ui -o jsonpath='{.metadata.annotations.cert-manager\.io/cluster-issuer}')
    check_eq "hubble-ui.host" "${HUBBLE_HOST:-}" "$actual_host" "scripts/26_manage_cilium_lifecycle.sh"
    check_eq "hubble-ui.issuer" "${expected_platform_issuer}" "$actual_issuer" "scripts/26_manage_cilium_lifecycle.sh"
    if feature_is_enabled oauth2_proxy && [[ "${INGRESS_PROVIDER:-nginx}" == "nginx" ]]; then
      expected_auth_url="$(verify_oauth_auth_url "${OAUTH_HOST}")"
      expected_auth_signin="$(verify_oauth_auth_signin "${OAUTH_HOST}")"
      actual_auth_url=$(kubectl -n kube-system get ingress hubble-ui -o jsonpath='{.metadata.annotations.nginx\.ingress\.kubernetes\.io/auth-url}')
      actual_auth_signin=$(kubectl -n kube-system get ingress hubble-ui -o jsonpath='{.metadata.annotations.nginx\.ingress\.kubernetes\.io/auth-signin}')
      check_eq "hubble-ui.auth-url" "${expected_auth_url}" "$actual_auth_url" "scripts/26_manage_cilium_lifecycle.sh"
      check_eq "hubble-ui.auth-signin" "${expected_auth_signin}" "$actual_auth_signin" "scripts/26_manage_cilium_lifecycle.sh"
    elif feature_is_enabled oauth2_proxy; then
      ok "INGRESS_PROVIDER=${INGRESS_PROVIDER:-nginx}; skipping NGINX auth annotation checks for hubble-ui"
    else
      ok "$(feature_flag_var oauth2_proxy)=false; skipping hubble-ui auth annotation checks"
    fi
  else
    mismatch "hubble-ui ingress not found"
    add_suggest "scripts/26_manage_cilium_lifecycle.sh"
  fi
else
  ok "$(feature_flag_var cilium)=false; skipping"
fi

say "Ingress"
if [[ "${INGRESS_PROVIDER:-nginx}" == "nginx" ]]; then
  if kubectl -n ingress get svc ingress-nginx-controller >/dev/null 2>&1; then
    actual_svc_type=$(kubectl -n ingress get svc ingress-nginx-controller -o jsonpath='{.spec.type}')
    check_eq "service.type" "$VERIFY_INGRESS_SERVICE_TYPE" "$actual_svc_type" "scripts/31_sync_helmfile_phase_core.sh"
    actual_http_np=$(kubectl -n ingress get svc ingress-nginx-controller -o jsonpath='{.spec.ports[?(@.name=="http")].nodePort}')
    actual_https_np=$(kubectl -n ingress get svc ingress-nginx-controller -o jsonpath='{.spec.ports[?(@.name=="https")].nodePort}')
    check_eq "nodePort.http" "$VERIFY_INGRESS_NODEPORT_HTTP" "$actual_http_np" "scripts/31_sync_helmfile_phase_core.sh"
    check_eq "nodePort.https" "$VERIFY_INGRESS_NODEPORT_HTTPS" "$actual_https_np" "scripts/31_sync_helmfile_phase_core.sh"
  else
    mismatch "ingress-nginx service not found"
    add_suggest "scripts/31_sync_helmfile_phase_core.sh"
  fi

  if kubectl -n ingress get cm ingress-nginx-controller >/dev/null 2>&1; then
    actual_log=$(kubectl -n ingress get cm ingress-nginx-controller -o jsonpath='{.data.log-format-upstream}')
    if [[ -n "$actual_log" ]]; then
      ok "log-format-upstream present"
    else
      mismatch "log-format-upstream missing"
      add_suggest "scripts/31_sync_helmfile_phase_core.sh"
    fi
  else
    mismatch "ingress-nginx configmap not found"
    add_suggest "scripts/31_sync_helmfile_phase_core.sh"
  fi

  if kubectl get ingressclass nginx >/dev/null 2>&1; then
    actual_default=$(kubectl get ingressclass nginx -o jsonpath='{.metadata.annotations.ingressclass\.kubernetes\.io/is-default-class}')
    check_eq "ingressclass.default" "true" "$actual_default" "scripts/31_sync_helmfile_phase_core.sh"
  else
    mismatch "ingressclass nginx not found"
    add_suggest "scripts/31_sync_helmfile_phase_core.sh"
  fi
else
  ok "INGRESS_PROVIDER=${INGRESS_PROVIDER:-nginx}; skipping ingress-nginx specific checks"
fi

say "oauth2-proxy"
if feature_is_enabled oauth2_proxy; then
  if kubectl -n ingress get deploy oauth2-proxy >/dev/null 2>&1; then
    ok "oauth2-proxy deployment present"
  else
    mismatch "oauth2-proxy deployment not found"
    add_suggest "scripts/36_sync_helmfile_phase_platform.sh"
  fi
  if kubectl -n ingress get ingress oauth2-proxy >/dev/null 2>&1; then
    actual_host=$(kubectl -n ingress get ingress oauth2-proxy -o jsonpath='{.spec.rules[0].host}')
    actual_issuer=$(kubectl -n ingress get ingress oauth2-proxy -o jsonpath='{.metadata.annotations.cert-manager\.io/cluster-issuer}')
    check_eq "oauth2-proxy.host" "${OAUTH_HOST:-}" "$actual_host" "scripts/36_sync_helmfile_phase_platform.sh"
    check_eq "oauth2-proxy.issuer" "${expected_platform_issuer}" "$actual_issuer" "scripts/36_sync_helmfile_phase_platform.sh"
  else
    mismatch "oauth2-proxy ingress not found"
    add_suggest "scripts/36_sync_helmfile_phase_platform.sh"
  fi
else
  ok "$(feature_flag_var oauth2_proxy)=false; skipping"
fi

say "ClickStack"
if feature_is_enabled clickstack; then
  if kubectl -n observability get ingress clickstack-app-ingress >/dev/null 2>&1; then
    actual_host=$(kubectl -n observability get ingress clickstack-app-ingress -o jsonpath='{.spec.rules[0].host}')
    actual_issuer=$(kubectl -n observability get ingress clickstack-app-ingress -o jsonpath='{.metadata.annotations.cert-manager\.io/cluster-issuer}')
    check_eq "clickstack.host" "${CLICKSTACK_HOST:-}" "$actual_host" "scripts/36_sync_helmfile_phase_platform.sh"
    check_eq "clickstack.issuer" "${expected_platform_issuer}" "$actual_issuer" "scripts/36_sync_helmfile_phase_platform.sh"
    if feature_is_enabled oauth2_proxy && [[ "${INGRESS_PROVIDER:-nginx}" == "nginx" ]]; then
      expected_auth_url="$(verify_oauth_auth_url "${OAUTH_HOST}")"
      expected_auth_signin="$(verify_oauth_auth_signin "${OAUTH_HOST}")"
      actual_auth_url=$(kubectl -n observability get ingress clickstack-app-ingress -o jsonpath='{.metadata.annotations.nginx\.ingress\.kubernetes\.io/auth-url}')
      actual_auth_signin=$(kubectl -n observability get ingress clickstack-app-ingress -o jsonpath='{.metadata.annotations.nginx\.ingress\.kubernetes\.io/auth-signin}')
      check_eq "clickstack.auth-url" "${expected_auth_url}" "$actual_auth_url" "scripts/36_sync_helmfile_phase_platform.sh"
      check_eq "clickstack.auth-signin" "${expected_auth_signin}" "$actual_auth_signin" "scripts/36_sync_helmfile_phase_platform.sh"
    elif feature_is_enabled oauth2_proxy; then
      ok "INGRESS_PROVIDER=${INGRESS_PROVIDER:-nginx}; skipping NGINX auth annotation checks for clickstack"
    fi
  else
    mismatch "clickstack ingress not found"
    add_suggest "scripts/36_sync_helmfile_phase_platform.sh"
  fi
  if kubectl -n observability get deploy clickstack-app >/dev/null 2>&1; then
    ok "clickstack app deployment present"
  else
    mismatch "clickstack app deployment not found"
    add_suggest "scripts/36_sync_helmfile_phase_platform.sh"
  fi
  if kubectl -n observability get deploy clickstack-otel-collector >/dev/null 2>&1; then
    ok "clickstack otel collector deployment present"
  else
    mismatch "clickstack otel collector deployment not found"
    add_suggest "scripts/36_sync_helmfile_phase_platform.sh"
  fi
  if kubectl -n observability get deploy clickstack-clickhouse >/dev/null 2>&1; then
    ok "clickstack clickhouse deployment present"
  else
    mismatch "clickstack clickhouse deployment not found"
    add_suggest "scripts/36_sync_helmfile_phase_platform.sh"
  fi
else
  ok "$(feature_flag_var clickstack)=false; skipping"
fi

say "Kubernetes OTel Collectors"
if feature_is_enabled otel_k8s; then
  if kubectl -n logging get ds -l app.kubernetes.io/instance=otel-k8s-daemonset >/dev/null 2>&1; then
    ok "otel-k8s daemonset release present"
  else
    mismatch "otel-k8s daemonset release not found"
    add_suggest "scripts/36_sync_helmfile_phase_platform.sh"
  fi
  if kubectl -n logging get deploy -l app.kubernetes.io/instance=otel-k8s-cluster >/dev/null 2>&1; then
    ok "otel-k8s cluster deployment release present"
  else
    mismatch "otel-k8s cluster deployment release not found"
    add_suggest "scripts/36_sync_helmfile_phase_platform.sh"
  fi
  sender_token="${CLICKSTACK_API_KEY:-}"
  if [[ -z "$sender_token" ]]; then
    mismatch "CLICKSTACK_API_KEY is empty; cannot verify otel token alignment"
    add_suggest "scripts/94_verify_config_inputs.sh"
  elif kubectl -n observability get deploy clickstack-otel-collector >/dev/null 2>&1; then
    receiver_token="$(
      kubectl -n observability exec deploy/clickstack-otel-collector -- sh -lc \
        "sed -n '40,60p' /etc/otel/supervisor-data/effective.yaml | sed -n 's/^[[:space:]]*-[[:space:]]*//p' | head -n1" \
        2>/dev/null || true
    )"
    if [[ -n "$sender_token" && -n "$receiver_token" && "$sender_token" != "$receiver_token" ]]; then
      mismatch "otel token mismatch: CLICKSTACK_API_KEY does not match clickstack receiver token"
      add_suggest "scripts/36_sync_helmfile_phase_platform.sh"
    else
      ok "otel token alignment check passed"
    fi
  fi
else
  ok "$(feature_flag_var otel_k8s)=false; skipping"
fi

say "MinIO"
if feature_is_enabled minio && [[ "${OBJECT_STORAGE_PROVIDER:-minio}" == "minio" ]]; then
  if kubectl -n storage get ingress minio >/dev/null 2>&1; then
    actual_host=$(kubectl -n storage get ingress minio -o jsonpath='{.spec.rules[0].host}')
    actual_issuer=$(kubectl -n storage get ingress minio -o jsonpath='{.metadata.annotations.cert-manager\.io/cluster-issuer}')
    check_eq "minio.host" "${MINIO_HOST:-}" "$actual_host" "scripts/36_sync_helmfile_phase_platform.sh"
    check_eq "minio.issuer" "${expected_platform_issuer}" "$actual_issuer" "scripts/36_sync_helmfile_phase_platform.sh"
  else
    mismatch "minio ingress not found"
    add_suggest "scripts/36_sync_helmfile_phase_platform.sh"
  fi
  if kubectl -n storage get ingress minio-console >/dev/null 2>&1; then
    actual_host=$(kubectl -n storage get ingress minio-console -o jsonpath='{.spec.rules[0].host}')
    actual_issuer=$(kubectl -n storage get ingress minio-console -o jsonpath='{.metadata.annotations.cert-manager\.io/cluster-issuer}')
    check_eq "minio-console.host" "${MINIO_CONSOLE_HOST:-}" "$actual_host" "scripts/36_sync_helmfile_phase_platform.sh"
    check_eq "minio-console.issuer" "${expected_platform_issuer}" "$actual_issuer" "scripts/36_sync_helmfile_phase_platform.sh"
  else
    mismatch "minio-console ingress not found"
    add_suggest "scripts/36_sync_helmfile_phase_platform.sh"
  fi
else
  if [[ "${OBJECT_STORAGE_PROVIDER:-minio}" != "minio" ]]; then
    ok "OBJECT_STORAGE_PROVIDER=${OBJECT_STORAGE_PROVIDER:-minio}; skipping MinIO checks"
  else
    ok "$(feature_flag_var minio)=false; skipping"
  fi
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
