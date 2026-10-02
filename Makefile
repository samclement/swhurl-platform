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
flux-reconcile: ## Fetch Git, reconcile the source layer and the stack, wait for every unit at that revision
	flux reconcile source git swhurl-platform -n flux-system --timeout=20m
	flux reconcile kustomization cluster-sources -n flux-system --timeout=20m
	flux reconcile kustomization cluster-stack -n flux-system --timeout=20m
	$(SWHURL) flux-wait

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

.PHONY: verify-logs
verify-logs: ## Live: summarize fresh log parsing, severity and trace coverage (no bodies; MINUTES=15)
	@$(SWHURL) verify-logs --minutes $(or $(MINUTES),15)

.PHONY: flux-install
flux-install: ## Install or upgrade the Flux controllers at the pinned version with the settings in Git (DRY_RUN=true shows the live diff)
	@DRY_RUN=$(DRY_RUN) $(SWHURL) flux-install

.PHONY: flux-bootstrap
flux-bootstrap: ## Apply the root units and sources (Flux must already be installed)
	@echo "[INFO] Requires Flux controllers already installed (see docs/bootstrap.md)."
	kubectl apply -k clusters/home/flux-system

# Apps ---------------------------------------------------------------------------

.PHONY: app-new
app-new: ## NAME=<app> ARGS="--env ... --image ..." Generate an app instance (ARGS=--help for options)
	@[[ -n "$(NAME)" ]] || { echo "Usage: make app-new NAME=<app> ARGS='--env staging --image repo:tag ...'" >&2; exit 2; }
	$(SWHURL) app-new $(NAME) $(ARGS)

.PHONY: app-promote
app-promote: ## APP=<app> Copy the staging image (tag and digest) into prod (Git edit; FROM=, TO= override)
	@[[ -n "$(APP)" ]] || { echo "Usage: make app-promote APP=<app> [FROM=staging TO=prod]" >&2; exit 2; }
	$(SWHURL) app-promote $(APP) $(if $(FROM),--from $(FROM)) $(if $(TO),--to $(TO))

.PHONY: app-scale
app-scale: ## APP= ENV= ARGS="--replicas N --cpu Q --memory Q --memory-limit Q" Change replicas or resources (Git edit)
	@[[ -n "$(APP)" && -n "$(ENV)" && -n "$(ARGS)" ]] || { echo "Usage: make app-scale APP=<app> ENV=<env> ARGS='--replicas 2'" >&2; exit 2; }
	$(SWHURL) app-scale $(APP) $(ENV) $(ARGS)

.PHONY: app-expose
app-expose: ## APP= ENV= ARGS="--exposure private|authenticated-web|public [--host H]" Change who can reach an instance (Git edit)
	@[[ -n "$(APP)" && -n "$(ENV)" && -n "$(ARGS)" ]] || { echo "Usage: make app-expose APP=<app> ENV=<env> ARGS='--exposure public --host app.example.com'" >&2; exit 2; }
	$(SWHURL) app-expose $(APP) $(ENV) $(ARGS)

.PHONY: app-remove
app-remove: ## APP= ENV= Delete an instance's files and unregister its unit (Git edit; Flux uninstalls on push)
	@[[ -n "$(APP)" && -n "$(ENV)" ]] || { echo "Usage: make app-remove APP=<app> ENV=<env>" >&2; exit 2; }
	$(SWHURL) app-remove $(APP) $(ENV)

.PHONY: app-repo
app-repo: ## NAME=<app> [STACK=typescript] [ANSWERS="kind=worker database=sqlite"] [DESCRIPTION=] Create the app's GitHub repository from a template; wait for its first image
	@[[ -n "$(NAME)" ]] || { echo "Usage: make app-repo NAME=<app> [STACK=typescript]" >&2; exit 2; }
	@DRY_RUN=$(DRY_RUN) STACK="$(STACK)" ANSWERS="$(ANSWERS)" DESCRIPTION="$(DESCRIPTION)" $(SWHURL) app-repo $(NAME)

.PHONY: app-status app-logs app-reconcile app-check
app-status app-logs app-reconcile app-check: ## APP=<app> ENV=<env> Operate one app instance
	@[[ -n "$(APP)" && -n "$(ENV)" ]] || { echo "Usage: make $@ APP=<app> ENV=<staging|prod>" >&2; exit 2; }
	@$(SWHURL) app $(@:app-%=%) $(APP) $(ENV)

.PHONY: console-image
console-image: ## Pin platform/console to the image published for this commit (src-<hash> tag and digest; Git edit)
	$(SWHURL) console-image

.PHONY: console-dev
console-dev: ## Read-only web console on http://127.0.0.1:8080 as a fixed dev identity (needs uv; PORT=)
	PYTHONPATH=$(CURDIR)/tools uv run --frozen python -m swhurl console --dev --port $(or $(PORT),8080)

# Settings -----------------------------------------------------------------------

.PHONY: platform-certs-staging platform-certs-prod
platform-certs-staging platform-certs-prod: ## Set CERT_ISSUER in platform-settings (Git edit only; commit + push, then flux-reconcile)
	@DRY_RUN=$(DRY_RUN) $(SWHURL) platform-certs $(@:platform-certs-%=letsencrypt-%)

# Lifecycle, backup and recovery -------------------------------------------------

.PHONY: suspend resume destroy-data
suspend resume destroy-data: ## TARGET=... [CONFIRM=...] Suspend/resume a unit or release; destroy released data
	@DRY_RUN=$(DRY_RUN) CONFIRM="$(CONFIRM)" $(SWHURL) lifecycle $@ "$(TARGET)"

.PHONY: clickstack-bootstrap
clickstack-bootstrap: ## Live: register the ClickStack admin and set the team ingestion key from SOPS (idempotent)
	@DRY_RUN=$(DRY_RUN) TIMEOUT_SECS=$(TIMEOUT_SECS) $(SWHURL) clickstack-bootstrap

.PHONY: clickstack-dashboards
clickstack-dashboards: ## Live: a ClickStack dashboard per app in Git (create, update; delete removed apps' ones). DRY_RUN=true lists changes
	@DRY_RUN=$(DRY_RUN) $(SWHURL) clickstack-dashboards

.PHONY: backup-mongodb
backup-mongodb: ## Encrypted ClickStack MongoDB backup to BACKUP_DIR, then prune
	@DRY_RUN=$(DRY_RUN) $(SWHURL) backup-mongodb

.PHONY: backup-sqlite
backup-sqlite: ## Encrypted backup of every app's SQLite database to BACKUP_DIR/sqlite and S3, then prune
	@DRY_RUN=$(DRY_RUN) $(SWHURL) backup-sqlite

.PHONY: restore-sqlite
restore-sqlite: ## APP= ENV= CONFIRM=<app>/<env> Restore an app's SQLite database from its newest backup (stops the app meanwhile)
	@[[ -n "$(APP)" && -n "$(ENV)" ]] || { echo "Usage: make restore-sqlite APP=<app> ENV=<env> CONFIRM=<app>/<env>" >&2; exit 2; }
	@DRY_RUN=$(DRY_RUN) CONFIRM="$(CONFIRM)" $(SWHURL) restore-sqlite $(APP) $(ENV)

# Offline checks (CI runs `make check`) -------------------------------------------

.PHONY: check
check: ## All offline checks: the same as CI (run in parallel; each target's output is kept together)
	@$(MAKE) --no-print-directory -j6 --output-sync=target check-repo test check-apps check-templates check-otel check-lint

.PHONY: check-repo
check-repo: ## Render active Flux paths, schemas, SOPS structure, shell syntax, doc links
	$(SWHURL) check-repo

.PHONY: test
test: ## Unit tests for the tooling, console, manifests and command safety (needs uv)
	PYTHONPATH=$(CURDIR)/tools uv run --frozen python tests/run.py

.PHONY: check-apps
check-apps: ## Render every app instance and check the app contract
	$(SWHURL) check-apps

.PHONY: check-templates
check-templates: ## Render every stack template combination; each swhurl.yaml must pass app-new and the app policy (needs network)
	$(SWHURL) check-templates

.PHONY: check-otel
check-otel: ## Render the OTel collectors and validate their config with that exact otelcol-k8s release (warns on deprecated names)
	$(SWHURL) check-otel

.PHONY: check-lint
check-lint: ## Lint the Python tooling and the bash that stays (needs uv)
	uvx ruff@0.16.9 check tools tests
	uvx --from shellcheck-py==0.11.0.1 shellcheck -x $$(git ls-files --cached --others --exclude-standard '*.sh')

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

.PHONY: live-test-restore-sqlite
live-test-restore-sqlite: ## Back up a throwaway app's SQLite database, change it, restore it and check it
	@DRY_RUN=$(DRY_RUN) $(SWHURL) live-test-restore-sqlite

# Host ---------------------------------------------------------------------------

.PHONY: host-dns
host-dns: ## Install or update the Route53 dynamic DNS timer (records in host/dns.env)
	@./host/install-timer.sh dns $(if $(filter true,$(DRY_RUN)),--dry-run)

.PHONY: host-backup
host-backup: ## Install or update the daily backup-mongodb systemd timer (uploads to S3; needs sudo)
	@./host/install-timer.sh backup $(if $(filter true,$(DRY_RUN)),--dry-run)

.PHONY: host-backup-delete
host-backup-delete: ## Remove the backup timer (backups are kept)
	@./host/install-timer.sh backup --delete $(if $(filter true,$(DRY_RUN)),--dry-run)

.PHONY: host-dns-delete
host-dns-delete: ## Remove the dynamic DNS timer
	@./host/install-timer.sh dns --delete $(if $(filter true,$(DRY_RUN)),--dry-run)
