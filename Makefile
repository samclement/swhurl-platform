SHELL := /usr/bin/env bash
PLATFORM_SETTINGS_FILE := clusters/home/flux-system/sources/configmap-platform-settings.yaml
DRY_RUN ?= false

define update_cert_issuer
	@set -eu; \
	file="$(PLATFORM_SETTINGS_FILE)"; issuer="$(1)"; \
	[[ -f "$$file" ]] || { echo "Missing settings file: $$file" >&2; exit 1; }; \
	tmp="$$(mktemp)"; trap 'rm -f "$$tmp"' EXIT; \
	awk -v issuer="$$issuer" 'BEGIN{updated=0} /^  CERT_ISSUER:/ {print "  CERT_ISSUER: " issuer; updated=1; next} {print} END{ if (updated==0) exit 42 }' "$$file" > "$$tmp" || status="$$?"; \
	if [[ "$${status:-0}" == "42" ]]; then echo "Missing key '  CERT_ISSUER:' in $$file" >&2; exit 1; fi; \
	if cmp -s "$$file" "$$tmp"; then \
	  echo "[INFO] CERT_ISSUER already set to $$issuer"; \
	elif [[ "$(DRY_RUN)" == "true" ]]; then \
	  echo "[INFO] CERT_ISSUER would update to $$issuer in $$file"; \
	  diff -u "$$file" "$$tmp" || true; \
	else \
	  mv "$$tmp" "$$file"; \
	  trap - EXIT; \
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
	@echo "  runtime-inputs-sync Reconcile Git-managed flux-system/platform-runtime-inputs (SOPS)"
	@echo "  otel-collectors-restart Restart otel-k8s collectors (reload hyperdx-secret)"
	@echo "  runtime-inputs-refresh-otel Reconcile runtime inputs, then restart otel-k8s collectors"
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
	@echo "  make host-dns [DRY_RUN=true] [HOST_ENV=/path/to/host.env]"
	@echo "  make host-dns-delete [DRY_RUN=true] [HOST_ENV=/path/to/host.env]"

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
	  echo "  - ./scripts/32_reconcile_flux_stack.sh --delete"; \
	  exit 0; \
	fi; \
	./scripts/32_reconcile_flux_stack.sh --delete

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
	flux reconcile kustomization homelab-flux-sources -n flux-system --with-source --timeout=20m

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
	flux reconcile kustomization homelab-platform -n flux-system --with-source --timeout=20m
	$(MAKE) wait-runtime-inputs-otel
	$(MAKE) otel-collectors-restart

.PHONY: wait-runtime-inputs-otel
wait-runtime-inputs-otel:
	@set -Eeuo pipefail; \
	echo "[INFO] Waiting for logging/hyperdx-secret to match flux-system/platform-runtime-inputs.CLICKSTACK_INGESTION_KEY"; \
	if ! kubectl -n logging get secret hyperdx-secret >/dev/null 2>&1; then \
	  echo "[WARN] logging/hyperdx-secret not found; skipping wait"; \
	  exit 0; \
	fi; \
	timeout_secs=$${TIMEOUT_SECS:-300}; \
	start_time=$$(date +%s); \
	while true; do \
	  src="$$(kubectl -n flux-system get secret platform-runtime-inputs -o jsonpath='{.data.CLICKSTACK_INGESTION_KEY}' 2>/dev/null || true)"; \
	  dst="$$(kubectl -n logging get secret hyperdx-secret -o jsonpath='{.data.HYPERDX_API_KEY}' 2>/dev/null || true)"; \
	  if [[ -n "$$src" && -n "$$dst" && "$$src" == "$$dst" ]]; then \
	    echo "[INFO] Runtime input propagation confirmed"; \
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
	./scripts/32_reconcile_flux_stack.sh

.PHONY: host-dns
host-dns:
	@set -Eeuo pipefail; \
	args=(); \
	if [[ -n "$(HOST_ENV)" ]]; then \
	  args+=(--host-env "$(HOST_ENV)"); \
	fi; \
	if [[ "$(DRY_RUN)" == "true" ]]; then \
	  args+=(--dry-run); \
	fi; \
	./host/dynamic-dns.sh "$${args[@]}"

.PHONY: host-dns-delete
host-dns-delete:
	@set -Eeuo pipefail; \
	args=(--delete); \
	if [[ -n "$(HOST_ENV)" ]]; then \
	  args+=(--host-env "$(HOST_ENV)"); \
	fi; \
	if [[ "$(DRY_RUN)" == "true" ]]; then \
	  args+=(--dry-run); \
	fi; \
	./host/dynamic-dns.sh "$${args[@]}"

.PHONY: platform-certs-staging
platform-certs-staging:
	$(call update_cert_issuer,letsencrypt-staging)

.PHONY: platform-certs-prod
platform-certs-prod:
	$(call update_cert_issuer,letsencrypt-prod)

.PHONY: verify-config
verify-config:
	./scripts/94_verify_config_inputs.sh

.PHONY: verify-platform
verify-platform:
	./scripts/91_verify_platform_state.sh

.PHONY: verify
verify: verify-config verify-platform
