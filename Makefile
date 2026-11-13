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
COMMANDS := help \
	live-test-app-template

.PHONY: $(COMMANDS)
$(COMMANDS):
	@DRY_RUN=$(DRY_RUN) $(SWHURL) make $@

# The console is v2's: a Go server, built here, with no Python in its path.
.PHONY: console-dev
console-dev:
	@DRY_RUN=; $(V2_BUILD); "$$work/swhurl-v2" console --dev --port "$${PORT:-8080}"

# One or two kubectl or flux calls (host/ops.sh), which read their variables from the environment.
.PHONY: app-logs app-reconcile suspend resume
app-logs app-reconcile:
	@DRY_RUN=$(DRY_RUN) ./host/ops.sh $(@:app-%=%) "$$APP" "$$ENV"
suspend resume:
	@DRY_RUN=$(DRY_RUN) ./host/ops.sh $@ "$$TARGET"
# Pin the console to the image Validate published for this commit (images/pin-console.sh; edits files only).
.PHONY: console-image
console-image:
	@[ "$(DRY_RUN)" != true ] || { echo "[ERROR] make console-image has no dry run: DRY_RUN=true would be ignored, so nothing was done" >&2; exit 2; }; \
	pinned=$$(bash images/pin-console.sh) || exit 1; echo "$$pinned"; \
	case "$$pinned" in '[OK] pinned'*) echo '[INFO] Next: commit, push, make flux-reconcile' ;; esac
# Flux's controllers with the settings in Git (host/flux-install.sh), which reads DRY_RUN from the environment.
.PHONY: flux-install
flux-install:
	@DRY_RUN=$(DRY_RUN) ./host/flux-install.sh
.PHONY: backup-mongodb backup-sqlite
backup-mongodb backup-sqlite:
	@DRY_RUN=$(DRY_RUN) ./host/ops.sh backup $(@:backup-%=%)

# Sequencing --------------------------------------------------------------------

.PHONY: flux-reconcile
flux-reconcile:
	flux reconcile source git swhurl-platform -n flux-system --timeout=20m
	flux reconcile kustomization cluster-sources -n flux-system --timeout=20m
	flux reconcile kustomization cluster-stack -n flux-system --timeout=20m
	@$(V2_BUILD); \
	umask 077; kubectl config view --raw -o json > "$$work/kubeconfig.json"; \
	"$$work/swhurl-v2" flux-wait --kubeconfig "$$work/kubeconfig.json"

# Fetch Git and reconcile one unit (swhurl-v2 unit: the source, then the unit, each through the API server), refusing
# the two root units make flux-bootstrap applies. The JSON kubeconfig is private and removed with the build directory.
.PHONY: reconcile
reconcile:
	@[[ -n "$${UNIT:-}" ]] || { echo 'Usage: make reconcile UNIT=<name>' >&2; exit 2; }; \
	if [[ "$(DRY_RUN)" == true ]]; then \
		printf 'Plan (reconcile %s):\n  would run: swhurl-v2 unit --action reconcile --unit %s\n' "$$UNIT" "$$UNIT"; exit 0; \
	fi; \
	$(V2_BUILD); \
	umask 077; kubectl config view --raw -o json > "$$work/kubeconfig.json"; \
	"$$work/swhurl-v2" unit --action reconcile --unit "$$UNIT" --kubeconfig "$$work/kubeconfig.json"

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

# CERT_ISSUER in platform-settings, the one line changed and comments kept (swhurl-v2 platform-certs): only a
# letsencrypt-* ClusterIssuer in infra/issuers. It edits the file only: commit and push it. Needs Go 1.26.0.
.PHONY: platform-certs-staging platform-certs-prod
platform-certs-staging platform-certs-prod:
	@$(V2_BUILD); "$$work/swhurl-v2" platform-certs --root . --issuer "letsencrypt-$(@:platform-certs-%=%)" $(if $(filter true,$(DRY_RUN)),--dry-run)

# A claim, or a Released volume, and its host data (swhurl-v2 destroy-data): refused unless CONFIRM repeats TARGET
# (checked here first, so that refusal runs no tool), and while a pod mounts the claim or a Helm release or Flux unit
# would recreate it. DRY_RUN=true makes the checks only. Needs Go 1.26.0.
.PHONY: destroy-data
destroy-data:
	@[ -n "$${TARGET:-}" ] || { echo 'Usage: make destroy-data TARGET=pvc/<ns>/<name>|pv/<name> CONFIRM=<TARGET>' >&2; exit 2; }; \
	[ "$${CONFIRM:-}" = "$$TARGET" ] || { echo "[ERROR] destroy-data permanently deletes $$TARGET and its data. Re-run with CONFIRM=$$TARGET" >&2; exit 2; }; \
	$(V2_BUILD); umask 077; kubectl config view --raw -o json > "$$work/kubeconfig.json"; \
	"$$work/swhurl-v2" destroy-data --target "$$TARGET" --confirm "$${CONFIRM:-}" --kubeconfig "$$work/kubeconfig.json" $(if $(filter true,$(DRY_RUN)),--dry-run)

# Suspend and resume, uninstall, destroy-data and an Orphan unit proved on the throwaway fixture
# tests/fixtures/lifecycle-app, reconciled from the pushed revision (swhurl-v2 lifecycle-test); it touches only its two
# lifecycle-test units, namespace and volumes, and cleans them up last. DRY_RUN=true prints the plan. Needs Go 1.26.0.
.PHONY: live-test-lifecycle
live-test-lifecycle:
	@$(V2_BUILD); \
	if [ "$(DRY_RUN)" = true ]; then "$$work/swhurl-v2" lifecycle-test --dry-run; exit; fi; \
	umask 077; kubectl config view --raw -o json > "$$work/kubeconfig.json"; \
	"$$work/swhurl-v2" lifecycle-test --kubeconfig "$$work/kubeconfig.json"

# The newest trace through a call and an event on three running apps the records link in ENV (default staging),
# checked in ClickHouse (swhurl-v2 team-scenario): it only reads. DRY_RUN=true prints the plan. Needs Go 1.26.0.
.PHONY: live-test-team-scenario
live-test-team-scenario:
	@$(V2_BUILD); \
	if [ "$(DRY_RUN)" = true ]; then "$$work/swhurl-v2" team-scenario --dry-run; exit; fi; \
	umask 077; kubectl config view --raw -o json > "$$work/kubeconfig.json"; \
	"$$work/swhurl-v2" team-scenario --env "$${ENV:-staging}" --records v2-records --profile v2/testdata/profile.json \
		--calls v2-records/calls.json --egress platform/egress/allocation.json --kubeconfig "$$work/kubeconfig.json"

# Every tracked SOPS Secret decrypted in memory (swhurl-v2 check-secrets): placeholders, empty values, probable double
# encoding and the copies kept equal, compared by hashes; no value is printed. The age key is SOPS_AGE_KEY_FILE, else
# ./age.agekey, and only sops reads it, so CI cannot run this. Needs Go 1.26.0 and sops.
.PHONY: check-secrets
check-secrets:
	@[ "$(DRY_RUN)" != true ] || { echo "[ERROR] make check-secrets has no dry run: DRY_RUN=true would be ignored, so nothing was done" >&2; exit 2; }; \
	$(V2_BUILD); "$$work/swhurl-v2" check-secrets --root . --age-key-file "$${SOPS_AGE_KEY_FILE:-./age.agekey}"

# The workstream record against Git (docs/workstreams.md#what-make-check-docs-enforces): swhurl-v2 reads this checkout.
.PHONY: check-docs
check-docs:
	@$(V2_BUILD); "$$work/swhurl-v2" workstreams --repo-root .

# swhurl-v2 decides; the script renders each path, substitutes it and parses the shell (SCHEMA=1 adds flux-schema).
.PHONY: check-repo
check-repo:
	@bash v2/check-repo.sh

.PHONY: check-apps
check-apps:
	@bash v2/check-apps.sh

# Helm renders each collector; its own image validates the config and runs the log fixtures (podman; CI sets docker).
.PHONY: check-otel
check-otel:
	@bash platform/otel/check.sh

.PHONY: check-templates
check-templates:
	@bash v2/check-templates.sh

.PHONY: check-v2
check-v2:
	cd v2 && export $(V2_GO_ENV) \
		&& unformatted=$$(gofmt -l .) && { [ -z "$$unformatted" ] || { echo "gofmt: run gofmt -w on: $$unformatted"; exit 1; }; } \
		&& go vet ./... && go test -count=1 ./... && ./check-rendered.sh

# Render the whole tree from v2's records: the resolution, every compiled folder and unit, the egress allocation and
# the proxy's objects, platform/auth, NATS's user list and Reloader's watch list. It writes only when the tree
# disagrees with them -- the check ignores the commit the two documents record -- so the Render workflow, which runs
# this on every push, does not bump that line on every push. Needs Go 1.26.0.
.PHONY: v2-render
v2-render:
	@$(V2_BUILD); 	if "$$work/swhurl-v2" render --records v2-records --profile v2/testdata/profile.json --out . --check >/dev/null; then echo "the tree is what the records render; nothing written"; else "$$work/swhurl-v2" render --records v2-records --profile v2/testdata/profile.json --out . --revision "$$(git rev-parse HEAD)"; fi

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

# Alert on the platform topic if the notification checker's CronJob is stale (swhurl-v2 notifications-heartbeat; the
# host timer runs this every five minutes and appends the output to its log file, so the recipe echoes nothing);
# DRY_RUN=true decides and logs without posting or saving; ARGS passes --max-age D or --state PATH. The JSON kubeconfig is private and removed with the
# build directory. Needs Go 1.26.0.
.PHONY: notifications-heartbeat
notifications-heartbeat:
	@$(V2_BUILD); \
	umask 077; kubectl config view --raw -o json > "$$work/kubeconfig.json"; \
	"$$work/swhurl-v2" notifications-heartbeat $(if $(filter true,$(DRY_RUN)),--dry-run) $(ARGS) \
		--kubeconfig "$$work/kubeconfig.json"

# Register the ClickStack admin and set the team ingestion key from SOPS (swhurl-v2 clickstack-bootstrap); idempotent,
# DRY_RUN=true reads and decides without writing. The JSON kubeconfig is private and removed with the build directory.
# Needs Go 1.26.0.
.PHONY: clickstack-bootstrap
clickstack-bootstrap:
	@$(V2_BUILD); \
	umask 077; kubectl config view --raw -o json > "$$work/kubeconfig.json"; \
	"$$work/swhurl-v2" clickstack-bootstrap $(if $(filter true,$(DRY_RUN)),--dry-run) --kubeconfig "$$work/kubeconfig.json"

# Keep the platform webhooks and each app's dashboard and alerts in ClickStack for the apps in v2's records
# (swhurl-v2 clickstack-dashboards), deleting those of apps the records do not name; DRY_RUN=true plans and writes
# nothing. The JSON kubeconfig is private and removed with the build directory. Needs Go 1.26.0.
.PHONY: clickstack-dashboards
clickstack-dashboards:
	@$(V2_BUILD); \
	umask 077; kubectl config view --raw -o json > "$$work/kubeconfig.json"; \
	"$$work/swhurl-v2" clickstack-dashboards --records v2-records $(if $(filter true,$(DRY_RUN)),--dry-run) \
		--kubeconfig "$$work/kubeconfig.json"

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

# A new app's repository (swhurl-v2 app-repo): render the stack's template, create the public repository with its
# first commit and image webhook, wait for its first image and open the staging pull request. The tokens come from
# the environment, in the child's environment only: CONSOLE_TOKEN and CONSOLE_REPO for the pull request,
# APP_REPOS_TOKEN for the repository, IMAGE_WEBHOOK_TOKEN for the webhook (without it, a warning). TEAM is the team
# that owns the app (a name in v2-records/teams.json), and REQUESTED_BY defaults to your Git identity. Needs Go 1.26.0.
.PHONY: app-repo
app-repo:
	@: "$${NAME:?usage: make app-repo NAME=<app> TYPE=<type> TEAM=<team>}" "$${TYPE:?usage: make app-repo NAME=<app> TYPE=<type> TEAM=<team>}" "$${TEAM:?usage: make app-repo NAME=<app> TYPE=<type> TEAM=<team>}"; \
	$(V2_BUILD); \
	identity="$${REQUESTED_BY:-$$(git config user.name) <$$(git config user.email)>}"; \
	host="flux-webhook.$$(jq -er '.routing.baseDomain | strings | select(length > 0)' v2/testdata/profile.json)"; \
	"$$work/swhurl-v2" app-repo --app "$$NAME" --type "$$TYPE" --set "team=$$TEAM" --requested-by "$$identity" \
		--hook-host "$$host" $${STACK:+--stack "$$STACK"} $${DESCRIPTION:+--description "$$DESCRIPTION"} \
		$${OWNER:+--owner "$$OWNER"} $(if $(filter true,$(RESUME)),--resume) $(if $(filter true,$(DRY_RUN)),--dry-run)

# The image webhook on every app repository whose staging release publishes the app's own image, ghcr.io/samclement/<app>
# (swhurl-v2 app-hooks): a missing one is created, a stale, disabled or wrong-event one repointed, a correct one left
# alone, none deleted. GH_TOKEN (else gh auth token) lists and writes the webhooks; the Receiver's token is decrypted
# from SOPS with SOPS_AGE_KEY_FILE (else ./age.agekey). Both are required before anything is built, and reach v2 in its
# environment only. DRY_RUN=true lists the webhooks and writes nothing. Needs Go 1.26.0, sops and jq.
APP_HOOKS_TOKEN = platform/flux-webhook/image-secret.sops.yaml
.PHONY: app-hooks
app-hooks:
	@set -Eeuo pipefail; \
	if [ -z "$${GH_TOKEN:-}" ]; then GH_TOKEN=$$(gh auth token) || GH_TOKEN=; fi; \
	[ -n "$$GH_TOKEN" ] || { echo "[ERROR] app-hooks: no GitHub token: set GH_TOKEN or sign in with gh auth login" >&2; exit 1; }; \
	key="$${SOPS_AGE_KEY_FILE:-./age.agekey}"; \
	IMAGE_WEBHOOK_TOKEN=$$(SOPS_AGE_KEY_FILE="$$key" sops decrypt --extract '["stringData"]["token"]' $(APP_HOOKS_TOKEN)) || IMAGE_WEBHOOK_TOKEN=; \
	[ -n "$$IMAGE_WEBHOOK_TOKEN" ] || { echo "[ERROR] app-hooks: cannot read the image webhook token from $(APP_HOOKS_TOKEN) (age key at $$key?)" >&2; exit 1; }; \
	export -n GH_TOKEN IMAGE_WEBHOOK_TOKEN; \
	$(V2_BUILD); \
	host="flux-webhook.$$(jq -er '.routing.baseDomain | strings | select(length > 0)' v2/testdata/profile.json)"; \
	GH_TOKEN="$$GH_TOKEN" IMAGE_WEBHOOK_TOKEN="$$IMAGE_WEBHOOK_TOKEN" "$$work/swhurl-v2" app-hooks --records v2-records --owner samclement --hook-host "$$host" $(if $(filter true,$(DRY_RUN)),--dry-run)

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

TIMERS := host-dns host-heartbeat host-probe
.PHONY: $(TIMERS) $(TIMERS:=-delete)
$(TIMERS):
	@./host/install-timer.sh $(@:host-%=%) $(if $(filter true,$(DRY_RUN)),--dry-run)
$(TIMERS:=-delete):
	@./host/install-timer.sh $(patsubst host-%-delete,%,$@) --delete $(if $(filter true,$(DRY_RUN)),--dry-run)
