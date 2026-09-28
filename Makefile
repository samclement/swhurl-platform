SHELL := /usr/bin/env bash
include config.env
export FEAT_VERIFY TIMEOUT_SECS DYNAMIC_DNS_RECORDS
PLATFORM_SETTINGS_FILE := clusters/home/flux-system/sources/configmap-platform-settings.yaml
# Operator tooling package (tools/swhurl); see docs/contributing.md.
SWHURL := PYTHONPATH=$(CURDIR)/tools python3 -m swhurl
DRY_RUN ?= false

define update_cert_issuer
	@set -eu; \
	file="$(PLATFORM_SETTINGS_FILE)"; issuer="$(1)"; \
	[[ -f "$$file" ]] || { echo "Missing settings file: $$file" >&2; exit 1; }; \
	grep -q '^\s*CERT_ISSUER:' "$$file" || { echo "Missing key 'CERT_ISSUER' in $$file" >&2; exit 1; }; \
	if grep -q "CERT_ISSUER: $$issuer$$" "$$file"; then \
	  echo "[INFO] CERT_ISSUER already set to $$issuer"; \
	elif [[ "$(DRY_RUN)" == "true" ]]; then \
	  echo "[INFO] CERT_ISSUER would update to $$issuer in $$file"; \
	else \
	  sed -i "s/^\(\s*CERT_ISSUER:\).*/\1 $$issuer/" "$$file"; \
	  echo "[INFO] CERT_ISSUER updated to $$issuer in $$file"; \
	fi; \
	echo "[INFO] Local Git edits only. Commit + push, then run: make flux-reconcile"
endef

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
	@echo "  test-safety         Test lifecycle guards, secret-safe verification and sign-in policy offline"
	@echo ""
	@echo "platform-certs-* targets edit Git-tracked files only. Commit + push before flux-reconcile."
	@echo ""
	@echo "Host dynamic DNS:"
	@echo "  make host-dns [DRY_RUN=true]"
	@echo "  make host-dns-delete [DRY_RUN=true]"

.PHONY: install
install:
	@set -Eeuo pipefail; \
	if [[ "$(DRY_RUN)" == "true" ]]; then \
	  echo "Plan (install):"; \
	  if [[ "$${FEAT_VERIFY:-true}" == "true" ]]; then \
	    echo "  - make verify-config"; \
	  fi; \
	  echo "  - make flux-reconcile"; \
	  if [[ "$${FEAT_VERIFY:-true}" == "true" ]]; then \
	    echo "  - make verify-platform"; \
	  fi; \
	  exit 0; \
	fi; \
	if [[ "$${FEAT_VERIFY:-true}" == "true" ]]; then \
	  $(MAKE) verify-config; \
	fi; \
	$(MAKE) flux-reconcile; \
	if [[ "$${FEAT_VERIFY:-true}" == "true" ]]; then \
	  $(MAKE) verify-platform; \
	fi

.PHONY: teardown reinstall
teardown reinstall:
	@set -Eeuo pipefail; \
	if [[ "$(DRY_RUN)" == "true" ]]; then \
	  echo "Plan ($@): disabled; no cluster commands will run."; \
	  exit 0; \
	fi; \
	echo "[ERROR] $@ is disabled: Flux pruning can delete namespaces, Helm releases and persistent data." >&2; \
	echo "Use Git updates and make flux-reconcile for deployment. See docs/operations.md#lifecycle." >&2; \
	exit 2

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
	@set -Eeuo pipefail; \
	echo "[INFO] Restarting otel-k8s collectors to reload logging/hyperdx-secret"; \
	if kubectl -n logging get deploy otel-k8s-cluster-opentelemetry-collector >/dev/null 2>&1; then \
	  kubectl -n logging rollout restart deploy/otel-k8s-cluster-opentelemetry-collector; \
	  kubectl -n logging rollout status deploy/otel-k8s-cluster-opentelemetry-collector --timeout=5m; \
	else \
	  echo "[WARN] logging/otel-k8s-cluster-opentelemetry-collector not found; skipping"; \
	fi; \
	if kubectl -n logging get ds otel-k8s-daemonset-opentelemetry-collector-agent >/dev/null 2>&1; then \
	  kubectl -n logging rollout restart ds/otel-k8s-daemonset-opentelemetry-collector-agent; \
	  kubectl -n logging rollout status ds/otel-k8s-daemonset-opentelemetry-collector-agent --timeout=5m; \
	else \
	  echo "[WARN] logging/otel-k8s-daemonset-opentelemetry-collector-agent not found; skipping"; \
	fi

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
	@set -Eeuo pipefail; \
	echo "[INFO] Waiting for logging/hyperdx-secret.HYPERDX_API_KEY to be present"; \
	timeout_secs=$${TIMEOUT_SECS:-300}; \
	start_time=$$(date +%s); \
	while true; do \
	  dst="$$(kubectl -n logging get secret hyperdx-secret -o jsonpath='{.data.HYPERDX_API_KEY}' 2>/dev/null || true)"; \
	  if [[ -n "$$dst" ]]; then \
	    echo "[INFO] logging/hyperdx-secret is present"; \
	    break; \
	  fi; \
	  now=$$(date +%s); \
	  if (( now - start_time >= timeout_secs )); then \
	    echo "[ERROR] Timed out waiting for hyperdx-secret propagation ($${timeout_secs}s)" >&2; \
	    exit 1; \
	  fi; \
	  sleep 5; \
	done

.PHONY: flux-reconcile
flux-reconcile:
	flux reconcile source git swhurl-platform -n flux-system --timeout=20m
	flux reconcile kustomization homelab-flux-sources -n flux-system --with-source --timeout=20m
	flux reconcile kustomization homelab-flux-stack -n flux-system --with-source --timeout=20m

.PHONY: host-dns
host-dns:
	@if [[ "$(DRY_RUN)" == "true" ]]; then \
	  ./host/dynamic-dns.sh --dry-run; \
	else \
	  ./host/dynamic-dns.sh; \
	fi

.PHONY: host-dns-delete
host-dns-delete:
	@if [[ "$(DRY_RUN)" == "true" ]]; then \
	  ./host/dynamic-dns.sh --delete --dry-run; \
	else \
	  ./host/dynamic-dns.sh --delete; \
	fi

.PHONY: platform-certs-staging
platform-certs-staging:
	$(call update_cert_issuer,letsencrypt-staging)

.PHONY: platform-certs-prod
platform-certs-prod:
	$(call update_cert_issuer,letsencrypt-prod)

.PHONY: verify-config
verify-config:
	@[[ -f platform-services/oauth2-proxy/base/secret-oauth2-proxy-shared.sops.yaml ]] || { echo "oauth2-proxy runtime SOPS secret missing"; exit 1; }
	@[[ -f platform-services/otel/base/secret-hyperdx.sops.yaml ]] || { echo "otel runtime SOPS secret missing"; exit 1; }
	@[[ -f platform-services/clickstack/base/secret-clickstack-runtime-inputs.sops.yaml ]] || { echo "clickstack runtime SOPS secret missing"; exit 1; }
	@grep -q '^\s*OAUTH_HOST:' clusters/home/flux-system/sources/configmap-platform-settings.yaml || { echo "OAUTH_HOST missing from platform-settings"; exit 1; }

.PHONY: verify-platform
verify-platform:
	./scripts/verify-platform.sh

.PHONY: verify
verify: verify-config verify-platform

.PHONY: suspend resume destroy-data
suspend resume destroy-data:
	@DRY_RUN=$(DRY_RUN) CONFIRM="$(CONFIRM)" ./scripts/lifecycle.sh $@ "$(TARGET)"

.PHONY: app-new
app-new:
	@[[ -n "$(NAME)" ]] || { echo "Usage: make app-new NAME=<app> ARGS='--env staging --image repo:tag ...'" >&2; exit 2; }
	$(SWHURL) app-new $(NAME) $(ARGS)

.PHONY: app-status app-logs app-reconcile app-check
app-status app-logs app-reconcile app-check:
	@[[ -n "$(APP)" && -n "$(ENV)" ]] || { echo "Usage: make $@ APP=<app> ENV=<staging|prod>" >&2; exit 2; }
	@./scripts/app.sh $(@:app-%=%) $(APP) $(ENV)

.PHONY: app-policy
app-policy:
	$(SWHURL) app-policy

.PHONY: app-template-test
app-template-test:
	DRY_RUN=$(DRY_RUN) ./scripts/app-template-test.sh

.PHONY: reloader-test
reloader-test:
	DRY_RUN=$(DRY_RUN) ./scripts/reloader-test.sh

.PHONY: lifecycle-test
lifecycle-test:
	DRY_RUN=$(DRY_RUN) ./scripts/lifecycle-test.sh

.PHONY: backup-clickstack-mongodb
backup-clickstack-mongodb:
	DRY_RUN=$(DRY_RUN) ./scripts/backup-clickstack-mongodb.sh

.PHONY: restore-test-clickstack-mongodb
restore-test-clickstack-mongodb:
	DRY_RUN=$(DRY_RUN) ./scripts/restore-test-clickstack-mongodb.sh

.PHONY: validate-repo
validate-repo:
	$(SWHURL) validate-repo

.PHONY: test-safety
test-safety:
	PYTHONPATH=$(CURDIR)/tools python3 -m unittest discover -s tests -v
