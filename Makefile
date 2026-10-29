SHELL := /usr/bin/env bash
# Operator interface. Every target is stated once, in tools/swhurl/commands.py: its
# variables, help line and what it touches (make help prints them; a test fails when a
# target here and a command there differ). Recipes here stay plain sequencing and host
# scripts; logic lives in tools/swhurl (see docs/contributing.md).
SWHURL := PYTHONPATH=$(CURDIR)/tools python3 -m swhurl
DRY_RUN ?= false
INSTALL_STEPS = flux-reconcile $(if $(SKIP_VERIFY),,verify-platform)
.DEFAULT_GOAL := help

# Commands: `swhurl make <target>` reads the make variables (APP=, ENV=, ARGS=, ...) from
# the environment, refuses a missing one and runs the command's function.
COMMANDS := help reconcile verify-platform notifications-check notifications-heartbeat verify-logs flux-install \
	clickstack-bootstrap clickstack-dashboards \
	app-repo app-hooks app-new app-expose team-add team-edit team-remove team-channel channel-add channel-remove \
	app-team app-alerts platform-signing-keys app-calls app-allows app-egress app-events app-offers app-deploy app-secret app-sync app-promote \
	app-status app-logs app-reconcile app-check app-scale app-schedule app-remove console-image console-dev \
	platform-certs-staging platform-certs-prod suspend resume destroy-data backup-mongodb backup-sqlite restore-sqlite \
	check-repo check-apps check-templates check-otel check-secrets check-docs v2-snapshot \
	live-test-lifecycle live-test-reloader live-test-app-template live-test-restore-mongodb live-test-restore-sqlite \
	live-test-call-identity live-test-network-policy live-test-team-scenario

.PHONY: $(COMMANDS)
$(COMMANDS):
	@DRY_RUN=$(DRY_RUN) $(SWHURL) make $@

# Sequencing --------------------------------------------------------------------

.PHONY: flux-reconcile
flux-reconcile:
	flux reconcile source git swhurl-platform -n flux-system --timeout=20m
	flux reconcile kustomization cluster-sources -n flux-system --timeout=20m
	flux reconcile kustomization cluster-stack -n flux-system --timeout=20m
	$(SWHURL) flux-wait

.PHONY: install
install:
	@$(if $(filter true,$(DRY_RUN)),printf 'Plan (install):\n'; printf '  - make %s\n' $(INSTALL_STEPS),$(foreach step,$(INSTALL_STEPS),$(MAKE) $(step) &&) true)

.PHONY: flux-bootstrap
flux-bootstrap:
	@echo "[INFO] Requires Flux controllers already installed (see docs/bootstrap.md)."
	kubectl apply -k clusters/home/flux-system

.PHONY: check
check:
	@$(MAKE) --no-print-directory -j6 --output-sync=target check-repo test check-apps check-otel check-lint check-codegraph check-docs

.PHONY: test
test:
	PYTHONPATH=$(CURDIR)/tools uv run --frozen python tests/run.py $(if $(K),-k '$(K)')

.PHONY: check-codegraph
check-codegraph:
	PYTHONPATH=$(CURDIR)/devtools/codegraph python3 -m unittest discover -s $(CURDIR)/devtools/codegraph/tests -t $(CURDIR)/devtools/codegraph

.PHONY: check-v2
check-v2:
	cd v2 && export GOTOOLCHAIN=go1.26.0 GOPROXY=off GOSUMDB=off CGO_ENABLED=0 \
		&& unformatted=$$(gofmt -l .) && { [ -z "$$unformatted" ] || { echo "gofmt: run gofmt -w on: $$unformatted"; exit 1; }; } \
		&& go vet ./... && go test -count=1 ./... && ./check-rendered.sh

# Render NATS's user list, platform/nats/users.json, from the records: v2 owns the file (docs/services.md#nats).
# It reads a snapshot of the working tree, so an uncommitted record change counts, and writes that one file.
.PHONY: v2-users
v2-users:
	@set -Eeuo pipefail; 	work=$$(mktemp -d); trap 'rm -rf "$$work"' EXIT; 	revision=$$(git stash create); 	$(MAKE) -s v2-snapshot REVISION="$${revision:-HEAD}" OUT="$$work/snapshot.json" >/dev/null; 	(cd v2 && GOTOOLCHAIN=go1.26.0 GOPROXY=off GOSUMDB=off CGO_ENABLED=0 go build -o "$$work/swhurl-v2" ./cmd/swhurl-v2); 	"$$work/swhurl-v2" users --snapshot "$$work/snapshot.json" --profile v2/testdata/profile.json --out platform

# Render Reloader's watch list, platform/reloader/values.json, from the records: v2 owns the file (docs/services.md#reloader).
# It reads a snapshot of the working tree, so an uncommitted record change counts, and writes that one file.
.PHONY: v2-reloader
v2-reloader:
	@set -Eeuo pipefail; 	work=$$(mktemp -d); trap 'rm -rf "$$work"' EXIT; 	revision=$$(git stash create); 	$(MAKE) -s v2-snapshot REVISION="$${revision:-HEAD}" OUT="$$work/snapshot.json" >/dev/null; 	(cd v2 && GOTOOLCHAIN=go1.26.0 GOPROXY=off GOSUMDB=off CGO_ENABLED=0 go build -o "$$work/swhurl-v2" ./cmd/swhurl-v2); 	"$$work/swhurl-v2" reloader --snapshot "$$work/snapshot.json" --profile v2/testdata/profile.json --out platform

.PHONY: v2-report
v2-report:
	@BASE='$(BASE)' AFTER='$(AFTER)' FORMAT='$(FORMAT)' v2/archtest/report.sh

.PHONY: check-lint
check-lint:
	uvx ruff@0.16.9 check tools tests devtools
	uvx --from shellcheck-py==0.11.0.1 shellcheck -x $$(git ls-files --cached --others --exclude-standard '*.sh')

# Host timers (host/install-timer.sh) -------------------------------------------

TIMERS := host-dns host-heartbeat
.PHONY: $(TIMERS) $(TIMERS:=-delete)
$(TIMERS):
	@./host/install-timer.sh $(@:host-%=%) $(if $(filter true,$(DRY_RUN)),--dry-run)
$(TIMERS:=-delete):
	@./host/install-timer.sh $(patsubst host-%-delete,%,$@) --delete $(if $(filter true,$(DRY_RUN)),--dry-run)
