SHELL := /usr/bin/env bash
# Operator interface. Recipes stay aliases and plain sequencing; logic lives in
# tools/swhurl (see docs/contributing.md). `## text` after a target is its help line.
SWHURL := PYTHONPATH=$(CURDIR)/tools python3 -m swhurl
DRY_RUN ?= false
INSTALL_STEPS = $(if $(SKIP_VERIFY),flux-reconcile,verify-config flux-reconcile verify-platform)
TEARDOWN_REFUSAL = is disabled: Flux pruning can delete namespaces, Helm releases and persistent data.\nUse Git updates and make flux-reconcile for deployment. See docs/operations.md\#lifecycle.
OTEL_COLLECTORS = deploy/otel-k8s-cluster-opentelemetry-collector ds/otel-k8s-daemonset-opentelemetry-collector-agent

.PHONY: help
help: ## List targets (generated from the ## comments in this Makefile)
	@$(SWHURL) make-help

# Deploy and verify --------------------------------------------------------------

.PHONY: flux-reconcile
flux-reconcile: ## Fetch Git and reconcile the source layer and the stack
	flux reconcile source git swhurl-platform -n flux-system --timeout=20m
	flux reconcile kustomization homelab-flux-sources -n flux-system --with-source --timeout=20m
	flux reconcile kustomization homelab-flux-stack -n flux-system --with-source --timeout=20m

.PHONY: install
install: ## verify-config, flux-reconcile, verify-platform (SKIP_VERIFY=1 skips the checks; DRY_RUN=true plans)
	@$(if $(filter true,$(DRY_RUN)),printf 'Plan (install):\n'; printf '  - make %s\n' $(INSTALL_STEPS),$(foreach step,$(INSTALL_STEPS),$(MAKE) $(step) &&) true)

.PHONY: verify-config
verify-config: ## Offline: decrypting units have Secrets, platform-settings has its keys
	@$(SWHURL) verify-config

.PHONY: verify-platform
verify-platform: ## Live: Flux units Ready, HTTPS redirect, ingestion key, retention (never prints keys)
	@$(SWHURL) verify-platform

.PHONY: verify
verify: verify-config verify-platform ## verify-config and verify-platform

.PHONY: flux-bootstrap
flux-bootstrap: ## Apply the root units and sources (Flux must already be installed)
	@echo "[INFO] Requires Flux controllers already installed (see docs/bootstrap.md)."
	kubectl apply -k clusters/home/flux-system

.PHONY: teardown reinstall
teardown reinstall: ## Refused: there is no whole-platform reset (see docs/operations.md#lifecycle)
	@$(if $(filter true,$(DRY_RUN)),echo "Plan ($@): disabled; no cluster commands will run.",printf '%b\n' "[ERROR] $@ $(TEARDOWN_REFUSAL)" >&2; exit 2)

# Apps ---------------------------------------------------------------------------

.PHONY: app-new
app-new: ## NAME=<app> ARGS="--env ... --image ..." Generate an app instance (ARGS=--help for options)
	@[[ -n "$(NAME)" ]] || { echo "Usage: make app-new NAME=<app> ARGS='--env staging --image repo:tag ...'" >&2; exit 2; }
	$(SWHURL) app-new $(NAME) $(ARGS)

.PHONY: app-status app-logs app-reconcile app-check
app-status app-logs app-reconcile app-check: ## APP=<app> ENV=<env> Operate one app instance
	@[[ -n "$(APP)" && -n "$(ENV)" ]] || { echo "Usage: make $@ APP=<app> ENV=<staging|prod>" >&2; exit 2; }
	@$(SWHURL) app $(@:app-%=%) $(APP) $(ENV)

.PHONY: app-policy
app-policy: ## Render every app instance and check the app contract
	$(SWHURL) app-policy

# Secrets and settings -----------------------------------------------------------

.PHONY: secrets-check
secrets-check: ## Decrypt tracked Secrets locally; flag placeholders and double encoding (never prints values)
	$(SWHURL) secrets-check

.PHONY: runtime-inputs-sync
runtime-inputs-sync: ## Fetch Git and reconcile the units that hold runtime Secrets
	flux reconcile source git swhurl-platform -n flux-system --timeout=5m
	flux reconcile kustomization homelab-auth -n flux-system --timeout=10m
	flux reconcile kustomization homelab-clickstack -n flux-system --timeout=20m
	flux reconcile kustomization homelab-otel -n flux-system --timeout=10m

.PHONY: wait-runtime-inputs-otel
wait-runtime-inputs-otel: ## Wait for logging/hyperdx-secret (TIMEOUT_SECS, default 300)
	@$(SWHURL) wait-secret-key logging hyperdx-secret HYPERDX_API_KEY

.PHONY: otel-collectors-restart
otel-collectors-restart: ## Restart both OTel collectors
	@echo "[INFO] Restarting otel-k8s collectors to reload logging/hyperdx-secret"
	kubectl -n logging rollout restart $(OTEL_COLLECTORS)
	$(foreach w,$(OTEL_COLLECTORS),kubectl -n logging rollout status $(w) --timeout=5m &&) true

.PHONY: runtime-inputs-refresh-otel
runtime-inputs-refresh-otel: runtime-inputs-sync wait-runtime-inputs-otel otel-collectors-restart verify-platform ## Fallback collector refresh (Reloader normally restarts them)

.PHONY: platform-certs-staging platform-certs-prod
platform-certs-staging platform-certs-prod: ## Set CERT_ISSUER in platform-settings (Git edit only; commit + push, then flux-reconcile)
	@DRY_RUN=$(DRY_RUN) $(SWHURL) platform-certs $(@:platform-certs-%=letsencrypt-%)

# Lifecycle, backup and recovery -------------------------------------------------

.PHONY: suspend resume destroy-data
suspend resume destroy-data: ## TARGET=... [CONFIRM=...] Suspend/resume a unit or release; destroy released data
	@DRY_RUN=$(DRY_RUN) CONFIRM="$(CONFIRM)" $(SWHURL) lifecycle $@ "$(TARGET)"

.PHONY: backup-clickstack-mongodb
backup-clickstack-mongodb: ## Encrypted ClickStack MongoDB backup to BACKUP_DIR, then prune
	@DRY_RUN=$(DRY_RUN) $(SWHURL) backup-mongodb

.PHONY: restore-test-clickstack-mongodb
restore-test-clickstack-mongodb: ## Restore the latest backup into a throwaway namespace and check it
	@DRY_RUN=$(DRY_RUN) $(SWHURL) restore-test-mongodb

# Tests --------------------------------------------------------------------------

.PHONY: test-safety
test-safety: ## Offline unit tests (CI runs this)
	PYTHONPATH=$(CURDIR)/tools python3 -m unittest discover -s tests -v

.PHONY: validate-repo
validate-repo: ## Offline: render active Flux paths, schemas, SOPS structure, shell syntax, doc links
	$(SWHURL) validate-repo

.PHONY: shellcheck
shellcheck: ## Lint the bash that stays (needs uv)
	uvx --from shellcheck-py==0.11.0.1 shellcheck -x $$(git ls-files '*.sh')

.PHONY: lifecycle-test
lifecycle-test: ## Live: prove suspend/uninstall/destroy-data/Orphan on a throwaway app
	@DRY_RUN=$(DRY_RUN) $(SWHURL) lifecycle-test

.PHONY: reloader-test
reloader-test: ## Live: prove Reloader restarts only opted-in workloads in watched namespaces
	@DRY_RUN=$(DRY_RUN) $(SWHURL) reloader-test

.PHONY: app-template-test
app-template-test: ## Live: deploy the generated app fixtures through Flux, check, remove
	@DRY_RUN=$(DRY_RUN) $(SWHURL) app-template-test

# Host and docs ------------------------------------------------------------------

.PHONY: host-dns
host-dns: ## Install or update the Route53 dynamic DNS timer (records in host/dns.env)
	@./host/dynamic-dns.sh $(if $(filter true,$(DRY_RUN)),--dry-run)

.PHONY: host-dns-delete
host-dns-delete: ## Remove the dynamic DNS timer
	@./host/dynamic-dns.sh --delete $(if $(filter true,$(DRY_RUN)),--dry-run)

.PHONY: charts-generate
charts-generate: ## Render docs/charts/c4/*.d2 to SVG (needs d2)
	./scripts/generate-charts.sh
