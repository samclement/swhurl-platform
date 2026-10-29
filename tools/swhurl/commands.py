"""Every command, stated once: its name, the make variables it takes, its help line and what it touches.

``make help``, the usage message for a missing variable, ``python3 -m swhurl``'s
list and the function a command runs all come from ``COMMANDS``. A test compares
it with the Makefile's targets and with the tables in docs/commands.md (name,
variables, the dry-run mark and Touches), and runs every dry run that plans
offline, so CI keeps no list of its own.

A command is a function ``(argv) -> int`` named by ``run``. The Makefile hands a
target to ``swhurl make <target>``, which builds the arguments from the make
variables in the environment. A command with no ``run`` is a recipe in the
Makefile (sequencing and host scripts); it is listed here for its help and checks.
This module imports nothing from the package until a command runs, so one broken
command cannot stop the others.
"""
from __future__ import annotations

import importlib
import os
import shlex
import sys
from collections.abc import Callable, Mapping
from dataclasses import dataclass

# What a command touches (the Touches column of docs/commands.md).
LOCAL = 'Local'  # this checkout at most; may read the network (templates, a collector binary)
GIT = 'Git'  # edits files for you to commit
READ = 'Cluster (read)'
CLUSTER = 'Cluster'
THROWAWAY = 'Cluster (throwaway)'
GITHUB = 'GitHub'
HOST = 'Host'
TOUCHES = (LOCAL, GIT, READ, CLUSTER, THROWAWAY, GITHUB, HOST)

# What DRY_RUN=true does: print the plan and touch nothing (the test suite runs these), or read without changing.
PLAN = 'plan'
READS = 'reads'

# The verb rule: a name starting with one of these touches exactly this.
VERBS = {'check': LOCAL, 'verify-': READ}

USAGE_EXIT = 2


@dataclass(frozen=True)
class Var:
    """A make variable a command takes, in the order its values become arguments."""
    name: str
    shows: str  # printed after NAME= in help and usage
    required: bool = True
    words: bool = False  # several arguments, split as a shell would (ARGS)
    flag: str = ''  # passed as ``flag value``
    default: str = ''
    env: bool = False  # the command reads it from the environment itself

    def signature(self) -> str:
        text = f'{self.name}={self.shows}'
        return text if self.required else f'[{text}]'

    def arguments(self, env: Mapping[str, str]) -> list[str]:
        value = env.get(self.name, '') or self.default
        if self.env or not value:
            return []
        return [*([self.flag] if self.flag else []), *(shlex.split(value) if self.words else [value])]


def args(shows: str, required: bool = True) -> Var:
    return Var('ARGS', shows, required=required, words=True)


APP = Var('APP', '<app>')
ENV = Var('ENV', '<env>')
MORE = args('"..."', required=False)


@dataclass(frozen=True)
class Command:
    name: str
    help: str
    touches: str = ''
    run: str = ''  # 'module:function'; empty when the recipe is in the Makefile
    prefix: tuple[str, ...] = ()  # arguments that make this command of a shared function (app status, lifecycle suspend)
    takes: tuple[Var | str, ...] = ()  # make variables, and words only the make target adds
    uv: bool = False  # needs the locked dependencies: make runs it through ``uv run --frozen``
    dry_run: str = ''
    internal: bool = False  # run by a pod, a workflow or another command; not a make target

    @property
    def variables(self) -> tuple[Var, ...]:
        return tuple(item for item in self.takes if isinstance(item, Var))

    def signature(self) -> str:
        return ' '.join([self.name, *(var.signature() for var in self.variables)])

    def missing(self, env: Mapping[str, str]) -> list[str]:
        """Required variables that are not set. One the command reads itself (``env``) is its own to refuse."""
        return [var.name for var in self.variables if var.required and not var.env and not env.get(var.name)]

    def arguments(self, env: Mapping[str, str]) -> list[str]:
        """What the make target passes after the command's name."""
        return [word for item in self.takes for word in ([item] if isinstance(item, str) else item.arguments(env))]


def _edit(name: str, function: str, help: str, *takes: Var | str) -> Command:
    return Command(name, help, GIT, f'swhurl.apps.{function}', takes=takes, uv=True)


def _live_test(name: str, module: str, help: str, *takes: Var, touches: str = THROWAWAY) -> Command:
    return Command(f'live-test-{name}', help, touches, f'swhurl.{module}', takes=takes, dry_run=PLAN)


def _timer(name: str, help: str) -> Command:
    return Command(name, help, HOST, dry_run=PLAN)


SECTIONS: tuple[tuple[str, tuple[Command, ...]], ...] = (
    ('Deploy and verify', (
        Command('flux-reconcile', 'Fetch Git, reconcile the source layer and the stack, wait for every unit at that revision', CLUSTER),
        Command('install', 'flux-reconcile, then verify-platform (SKIP_VERIFY=1 skips the checks)', CLUSTER, dry_run=PLAN),
        Command('reconcile', 'Fetch Git and reconcile one Flux unit (for example after changing its Secret)', CLUSTER,
                'swhurl.flux:reconcile_main', takes=(Var('UNIT', '<name>'),), dry_run=PLAN),
        Command('verify-platform', 'Every live platform check (never prints keys)', READ, 'swhurl.verify:main'),
        Command('notifications-check', 'Preview lifecycle and health notifications without sending or saving', READ,
                'swhurl.notifications:main', takes=('--dry-run',), uv=True),
        Command('notifications-heartbeat', 'Alert if the notification checker is stale (the host timer runs it)', READ,
                'swhurl.notifications.heartbeat:main', takes=(args('--dry-run', required=False),), uv=True),
        Command('verify-logs', 'Summarize fresh log parsing, severity and trace coverage (no bodies)', READ,
                'swhurl.logs:main', takes=(Var('MINUTES', '15', required=False, flag='--minutes', default='15'),)),
        Command('flux-install', 'Install or upgrade the Flux controllers at the pinned version with the settings in Git', CLUSTER,
                'swhurl.flux:install_main', dry_run=READS),
        Command('flux-bootstrap', 'Apply the root units and sources (Flux must already be installed)', CLUSTER),
        Command('clickstack-bootstrap', 'Register the ClickStack admin and set the team ingestion key from SOPS (idempotent)', CLUSTER,
                'swhurl.clickstack:main', dry_run=READS),
        Command('clickstack-dashboards', "Keep platform webhooks and app dashboards and alerts in ClickStack; delete removed apps' ones", CLUSTER,
                'swhurl.dashboards:main', dry_run=READS),
    )),
    ('Apps', (
        Command('app-repo', "Create the app's GitHub repository from a stack template; wait for its first image", GITHUB,
                'swhurl.apps.repo:main', takes=(Var('NAME', '<app>'),), dry_run=READS),
        Command('app-hooks', "Create or repoint the image webhook on every auto-deploy app's repository (idempotent)", GITHUB,
                'swhurl.apps.hooks:main', dry_run=READS),
        _edit('app-new', 'new:main', 'Generate the staging instance of an app made by app-repo, which prints this line (ARGS=--help for options)',
              Var('NAME', '<app>'), args('"--from-repo OWNER/<app> --env staging --image ghcr.io/OWNER/<app>:TAG@DIGEST"')),
        _edit('app-expose', 'cli:main_expose', 'Change who can reach an instance',
              APP, ENV, args('"--exposure private|authenticated-web|public [--host H]"')),
        _edit('team-add', 'teams:main_add', 'Register a team in apps/teams.yaml',
              Var('NAME', '<team>'), Var('DESCRIPTION', '"what it owns"', required=False, flag='--description')),
        _edit('team-edit', 'teams:main_edit', 'Change a team; a rename relabels its apps',
              Var('NAME', '<team>'), args('"--description TEXT | --rename NEW | --notify CHANNEL"')),
        _edit('team-remove', 'teams:main_remove', 'Remove a team; refused while it owns apps unless they are moved',
              Var('NAME', '<team>'), args('"--move-apps-to OTHER"', required=False)),
        _edit('team-channel', 'teams:main_team_channel', 'Give a team its notification channel or a new address; prompts without showing it',
              Var('NAME', '<team>'), args('--remove', required=False)),
        _edit('channel-add', 'teams:main_channel_add', 'Add a notification channel; prompts for its address without showing it',
              Var('NAME', '<channel>'), args('--replace', required=False)),
        _edit('channel-remove', 'teams:main_channel_remove', 'Remove a notification channel no team notifies', Var('NAME', '<channel>')),
        _edit('app-team', 'cli:main_team', 'Move an app (every environment) to a team in apps/teams.yaml; adds a missing quota and NetworkPolicy',
              APP, args('"--team T"')),
        _edit('app-alerts', 'cli:main_alerts', "Change an app's default alerts in every environment",
              APP, args('"--error-logs N|none --errors N|none | --none | --default"')),
        _edit('platform-signing-keys', 'keys:main', 'Refresh apps/signing-keys.json from the cluster and rewrite the instances that carry it'),
        _edit('app-calls', 'cli:main_calls', 'Change which apps an instance calls; connects each the callee allows',
              APP, ENV, args('"--add orders | --remove orders"')),
        _edit('app-allows', 'cli:main_allows', 'Change which apps (or team:<team>) may call an instance; connects those that call it',
              APP, ENV, args('"--add team:demo | --remove shop"')),
        _edit('app-egress', 'cli:main_egress', 'Change which hosts outside the cluster an instance may reach; rewrites the egress proxy',
              APP, ENV, args('"--add api.stripe.com | --remove api.stripe.com"')),
        _edit('app-events', 'cli:main_events', 'Set the events an instance receives; publications come from its AsyncAPI document',
              APP, ENV, args('"--add-subscribe orders.created"')),
        _edit('app-offers', 'cli:main_offers', 'Read what an app offers, its API and event documents, from its repository (APP and ENV, or ARGS)',
              Var('APP', '<app>', required=False), Var('ENV', '<env>', required=False), args('"--all staging"', required=False)),
        _edit('app-deploy', 'cli:main_deploy', "Bring staging to each app's newest image, with what it needs and offers (the Deploy workflow runs it)",
              Var('APP', '<app>', required=False), args('"--refresh"', required=False)),
        _edit('app-secret', 'cli:main_secret', 'Set one secret value of an instance, read from standard input or asked for '
              'without echo (needs the age key: run it in your own terminal)', APP, ENV, Var('NAME', '<VARIABLE>')),
        _edit('app-sync', 'cli:main_sync', 'Rewrite derived files; give missing NATS identities, or explicitly rotate one NATS key',
              args('"--rotate-nkey demo-orders/staging"', required=False)),
        _edit('app-promote', 'cli:main_promote', 'Create production from staging or update its image',
              APP, args('"--expect-image=... | --host=..."', required=False)),
        Command('app-status', 'Its state, whether Git is applied, the running image, replicas, route and certificate', READ,
                'swhurl.apps.ops:main', ('status',), (APP, ENV)),
        Command('app-logs', 'Recent workload logs (FOLLOW=true, TAIL=N, PREVIOUS=true)', READ, 'swhurl.apps.ops:main', ('logs',), (APP, ENV)),
        Command('app-reconcile', 'Fetch Git and reconcile only that instance', CLUSTER, 'swhurl.apps.ops:main', ('reconcile',), (APP, ENV)),
        Command('app-check', 'Render one instance and check it against the app policy', LOCAL, 'swhurl.apps.ops:main', ('check',), (APP, ENV)),
        _edit('app-schedule', 'cli:main_schedule', 'Change when a job runs (five cron fields, Etc/UTC)',
              APP, ENV, args('"--schedule \'*/10 * * * *\'"')),
        _edit('app-scale', 'cli:main_scale', "Change replicas or resources (never below the image's minimum)", APP, ENV, args('"--replicas N --cpu Q --memory Q --memory-limit Q"')),
        _edit('app-remove', 'cli:main_remove', "Delete an instance's files and unregister its unit (Flux uninstalls on push)", APP, ENV),
        Command('console-image', 'Pin platform/console to the image published for this commit', GIT, 'swhurl.images:main'),
        Command('v2-snapshot', "Export one pinned revision as the JSON snapshot v2's shadow report reads (records and object identities, SOPS files skipped)", LOCAL,
                'swhurl.v2snapshot:main', takes=(Var('REVISION', '<rev>', flag='--revision'), Var('OUT', '<file>', flag='--out')), dry_run=PLAN),
        Command('console-dev', 'Web console on http://127.0.0.1:8080 as a fixed dev identity (needs uv)', CLUSTER, 'swhurl.console.server:main',
                takes=('--dev', Var('PORT', '8080', required=False, flag='--port', default='8080')), uv=True),
    )),
    ('Settings', (
        Command('platform-certs-staging', 'Set CERT_ISSUER in platform-settings to letsencrypt-staging', GIT,
                'swhurl.settings:platform_certs', ('letsencrypt-staging',), dry_run=PLAN),
        Command('platform-certs-prod', 'Set CERT_ISSUER in platform-settings to letsencrypt-prod', GIT,
                'swhurl.settings:platform_certs', ('letsencrypt-prod',), dry_run=PLAN),
    )),
    ('Lifecycle, backup and recovery', (
        Command('suspend', 'flux suspend a unit or release; workloads and data untouched', CLUSTER, 'swhurl.lifecycle:main', ('suspend',),
                (Var('TARGET', 'kustomization/<name>|helmrelease/<ns>/<name>'),), dry_run=READS),
        Command('resume', 'flux resume a unit or release', CLUSTER, 'swhurl.lifecycle:main', ('resume',),
                (Var('TARGET', 'kustomization/<name>|helmrelease/<ns>/<name>'),), dry_run=READS),
        Command('destroy-data', 'Delete a released claim or volume and its host data', CLUSTER, 'swhurl.lifecycle:main', ('destroy-data',),
                (Var('TARGET', 'pvc/<ns>/<name>|pv/<name>'), Var('CONFIRM', '<TARGET>', env=True)), dry_run=READS),
        Command('backup-mongodb', "Run ClickStack MongoDB's backup job now and show its result", CLUSTER,
                'swhurl.backup_job:run_mongodb_main', dry_run=PLAN),
        Command('backup-sqlite', "Run every app SQLite database's backup job now and show the results", CLUSTER,
                'swhurl.backup_job:run_sqlite_main', dry_run=READS),
        Command('restore-sqlite', "Restore an app's SQLite database from its newest backup (stops the app meanwhile)", CLUSTER,
                'swhurl.sqlite_restore:main', takes=(APP, ENV, Var('CONFIRM', '<app>/<env>', env=True)), dry_run=READS),
    )),
    ('Offline checks', (
        Command('check', 'The offline checks in parallel; CI also renders the templates (check-templates)', LOCAL),
        Command('check-repo', 'Render active Flux paths; schemas, SOPS structure, required Secrets and settings, shell syntax, doc links', LOCAL,
                'swhurl.validate:main'),
        Command('test', 'Unit tests for the tooling, console, manifests and command safety; K= runs only matching classes (needs uv)', LOCAL,
                takes=(Var('K', '<pattern>', required=False, flag='-k'),)),
        Command('check-apps', 'Render every app instance and check the app policy and the app model', LOCAL, 'swhurl.apps.policy:main'),
        Command('check-templates', 'Render each app type from its stack template; each swhurl.yaml must pass app-new and the app policy (needs network)',
                LOCAL, 'swhurl.apps.templates_check:main', uv=True),
        Command('check-otel', 'Render the OTel collectors and validate their config with that exact otelcol-k8s release', LOCAL, 'swhurl.otel:main'),
        Command('check-lint', 'Lint the Python tooling and the bash that stays (needs uv)', LOCAL),
        Command('check-codegraph', 'Unit tests for the code graph development aid (devtools/codegraph)', LOCAL),
        Command('check-docs', 'Compare the workstream registry and its status pages with Git: state, next gate, uncommitted work, cited commits, page-per-change', LOCAL,
                'swhurl.docs:main'),
        Command('check-v2', 'Format, vet and test the Go v2 module, its design guardrails included (needs Go 1.26.0)', LOCAL),
        Command('v2-users', "Render NATS's user list (platform/nats/users.json) from the records; v2 owns that file (needs Go 1.26.0)", LOCAL),
        Command('v2-report', 'What a change does to v2 against the merge base with BASE: API, budgets, packages, imports, files, exceptions',
                LOCAL, takes=(Var('BASE', 'origin/main', required=False, default='origin/main'), Var('AFTER', '<rev>', required=False),
                              Var('FORMAT', 'json', required=False))),
        Command('check-secrets', 'Decrypt tracked Secrets locally; flag placeholders and double encoding (never prints values)', LOCAL,
                'swhurl.secrets_check:main'),
        Command('help', 'List the targets, from tools/swhurl/commands.py', LOCAL, 'swhurl.commands:help_main'),
    )),
    ('Live tests', (
        _live_test('lifecycle', 'livetests.lifecycle:main', 'Prove suspend, uninstall, destroy-data and Orphan on a throwaway app'),
        _live_test('reloader', 'livetests.reloader:main', 'Prove Reloader restarts only opted-in workloads in watched namespaces'),
        _live_test('app-template', 'livetests.app_template:main', 'Deploy the generated app fixtures through Flux, check, remove'),
        _live_test('restore-mongodb', 'recovery:restore_test_mongodb', 'Restore the latest MongoDB backup into a throwaway namespace and check it'),
        _live_test('restore-sqlite', 'livetests.restore_sqlite:main', "Back up a throwaway app's SQLite database, change it, restore it and check it"),
        _live_test('call-identity', 'livetests.call_identity:main',
                   "Prove a callee with no API token can verify a caller's audience-bound token (SKIP_ROTATION=true)"),
        _live_test('network-policy', 'livetests.network_policy:main',
                   'Prove a NetworkPolicy on app pods blocks other namespaces but not Traefik or probes'),
        _live_test('team-scenario', 'livetests.team_scenario:main',
                   'One request through a call and an event on three deployed apps, checked in ClickStack',
                   Var('CALLER', '<app>', env=True), Var('CALLEE', '<app>', env=True), Var('SUBSCRIBER', '<app>', env=True),
                   Var('ENV', 'staging', required=False, env=True), touches=CLUSTER),
    )),
    ('Host', (
        _timer('host-dns', 'Install or update the Route 53 dynamic DNS timer (records in host/dns.env; needs sudo)'),
        _timer('host-dns-delete', 'Remove the dynamic DNS timer'),
        _timer('host-heartbeat', 'Install or update the five-minute notification heartbeat timer (needs sudo)'),
        _timer('host-heartbeat-delete', 'Remove the notification heartbeat timer'),
    )),
)

INTERNAL = (
    Command('make', 'Run a make target: its arguments come from the make variables in the environment', run='swhurl.commands:make', internal=True),
    Command('console', 'Serve the web console (the pod; --dev: loopback, fixed identity)', run='swhurl.console.server:main', internal=True),
    Command('console-merge', 'CI: validate and merge an eligible console pull request', run='swhurl.console.merge:main', internal=True),
    Command('bot-render', 'CI (Render): render the newest main from its records, check it and push it', run='swhurl.bots:main_render', internal=True),
    Command('bot-deploy', "CI (Deploy): bring staging to each app's newest image, check it and push it", run='swhurl.bots:main_deploy', internal=True),
    Command('app-build-image', 'CI: retain a tested PR image and reuse it only for matching main build inputs',
            run='swhurl.apps.build_image:main', internal=True),
    Command('notify-serve', "Serve the notify service: accept messages about apps and deliver them to their team's channels",
            run='swhurl.notifications.service:main', internal=True),
    Command('flux-wait', 'Wait for every Flux unit to apply the fetched Git revision (flux-reconcile ends with it)',
            run='swhurl.flux:main', internal=True),
    Command('backup-job-sqlite', "The backup job's pod: copy, check, encrypt and publish one app's SQLite database",
            run='swhurl.backup_job:sqlite_main', internal=True),
    Command('backup-job-mongodb', "The backup job's pod: dump, encrypt and publish ClickStack's MongoDB",
            run='swhurl.backup_job:mongodb_main', internal=True),
)

COMMANDS: dict[str, Command] = {command.name: command for commands in (*(found for _, found in SECTIONS), INTERNAL) for command in commands}
TARGETS: dict[str, Command] = {name: command for name, command in COMMANDS.items() if not command.internal}


def usage() -> str:
    """What ``python3 -m swhurl`` can run."""
    runnable = {name: command for name, command in COMMANDS.items() if command.run}
    width = max(map(len, runnable))
    return '\n'.join(['usage: python3 -m swhurl <command> [args]', '', 'commands:',
                      *(f'  {name:<{width}}  {command.help}' for name, command in runnable.items())])


def call(command: Command, argv: list[str]) -> int:
    """Run a command's function. A failed external command it did not handle is one line, not a traceback."""
    from swhurl.run import CommandError
    module, function = command.run.split(':')
    try:
        return getattr(importlib.import_module(module), function)([*command.prefix, *argv])
    except CommandError as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 1


def make(argv: list[str] | None = None, env: Mapping[str, str] | None = None,
         execute: Callable[[str, list[str]], object] = os.execvp) -> int:
    """``swhurl make <target>``: refuse a missing variable, or DRY_RUN=true where the command has no dry run,
    build the arguments, run the command (under uv when it needs it)."""
    env = os.environ if env is None else env
    command = TARGETS.get(argv[0]) if argv and len(argv) == 1 else None
    if command is None or not command.run:
        print(f'usage: python3 -m swhurl make <target> (one of: {" ".join(n for n, c in TARGETS.items() if c.run)})', file=sys.stderr)
        return USAGE_EXIT
    if env.get('DRY_RUN') == 'true' and not command.dry_run:
        print(f'[ERROR] make {command.name} has no dry run: DRY_RUN=true would be ignored, so nothing was done', file=sys.stderr)
        return USAGE_EXIT
    if command.missing(env):
        print(f'Usage: make {command.signature()}', file=sys.stderr)
        return USAGE_EXIT
    arguments = command.arguments(env)
    if command.uv:
        sys.stdout.flush()
        execute('uv', ['uv', 'run', '--frozen', 'python', '-m', 'swhurl', command.name, *arguments])
    return call(command, arguments)


def help_text() -> str:
    width = max(len(name) for name in TARGETS)
    lines = ['Targets († takes DRY_RUN=true; variables in [] are optional; details in docs/commands.md):']
    for title, commands in SECTIONS:
        lines += ['', f'{title}:']
        for command in commands:
            mark = ' †' if command.dry_run else ''
            variables = ' '.join(var.signature() for var in command.variables)
            lines.append(f'  {command.name + mark:<{width + 2}}  {command.touches:<19}  {command.help}')
            if variables:
                lines.append(f'  {"":<{width + 2}}  {"":<19}  {variables}')
    return '\n'.join(lines)


def help_main(argv: list[str] | None = None) -> int:
    print(help_text())
    return 0
