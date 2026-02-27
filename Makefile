SHELL := /usr/bin/env bash

.PHONY: help
help:
	@echo "Targets:"
	@echo "  host-plan           Print host plan"
	@echo "  host-apply          Apply host layer"
	@echo "  host-delete         Delete host layer"
	@echo "  cluster-plan        Print cluster plan"
	@echo "  cluster-apply       Apply cluster layer"
	@echo "  cluster-apply-staging  Apply with staging cert/runtime profile"
	@echo "  cluster-apply-prod  Apply with production cert/runtime profile"
	@echo "  cluster-delete      Delete cluster layer"
	@echo "  test-loop           Run destructive scratch cycles (k3s uninstall/install + apply/delete)"
	@echo "  all-apply           Apply host + cluster"
	@echo "  all-delete          Delete cluster + host"
	@echo "  verify              Run verification scripts against current context"
	@echo "  flux-bootstrap      Install Flux and apply cluster/flux bootstrap manifests"
	@echo "  runtime-inputs-sync Sync flux-system/platform-runtime-inputs from local env/profile"
	@echo "  flux-reconcile      Reconcile Git source and Flux stack"

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
	./run.sh --profile profiles/overlay-staging.env

.PHONY: cluster-apply-prod
cluster-apply-prod:
	./run.sh --profile profiles/overlay-prod.env

.PHONY: cluster-delete
cluster-delete:
	./run.sh --delete

.PHONY: test-loop
test-loop:
	./scripts/compat/repeat-scratch-cycles.sh --yes

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
