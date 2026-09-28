SHELL := /usr/bin/env bash
include config.env
export FEAT_VERIFY TIMEOUT_SECS DYNAMIC_DNS_RECORDS
# Operator tooling package (tools/swhurl); see docs/contributing.md.
SWHURL := PYTHONPATH=$(CURDIR)/tools python3 -m swhurl
DRY_RUN ?= false
# make install runs verification unless FEAT_VERIFY is set to something other than true.
INSTALL_STEPS = $(if $(filter-out true,$(or $(FEAT_VERIFY),true)),,verify-config) flux-reconcile $(if $(filter-out true,$(or $(FEAT_VERIFY),true)),,verify-platform)
TEARDOWN_REFUSAL = is disabled: Flux pruning can delete namespaces, Helm releases and persistent data.\nUse Git updates and make flux-reconcile for deployment. See docs/operations.md\#lifecycle.
OTEL_COLLECTORS = deploy/otel-k8s-cluster-opentelemetry-collector ds/otel-k8s-daemonset-opentelemetry-collector-agent

.PHONY: help
help:
	@echo "Targets:"
	@echo "  install             Reconcile a bootstrapped stack and verify it"
	@echo "  teardown            Disabled: cascading deletion can destroy persistent data"
	@echo "  reinstall           Disabled: use Git updates and flux-reconcile"
	@echo "  platform-certs-staging | platform-certs-prod"
	@echo "  flux-bootstrap      Apply Flux bootstrap manifests (requires manual Flux install)"
	@echo "  runtime-inputs-sync Reconcile Git-managed platform runtime SOPS secrets"
	@echo "  otel-collectors-restart Restart otel-k8s collectors (reload hyperdx-secret)"
	@echo "  runtime-inputs-refresh-otel Fallback: reconcile runtime inputs, restart OTel collectors, verify (Reloader normally restarts them)"
	@echo "  secrets-check       Decrypt Git SOPS Secrets locally and flag placeholders/double encoding (never prints values)"
	@echo "  charts-generate     Render C4 architecture charts from D2 sources"
	@echo "  flux-reconcile      Reconcile Git source and Flux stack"
	@echo "  host-dns            Configure host dynamic DNS systemd updater"
	@echo "  host-dns-delete     Remove host dynamic DNS systemd updater"
	@echo "  verify-config       Run config input contract checks"
	@echo "  verify-platform     Run in-cluster platform state checks"
	@echo "  verify              Run verification scripts against current context"
	@echo "  validate-repo       Validate active manifests and shell scripts locally"
	@echo "  suspend TARGET=kustomization/<name>|helmrelease/<ns>/<name>  Stop applying Git changes; workloads keep running"
	@echo "  resume TARGET=...   Resume a suspended Kustomization or HelmRelease"
	@echo "  destroy-data TARGET=pvc/<ns>/<name>|pv/<name> CONFIRM=<TARGET>  Permanently delete a released claim/PV and its data"
	@echo "  app-new NAME=<app> ARGS='--env staging --image repo:tag ...'  Generate an app instance (make app-new NAME=x ARGS=--help)"
	@echo "  app-status|app-logs|app-reconcile|app-check APP=<app> ENV=<env>  Operate one app instance"
	@echo "  app-policy          Render every app instance and check it against the app contract"
	@echo "  app-template-test   Deploy the generated app fixtures through Flux, check them, remove them"
	@echo "  reloader-test       Prove Reloader restarts only opted-in workloads in watched namespaces"
	@echo "  lifecycle-test      Prove suspend/uninstall/destroy-data/Orphan on a disposable app"
	@echo "  backup-clickstack-mongodb      Dump ClickStack MongoDB to an age-encrypted local archive"
	@echo "  restore-test-clickstack-mongodb Restore the latest backup into a disposable namespace and check it"
	@echo "  shellcheck          Lint the bash that stays (host DNS, chart rendering, fixture script)"
	@echo "  test-safety         Test lifecycle guards, secret-safe verification and sign-in policy offline"
	@echo ""
	@echo "platform-certs-* targets edit Git-tracked files only. Commit + push before flux-reconcile."
	@echo ""
	@echo "Host dynamic DNS:"
	@echo "  make host-dns [DRY_RUN=true]"
	@echo "  make host-dns-delete [DRY_RUN=true]"

.PHONY: install
install:
	@$(if $(filter true,$(DRY_RUN)),printf 'Plan (install):\n'; printf '  - make %s\n' $(INSTALL_STEPS),$(foreach step,$(INSTALL_STEPS),$(MAKE) $(step) &&) true)

.PHONY: teardown reinstall
teardown reinstall:
	@$(if $(filter true,$(DRY_RUN)),echo "Plan ($@): disabled; no cluster commands will run.",printf '%b\n' "[ERROR] $@ $(TEARDOWN_REFUSAL)" >&2; exit 2)

.PHONY: flux-bootstrap
flux-bootstrap:
	@echo "[INFO] Requires Flux controllers already installed (see docs/bootstrap.md)."
	kubectl apply -k clusters/home/flux-system

.PHONY: runtime-inputs-sync
runtime-inputs-sync:
	flux reconcile source git swhurl-platform -n flux-system --timeout=5m
	flux reconcile kustomization homelab-auth -n flux-system --timeout=10m
	flux reconcile kustomization homelab-clickstack -n flux-system --timeout=20m
	flux reconcile kustomization homelab-otel -n flux-system --timeout=10m

.PHONY: charts-generate
charts-generate:
	./scripts/generate-charts.sh

.PHONY: otel-collectors-restart
otel-collectors-restart:
	@echo "[INFO] Restarting otel-k8s collectors to reload logging/hyperdx-secret"
	kubectl -n logging rollout restart $(OTEL_COLLECTORS)
	$(foreach w,$(OTEL_COLLECTORS),kubectl -n logging rollout status $(w) --timeout=5m &&) true

.PHONY: runtime-inputs-refresh-otel
runtime-inputs-refresh-otel:
	$(MAKE) runtime-inputs-sync
	$(MAKE) wait-runtime-inputs-otel
	$(MAKE) otel-collectors-restart
	$(MAKE) verify-platform

.PHONY: secrets-check
secrets-check:
	$(SWHURL) secrets-check

.PHONY: wait-runtime-inputs-otel
wait-runtime-inputs-otel:
	@$(SWHURL) wait-secret-key logging hyperdx-secret HYPERDX_API_KEY --timeout $${TIMEOUT_SECS:-300}

.PHONY: flux-reconcile
flux-reconcile:
	flux reconcile source git swhurl-platform -n flux-system --timeout=20m
	flux reconcile kustomization homelab-flux-sources -n flux-system --with-source --timeout=20m
	flux reconcile kustomization homelab-flux-stack -n flux-system --with-source --timeout=20m

.PHONY: host-dns
host-dns:
	@./host/dynamic-dns.sh $(if $(filter true,$(DRY_RUN)),--dry-run)

.PHONY: host-dns-delete
host-dns-delete:
	@./host/dynamic-dns.sh --delete $(if $(filter true,$(DRY_RUN)),--dry-run)

.PHONY: platform-certs-staging platform-certs-prod
platform-certs-staging platform-certs-prod:
	@DRY_RUN=$(DRY_RUN) $(SWHURL) platform-certs $(@:platform-certs-%=letsencrypt-%)

.PHONY: verify-config
verify-config:
	@$(SWHURL) verify-config

.PHONY: verify-platform
verify-platform:
	@$(SWHURL) verify-platform

.PHONY: verify
verify: verify-config verify-platform

.PHONY: suspend resume destroy-data
suspend resume destroy-data:
	@DRY_RUN=$(DRY_RUN) CONFIRM="$(CONFIRM)" $(SWHURL) lifecycle $@ "$(TARGET)"

.PHONY: app-new
app-new:
	@[[ -n "$(NAME)" ]] || { echo "Usage: make app-new NAME=<app> ARGS='--env staging --image repo:tag ...'" >&2; exit 2; }
	$(SWHURL) app-new $(NAME) $(ARGS)

.PHONY: app-status app-logs app-reconcile app-check
app-status app-logs app-reconcile app-check:
	@[[ -n "$(APP)" && -n "$(ENV)" ]] || { echo "Usage: make $@ APP=<app> ENV=<staging|prod>" >&2; exit 2; }
	@$(SWHURL) app $(@:app-%=%) $(APP) $(ENV)

.PHONY: app-policy
app-policy:
	$(SWHURL) app-policy

.PHONY: app-template-test
app-template-test:
	@DRY_RUN=$(DRY_RUN) $(SWHURL) app-template-test

.PHONY: reloader-test
reloader-test:
	@DRY_RUN=$(DRY_RUN) $(SWHURL) reloader-test

.PHONY: lifecycle-test
lifecycle-test:
	@DRY_RUN=$(DRY_RUN) $(SWHURL) lifecycle-test

.PHONY: backup-clickstack-mongodb
backup-clickstack-mongodb:
	@DRY_RUN=$(DRY_RUN) $(SWHURL) backup-mongodb

.PHONY: restore-test-clickstack-mongodb
restore-test-clickstack-mongodb:
	@DRY_RUN=$(DRY_RUN) $(SWHURL) restore-test-mongodb

.PHONY: shellcheck
shellcheck:
	uvx --from shellcheck-py==0.11.0.1 shellcheck -x $$(git ls-files '*.sh')

.PHONY: validate-repo
validate-repo:
	$(SWHURL) validate-repo

.PHONY: test-safety
test-safety:
	PYTHONPATH=$(CURDIR)/tools python3 -m unittest discover -s tests -v
