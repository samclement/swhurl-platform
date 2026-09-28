SHELL := /usr/bin/env bash
# Operator interface. Recipes stay aliases and plain sequencing; logic lives in
# tools/swhurl (see docs/contributing.md). `## text` after a target is its help line.
SWHURL := PYTHONPATH=$(CURDIR)/tools python3 -m swhurl
DRY_RUN ?= false
INSTALL_STEPS = $(if $(SKIP_VERIFY),flux-reconcile,check-config flux-reconcile verify-platform)

.PHONY: help
help: ## List targets (generated from the ## comments in this Makefile)
	@$(SWHURL) make-help

# Deploy and verify --------------------------------------------------------------

.PHONY: flux-reconcile
flux-reconcile: ## Fetch Git and reconcile the source layer and the stack
	flux reconcile source git swhurl-platform -n flux-system --timeout=20m
	flux reconcile kustomization cluster-sources -n flux-system --with-source --timeout=20m
	flux reconcile kustomization cluster-stack -n flux-system --with-source --timeout=20m

.PHONY: install
install: ## check-config, flux-reconcile, verify-platform (SKIP_VERIFY=1 skips the checks; DRY_RUN=true plans)
	@$(if $(filter true,$(DRY_RUN)),printf 'Plan (install):\n'; printf '  - make %s\n' $(INSTALL_STEPS),$(foreach step,$(INSTALL_STEPS),$(MAKE) $(step) &&) true)

.PHONY: reconcile
reconcile: ## UNIT=<name> Fetch Git and reconcile one Flux unit (for example after changing its Secret)
	@[[ -n "$(UNIT)" ]] || { echo "Usage: make reconcile UNIT=<name> (see flux get kustomizations)" >&2; exit 2; }
	flux reconcile kustomization $(UNIT) -n flux-system --with-source --timeout=20m

.PHONY: verify-platform
verify-platform: ## Live: Flux units Ready, HTTPS redirect, ingestion key, retention (never prints keys)
	@$(SWHURL) verify-platform

.PHONY: flux-bootstrap
flux-bootstrap: ## Apply the root units and sources (Flux must already be installed)
	@echo "[INFO] Requires Flux controllers already installed (see docs/bootstrap.md)."
	kubectl apply -k clusters/home/flux-system

# Apps ---------------------------------------------------------------------------

.PHONY: app-new
app-new: ## NAME=<app> ARGS="--env ... --image ..." Generate an app instance (ARGS=--help for options)
	@[[ -n "$(NAME)" ]] || { echo "Usage: make app-new NAME=<app> ARGS='--env staging --image repo:tag ...'" >&2; exit 2; }
	$(SWHURL) app-new $(NAME) $(ARGS)

.PHONY: app-status app-logs app-reconcile app-check
app-status app-logs app-reconcile app-check: ## APP=<app> ENV=<env> Operate one app instance
	@[[ -n "$(APP)" && -n "$(ENV)" ]] || { echo "Usage: make $@ APP=<app> ENV=<staging|prod>" >&2; exit 2; }
	@$(SWHURL) app $(@:app-%=%) $(APP) $(ENV)

# Settings -----------------------------------------------------------------------

.PHONY: platform-certs-staging platform-certs-prod
platform-certs-staging platform-certs-prod: ## Set CERT_ISSUER in platform-settings (Git edit only; commit + push, then flux-reconcile)
	@DRY_RUN=$(DRY_RUN) $(SWHURL) platform-certs $(@:platform-certs-%=letsencrypt-%)

# Lifecycle, backup and recovery -------------------------------------------------

.PHONY: suspend resume destroy-data
suspend resume destroy-data: ## TARGET=... [CONFIRM=...] Suspend/resume a unit or release; destroy released data
	@DRY_RUN=$(DRY_RUN) CONFIRM="$(CONFIRM)" $(SWHURL) lifecycle $@ "$(TARGET)"

.PHONY: backup-mongodb
backup-mongodb: ## Encrypted ClickStack MongoDB backup to BACKUP_DIR, then prune
	@DRY_RUN=$(DRY_RUN) $(SWHURL) backup-mongodb

# Offline checks (CI runs `make check`) -------------------------------------------

.PHONY: check
check: check-repo test check-apps check-lint ## All offline checks: the same as CI

.PHONY: check-repo
check-repo: ## Render active Flux paths, schemas, SOPS structure, shell syntax, doc links
	$(SWHURL) check-repo

.PHONY: test
test: ## Unit tests for the tooling, manifests and command safety
	PYTHONPATH=$(CURDIR)/tools python3 -m unittest discover -s tests -v

.PHONY: check-apps
check-apps: ## Render every app instance and check the app contract
	$(SWHURL) check-apps

.PHONY: check-lint
check-lint: ## Lint the Python tooling and the bash that stays (needs uv)
	uvx ruff@0.16.9 check tools tests
	uvx --from shellcheck-py==0.11.0.1 shellcheck -x $$(git ls-files '*.sh')

.PHONY: check-config
check-config: ## Decrypting units have Secrets, platform-settings has its keys
	@$(SWHURL) check-config

.PHONY: check-secrets
check-secrets: ## Decrypt tracked Secrets locally; flag placeholders and double encoding (never prints values)
	$(SWHURL) check-secrets

# Live tests (throwaway resources, cleaned up) -----------------------------------

.PHONY: live-test-lifecycle
live-test-lifecycle: ## Prove suspend/uninstall/destroy-data/Orphan on a throwaway app
	@DRY_RUN=$(DRY_RUN) $(SWHURL) live-test-lifecycle

.PHONY: live-test-reloader
live-test-reloader: ## Prove Reloader restarts only opted-in workloads in watched namespaces
	@DRY_RUN=$(DRY_RUN) $(SWHURL) live-test-reloader

.PHONY: live-test-app-template
live-test-app-template: ## Deploy the generated app fixtures through Flux, check, remove
	@DRY_RUN=$(DRY_RUN) $(SWHURL) live-test-app-template

.PHONY: live-test-restore-mongodb
live-test-restore-mongodb: ## Restore the latest MongoDB backup into a throwaway namespace and check it
	@DRY_RUN=$(DRY_RUN) $(SWHURL) live-test-restore-mongodb

# Host ---------------------------------------------------------------------------

.PHONY: host-dns
host-dns: ## Install or update the Route53 dynamic DNS timer (records in host/dns.env)
	@./host/dynamic-dns.sh $(if $(filter true,$(DRY_RUN)),--dry-run)

.PHONY: host-dns-delete
host-dns-delete: ## Remove the dynamic DNS timer
	@./host/dynamic-dns.sh --delete $(if $(filter true,$(DRY_RUN)),--dry-run)

