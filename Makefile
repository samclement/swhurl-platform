SHELL := /usr/bin/env bash
RECONCILE_ONLY := sync-runtime-inputs.sh,32_reconcile_flux_stack.sh
MODE_DIR := clusters/home/modes
FLUX_INFRA_FILE := clusters/home/infrastructure.yaml
FLUX_PLATFORM_FILE := clusters/home/platform.yaml
FLUX_TENANTS_FILE := clusters/home/tenants.yaml
CERT_ENV ?= staging
APP_ENV ?= staging
LE_ENV ?= staging
DRY_RUN ?= false

.PHONY: help
help:
	@echo "Targets:"
	@echo "  install             Clean install path (cluster defaults)"
	@echo "  teardown            Clean teardown path (cluster defaults)"
	@echo "  reinstall           Teardown then install (cluster defaults)"
	@echo "  platform-certs      Set platform cert mode (CERT_ENV=staging|prod, optional DRY_RUN=true)"
	@echo "  platform-certs-staging | platform-certs-prod"
	@echo "  app-test            Set app URL/issuer test mode (APP_ENV=staging|prod LE_ENV=staging|prod, optional DRY_RUN=true)"
	@echo "  app-test-staging-le-staging | app-test-staging-le-prod"
	@echo "  app-test-prod-le-staging    | app-test-prod-le-prod"
	@echo "  flux-bootstrap      Install Flux and apply clusters/home/flux-system bootstrap manifests"
	@echo "  runtime-inputs-sync Sync flux-system/platform-runtime-inputs from local env/profile"
	@echo "  flux-reconcile      Reconcile Git source and Flux stack"
	@echo "  verify              Run verification scripts against current context"
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

.PHONY: platform-certs
platform-certs:
	@set -eu; \
	case "$(CERT_ENV)" in \
	  staging|prod) ;; \
	  *) echo "CERT_ENV must be staging or prod (got: $(CERT_ENV))" >&2; exit 1 ;; \
	esac; \
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
	mode_sync_file "$(MODE_DIR)/infrastructure-$(CERT_ENV).yaml" "$(FLUX_INFRA_FILE)" "infrastructure mode"; \
	mode_sync_file "$(MODE_DIR)/platform-$(CERT_ENV).yaml" "$(FLUX_PLATFORM_FILE)" "platform mode"; \
	dry_run_flag=""; \
	if [[ "$(DRY_RUN)" == "true" ]]; then dry_run_flag="--dry-run"; fi; \
	./run.sh $$dry_run_flag --only $(RECONCILE_ONLY)

.PHONY: platform-certs-staging
platform-certs-staging:
	@$(MAKE) platform-certs CERT_ENV=staging DRY_RUN=$(DRY_RUN)

.PHONY: platform-certs-prod
platform-certs-prod:
	@$(MAKE) platform-certs CERT_ENV=prod DRY_RUN=$(DRY_RUN)

.PHONY: app-test
app-test:
	@set -eu; \
	case "$(APP_ENV)" in \
	  staging|prod) ;; \
	  *) echo "APP_ENV must be staging or prod (got: $(APP_ENV))" >&2; exit 1 ;; \
	esac; \
	case "$(LE_ENV)" in \
	  staging|prod) ;; \
	  *) echo "LE_ENV must be staging or prod (got: $(LE_ENV))" >&2; exit 1 ;; \
	esac; \
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
	mode_sync_file "$(MODE_DIR)/tenants-app-$(APP_ENV)-le-$(LE_ENV).yaml" "$(FLUX_TENANTS_FILE)" "tenants mode"; \
	dry_run_flag=""; \
	if [[ "$(DRY_RUN)" == "true" ]]; then dry_run_flag="--dry-run"; fi; \
	./run.sh $$dry_run_flag --only $(RECONCILE_ONLY)

.PHONY: app-test-staging-le-staging
app-test-staging-le-staging:
	@$(MAKE) app-test APP_ENV=staging LE_ENV=staging DRY_RUN=$(DRY_RUN)

.PHONY: app-test-staging-le-prod
app-test-staging-le-prod:
	@$(MAKE) app-test APP_ENV=staging LE_ENV=prod DRY_RUN=$(DRY_RUN)

.PHONY: app-test-prod-le-staging
app-test-prod-le-staging:
	@$(MAKE) app-test APP_ENV=prod LE_ENV=staging DRY_RUN=$(DRY_RUN)

.PHONY: app-test-prod-le-prod
app-test-prod-le-prod:
	@$(MAKE) app-test APP_ENV=prod LE_ENV=prod DRY_RUN=$(DRY_RUN)

.PHONY: verify
verify:
	./scripts/94_verify_config_inputs.sh
	./scripts/91_verify_platform_state.sh
