SHELL := /usr/bin/env bash
RECONCILE_ONLY := sync-runtime-inputs.sh,32_reconcile_flux_stack.sh

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
	@echo "  cluster-apply-staging  Apply with staging cert/runtime profile"
	@echo "  cluster-apply-prod  Apply with production cert/runtime profile"
	@echo "  cluster-delete      Delete cluster layer"
	@echo "  test-loop           Run destructive scratch cycles (k3s uninstall/install + apply/delete)"
	@echo "  all-apply           Apply host + cluster"
	@echo "  all-delete          Delete cluster + host"
	@echo "  verify              Run verification scripts against current context"
	@echo "  platform-certs-staging  Set platform cert issuer intent to letsencrypt-staging and reconcile"
	@echo "  platform-certs-prod  Set platform cert issuer intent to letsencrypt-prod and reconcile"
	@echo "  app-test-staging-le-staging  Deploy app to staging URL with staging LE issuer"
	@echo "  app-test-staging-le-prod  Deploy app to staging URL with prod LE issuer"
	@echo "  app-test-prod-le-staging  Deploy app to prod URL with staging LE issuer"
	@echo "  app-test-prod-le-prod  Deploy app to prod URL with prod LE issuer"
	@echo "  flux-bootstrap      Install Flux and apply cluster/flux bootstrap manifests"
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

.PHONY: platform-certs-staging
platform-certs-staging:
	./run.sh --profile profiles/overlay-staging.env --only $(RECONCILE_ONLY)

.PHONY: platform-certs-prod
platform-certs-prod:
	./run.sh --profile profiles/overlay-prod.env --only $(RECONCILE_ONLY)

.PHONY: app-test-staging-le-staging
app-test-staging-le-staging:
	./run.sh --profile profiles/app-test-staging-le-staging.env --only $(RECONCILE_ONLY)

.PHONY: app-test-staging-le-prod
app-test-staging-le-prod:
	./run.sh --profile profiles/app-test-staging-le-prod.env --only $(RECONCILE_ONLY)

.PHONY: app-test-prod-le-staging
app-test-prod-le-staging:
	./run.sh --profile profiles/app-test-prod-le-staging.env --only $(RECONCILE_ONLY)

.PHONY: app-test-prod-le-prod
app-test-prod-le-prod:
	./run.sh --profile profiles/app-test-prod-le-prod.env --only $(RECONCILE_ONLY)
