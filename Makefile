SHELL := /usr/bin/env bash
PLATFORM_SETTINGS_FILE := clusters/home/flux-system/sources/configmap-platform-settings.yaml
DRY_RUN ?= false

run_dry_flag = $(if $(filter true,$(DRY_RUN)),--dry-run,)

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
	@echo "  teardown            Clean teardown path (cluster defaults)"
	@echo "  reinstall           Teardown then install (cluster defaults)"
	@echo "  platform-certs-staging | platform-certs-prod"
	@echo "  cilium-bootstrap    Apply pre-Flux k3s HelmChart bootstrap for Cilium and wait ready"
	@echo "  flux-bootstrap      Apply Flux bootstrap manifests (requires manual Flux install)"
	@echo "  runtime-inputs-sync Sync flux-system/platform-runtime-inputs from local env/profile"
	@echo "  otel-collectors-restart Restart otel-k8s collectors (reload hyperdx-secret)"
	@echo "  runtime-inputs-refresh-otel Sync+reconcile runtime inputs, then restart otel-k8s collectors"
	@echo "  flux-reconcile      Reconcile Git source and Flux stack"
	@echo "  verify              Run verification scripts against current context"
	@echo ""
	@echo "platform-certs-* targets edit Git-tracked files only. Commit + push before flux-reconcile."
	@echo ""
	@echo "Host operations are intentionally direct:"
	@echo "  ./host/run-host.sh [--dry-run|--delete]"

.PHONY: install
install:
	./run.sh $(call run_dry_flag)

.PHONY: teardown
teardown:
	./run.sh $(call run_dry_flag) --delete

.PHONY: reinstall
reinstall:
	./run.sh --delete
	./run.sh

.PHONY: cilium-bootstrap
cilium-bootstrap:
	kubectl apply -f bootstrap/k3s-manifests/cilium-helmchart.yaml
	kubectl -n kube-system rollout status ds/cilium --timeout=10m
	kubectl -n kube-system rollout status deploy/cilium-operator --timeout=10m
	@if kubectl -n kube-system get deploy hubble-relay >/dev/null 2>&1; then \
	  kubectl -n kube-system rollout status deploy/hubble-relay --timeout=10m; \
	fi
	@if kubectl -n kube-system get deploy hubble-ui >/dev/null 2>&1; then \
	  kubectl -n kube-system rollout status deploy/hubble-ui --timeout=10m; \
	fi

.PHONY: flux-bootstrap
flux-bootstrap:
	@echo "[INFO] Requires Flux controllers already installed (see README: Manual Flux installation)."
	kubectl apply -k clusters/home/flux-system

.PHONY: runtime-inputs-sync
runtime-inputs-sync:
	./scripts/bootstrap/sync-runtime-inputs.sh

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
	$(MAKE) flux-reconcile
	$(MAKE) otel-collectors-restart

.PHONY: flux-reconcile
flux-reconcile:
	./scripts/bootstrap/sync-runtime-inputs.sh
	./scripts/32_reconcile_flux_stack.sh

.PHONY: platform-certs-staging
platform-certs-staging:
	$(call update_cert_issuer,letsencrypt-staging)

.PHONY: platform-certs-prod
platform-certs-prod:
	$(call update_cert_issuer,letsencrypt-prod)

.PHONY: verify
verify:
	./scripts/94_verify_config_inputs.sh
	./scripts/91_verify_platform_state.sh
