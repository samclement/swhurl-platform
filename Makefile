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
	app-repo app-hooks team-add team-edit team-remove team-channel channel-add channel-remove \
	console-image console-dev \
	platform-certs-staging platform-certs-prod destroy-data backup-mongodb backup-sqlite restore-sqlite \
	check-repo check-apps check-templates check-otel check-secrets check-docs v2-snapshot \
	live-test-lifecycle live-test-reloader live-test-app-template live-test-restore-mongodb live-test-restore-sqlite \
	live-test-call-identity live-test-network-policy live-test-team-scenario

.PHONY: $(COMMANDS)
$(COMMANDS):
	@DRY_RUN=$(DRY_RUN) $(SWHURL) make $@

# One or two kubectl or flux calls (host/ops.sh), which read their variables from the environment.
.PHONY: app-logs app-reconcile suspend resume
app-logs app-reconcile:
	@DRY_RUN=$(DRY_RUN) ./host/ops.sh $(@:app-%=%) "$$APP" "$$ENV"
suspend resume:
	@DRY_RUN=$(DRY_RUN) ./host/ops.sh $@ "$$TARGET"

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

# v2's toolchain: the Go v2/go.mod states, and nothing fetched (the module is standard library only).
V2_GO_ENV = GOTOOLCHAIN=go1.26.0 GOPROXY=off GOSUMDB=off CGO_ENABLED=0
# Starts a recipe that runs v2's own CLI: builds "$$work/swhurl-v2" into a throwaway folder removed on exit.
V2_BUILD = set -Eeuo pipefail; work=$$(mktemp -d); trap 'rm -rf "$$work"' EXIT; (cd v2 && $(V2_GO_ENV) go build -o "$$work/swhurl-v2" ./cmd/swhurl-v2)

.PHONY: check-v2
check-v2:
	cd v2 && export $(V2_GO_ENV) \
		&& unformatted=$$(gofmt -l .) && { [ -z "$$unformatted" ] || { echo "gofmt: run gofmt -w on: $$unformatted"; exit 1; }; } \
		&& go vet ./... && go test -count=1 ./... && ./check-rendered.sh

# Render NATS's user list, platform/nats/users.json, from the records: v2 owns the file (docs/services.md#nats).
# It reads a snapshot of the working tree, so an uncommitted record change counts, and writes that one file.
.PHONY: v2-users
v2-users:
	@$(V2_BUILD); 	revision=$$(git stash create); 	$(MAKE) -s v2-snapshot REVISION="$${revision:-HEAD}" OUT="$$work/snapshot.json" >/dev/null; 	"$$work/swhurl-v2" users --snapshot "$$work/snapshot.json" --profile v2/testdata/profile.json --out platform

# Render the whole tree from v2's records: the resolution, every compiled folder and unit, the egress allocation and
# the proxy's objects, platform/auth, NATS's user list and Reloader's watch list. It writes only when the tree
# disagrees with them -- the check ignores the commit the two documents record -- so the Render workflow, which runs
# this on every push, does not bump that line on every push. Needs Go 1.26.0.
.PHONY: v2-render
v2-render:
	@$(V2_BUILD); 	if "$$work/swhurl-v2" render --records v2-records --profile v2/testdata/profile.json --out . --check >/dev/null; then echo "the tree is what the records render; nothing written"; else "$$work/swhurl-v2" render --records v2-records --profile v2/testdata/profile.json --out . --revision "$$(git rev-parse HEAD)"; fi

# Render Reloader's watch list, platform/reloader/values.json, from the records: v2 owns the file (docs/services.md#reloader).
# It reads a snapshot of the working tree, so an uncommitted record change counts, and writes that one file.
.PHONY: v2-reloader
v2-reloader:
	@$(V2_BUILD); 	revision=$$(git stash create); 	$(MAKE) -s v2-snapshot REVISION="$${revision:-HEAD}" OUT="$$work/snapshot.json" >/dev/null; 	"$$work/swhurl-v2" reloader --snapshot "$$work/snapshot.json" --profile v2/testdata/profile.json --out platform

# Bring every v2-owned staging instance to the newest image its repository publishes, with v2's own records and
# files: swhurl-v2 deploy --records reads v2-records/<app>/staging, delivers each instance on its own, and fails the
# run if any refused or failed. Needs Go 1.26.0 and read-only access to the public registry.
.PHONY: v2-deploy
v2-deploy:
	@$(V2_BUILD); 	"$$work/swhurl-v2" deploy --records v2-records --profile v2/testdata/profile.json --calls v2-records/calls.json --egress platform/egress/allocation.json --out .

# One instance's state from v2's records (swhurl-v2 status): what they say should run at HEAD, compared with the
# cluster the caller's kubeconfig names. Its JSON form is written into the throwaway folder, readable by the caller
# only, and removed on exit. Needs Go 1.26.0.
.PHONY: app-status
app-status:
	@: "$${APP:?usage: make app-status APP=<app> ENV=<env>}" "$${ENV:?usage: make app-status APP=<app> ENV=<env>}"; \
	$(V2_BUILD); \
	umask 077; kubectl config view --raw -o json > "$$work/kubeconfig.json"; \
	"$$work/swhurl-v2" status --records v2-records --app "$$APP" --env "$$ENV" --profile v2/testdata/profile.json \
		--calls v2-records/calls.json --egress platform/egress/allocation.json --revision "$$(git rev-parse HEAD)" \
		--kubeconfig "$$work/kubeconfig.json"

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
