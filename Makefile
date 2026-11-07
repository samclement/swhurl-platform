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
COMMANDS := help reconcile notifications-heartbeat verify-logs flux-install \
	clickstack-bootstrap clickstack-dashboards \
	app-repo app-hooks \
	console-image console-dev \
	platform-certs-staging platform-certs-prod destroy-data \
	check-repo check-otel check-secrets v2-snapshot \
	live-test-lifecycle live-test-reloader live-test-app-template live-test-restore-mongodb \
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
.PHONY: backup-mongodb backup-sqlite
backup-mongodb backup-sqlite:
	@DRY_RUN=$(DRY_RUN) ./host/ops.sh backup $(@:backup-%=%)

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

# Bootstrap is explicit; all builds/checks stay offline after the pinned dependency is downloaded.
V2_GO_ENV = GOTOOLCHAIN=go1.26.0 GOPROXY=off GOSUMDB=off CGO_ENABLED=0
# Starts a recipe that runs v2's own CLI: builds "$$work/swhurl-v2" into a throwaway folder removed on exit.
V2_BUILD = set -Eeuo pipefail; work=$$(mktemp -d); trap 'rm -rf "$$work"' EXIT; (cd v2 && $(V2_GO_ENV) go build -o "$$work/swhurl-v2" ./cmd/swhurl-v2)

.PHONY: v2-deps
v2-deps:
	cd v2 && GOTOOLCHAIN=go1.26.0 GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org go mod download go.yaml.in/yaml/v3

# The workstream record against Git (docs/workstreams.md#what-make-check-docs-enforces): swhurl-v2 reads this checkout.
.PHONY: check-docs
check-docs:
	@$(V2_BUILD); "$$work/swhurl-v2" workstreams --repo-root .

.PHONY: check-apps
check-apps:
	@bash v2/check-apps.sh

.PHONY: check-templates
check-templates:
	@bash v2/check-templates.sh

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

# Preview one notification pass against the caller's cluster, never posting or saving state. The JSON
# kubeconfig is private and removed with the build directory. No notification topic or token is needed.
# Console links default to the profile's routing domain; CONSOLE_URL overrides it. Needs Go 1.26.0 and jq.
.PHONY: notifications-check
notifications-check:
	@$(V2_BUILD); \
	umask 077; kubectl config view --raw -o json > "$$work/kubeconfig.json"; \
	if [ -z "$${CONSOLE_URL:-}" ]; then \
		base=$$(jq -er '.routing.baseDomain | strings | select(length > 0)' v2/testdata/profile.json); \
		CONSOLE_URL="https://console.$$base"; \
	fi; \
	export CONSOLE_URL; \
	"$$work/swhurl-v2" notifications-check --dry-run --kubeconfig "$$work/kubeconfig.json"

# Every live platform check: swhurl-v2 verify --scope platform, which reads the cluster, this checkout, the registry,
# S3, GitHub and the host facts this recipe supplies, with the caller's kubeconfig in the throwaway folder, readable
# by the caller only, and removed on exit. AWS credentials come from the AWS CLI unless AWS_ACCESS_KEY_ID is set, and
# the console's content tag from images/content-tag.sh. Hook listings use GH_TOKEN or gh auth token. The heartbeat
# facts are systemctl's output for the two host units; a systemctl that fails leaves the file empty, which the check
# reports. Missing inputs are BAD, without stopping unrelated checks. Needs Go 1.26.0.
.PHONY: verify-platform
verify-platform:
	@$(V2_BUILD); \
	umask 077; kubectl config view --raw -o json > "$$work/kubeconfig.json"; \
	if [ -z "$${AWS_ACCESS_KEY_ID:-}" ]; then \
		if credentials=$$(aws configure export-credentials --format env); then eval "$$credentials"; \
		else echo "[WARN] no AWS credentials from aws configure export-credentials: the backup checks cannot list S3" >&2; fi; \
	fi; \
	if [ -z "$${GH_TOKEN:-}" ]; then GH_TOKEN=$$(gh auth token 2>/dev/null) || GH_TOKEN=; fi; \
	export GH_TOKEN; \
	systemctl show swhurl-notification-heartbeat.timer swhurl-notification-heartbeat.service --timestamp=unix \
		--property=Id --property=ActiveState --property=Result --property=ExecMainStatus \
		--property=ExecMainExitTimestamp > "$$work/heartbeat.txt" 2>/dev/null || : > "$$work/heartbeat.txt"; \
	"$$work/swhurl-v2" verify --scope platform --kubeconfig "$$work/kubeconfig.json" --root "$(CURDIR)" \
		--console-tag "$$(bash images/content-tag.sh console)" --heartbeat "$$work/heartbeat.txt" --source verify-platform

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

# Restore one instance's SQLite database with v2 (swhurl-v2 restore): DRY_RUN=true writes the plan and changes
# nothing, CONFIRM=<app>/<env> performs it. FROM=<key> restores an older archive (MAX_AGE=<duration> admits it);
# AGE_KEY_FILE names the age identity (else SOPS_AGE_KEY_FILE, else ./age.agekey). v2 reads S3 credentials from the
# environment only: unless AWS_ACCESS_KEY_ID is set, the AWS CLI's own are exported into this recipe's shell.
# Refused before anything is built or read.
.PHONY: restore-sqlite
restore-sqlite:
	@: "$${APP:?usage: make restore-sqlite APP=<app> ENV=<env> CONFIRM=<app>/<env>}" "$${ENV:?usage: make restore-sqlite APP=<app> ENV=<env> CONFIRM=<app>/<env>}"; \
	perform=(); \
	if [ "$(DRY_RUN)" != true ]; then \
		[ "$${CONFIRM:-}" = "$$APP/$$ENV" ] || { echo "[ERROR] restore-sqlite replaces the live database of $$APP/$$ENV and stops the app meanwhile. Re-run with CONFIRM=$$APP/$$ENV, or with DRY_RUN=true for the plan" >&2; exit 2; }; \
		perform=(--confirm "$$APP/$$ENV" --age-key-file "$${AGE_KEY_FILE:-$${SOPS_AGE_KEY_FILE:-./age.agekey}}"); \
	fi; \
	$(V2_BUILD); \
	umask 077; kubectl config view --raw -o json > "$$work/kubeconfig.json"; \
	if [ -z "$${AWS_ACCESS_KEY_ID:-}" ]; then credentials=$$(aws configure export-credentials --format env); eval "$$credentials"; fi; \
	"$$work/swhurl-v2" restore --instance "v2-records/$$APP/$$ENV/instance.json" --release "v2-records/$$APP/$$ENV/release.json" \
		--profile v2/testdata/profile.json --calls v2-records/calls.json --egress platform/egress/allocation.json \
		--revision "$$(git rev-parse HEAD)" --kubeconfig "$$work/kubeconfig.json" \
		$${FROM:+--from "$$FROM"} $${MAX_AGE:+--max-age "$$MAX_AGE"} "$${perform[@]}"

# The team registry, v2-records/teams.json, and the notification files v2 renders from it (swhurl-v2 team). Each
# recipe edits files only: commit and push them. team-channel reads the address without showing it on a terminal,
# else one line of standard input, and hands it to v2 on standard input, never as an argument. Needs Go 1.26.0.
V2_TEAM = "$$work/swhurl-v2" team --records v2-records --profile v2/testdata/profile.json --out . --team "$$NAME"
.PHONY: team-add team-edit team-remove team-channel
team-add:
	@: "$${NAME:?usage: make team-add NAME=<team> [DESCRIPTION=\"what it owns\"]}"; \
	$(V2_BUILD); $(V2_TEAM) --action add $${DESCRIPTION:+--description "$$DESCRIPTION"}
team-edit:
	@: "$${NAME:?usage: make team-edit NAME=<team> DESCRIPTION=\"...\" and/or RENAME=<new>}"; \
	[ -n "$${DESCRIPTION+set}$${RENAME:-}" ] || { echo "usage: make team-edit NAME=<team> DESCRIPTION=\"...\" and/or RENAME=<new>" >&2; exit 2; }; \
	$(V2_BUILD); \
	if [ -n "$${DESCRIPTION+set}" ]; then $(V2_TEAM) --action describe --description "$$DESCRIPTION"; fi; \
	if [ -n "$${RENAME:-}" ]; then $(V2_TEAM) --action rename --rename "$$RENAME"; fi
team-remove:
	@: "$${NAME:?usage: make team-remove NAME=<team>}"; $(V2_BUILD); $(V2_TEAM) --action remove
team-channel:
	@: "$${NAME:?usage: make team-channel NAME=<team> [REMOVE=true]}"; $(V2_BUILD); \
	if [ "$${REMOVE:-}" = true ]; then $(V2_TEAM) --action drop-channel; exit; fi; \
	if [ -t 0 ]; then read -rsp 'Channel address (not shown): ' address; echo >&2; else read -r address || true; fi; \
	printf '%s\n' "$$address" | $(V2_TEAM) --action channel

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
