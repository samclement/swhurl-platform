SHELL := /usr/bin/env bash
RECONCILE_ONLY := sync-runtime-inputs.sh,32_reconcile_flux_stack.sh
CERT_ENV ?= staging
APP_ENV ?= staging
LE_ENV ?= staging
DRY_RUN ?= false
LOOP_CERT_MODE ?= staging
CYCLES ?= 3

.PHONY: help
help:
	@echo "Targets:"
	@echo "  install             Clean install path (cluster defaults)"
	@echo "  teardown            Clean teardown path (cluster defaults)"
	@echo "  reinstall           Teardown then install (cluster defaults)"
	@echo "  install-all         Install cluster + host layer"
	@echo "  teardown-all        Teardown cluster + host layer"
	@echo "  host-plan           Print host plan"
	@echo "  host-apply          Apply host layer"
	@echo "  host-delete         Delete host layer"
	@echo "  cluster-plan        Print cluster plan"
	@echo "  cluster-apply       Apply cluster layer"
	@echo "  cluster-apply-staging  Apply with staging infrastructure/platform cert overlays"
	@echo "  cluster-apply-prod  Apply with production infrastructure/platform cert overlays"
	@echo "  cluster-delete      Delete cluster layer"
	@echo "  test-loop           Run destructive scratch cycles (LOOP_CERT_MODE=staging|prod CYCLES=N)"
	@echo "  all-apply           Apply host + cluster"
	@echo "  all-delete          Delete cluster + host"
	@echo "  verify              Run verification scripts against current context"
	@echo "  platform-certs      Reconcile infrastructure+platform cert overlays (CERT_ENV=staging|prod, optional DRY_RUN=true)"
	@echo "  platform-certs-staging  Shortcut for CERT_ENV=staging"
	@echo "  platform-certs-prod  Shortcut for CERT_ENV=prod"
	@echo "  app-test            Reconcile app test mode by tenants path (APP_ENV=staging|prod LE_ENV=staging|prod, optional DRY_RUN=true)"
	@echo "  flux-bootstrap      Install Flux and apply clusters/home/flux-system bootstrap manifests"
	@echo "  runtime-inputs-sync Sync flux-system/platform-runtime-inputs from local env/profile"
	@echo "  flux-reconcile      Reconcile Git source and Flux stack"

.PHONY: install
install: cluster-apply

.PHONY: teardown
teardown: cluster-delete

.PHONY: reinstall
reinstall:
	./run.sh --delete
	./run.sh

.PHONY: install-all
install-all: all-apply

.PHONY: teardown-all
teardown-all: all-delete

.PHONY: host-plan
host-plan:
	./host/run-host.sh --dry-run

.PHONY: host-apply
host-apply:
	./host/run-host.sh

.PHONY: host-delete
host-delete:
	./host/run-host.sh --delete

.PHONY: cluster-plan
cluster-plan:
	./run.sh --dry-run

.PHONY: cluster-apply
cluster-apply:
	./run.sh

.PHONY: cluster-apply-staging
cluster-apply-staging:
	@$(MAKE) cluster-apply-cert CERT_ENV=staging DRY_RUN=$(DRY_RUN)

.PHONY: cluster-apply-prod
cluster-apply-prod:
	@$(MAKE) cluster-apply-cert CERT_ENV=prod DRY_RUN=$(DRY_RUN)

.PHONY: cluster-apply-cert
cluster-apply-cert:
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
	./run.sh $$dry_run_flag

.PHONY: cluster-delete
cluster-delete:
	./run.sh --delete

.PHONY: test-loop
test-loop:
	./scripts/compat/repeat-scratch-cycles.sh --cycles "$(CYCLES)" --cert-mode "$(LOOP_CERT_MODE)" --yes

.PHONY: all-apply
all-apply:
	./run.sh --with-host

.PHONY: all-delete
all-delete:
	./run.sh --with-host --delete

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
	mode_flags=""; \
	if [[ "$(DRY_RUN)" == "true" ]]; then mode_flags="--dry-run"; fi; \
	./scripts/bootstrap/set-flux-path-modes.sh $$mode_flags --app-env "$(APP_ENV)" --app-le-env "$(LE_ENV)"; \
	dry_run_flag=""; \
	if [[ "$(DRY_RUN)" == "true" ]]; then dry_run_flag="--dry-run"; fi; \
	./run.sh $$dry_run_flag --only $(RECONCILE_ONLY)
