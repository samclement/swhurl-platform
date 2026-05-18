SHELL := /usr/bin/env bash
include config.env
export BASE_DOMAIN FEAT_VERIFY TIMEOUT_SECS DYNAMIC_DNS_RECORDS
PLATFORM_SETTINGS_FILE := clusters/home/flux-system/sources/configmap-platform-settings.yaml
DRY_RUN ?= false

define update_cert_issuer
	@set -eu; \
	file="$(PLATFORM_SETTINGS_FILE)"; issuer="$(1)"; \
	[[ -f "$$file" ]] || { echo "Missing settings file: $$file" >&2; exit 1; }; \
	grep -q '^\s*CERT_ISSUER:' "$$file" || { echo "Missing key 'CERT_ISSUER' in $$file" >&2; exit 1; }; \
	if grep -q "CERT_ISSUER: $$issuer$$" "$$file"; then \
	  echo "[INFO] CERT_ISSUER already set to $$issuer"; \
	elif [[ "$(DRY_RUN)" == "true" ]]; then \
	  echo "[INFO] CERT_ISSUER would update to $$issuer in $$file"; \
	else \
	  sed -i "s/^\(\s*CERT_ISSUER:\).*/\1 $$issuer/" "$$file"; \
	  echo "[INFO] CERT_ISSUER updated to $$issuer in $$file"; \
	fi; \
	echo "[INFO] Local Git edits only. Commit + push, then run: make flux-reconcile"
endef

.PHONY: help
help:
	@echo "Targets:"
	@echo "  install             Clean install path (cluster defaults)"
	@echo "  teardown            Stack-only teardown (delete Flux stack kustomizations)"
	@echo "  reinstall           Teardown then install (cluster defaults)"
	@echo "  platform-certs-staging | platform-certs-prod"
	@echo "  flux-bootstrap      Apply Flux bootstrap manifests (requires manual Flux install)"
	@echo "  runtime-inputs-sync Reconcile Git-managed platform runtime SOPS secrets"
	@echo "  otel-collectors-restart Restart otel-k8s collectors (reload hyperdx-secret)"
	@echo "  runtime-inputs-refresh-otel Reconcile runtime inputs, then restart otel-k8s collectors"
	@echo "  runtime-inputs-refresh-clickstack-otel Reconcile ClickStack + OTel (use after rotating ingestion key)"
	@echo "  charts-generate     Render C4 architecture charts from D2 sources"
	@echo "  flux-reconcile      Reconcile Git source and Flux stack"
	@echo "  host-dns            Configure host dynamic DNS systemd updater"
	@echo "  host-dns-delete     Remove host dynamic DNS systemd updater"
	@echo "  verify-config       Run config input contract checks"
	@echo "  verify-platform     Run in-cluster platform state checks"
	@echo "  verify              Run verification scripts against current context"
	@echo ""
	@echo "platform-certs-* targets edit Git-tracked files only. Commit + push before flux-reconcile."
	@echo ""
	@echo "Host dynamic DNS:"
	@echo "  make host-dns [DRY_RUN=true]"
	@echo "  make host-dns-delete [DRY_RUN=true]"

.PHONY: install
install:
	@set -Eeuo pipefail; \
	if [[ "$(DRY_RUN)" == "true" ]]; then \
	  echo "Plan (install):"; \
	  if [[ "$${FEAT_VERIFY:-true}" == "true" ]]; then \
	    echo "  - make verify-config"; \
	  fi; \
	  echo "  - make flux-reconcile"; \
	  if [[ "$${FEAT_VERIFY:-true}" == "true" ]]; then \
	    echo "  - make verify-platform"; \
	  fi; \
	  exit 0; \
	fi; \
	if [[ "$${FEAT_VERIFY:-true}" == "true" ]]; then \
	  $(MAKE) verify-config; \
	fi; \
	$(MAKE) flux-reconcile; \
	if [[ "$${FEAT_VERIFY:-true}" == "true" ]]; then \
	  $(MAKE) verify-platform; \
	fi

.PHONY: teardown
teardown:
	@set -Eeuo pipefail; \
	if [[ "$(DRY_RUN)" == "true" ]]; then \
	  echo "Plan (teardown):"; \
	  echo "  - delete homelab-flux-stack and homelab-flux-sources kustomizations"; \
	  exit 0; \
	fi; \
	kubectl -n flux-system delete kustomization homelab-flux-stack --ignore-not-found; \
	kubectl -n flux-system delete kustomization homelab-flux-sources --ignore-not-found

.PHONY: reinstall
reinstall:
	$(MAKE) teardown
	$(MAKE) install

.PHONY: flux-bootstrap
flux-bootstrap:
	@echo "[INFO] Requires Flux controllers already installed (see README: Manual Flux installation)."
	kubectl apply -k clusters/home/flux-system

.PHONY: runtime-inputs-sync
runtime-inputs-sync:
	flux reconcile kustomization homelab-platform -n flux-system --with-source --timeout=20m

.PHONY: charts-generate
charts-generate:
	./scripts/generate-charts.sh

.PHONY: otel-collectors-restart
otel-collectors-restart:
	@set -Eeuo pipefail; \
	echo "[INFO] Restarting otel-k8s collectors to reload logging/hyperdx-secret"; \
	if kubectl -n logging get deploy otel-k8s-cluster-opentelemetry-collector >/dev/null 2>&1; then \
	  kubectl -n logging rollout restart deploy/otel-k8s-cluster-opentelemetry-collector; \
	  kubectl -n logging rollout status deploy/otel-k8s-cluster-opentelemetry-collector --timeout=5m; \
	else \
	  echo "[WARN] logging/otel-k8s-cluster-opentelemetry-collector not found; skipping"; \
	fi; \
	if kubectl -n logging get ds otel-k8s-daemonset-opentelemetry-collector-agent >/dev/null 2>&1; then \
	  kubectl -n logging rollout restart ds/otel-k8s-daemonset-opentelemetry-collector-agent; \
	  kubectl -n logging rollout status ds/otel-k8s-daemonset-opentelemetry-collector-agent --timeout=5m; \
	else \
	  echo "[WARN] logging/otel-k8s-daemonset-opentelemetry-collector-agent not found; skipping"; \
	fi

.PHONY: runtime-inputs-refresh-otel
runtime-inputs-refresh-otel:
	$(MAKE) runtime-inputs-sync
	$(MAKE) wait-runtime-inputs-otel
	$(MAKE) otel-collectors-restart

# Use this after rotating the ingestion key in both SOPS files:
#   secret-clickstack-runtime-inputs.sops.yaml (CLICKSTACK_API_KEY)
#   secret-hyperdx.sops.yaml (HYPERDX_API_KEY — must equal CLICKSTACK_API_KEY)
.PHONY: runtime-inputs-refresh-clickstack-otel
runtime-inputs-refresh-clickstack-otel:
	$(MAKE) runtime-inputs-sync
	$(MAKE) wait-runtime-inputs-otel
	$(MAKE) otel-collectors-restart
	$(MAKE) verify-platform

.PHONY: wait-runtime-inputs-otel
wait-runtime-inputs-otel:
	@set -Eeuo pipefail; \
	echo "[INFO] Waiting for logging/hyperdx-secret.HYPERDX_API_KEY to be present"; \
	timeout_secs=$${TIMEOUT_SECS:-300}; \
	start_time=$$(date +%s); \
	while true; do \
	  dst="$$(kubectl -n logging get secret hyperdx-secret -o jsonpath='{.data.HYPERDX_API_KEY}' 2>/dev/null || true)"; \
	  if [[ -n "$$dst" ]]; then \
	    echo "[INFO] logging/hyperdx-secret is present"; \
	    break; \
	  fi; \
	  now=$$(date +%s); \
	  if (( now - start_time >= timeout_secs )); then \
	    echo "[ERROR] Timed out waiting for hyperdx-secret propagation ($${timeout_secs}s)" >&2; \
	    exit 1; \
	  fi; \
	  sleep 5; \
	done

.PHONY: flux-reconcile
flux-reconcile:
	flux reconcile source git swhurl-platform -n flux-system --timeout=20m
	flux reconcile kustomization homelab-flux-sources -n flux-system --with-source --timeout=20m
	flux reconcile kustomization homelab-flux-stack -n flux-system --with-source --timeout=20m

.PHONY: host-dns
host-dns:
	@if [[ "$(DRY_RUN)" == "true" ]]; then \
	  ./host/dynamic-dns.sh --dry-run; \
	else \
	  ./host/dynamic-dns.sh; \
	fi

.PHONY: host-dns-delete
host-dns-delete:
	@if [[ "$(DRY_RUN)" == "true" ]]; then \
	  ./host/dynamic-dns.sh --delete --dry-run; \
	else \
	  ./host/dynamic-dns.sh --delete; \
	fi

.PHONY: platform-certs-staging
platform-certs-staging:
	$(call update_cert_issuer,letsencrypt-staging)

.PHONY: platform-certs-prod
platform-certs-prod:
	$(call update_cert_issuer,letsencrypt-prod)

.PHONY: verify-config
verify-config:
	@[[ -n "$${BASE_DOMAIN:-}" ]] || { echo "BASE_DOMAIN not set in config.env"; exit 1; }
	@[[ -f platform-services/oauth2-proxy/base/secret-oauth2-proxy-shared.sops.yaml ]] || { echo "oauth2-proxy runtime SOPS secret missing"; exit 1; }
	@[[ -f platform-services/otel/base/secret-hyperdx.sops.yaml ]] || { echo "otel runtime SOPS secret missing"; exit 1; }
	@[[ -f platform-services/clickstack/base/secret-clickstack-runtime-inputs.sops.yaml ]] || { echo "clickstack runtime SOPS secret missing"; exit 1; }
	@grep -q '^\s*OAUTH_HOST:' clusters/home/flux-system/sources/configmap-platform-settings.yaml || { echo "OAUTH_HOST missing from platform-settings"; exit 1; }

.PHONY: verify-platform
verify-platform:
	./scripts/verify-platform.sh

.PHONY: verify
verify: verify-config verify-platform
