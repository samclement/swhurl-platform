#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

ensure_context

fail=0
declare -a SUGGEST=()
SUGGEST_RECONCILE_STACK="scripts/32_reconcile_flux_stack.sh"
SUGGEST_RECONCILE_PLATFORM="flux reconcile kustomization homelab-platform -n flux-system"

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

suggest_reconcile_stack() {
  add_suggest "$SUGGEST_RECONCILE_STACK"
}

suggest_reconcile_platform() {
  add_suggest "$SUGGEST_RECONCILE_PLATFORM"
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

expected_ingress_class="${INGRESS_PROVIDER:-traefik}"

ingress_class() {
  local namespace="$1" name="$2" class=""
  class="$(kubectl -n "$namespace" get ingress "$name" -o jsonpath='{.spec.ingressClassName}' 2>/dev/null || true)"
  if [[ -z "$class" ]]; then
    class="$(kubectl -n "$namespace" get ingress "$name" -o jsonpath='{.metadata.annotations.kubernetes\.io/ingress\.class}' 2>/dev/null || true)"
  fi
  printf '%s' "$class"
}

check_flux_kustomization_path() {
  local name="$1" expected_path="$2"
  local path=""
  if kubectl -n flux-system get kustomization "$name" >/dev/null 2>&1; then
    path="$(kubectl -n flux-system get kustomization "$name" -o jsonpath='{.spec.path}')"
    if [[ "$path" != "$expected_path" ]]; then
      warn "${name} path '$path' is unexpected (expected ${expected_path})"
    fi
  else
    warn "${name} kustomization not found"
  fi
  printf '%s' "$path"
}

check_cluster_resource_present() {
  local kind="$1" name="$2" present_msg="$3" missing_msg="$4" suggest="${5:-}"
  if kubectl get "$kind" "$name" >/dev/null 2>&1; then
    ok "$present_msg"
  else
    mismatch "$missing_msg"
    [[ -n "$suggest" ]] && add_suggest "$suggest"
  fi
}

check_namespaced_resource_present() {
  local kind="$1" namespace="$2" name="$3" present_msg="$4" missing_msg="$5" suggest="${6:-}"
  if kubectl -n "$namespace" get "$kind" "$name" >/dev/null 2>&1; then
    ok "$present_msg"
  else
    mismatch "$missing_msg"
    [[ -n "$suggest" ]] && add_suggest "$suggest"
  fi
}

check_namespaced_selector_present() {
  local kind="$1" namespace="$2" selector="$3" present_msg="$4" missing_msg="$5" suggest="${6:-}"
  local names
  names="$(kubectl -n "$namespace" get "$kind" -l "$selector" -o jsonpath='{.items[*].metadata.name}' 2>/dev/null || true)"
  if [[ -n "$names" ]]; then
    ok "$present_msg"
  else
    mismatch "$missing_msg"
    [[ -n "$suggest" ]] && add_suggest "$suggest"
  fi
}

check_service_nodeport() {
  local namespace="$1" service="$2" port_name="$3" expected_nodeport="$4" suggest="${5:-}"
  local actual_nodeport

  if ! kubectl -n "$namespace" get svc "$service" >/dev/null 2>&1; then
    mismatch "${namespace}/${service} service not found"
    [[ -n "$suggest" ]] && add_suggest "$suggest"
    return
  fi

  actual_nodeport="$(kubectl -n "$namespace" get svc "$service" -o jsonpath="{.spec.ports[?(@.name==\"${port_name}\")].nodePort}" 2>/dev/null || true)"
  if [[ -z "$actual_nodeport" ]]; then
    mismatch "${namespace}/${service} port '${port_name}' nodePort is empty"
    [[ -n "$suggest" ]] && add_suggest "$suggest"
    return
  fi

  check_eq "${service}.nodePort.${port_name}" "$expected_nodeport" "$actual_nodeport" "$suggest"
}

check_ingress_contract() {
  local namespace="$1" name="$2" label_prefix="$3" expected_host="$4" expected_issuer="$5" expected_class="$6"
  local suggest_host="$7" suggest_issuer="$8" suggest_class="$9"

  if kubectl -n "$namespace" get ingress "$name" >/dev/null 2>&1; then
    local actual_host actual_issuer actual_class
    actual_host="$(kubectl -n "$namespace" get ingress "$name" -o jsonpath='{.spec.rules[0].host}')"
    actual_issuer="$(kubectl -n "$namespace" get ingress "$name" -o jsonpath='{.metadata.annotations.cert-manager\.io/cluster-issuer}')"
    actual_class="$(ingress_class "$namespace" "$name")"
    [[ -n "$expected_host" ]] && check_eq "${label_prefix}.host" "$expected_host" "$actual_host" "$suggest_host"
    [[ -n "$expected_issuer" ]] && check_eq "${label_prefix}.issuer" "$expected_issuer" "$actual_issuer" "$suggest_issuer"
    [[ -n "$expected_class" ]] && check_eq "${label_prefix}.class" "$expected_class" "$actual_class" "$suggest_class"
  else
    mismatch "${name} ingress not found in namespace ${namespace}"
    [[ -n "$suggest_host" ]] && add_suggest "$suggest_host"
  fi
}

check_certificate_contract() {
  local namespace="$1" name="$2" label_prefix="$3" expected_host="$4" expected_issuer="$5" suggest="$6"

  if kubectl -n "$namespace" get certificate "$name" >/dev/null 2>&1; then
    local actual_cert_host actual_cert_issuer
    actual_cert_host="$(kubectl -n "$namespace" get certificate "$name" -o jsonpath='{.spec.dnsNames[0]}')"
    actual_cert_issuer="$(kubectl -n "$namespace" get certificate "$name" -o jsonpath='{.spec.issuerRef.name}')"
    check_eq "${label_prefix}.host" "$expected_host" "$actual_cert_host" "$suggest"
    check_eq "${label_prefix}.issuer" "$expected_issuer" "$actual_cert_issuer" "$suggest"
  else
    mismatch "${name} certificate not found in namespace ${namespace}"
    [[ -n "$suggest" ]] && add_suggest "$suggest"
  fi
}

read_secret_data() {
  local namespace="$1" name="$2" key="$3"
  kubectl -n "$namespace" get secret "$name" -o jsonpath="{.data.${key}}" 2>/dev/null \
    | base64 --decode 2>/dev/null || true
}

say "ClusterIssuer"
platform_cert_issuer="letsencrypt-staging"
if kubectl -n flux-system get configmap platform-settings >/dev/null 2>&1; then
  configured_platform_cert_issuer="$(kubectl -n flux-system get configmap platform-settings -o jsonpath='{.data.CERT_ISSUER}')"
  case "$configured_platform_cert_issuer" in
    letsencrypt-staging|letsencrypt-prod)
      platform_cert_issuer="$configured_platform_cert_issuer"
      ;;
    "")
      warn "platform-settings.CERT_ISSUER is empty; defaulting expected platform issuer to letsencrypt-staging"
      ;;
    *)
      warn "platform-settings.CERT_ISSUER has unsupported value '$configured_platform_cert_issuer'; expected letsencrypt-staging|letsencrypt-prod. Defaulting to letsencrypt-staging"
      ;;
  esac
else
  warn "platform-settings ConfigMap not found in flux-system; defaulting expected platform issuer to letsencrypt-staging"
fi

expected_infrastructure_issuer="$platform_cert_issuer"
infrastructure_path="$(check_flux_kustomization_path homelab-infrastructure ./infrastructure/overlays/home)"
ok "infrastructure issuer expectation: ${expected_infrastructure_issuer} (path: ${infrastructure_path:-<unknown>}, source: flux-system/platform-settings.CERT_ISSUER)"

expected_platform_services_issuer="$platform_cert_issuer"
platform_services_path="$(check_flux_kustomization_path homelab-platform ./platform-services/overlays/home)"
ok "platform-services issuer expectation: ${expected_platform_services_issuer} (path: ${platform_services_path:-<unknown>}, source: flux-system/platform-settings.CERT_ISSUER)"

tenants_path="$(check_flux_kustomization_path homelab-tenants ./tenants/app-envs)"

app_example_path="$(check_flux_kustomization_path homelab-app-example ./tenants/apps/example)"
ok "app expectation: staged and prod overlays both deployed with letsencrypt-prod (path: ${app_example_path:-<unknown>})"

check_cluster_resource_present "clusterissuer" "selfsigned" \
  "selfsigned ClusterIssuer present" \
  "ClusterIssuer selfsigned not found" \
  "$SUGGEST_RECONCILE_STACK"

for issuer_name in letsencrypt-staging letsencrypt-prod; do
  check_cluster_resource_present "clusterissuer" "$issuer_name" \
    "${issuer_name} ClusterIssuer present" \
    "ClusterIssuer ${issuer_name} not found" \
    "$SUGGEST_RECONCILE_STACK"
done

expected_staging_server="$(verify_expected_letsencrypt_server staging)"
expected_prod_server="$(verify_expected_letsencrypt_server prod)"
if kubectl get clusterissuer letsencrypt-staging >/dev/null 2>&1; then
  actual_server=$(kubectl get clusterissuer letsencrypt-staging -o jsonpath='{.spec.acme.server}')
  check_eq "letsencrypt-staging.server" "${expected_staging_server}" "$actual_server" "$SUGGEST_RECONCILE_STACK"
fi
if kubectl get clusterissuer letsencrypt-prod >/dev/null 2>&1; then
  actual_server=$(kubectl get clusterissuer letsencrypt-prod -o jsonpath='{.spec.acme.server}')
  check_eq "letsencrypt-prod.server" "${expected_prod_server}" "$actual_server" "$SUGGEST_RECONCILE_STACK"
fi

say "Ingress"
if [[ "${INGRESS_PROVIDER:-traefik}" == "nginx" ]]; then
  if kubectl -n ingress get svc ingress-nginx-controller >/dev/null 2>&1; then
    actual_svc_type=$(kubectl -n ingress get svc ingress-nginx-controller -o jsonpath='{.spec.type}')
    check_eq "service.type" "$VERIFY_INGRESS_SERVICE_TYPE" "$actual_svc_type" "$SUGGEST_RECONCILE_STACK"
    actual_http_np=$(kubectl -n ingress get svc ingress-nginx-controller -o jsonpath='{.spec.ports[?(@.name=="http")].nodePort}')
    actual_https_np=$(kubectl -n ingress get svc ingress-nginx-controller -o jsonpath='{.spec.ports[?(@.name=="https")].nodePort}')
    check_eq "nodePort.http" "$VERIFY_INGRESS_NODEPORT_HTTP" "$actual_http_np" "$SUGGEST_RECONCILE_STACK"
    check_eq "nodePort.https" "$VERIFY_INGRESS_NODEPORT_HTTPS" "$actual_https_np" "$SUGGEST_RECONCILE_STACK"
  else
    mismatch "ingress-nginx service not found"
    suggest_reconcile_stack
  fi

  if kubectl -n ingress get cm ingress-nginx-controller >/dev/null 2>&1; then
    actual_log=$(kubectl -n ingress get cm ingress-nginx-controller -o jsonpath='{.data.log-format-upstream}')
    if [[ -n "$actual_log" ]]; then
      ok "log-format-upstream present"
    else
      mismatch "log-format-upstream missing"
      suggest_reconcile_stack
    fi
  else
    mismatch "ingress-nginx configmap not found"
    suggest_reconcile_stack
  fi

  if kubectl get ingressclass nginx >/dev/null 2>&1; then
    actual_default=$(kubectl get ingressclass nginx -o jsonpath='{.metadata.annotations.ingressclass\.kubernetes\.io/is-default-class}')
    check_eq "ingressclass.default" "true" "$actual_default" "$SUGGEST_RECONCILE_STACK"
  else
    mismatch "ingressclass nginx not found"
    suggest_reconcile_stack
  fi
elif [[ "${INGRESS_PROVIDER:-traefik}" == "traefik" ]]; then
  check_namespaced_resource_present "deploy" "kube-system" "traefik" \
    "traefik deployment present" \
    "traefik deployment not found in kube-system" \
    "verify k3s packaged traefik is enabled"
  check_namespaced_resource_present "svc" "kube-system" "traefik" \
    "traefik service present" \
    "traefik service not found in kube-system" \
    "verify k3s packaged traefik is enabled"
  check_cluster_resource_present "ingressclass" "traefik" \
    "ingressclass traefik present" \
    "ingressclass traefik not found" \
    "verify k3s packaged traefik is enabled"
  check_service_nodeport "kube-system" "traefik" "web" "$VERIFY_INGRESS_NODEPORT_HTTP" \
    "verify k3s packaged traefik is enabled"
  check_service_nodeport "kube-system" "traefik" "websecure" "$VERIFY_INGRESS_NODEPORT_HTTPS" \
    "verify k3s packaged traefik is enabled"
else
  ok "INGRESS_PROVIDER=${INGRESS_PROVIDER:-traefik}; skipping ingress controller specific checks"
fi

say "oauth2-proxy-shared"
check_namespaced_resource_present "deploy" "ingress" "oauth2-proxy-shared" \
  "oauth2-proxy-shared deployment present" \
  "oauth2-proxy-shared deployment not found" \
  "$SUGGEST_RECONCILE_STACK"
check_ingress_contract "ingress" "oauth2-proxy-shared" "oauth2-proxy-shared" \
  "${OAUTH_HOST:-}" "${expected_platform_services_issuer}" "${expected_ingress_class}" \
  "$SUGGEST_RECONCILE_STACK" "clusters/home/platform.yaml" "docs/runbooks/migrate-ingress-nginx-to-traefik.md"

say "ClickStack"
check_ingress_contract "observability" "clickstack-app-ingress" "clickstack" \
  "${CLICKSTACK_HOST:-}" "${expected_platform_services_issuer}" "${expected_ingress_class}" \
  "$SUGGEST_RECONCILE_STACK" "clusters/home/platform.yaml" "docs/runbooks/migrate-ingress-nginx-to-traefik.md"
for clickstack_deploy in clickstack-app clickstack-otel-collector clickstack-clickhouse; do
  check_namespaced_resource_present "deploy" "observability" "$clickstack_deploy" \
    "${clickstack_deploy} deployment present" \
    "${clickstack_deploy} deployment not found" \
    "$SUGGEST_RECONCILE_STACK"
done
source_api_key="$(read_secret_data flux-system platform-runtime-inputs CLICKSTACK_API_KEY)"
source_ingestion_key="$(read_secret_data flux-system platform-runtime-inputs CLICKSTACK_INGESTION_KEY)"
if kubectl -n observability get secret clickstack-runtime-inputs >/dev/null 2>&1; then
  runtime_api_key="$(read_secret_data observability clickstack-runtime-inputs CLICKSTACK_API_KEY)"
  if [[ -z "$source_api_key" ]]; then
    mismatch "flux-system/platform-runtime-inputs.CLICKSTACK_API_KEY is empty; cannot verify clickstack runtime-input alignment"
    add_suggest "make runtime-inputs-sync"
  elif [[ -z "$runtime_api_key" ]]; then
    mismatch "clickstack-runtime-inputs.CLICKSTACK_API_KEY is empty"
    suggest_reconcile_platform
  elif [[ "$source_api_key" != "$runtime_api_key" ]]; then
    mismatch "clickstack runtime-input mismatch: flux-system/platform-runtime-inputs.CLICKSTACK_API_KEY does not match observability/clickstack-runtime-inputs.CLICKSTACK_API_KEY"
    add_suggest "make runtime-inputs-sync"
    suggest_reconcile_platform
  else
    ok "clickstack runtime-input key alignment check passed"
  fi
else
  mismatch "clickstack-runtime-inputs secret not found"
  suggest_reconcile_platform
fi

say "Kubernetes OTel Collectors"
check_namespaced_selector_present "ds" "logging" "app.kubernetes.io/instance=otel-k8s-daemonset" \
  "otel-k8s daemonset release present" \
  "otel-k8s daemonset release not found" \
  "$SUGGEST_RECONCILE_STACK"
check_namespaced_selector_present "deploy" "logging" "app.kubernetes.io/instance=otel-k8s-cluster" \
  "otel-k8s cluster deployment release present" \
  "otel-k8s cluster deployment release not found" \
  "$SUGGEST_RECONCILE_STACK"
sender_token="${source_ingestion_key:-${source_api_key:-}}"
if [[ -z "$sender_token" ]]; then
  mismatch "platform-runtime-inputs.CLICKSTACK_INGESTION_KEY/CLICKSTACK_API_KEY are empty; cannot verify otel token alignment"
  add_suggest "make runtime-inputs-sync"
elif kubectl -n logging get secret hyperdx-secret >/dev/null 2>&1; then
  receiver_token="$(read_secret_data logging hyperdx-secret HYPERDX_API_KEY)"
  if [[ -z "$receiver_token" ]]; then
    mismatch "hyperdx-secret.HYPERDX_API_KEY is empty"
    add_suggest "make runtime-inputs-sync"
    suggest_reconcile_platform
  elif [[ "$sender_token" != "$receiver_token" ]]; then
    mismatch "otel token mismatch: platform-runtime-inputs.CLICKSTACK_INGESTION_KEY (or CLICKSTACK_API_KEY fallback) does not match hyperdx-secret.HYPERDX_API_KEY"
    add_suggest "make runtime-inputs-sync"
    suggest_reconcile_platform
  else
    ok "otel token alignment check passed"
    if [[ -z "$source_ingestion_key" ]]; then
      warn "CLICKSTACK_INGESTION_KEY is not set; using CLICKSTACK_API_KEY fallback for OTel exporters"
    fi
  fi
else
  mismatch "hyperdx-secret not found"
  suggest_reconcile_platform
fi

say "MinIO"
if [[ "${OBJECT_STORAGE_PROVIDER:-minio}" == "minio" ]]; then
  check_ingress_contract "storage" "minio" "minio" \
    "${MINIO_HOST:-}" "${expected_infrastructure_issuer}" "${expected_ingress_class}" \
    "$SUGGEST_RECONCILE_STACK" "clusters/home/infrastructure.yaml" "docs/runbooks/migrate-ingress-nginx-to-traefik.md"
  check_ingress_contract "storage" "minio-console" "minio-console" \
    "${MINIO_CONSOLE_HOST:-}" "${expected_infrastructure_issuer}" "${expected_ingress_class}" \
    "$SUGGEST_RECONCILE_STACK" "clusters/home/infrastructure.yaml" "docs/runbooks/migrate-ingress-nginx-to-traefik.md"
else
  ok "OBJECT_STORAGE_PROVIDER=${OBJECT_STORAGE_PROVIDER:-minio}; skipping MinIO checks"
fi

say "Example App"
check_ingress_contract "apps-staging" "hello-web" "hello-web.staging" \
  "staging-hello.homelab.swhurl.com" "" "${expected_ingress_class}" \
  "clusters/home/app-example.yaml" "" "docs/runbooks/migrate-ingress-nginx-to-traefik.md"
check_certificate_contract "apps-staging" "hello-web" "hello-web.staging.certificate" \
  "staging-hello.homelab.swhurl.com" "letsencrypt-prod" "clusters/home/app-example.yaml"
check_ingress_contract "apps-prod" "hello-web" "hello-web.prod" \
  "hello.homelab.swhurl.com" "" "${expected_ingress_class}" \
  "clusters/home/app-example.yaml" "" "docs/runbooks/migrate-ingress-nginx-to-traefik.md"
check_certificate_contract "apps-prod" "hello-web" "hello-web.prod.certificate" \
  "hello.homelab.swhurl.com" "letsencrypt-prod" "clusters/home/app-example.yaml"

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
