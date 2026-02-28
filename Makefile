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
	@echo "  flux-bootstrap      Apply Flux bootstrap manifests (requires manual Flux install)"
	@echo "  runtime-inputs-sync Sync flux-system/platform-runtime-inputs from local env/profile"
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

.PHONY: flux-bootstrap
flux-bootstrap:
	@echo "[INFO] Requires Flux controllers already installed (see README: Manual Flux installation)."
	kubectl apply -k clusters/home/flux-system

.PHONY: runtime-inputs-sync
runtime-inputs-sync:
	./scripts/bootstrap/sync-runtime-inputs.sh

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
