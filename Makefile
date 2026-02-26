SHELL := /usr/bin/env bash

.PHONY: help
help:
	@echo "Targets:"
	@echo "  host-plan           Print host plan"
	@echo "  host-apply          Apply host layer"
	@echo "  host-delete         Delete host layer"
	@echo "  cluster-plan        Print cluster plan"
	@echo "  cluster-apply       Apply cluster layer"
	@echo "  cluster-apply-traefik  Apply with Traefik provider profile"
	@echo "  cluster-apply-ceph     Apply with Ceph storage profile"
	@echo "  cluster-apply-traefik-ceph  Apply with Traefik+Ceph provider profile"
	@echo "  cluster-delete      Delete cluster layer"
	@echo "  all-apply           Apply host + cluster"
	@echo "  all-delete          Delete cluster + host"
	@echo "  verify-legacy       Run legacy verification suite"
	@echo "  flux-bootstrap      Install Flux and apply cluster/flux bootstrap manifests"

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

.PHONY: all-apply
all-apply:
	./run.sh --with-host

.PHONY: all-delete
all-delete:
	./run.sh --with-host --delete

.PHONY: verify-legacy
verify-legacy:
	./scripts/compat/verify-legacy-contracts.sh

.PHONY: flux-bootstrap
flux-bootstrap:
	./scripts/bootstrap/install-flux.sh
