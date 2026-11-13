SHELL := /usr/bin/env bash
# Operator interface. Every target is stated once, in the lines below: "##@ <section>", then
# "## <target> | <touches> | <dry run: plan, reads or -> | <help>[ | <variables>]" in the order make help prints
# them (tools/make-help.awk). tests/test_commands.py fails when these lines, the targets and docs/commands.md differ,
# and runs every target whose dry run is "plan". Recipes stay sequencing and host scripts; decisions are swhurl-v2's.
#
##@ Deploy and verify
## flux-reconcile | Cluster | - | Fetch Git, reconcile the source layer and the stack, wait for every unit at that revision
## install | Cluster | plan | flux-reconcile, then verify-platform (SKIP_VERIFY=1 skips the checks)
## reconcile | Cluster | plan | Fetch Git and reconcile one Flux unit (for example after changing its Secret; swhurl-v2 unit; needs Go 1.26.0) | UNIT=<name>
## verify-platform | Cluster (read) | - | Every live platform check: swhurl-v2 verify, then two host checks (never prints keys; needs Go 1.26.0)
## notifications-check | Cluster (read) | - | Preview lifecycle and health notifications without sending or saving (swhurl-v2; needs Go 1.26.0 and jq)
## notifications-heartbeat | Cluster (read) | reads | Alert if the notification checker is stale (the host timer runs it; swhurl-v2; needs Go 1.26.0) | [ARGS="..."]
## flux-install | Cluster | reads | Install or upgrade the Flux controllers at the pinned version with the settings in Git
## flux-bootstrap | Cluster | - | Apply the root units and sources (Flux must already be installed)
## clickstack-bootstrap | Cluster | reads | Register the ClickStack admin and set the team ingestion key from SOPS (idempotent; swhurl-v2; needs Go 1.26.0)
## clickstack-dashboards | Cluster | reads | Keep platform webhooks and app dashboards and alerts in ClickStack; delete removed apps' ones (swhurl-v2; needs Go 1.26.0)
##@ Apps
## app-repo | GitHub | reads | Create the app's GitHub repository from a stack template, wait for its first image and open the pull request that creates it (swhurl-v2 app-repo; needs Go 1.26.0) | NAME=<app> TYPE=<type> TEAM=<team> [STACK=<stack>] [DESCRIPTION="..."] [RESUME=true]
## app-hooks | GitHub | reads | Create or repoint the image webhook on every auto-deploy app's repository (idempotent; swhurl-v2 app-hooks; needs Go 1.26.0)
## team-add | Git | - | Register a team in v2-records/teams.json (swhurl-v2 team; needs Go 1.26.0) | NAME=<team> [DESCRIPTION="what it owns"]
## team-edit | Git | - | Change a team's description and/or name; a rename is refused while an app names it | NAME=<team> [DESCRIPTION="..."] [RENAME=<new>]
## team-remove | Git | - | Remove a team nothing names and that has no channel | NAME=<team>
## team-channel | Git | - | Give a team its notification channel or a new address (prompts without showing it); REMOVE=true takes it away | NAME=<team> [REMOVE=true]
## app-status | Cluster (read) | - | Its state against v2's records at HEAD: Git applied, the image, replicas, route, certificate, failing containers (needs Go 1.26.0) | APP=<app> ENV=<env>
## app-logs | Cluster (read) | - | Recent workload logs (FOLLOW=true, TAIL=N, PREVIOUS=true) | APP=<app> ENV=<env>
## app-reconcile | Cluster | plan | Fetch Git and reconcile only that instance | APP=<app> ENV=<env>
## console-image | Git | - | Pin platform/console to the image published for this commit (images/pin-console.sh)
## console-dev | Cluster | - | Web console on http://127.0.0.1:8080 as a fixed dev identity (swhurl-v2; needs Go 1.26.0) | [PORT=8080]
##@ Settings
## platform-certs-staging | Git | plan | Set CERT_ISSUER in platform-settings to letsencrypt-staging (swhurl-v2 platform-certs; needs Go 1.26.0)
## platform-certs-prod | Git | plan | Set CERT_ISSUER in platform-settings to letsencrypt-prod (swhurl-v2 platform-certs; needs Go 1.26.0)
##@ Lifecycle, backup and recovery
## suspend | Cluster | plan | Suspend a Flux unit or HelmRelease; workloads and data untouched (swhurl-v2 unit; needs Go 1.26.0) | TARGET=kustomization/<name>|helmrelease/<ns>/<name>
## resume | Cluster | plan | Resume a Flux unit or HelmRelease and wait until it is applied (swhurl-v2 unit; needs Go 1.26.0) | TARGET=kustomization/<name>|helmrelease/<ns>/<name>
## destroy-data | Cluster | reads | Delete a released claim or volume and its host data (swhurl-v2 destroy-data; needs Go 1.26.0) | TARGET=pvc/<ns>/<name>|pv/<name> CONFIRM=<TARGET>
## backup-mongodb | Cluster | plan | Run ClickStack MongoDB's backup job now and show its result
## backup-sqlite | Cluster | reads | Run every app SQLite database's backup job now and show the results
## restore-sqlite | Cluster | reads | Restore an app's SQLite database with swhurl-v2 restore from its newest backup (stops the app meanwhile; needs Go 1.26.0) | APP=<app> ENV=<env> CONFIRM=<app>/<env>
##@ Offline checks
## check | Local | - | The offline checks in parallel; CI also renders the templates (check-templates)
## check-repo | Local | - | Doc links and tables, SOPS structure, settings and render paths (swhurl-v2); render and substitute each path, shell syntax; SCHEMA=1 adds flux-schema (needs Go) | [SCHEMA=1]
## test | Local | - | Unit tests for the tooling, console, manifests and command safety; K= runs only matching classes (needs uv) | [K=<pattern>]
## check-apps | Local | - | v2's records match Git and every compiled instance passes Go Kubernetes policy (needs Go and Helm; no arguments)
## check-templates | Local | - | Render each app type from its stack template (v2/contract/stacks.json); its names must be listed and the label its build writes must pass swhurl-v2 declaration (needs network, jq and uvx; builds swhurl-v2 unless SWHURL_V2 names one)
## check-otel | Local | - | Render the OTel collectors; validate each config and run the log fixtures in the image its release renders (needs helm, kubectl, jq, openssl, curl and podman, or CONTAINER_RUNTIME=docker) | [CONTAINER_RUNTIME=docker]
## check-lint | Local | - | Lint the Python tooling and the bash that stays (needs uv)
## check-codegraph | Local | - | Unit tests for the code graph development aid (devtools/codegraph)
## check-docs | Local | - | Compare the workstream registry and its status pages with Git (swhurl-v2 workstreams): state, next gate, uncommitted work, cited commits, page-per-change (needs Go 1.26.0)
## v2-deps | Local | - | Download the pinned v2 YAML parser with checksum verification before offline builds and checks
## check-v2 | Local | - | Format, vet and test the Go v2 module, its design guardrails included (needs Go 1.26.0)
## v2-render | Git | - | Render what the records decide when the tree disagrees with them: every compiled folder and unit, the resolution, the egress allocation and the proxy's objects, auth, NATS's users and Reloader's list (needs Go 1.26.0)
## v2-deploy | Git | - | Bring every v2-owned staging instance to the newest image its repository publishes (v2-records and v2-apps; needs Go 1.26.0)
## v2-report | Local | - | What a change does to v2 against the merge base with BASE: API, budgets, packages, imports, files, exceptions | [BASE=origin/main] [AFTER=<rev>] [FORMAT=json]
## check-secrets | Local | - | Decrypt tracked Secrets locally; flag placeholders and double encoding (never prints values; swhurl-v2 check-secrets; needs Go 1.26.0 and sops)
## help | Local | - | List the targets, from the ## lines at the top of the Makefile
##@ Live tests
## live-test-lifecycle | Cluster (throwaway) | plan | Prove suspend, uninstall, destroy-data and Orphan on a throwaway app (swhurl-v2 lifecycle-test; needs Go 1.26.0)
## live-test-app-template | Cluster (throwaway) | plan | Deploy two instances v2 renders from fixture records through Flux, check, remove (swhurl-v2 app-template-test; needs Go 1.26.0)
## live-test-team-scenario | Cluster (read) | plan | The newest trace through a call and an event on three running apps the records link, checked in ClickStack (swhurl-v2 team-scenario; needs Go 1.26.0) | [ENV=staging]
##@ Host
## host-dns | Host | plan | Install or update the Route 53 dynamic DNS timer (records in host/dns.env; needs sudo)
## host-dns-delete | Host | plan | Remove the dynamic DNS timer
## host-heartbeat | Host | plan | Install or update the five-minute notification heartbeat timer (needs sudo)
## host-heartbeat-delete | Host | plan | Remove the notification heartbeat timer
## host-probe | Host | plan | Install or update the every-minute connectivity probe timer (needs sudo)
## host-probe-delete | Host | plan | Remove the connectivity probe timer

DRY_RUN ?= false
INSTALL_STEPS = flux-reconcile $(if $(SKIP_VERIFY),,verify-platform)
.DEFAULT_GOAL := help

.PHONY: help
help:
	@awk -f tools/make-help.awk Makefile

# The console is v2's: a Go server, built here, with no Python in its path.
.PHONY: console-dev
console-dev:
	@DRY_RUN=; $(V2_BUILD); "$$work/swhurl-v2" console --dev --port "$${PORT:-8080}"

# One or two kubectl or flux calls (host/ops.sh), which read their variables from the environment.
.PHONY: app-logs app-reconcile
app-logs app-reconcile:
	@DRY_RUN=$(DRY_RUN) ./host/ops.sh $(@:app-%=%) "$$APP" "$$ENV"

# Stop or restart Flux applying Git to one unit or HelmRelease (swhurl-v2 unit --target, which checks TARGET's shape
# and names before any request); workloads and data are untouched. DRY_RUN=true prints the plan and reads nothing.
# Needs Go 1.26.0.
.PHONY: suspend resume
suspend resume:
	@[ -n "$${TARGET:-}" ] || { echo 'Usage: make $@ TARGET=kustomization/<name>|helmrelease/<ns>/<name>' >&2; exit 2; }; \
	if [ "$(DRY_RUN)" = true ]; then \
		printf 'Plan (%s %s):\n  would run: swhurl-v2 unit --action %s --target %s\n' $@ "$$TARGET" $@ "$$TARGET"; exit 0; \
	fi; \
	$(V2_BUILD); \
	umask 077; kubectl config view --raw -o json > "$$work/kubeconfig.json"; \
	"$$work/swhurl-v2" unit --action $@ --target "$$TARGET" --kubeconfig "$$work/kubeconfig.json"; \
	if [ $@ = suspend ]; then \
		echo "[INFO] Git changes stop applying to $$TARGET; running workloads and data are untouched."; \
		case "$$TARGET" in kustomization/*) echo '[INFO] HelmReleases it created keep reconciling their last applied spec; suspend them separately to freeze Helm.';; esac; \
	fi
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

# Two throwaway instances v2 renders from the fixture records (tests/fixtures/app-template, reconciled from the pushed
# revision) deployed through their rendered units, checked and removed (swhurl-v2 app-template-test); it touches only
# their two units, namespaces and the web app's volume, and cleans them up last. DRY_RUN=true prints the plan. Needs
# Go 1.26.0.
.PHONY: live-test-app-template
live-test-app-template:
	@$(V2_BUILD); \
	if [ "$(DRY_RUN)" = true ]; then "$$work/swhurl-v2" app-template-test --dry-run; exit; fi; \
	umask 077; kubectl config view --raw -o json > "$$work/kubeconfig.json"; \
	"$$work/swhurl-v2" app-template-test --kubeconfig "$$work/kubeconfig.json"

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
# the proxy's objects, platform/auth, NATS's user list and Reloader's watch list; then the same for the app-template
# live test's fixture (tests/fixtures/app-template, from its own records). It writes only when a tree disagrees with
# its records -- the check ignores the commit the two documents record -- so the Render workflow, which runs this on
# every push, does not bump that line on every push. Needs Go 1.26.0.
.PHONY: v2-render
v2-render:
	@$(V2_BUILD); \
	for tree in .:v2-records tests/fixtures/app-template:tests/fixtures/app-template/records; do \
		out=$${tree%%:*}; records=$${tree#*:}; \
		if "$$work/swhurl-v2" render --records "$$records" --profile v2/testdata/profile.json --out "$$out" --check >/dev/null; then \
			echo "$$out: the tree is what the records render; nothing written"; \
		else "$$work/swhurl-v2" render --records "$$records" --profile v2/testdata/profile.json --out "$$out" --revision "$$(git rev-parse HEAD)"; fi; \
	done

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
