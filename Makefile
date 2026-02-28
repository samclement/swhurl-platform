SHELL := /usr/bin/env bash
MODE_DIR := clusters/home/modes
FLUX_TENANTS_FILE := clusters/home/tenants.yaml
PLATFORM_SETTINGS_FILE := clusters/home/flux-system/sources/configmap-platform-settings.yaml
DRY_RUN ?= false

.PHONY: help
help:
	@echo "Targets:"
	@echo "  install             Clean install path (cluster defaults)"
	@echo "  teardown            Clean teardown path (cluster defaults)"
	@echo "  reinstall           Teardown then install (cluster defaults)"
	@echo "  platform-certs-staging | platform-certs-prod"
	@echo "  app-test-staging-le-staging | app-test-staging-le-prod"
	@echo "  app-test-prod-le-staging    | app-test-prod-le-prod"
	@echo "  flux-bootstrap      Install Flux and apply clusters/home/flux-system bootstrap manifests"
	@echo "  runtime-inputs-sync Sync flux-system/platform-runtime-inputs from local env/profile"
	@echo "  flux-reconcile      Reconcile Git source and Flux stack"
	@echo "  verify              Run verification scripts against current context"
	@echo ""
	@echo "Mode targets edit Git-tracked files only. Commit + push before flux-reconcile."
	@echo ""
	@echo "Host operations are intentionally direct:"
	@echo "  ./host/run-host.sh [--dry-run|--delete]"

.PHONY: install
install:
	@set -eu; \
	dry_run_flag=""; \
	if [[ "$(DRY_RUN)" == "true" ]]; then dry_run_flag="--dry-run"; fi; \
	./run.sh $$dry_run_flag

.PHONY: teardown
teardown:
	@set -eu; \
	dry_run_flag=""; \
	if [[ "$(DRY_RUN)" == "true" ]]; then dry_run_flag="--dry-run"; fi; \
	./run.sh $$dry_run_flag --delete

.PHONY: reinstall
reinstall:
	./run.sh --delete
	./run.sh

.PHONY: flux-bootstrap
flux-bootstrap:
	./scripts/bootstrap/install-flux.sh

.PHONY: runtime-inputs-sync
runtime-inputs-sync:
	./scripts/bootstrap/sync-runtime-inputs.sh

.PHONY: flux-reconcile
flux-reconcile:
	./scripts/bootstrap/sync-runtime-inputs.sh
	./scripts/32_reconcile_flux_stack.sh

.PHONY: platform-certs-staging
platform-certs-staging:
	@set -eu; \
	file="$(PLATFORM_SETTINGS_FILE)"; issuer="letsencrypt-staging"; \
	[[ -f "$$file" ]] || { echo "Missing settings file: $$file" >&2; exit 1; }; \
	tmp="$$(mktemp)"; trap 'rm -f "$$tmp"' EXIT; \
	awk -v issuer="$$issuer" 'BEGIN{updated=0} /^  CERT_ISSUER:/ {print "  CERT_ISSUER: " issuer; updated=1; next} {print} END{ if (updated==0) exit 42 }' "$$file" > "$$tmp" || status="$$?"; \
	if [[ "$${status:-0}" == "42" ]]; then echo "Missing key '  CERT_ISSUER:' in $$file" >&2; exit 1; fi; \
	if cmp -s "$$file" "$$tmp"; then \
	  echo "[INFO] CERT_ISSUER already set to $$issuer"; \
	else \
	  if [[ "$(DRY_RUN)" == "true" ]]; then \
	    echo "[INFO] CERT_ISSUER would update to $$issuer in $$file"; \
	    diff -u "$$file" "$$tmp" || true; \
	  else \
	    mv "$$tmp" "$$file"; \
	    trap - EXIT; \
	    echo "[INFO] CERT_ISSUER updated to $$issuer in $$file"; \
	  fi; \
	fi; \
	echo "[INFO] Local Git edits only. Commit + push, then run: make flux-reconcile"

.PHONY: platform-certs-prod
platform-certs-prod:
	@set -eu; \
	file="$(PLATFORM_SETTINGS_FILE)"; issuer="letsencrypt-prod"; \
	[[ -f "$$file" ]] || { echo "Missing settings file: $$file" >&2; exit 1; }; \
	tmp="$$(mktemp)"; trap 'rm -f "$$tmp"' EXIT; \
	awk -v issuer="$$issuer" 'BEGIN{updated=0} /^  CERT_ISSUER:/ {print "  CERT_ISSUER: " issuer; updated=1; next} {print} END{ if (updated==0) exit 42 }' "$$file" > "$$tmp" || status="$$?"; \
	if [[ "$${status:-0}" == "42" ]]; then echo "Missing key '  CERT_ISSUER:' in $$file" >&2; exit 1; fi; \
	if cmp -s "$$file" "$$tmp"; then \
	  echo "[INFO] CERT_ISSUER already set to $$issuer"; \
	else \
	  if [[ "$(DRY_RUN)" == "true" ]]; then \
	    echo "[INFO] CERT_ISSUER would update to $$issuer in $$file"; \
	    diff -u "$$file" "$$tmp" || true; \
	  else \
	    mv "$$tmp" "$$file"; \
	    trap - EXIT; \
	    echo "[INFO] CERT_ISSUER updated to $$issuer in $$file"; \
	  fi; \
	fi; \
	echo "[INFO] Local Git edits only. Commit + push, then run: make flux-reconcile"

.PHONY: app-test-staging-le-staging
app-test-staging-le-staging:
	@set -eu; \
	mode_sync_file(){ \
	  src="$$1"; dst="$$2"; label="$$3"; \
	  [[ -f "$$src" ]] || { echo "Missing mode file: $$src" >&2; exit 1; }; \
	  if cmp -s "$$src" "$$dst"; then \
	    echo "[INFO] $$label already set ($$src)"; \
	    return 0; \
	  fi; \
	  if [[ "$(DRY_RUN)" == "true" ]]; then \
	    echo "[INFO] $$label would update: $$dst <= $$src"; \
	    diff -u "$$dst" "$$src" || true; \
	  else \
	    cp "$$src" "$$dst"; \
	    echo "[INFO] $$label updated: $$dst <= $$src"; \
	  fi; \
	}; \
	mode_sync_file "$(MODE_DIR)/tenants-app-staging-le-staging.yaml" "$(FLUX_TENANTS_FILE)" "tenants mode"; \
	echo "[INFO] Local Git edits only. Commit + push, then run: make flux-reconcile"

.PHONY: app-test-staging-le-prod
app-test-staging-le-prod:
	@set -eu; \
	mode_sync_file(){ \
	  src="$$1"; dst="$$2"; label="$$3"; \
	  [[ -f "$$src" ]] || { echo "Missing mode file: $$src" >&2; exit 1; }; \
	  if cmp -s "$$src" "$$dst"; then \
	    echo "[INFO] $$label already set ($$src)"; \
	    return 0; \
	  fi; \
	  if [[ "$(DRY_RUN)" == "true" ]]; then \
	    echo "[INFO] $$label would update: $$dst <= $$src"; \
	    diff -u "$$dst" "$$src" || true; \
	  else \
	    cp "$$src" "$$dst"; \
	    echo "[INFO] $$label updated: $$dst <= $$src"; \
	  fi; \
	}; \
	mode_sync_file "$(MODE_DIR)/tenants-app-staging-le-prod.yaml" "$(FLUX_TENANTS_FILE)" "tenants mode"; \
	echo "[INFO] Local Git edits only. Commit + push, then run: make flux-reconcile"

.PHONY: app-test-prod-le-staging
app-test-prod-le-staging:
	@set -eu; \
	mode_sync_file(){ \
	  src="$$1"; dst="$$2"; label="$$3"; \
	  [[ -f "$$src" ]] || { echo "Missing mode file: $$src" >&2; exit 1; }; \
	  if cmp -s "$$src" "$$dst"; then \
	    echo "[INFO] $$label already set ($$src)"; \
	    return 0; \
	  fi; \
	  if [[ "$(DRY_RUN)" == "true" ]]; then \
	    echo "[INFO] $$label would update: $$dst <= $$src"; \
	    diff -u "$$dst" "$$src" || true; \
	  else \
	    cp "$$src" "$$dst"; \
	    echo "[INFO] $$label updated: $$dst <= $$src"; \
	  fi; \
	}; \
	mode_sync_file "$(MODE_DIR)/tenants-app-prod-le-staging.yaml" "$(FLUX_TENANTS_FILE)" "tenants mode"; \
	echo "[INFO] Local Git edits only. Commit + push, then run: make flux-reconcile"

.PHONY: app-test-prod-le-prod
app-test-prod-le-prod:
	@set -eu; \
	mode_sync_file(){ \
	  src="$$1"; dst="$$2"; label="$$3"; \
	  [[ -f "$$src" ]] || { echo "Missing mode file: $$src" >&2; exit 1; }; \
	  if cmp -s "$$src" "$$dst"; then \
	    echo "[INFO] $$label already set ($$src)"; \
	    return 0; \
	  fi; \
	  if [[ "$(DRY_RUN)" == "true" ]]; then \
	    echo "[INFO] $$label would update: $$dst <= $$src"; \
	    diff -u "$$dst" "$$src" || true; \
	  else \
	    cp "$$src" "$$dst"; \
	    echo "[INFO] $$label updated: $$dst <= $$src"; \
	  fi; \
	}; \
	mode_sync_file "$(MODE_DIR)/tenants-app-prod-le-prod.yaml" "$(FLUX_TENANTS_FILE)" "tenants mode"; \
	echo "[INFO] Local Git edits only. Commit + push, then run: make flux-reconcile"

.PHONY: verify
verify:
	./scripts/94_verify_config_inputs.sh
	./scripts/91_verify_platform_state.sh
