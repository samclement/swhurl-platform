#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

ensure_context

VERIFY_FAIL=0
declare -a VERIFY_SUGGEST=()
SUGGEST_RECONCILE_STACK="scripts/32_reconcile_flux_stack.sh"
SUGGEST_RECONCILE_PLATFORM="flux reconcile kustomization homelab-platform -n flux-system"

suggest_reconcile_stack() { verify_add_suggest "$SUGGEST_RECONCILE_STACK"; }
suggest_reconcile_platform() { verify_add_suggest "$SUGGEST_RECONCILE_PLATFORM"; }

expected_ingress_class="traefik"

verify_say "ClusterIssuer"
platform_cert_issuer="letsencrypt-staging"
if kubectl -n flux-system get configmap platform-settings >/dev/null 2>&1; then
  configured_platform_cert_issuer="$(kubectl -n flux-system get configmap platform-settings -o jsonpath='{.data.CERT_ISSUER}')"
  case "$configured_platform_cert_issuer" in
    letsencrypt-staging|letsencrypt-prod)
      platform_cert_issuer="$configured_platform_cert_issuer"
      ;;
    "")
      verify_warn "platform-settings.CERT_ISSUER is empty; defaulting expected platform issuer to letsencrypt-staging"
      ;;
    *)
      verify_warn "platform-settings.CERT_ISSUER has unsupported value '$configured_platform_cert_issuer'; expected letsencrypt-staging|letsencrypt-prod. Defaulting to letsencrypt-staging"
      ;;
  esac
else
  verify_warn "platform-settings ConfigMap not found in flux-system; defaulting expected platform issuer to letsencrypt-staging"
fi

expected_infrastructure_issuer="$platform_cert_issuer"
infrastructure_path="$(check_flux_kustomization_path homelab-infrastructure ./infrastructure/overlays/home)"
verify_ok "infrastructure issuer expectation: ${expected_infrastructure_issuer} (path: ${infrastructure_path:-<unknown>}, source: flux-system/platform-settings.CERT_ISSUER)"

expected_platform_services_issuer="$platform_cert_issuer"
platform_services_path="$(check_flux_kustomization_path homelab-platform ./platform-services/overlays/home)"
verify_ok "platform-services issuer expectation: ${expected_platform_services_issuer} (path: ${platform_services_path:-<unknown>}, source: flux-system/platform-settings.CERT_ISSUER)"

tenants_path="$(check_flux_kustomization_path homelab-tenants ./tenants/app-envs)"

app_example_path="$(check_flux_kustomization_path homelab-app-example ./tenants/apps/example)"
verify_ok "app expectation: staged and prod overlays both deployed with letsencrypt-prod (path: ${app_example_path:-<unknown>})"

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

verify_say "Ingress (k3s packaged traefik)"
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

verify_say "oauth2-proxy-shared"
check_namespaced_resource_present "deploy" "ingress" "oauth2-proxy-shared" \
  "oauth2-proxy-shared deployment present" \
  "oauth2-proxy-shared deployment not found" \
  "$SUGGEST_RECONCILE_STACK"
check_ingress_contract "ingress" "oauth2-proxy-shared" "oauth2-proxy-shared" \
  "${OAUTH_HOST:-}" "${expected_platform_services_issuer}" "${expected_ingress_class}" \
  "$SUGGEST_RECONCILE_STACK" "clusters/home/platform.yaml" "docs/runbooks/migrate-ingress-nginx-to-traefik.md"

verify_say "ClickStack"
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
    verify_mismatch "flux-system/platform-runtime-inputs.CLICKSTACK_API_KEY is empty; cannot verify clickstack runtime-input alignment"
    verify_add_suggest "make runtime-inputs-sync"
  elif [[ -z "$runtime_api_key" ]]; then
    verify_mismatch "clickstack-runtime-inputs.CLICKSTACK_API_KEY is empty"
    suggest_reconcile_platform
  elif [[ "$source_api_key" != "$runtime_api_key" ]]; then
    verify_mismatch "clickstack runtime-input mismatch: flux-system/platform-runtime-inputs.CLICKSTACK_API_KEY does not match observability/clickstack-runtime-inputs.CLICKSTACK_API_KEY"
    verify_add_suggest "make runtime-inputs-sync"
    suggest_reconcile_platform
  else
    verify_ok "clickstack runtime-input key alignment check passed"
  fi
else
  verify_mismatch "clickstack-runtime-inputs secret not found"
  suggest_reconcile_platform
fi

verify_say "Kubernetes OTel Collectors"
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
  verify_mismatch "platform-runtime-inputs.CLICKSTACK_INGESTION_KEY/CLICKSTACK_API_KEY are empty; cannot verify otel token alignment"
  verify_add_suggest "make runtime-inputs-sync"
elif kubectl -n logging get secret hyperdx-secret >/dev/null 2>&1; then
  receiver_token="$(read_secret_data logging hyperdx-secret HYPERDX_API_KEY)"
  if [[ -z "$receiver_token" ]]; then
    verify_mismatch "hyperdx-secret.HYPERDX_API_KEY is empty"
    verify_add_suggest "make runtime-inputs-sync"
    suggest_reconcile_platform
  elif [[ "$sender_token" != "$receiver_token" ]]; then
    verify_mismatch "otel token mismatch: platform-runtime-inputs.CLICKSTACK_INGESTION_KEY (or CLICKSTACK_API_KEY fallback) does not match hyperdx-secret.HYPERDX_API_KEY"
    verify_add_suggest "make runtime-inputs-sync"
    suggest_reconcile_platform
  else
    verify_ok "otel token alignment check passed"
    if [[ -z "$source_ingestion_key" ]]; then
      verify_warn "CLICKSTACK_INGESTION_KEY is not set; using CLICKSTACK_API_KEY fallback for OTel exporters"
    fi
  fi
else
  verify_mismatch "hyperdx-secret not found"
  suggest_reconcile_platform
fi

verify_say "MinIO"
check_ingress_contract "storage" "minio" "minio" \
  "${MINIO_HOST:-}" "${expected_infrastructure_issuer}" "${expected_ingress_class}" \
  "$SUGGEST_RECONCILE_STACK" "clusters/home/infrastructure.yaml" "docs/runbooks/migrate-ingress-nginx-to-traefik.md"
check_ingress_contract "storage" "minio-console" "minio-console" \
  "${MINIO_CONSOLE_HOST:-}" "${expected_infrastructure_issuer}" "${expected_ingress_class}" \
  "$SUGGEST_RECONCILE_STACK" "clusters/home/infrastructure.yaml" "docs/runbooks/migrate-ingress-nginx-to-traefik.md"

verify_say "Example App"
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

if [[ "$VERIFY_FAIL" -eq 1 ]]; then
  printf "\nValidation failed.\n"
  if [[ ${#VERIFY_SUGGEST[@]} -gt 0 ]]; then
    printf "Suggested re-runs:\n"
    for s in "${VERIFY_SUGGEST[@]}"; do
      printf "  - %s\n" "$s"
    done
  fi
  exit 1
fi

printf "\nValidation passed.\n"
