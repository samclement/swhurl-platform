SHELL := /usr/bin/env bash

.PHONY: help
help:
	@echo "Targets:"
	@echo "  host-plan           Print host plan"
	@echo "  host-apply          Apply host layer"
	@echo "  host-delete         Delete host layer"
	@echo "  cluster-plan        Print cluster plan"
	@echo "  cluster-apply       Apply cluster layer"
	@echo "  cluster-apply-staging  Apply staging overlay profile"
	@echo "  cluster-apply-prod  Apply production overlay profile"
	@echo "  cluster-apply-traefik  Apply with Traefik provider profile"
	@echo "  cluster-apply-ceph     Apply with Ceph storage profile"
	@echo "  cluster-apply-traefik-ceph  Apply with Traefik+Ceph provider profile"
	@echo "  cluster-delete      Delete cluster layer"
	@echo "  test-loop           Run destructive scratch cycles (k3s uninstall/install + apply/delete)"
	@echo "  all-apply           Apply host + cluster"
	@echo "  all-delete          Delete cluster + host"
	@echo "  verify-legacy       Run legacy verification suite"
	@echo "  verify-provider-matrix  Validate Helmfile provider gating matrix"
	@echo "  flux-bootstrap      Install Flux and apply cluster/flux bootstrap manifests"
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
	./scripts/02_print_plan.sh

.PHONY: cluster-apply
cluster-apply:
	./run.sh

.PHONY: cluster-apply-staging
cluster-apply-staging:
	./run.sh --profile profiles/overlay-staging.env

.PHONY: cluster-apply-prod
cluster-apply-prod:
	./run.sh --profile profiles/overlay-prod.env

.PHONY: cluster-apply-traefik
cluster-apply-traefik:
	./run.sh --profile profiles/provider-traefik.env

.PHONY: cluster-apply-ceph
cluster-apply-ceph:
	./run.sh --profile profiles/provider-ceph.env

.PHONY: cluster-apply-traefik-ceph
cluster-apply-traefik-ceph:
	./run.sh --profile profiles/provider-traefik-ceph.env

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

.PHONY: verify-legacy
verify-legacy:
	./scripts/compat/verify-legacy-contracts.sh

.PHONY: verify-provider-matrix
verify-provider-matrix:
	./scripts/97_verify_provider_matrix.sh

.PHONY: flux-bootstrap
flux-bootstrap:
	./scripts/bootstrap/install-flux.sh

.PHONY: flux-reconcile
flux-reconcile:
	flux reconcile source git swhurl-platform -n flux-system --timeout=20m
	flux reconcile kustomization homelab-flux-stack -n flux-system --with-source --timeout=20m
