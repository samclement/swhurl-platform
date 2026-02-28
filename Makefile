SHELL := /usr/bin/env bash
RECONCILE_ONLY := sync-runtime-inputs.sh,32_reconcile_flux_stack.sh
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
	@echo "  platform-certs      Reconcile infrastructure+platform cert overlays (CERT_ENV=staging|prod, optional DRY_RUN=true)"
	@echo "  app-test            Reconcile app test mode by tenants path (APP_ENV=staging|prod LE_ENV=staging|prod, optional DRY_RUN=true)"
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
	mode_flags=""; \
	if [[ "$(DRY_RUN)" == "true" ]]; then mode_flags="--dry-run"; fi; \
	./scripts/bootstrap/set-flux-path-modes.sh $$mode_flags --platform-cert-env "$(CERT_ENV)"; \
	dry_run_flag=""; \
	if [[ "$(DRY_RUN)" == "true" ]]; then dry_run_flag="--dry-run"; fi; \
	./run.sh $$dry_run_flag --only $(RECONCILE_ONLY)

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
	mode_flags=""; \
	if [[ "$(DRY_RUN)" == "true" ]]; then mode_flags="--dry-run"; fi; \
	./scripts/bootstrap/set-flux-path-modes.sh $$mode_flags --app-env "$(APP_ENV)" --app-le-env "$(LE_ENV)"; \
	dry_run_flag=""; \
	if [[ "$(DRY_RUN)" == "true" ]]; then dry_run_flag="--dry-run"; fi; \
	./run.sh $$dry_run_flag --only $(RECONCILE_ONLY)

.PHONY: verify
verify:
	./scripts/94_verify_config_inputs.sh
	./scripts/91_verify_platform_state.sh
ifeq ($(FEAT_VERIFY_DEEP),true)
	./scripts/90_verify_runtime_smoke.sh
	./scripts/93_verify_expected_releases.sh
	./scripts/95_capture_cluster_diagnostics.sh
	./scripts/96_verify_orchestrator_contract.sh
endif
