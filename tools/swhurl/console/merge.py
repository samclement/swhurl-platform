"""The merge gate: merge an eligible console PR onto the newest main, check the result and push it.

Every gated console PR carries only input records: a new app's, a promotion's, or one instance's
Settings (ACTIONS). The gate merges them onto the newest main, applies the action's rule, renders with
app-sync, checks and pushes one commit, merging again when main moved first. A PR the gate holds loses its auto-merge label and gets a comment saying why;
any other failure keeps the label, so the next run tries again.
"""
from __future__ import annotations

import json
import os
import re
import time
from collections.abc import Callable, Iterator, Mapping
from dataclasses import dataclass
from dataclasses import field as dataclass_field
from pathlib import Path

import yaml

from swhurl import ROOT, bots, interface, yamlio
from swhurl.apps import channels, egress, events, promotion
from swhurl.apps.calling import NOTIFY
from swhurl.apps.contract import SECRET_FILE, TEAMS_FILE, schedule_problem
from swhurl.apps.errors import EditError
from swhurl.apps.instance import instance_folder, instance_unit_file
from swhurl.apps.model import records
from swhurl.apps.model.records import (
    ALLOW_RE,
    APP_FILE,
    RECORD_FILES,
    RELEASE_FILE,
    SETTINGS_FILE,
    App,
    RecordError,
    Release,
    Settings,
)
from swhurl.console.actions import ActionError
from swhurl.run import CommandError, Runner

VALIDATE_WAIT = 120  # seconds this run waits for Validate on the PR head, asking every VALIDATE_POLL
VALIDATE_POLL = 5
ATTEMPTS = 3  # a PR is merged again on a newer main this many times when its push loses a race
REFUSAL = 'Auto-merge refused: '  # starts the comment the gate leaves on a PR it holds; the console shows the rest
REFUSAL_LIMIT = 300
PUBLIC_KEY = re.compile(r'U[A-Z2-7]{55}')  # an nkeys user public key
GIT = ['git', '-c', 'user.name=swhurl auto-merge', '-c', 'user.email=console@users.noreply.github.com']


class Hold(Exception):
    """A person merges this PR: the gate removes its label and comments the reason."""


class Skip(Exception):
    """Not this run's to merge (held, not yet validated, moved): nothing is said on the PR."""


def recipients(root: Path, path: str) -> set[str]:
    """The age recipients .sops.yaml's first rule matching ``path`` encrypts to."""
    config = root / '.sops.yaml'
    for rule in (yamlio.load(config.read_text()) if config.is_file() else {}).get('creation_rules') or []:
        if re.search(rule.get('path_regex', ''), path):
            return {r.strip() for r in str(rule.get('age', '')).split(',') if r.strip()}
    return set()


def new_app_records(app: str, path: str) -> bool:
    """The only inputs a new-app PR may add: its App, staging records, offers and events key."""
    folder = f'apps/{app}/staging/'
    return path == f'apps/{app}/{APP_FILE}' or (path.startswith(folder) and (
        path.removeprefix(folder) in (RELEASE_FILE, SETTINGS_FILE, events.SECRET_FILE)
        or path.startswith(f'{folder}offers/')))


def check_events_key(root: Path, app: str, settings: Settings) -> None:
    """A new encrypted Secret containing only the NATS seed, to the repository's recipients alone.
    The public half is an allocation in instance.yaml; neither half is made again by the gate."""
    key = f'apps/{app}/staging/{events.SECRET_FILE}'
    try:
        doc = yamlio.load((root / key).read_text())
        if not isinstance(doc, dict):
            raise ValueError('not a Secret')
        sops, values = doc.get('sops'), doc.get('stringData')
        wanted = recipients(root, key)
        if (doc.get('apiVersion') != 'v1' or doc.get('kind') != 'Secret' or 'data' in doc
                or doc.get('metadata') != {'name': events.secret_name(app), 'namespace': f'{app}-staging'}
                or not isinstance(values, dict) or set(values) != {interface.SEED_VARIABLE}
                or not isinstance(values[interface.SEED_VARIABLE], str) or not values[interface.SEED_VARIABLE].startswith('ENC[')
                or not isinstance(sops, dict) or not wanted
                or not isinstance(sops.get('age'), list)
                or any(not isinstance(r, dict) or not isinstance(r.get('recipient'), str) for r in sops['age'])
                or {r.get('recipient') for r in sops['age']} != wanted
                or any(sops.get(other) for other in ('kms', 'gcp_kms', 'azure_kv', 'hc_vault', 'pgp'))):
            raise ValueError('not the seed alone')
    except (OSError, ValueError, yaml.YAMLError):
        raise Hold("the events key is not the app's generated, encrypted NATS seed alone") from None
    if not PUBLIC_KEY.fullmatch(settings.allocations.events_key):
        raise Hold('the PR pairs its events key with no NATS public key')


def encrypted_reason(action: str, app: str, env: str, statuses: Mapping[str, str], root: Path) -> str:
    """One encrypted-input rule for publication and every merge attempt. Deletion needs no setup.
    Only a new app's added staging seed, checked against its recipients and public allocation, is exempt.
    """
    encrypted = [p for p, status in statuses.items() if status != 'D' and p.endswith('.sops.yaml')]
    if not encrypted:
        return ''
    if action == 'new' and env == 'staging' and encrypted == [f'apps/{app}/staging/{events.SECRET_FILE}'] and statuses[encrypted[0]] == 'A':
        try:
            settings = settings_of(root, app, env)
            if settings is None:
                raise Hold('the new app has no staging settings')
            check_events_key(root, app, settings)
        except Hold as error:
            return str(error)
        return ''
    if all(Path(p).name == events.SECRET_FILE for p in encrypted):
        return "adds or changes the app's generated encrypted events key"
    return 'needs Secret values to be set on the pull request branch'


def check_new_app(work: Path, app: str) -> None:
    """Required, strict records; no port chosen on the PR branch and no grants to other apps."""
    if app == NOTIFY:
        raise Hold(f'{NOTIFY} is reserved for the platform notification service')
    folder = instance_folder(work, app, 'staging')
    try:
        App.parse(yamlio.load((folder.parent / APP_FILE).read_text()))
        release = Release.parse(yamlio.load((folder / RELEASE_FILE).read_text()))
        settings = Settings.parse(yamlio.load((folder / SETTINGS_FILE).read_text()))
    except (OSError, RecordError, yaml.YAMLError) as error:
        raise Hold(f'the new app records cannot be read: {error}') from None
    if settings.grants.exceptions:
        raise Hold('it sets policy exceptions: a person reviews it')
    if settings.allocations.egress_port is not None:
        raise Hold('a new-app PR must not preallocate an egress port')
    if settings.grants.calls or settings.grants.subscriptions:
        raise Hold('it calls or subscribes to another app: a person reviews it')
    if events.has_nats(work) or release.publishes or settings.allocations.events_key or (folder / events.SECRET_FILE).exists():
        check_events_key(work, app, settings)


def instance_records(app: str, env: str, path: str) -> bool:
    return path == f'apps/{app}/{env}/{SETTINGS_FILE}'


def removal_inputs(app: str, env: str, path: str) -> bool:
    """The inputs a removal deletes: the instance's records, encrypted files and offers, and the App record."""
    folder = f'apps/{app}/{env}/'
    return path == f'apps/{app}/{APP_FILE}' or (path.startswith(folder) and (
        path.removeprefix(folder) in RECORD_FILES | {SECRET_FILE, events.SECRET_FILE} or path.startswith(f'{folder}offers/')))


def team_inputs(path: str) -> bool:
    """A team change's inputs: the registry, and the App and Settings records that name the team."""
    return path == str(TEAMS_FILE) or Path(path).match('apps/*/app.yaml') or Path(path).match('apps/*/*/instance.yaml')


def channel_inputs(path: str) -> bool:
    """A channel change's inputs: the registry, the encrypted channel and its kustomization; not the generated routes."""
    return path in (str(TEAMS_FILE), str(channels.UNIT_FILE), str(channels.NOTIFY_DIR / 'kustomization.yaml')) or (
        Path(path).parent == channels.NOTIFY_DIR and Path(path).match('channel-*.sops.yaml'))


@dataclass(frozen=True)
class Command:
    """One command of the clone's own tooling. ``input`` is its standard input: the one way a secret
    reaches it (never an argument, never in a message)."""
    name: str
    argv: tuple[str, ...]
    input: str | None = dataclass_field(default=None, repr=False)

    def as_make(self) -> str:
        """The change as the operator would type it, never including a credential."""
        name, *args = self.argv
        if self.name == 'app-new':
            return f'make app-new NAME={name} ARGS="{" ".join(args)}"'
        if self.name == 'team-channel':
            suffix = ' ARGS=--remove' if '--remove' in args else '    # the address was entered on the console and is only in the encrypted file'
            return f'make team-channel NAME={name}{suffix}'
        if self.name.startswith('team-'):
            if self.name == 'team-add':
                return f"make team-add NAME={name} DESCRIPTION='{args[0].removeprefix('--description=')}'"
            if self.name == 'team-remove':
                suffix = f' ARGS="--move-apps-to {args[0].split("=", 1)[1]}"' if args else ''
                return f'make team-remove NAME={name}{suffix}'
            return f'make {self.name} NAME={name} ARGS="{" ".join(args)}"'
        flags = f' ARGS="{" ".join(self.argv[2:])}"' if self.argv[2:] else ''
        return f'make {self.name} APP={self.argv[0]} ENV={self.argv[1]}{flags}'


SCALE_FIELDS = (('replicas', 'replicas', 'Replicas'), ('cpu', 'cpu', 'CPU request'),
                ('memory', 'memory', 'Memory request'), ('memory_limit', 'memory-limit', 'Memory limit'))
HOST_RE = re.compile(r'[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+')


def checked_host(host: str) -> str:
    """``host`` if it is empty or a DNS name."""
    if host and not HOST_RE.fullmatch(host):
        raise ActionError(f'host {host!r} is not a DNS name')
    return host


def scale_flags(form: Mapping[str, str]) -> list[str]:
    """``--flag=value`` for each filled-in scale field; at least one is needed."""
    flags = []
    for field, flag, label in SCALE_FIELDS:
        value = form.get(field, '').strip()
        if value:
            if not re.fullmatch(r'[0-9A-Za-z.]{1,12}', value):
                raise ActionError(f'{label} {value!r} is not a number or quantity')
            flags.append(f'--{flag}={value}')
    if not flags:
        raise ActionError('change at least one of replicas, CPU, memory or memory limit')
    return flags


def expose_flags(form: Mapping[str, str]) -> list[str]:
    """``--exposure=`` and an optional ``--host=`` for app-expose."""
    exposure, host = form.get('exposure', '').strip(), checked_host(form.get('host', '').strip())
    if exposure not in records.EXPOSURES:
        raise ActionError(f'exposure must be one of {", ".join(records.EXPOSURES)}')
    return [f'--exposure={exposure}'] + ([f'--host={host}'] if host else [])


@dataclass(frozen=True)
class Connections:
    """Platform grants: apps to call and ``<app>.<event>`` to receive.
    Publications belong to the image's AsyncAPI document, not a connection form."""
    calls: tuple[str, ...] = ()
    subscribe: tuple[str, ...] = ()

    @classmethod
    def from_form(cls, form: Mapping[str, str]) -> Connections:
        if 'publish' in form:
            raise ActionError('publishing choices are no longer supported: edit asyncapi.yaml named by provides.eventsFile')
        chosen = cls(*(tuple(name.strip() for name in form.get(field, '').split(',') if name.strip())
                       for field in ('calls', 'subscribe')))
        bad = [name for name in chosen.calls if not records.NAME_RE.match(name)]
        bad += [name for name in chosen.subscribe if not records.SUBSCRIPTION_RE.match(name)]
        if bad:
            raise ActionError(f'{", ".join(bad)}: a call names an app, a subscription <app>.<event>')
        return chosen

    def check_offered(self, callable_apps: set[str], offered_events: set[str]) -> None:
        """Refuse a choice outside the lists the form offered, before a repository is created for an
        app that could not then be added."""
        missing = [name for name in self.calls if name not in callable_apps]
        missing += [name for name in self.subscribe if name not in offered_events]
        if missing:
            raise ActionError(f'{", ".join(missing)}: not a web app, or an event, that staging has now; reload the form')

    def new_app_flags(self) -> list[str]:
        """``--calls=`` and ``--subscribe=`` for app-new."""
        return [f'--{field}={",".join(names)}' for field, names in
                (('calls', self.calls), ('subscribe', self.subscribe)) if names]

    def adding(self, app: str, env: str) -> tuple[Command, ...]:
        """The app-calls and app-events commands that add these to an instance; one clone runs both,
        so one pull request carries them."""
        commands = []
        if self.calls:
            commands.append(Command('app-calls', (app, env, f'--add={",".join(self.calls)}')))
        if self.subscribe:
            commands.append(Command('app-events', (app, env, f'--add-subscribe={",".join(self.subscribe)}')))
        if not commands:
            raise ActionError('choose an app to call or an event to receive')
        return tuple(commands)


def removing(app: str, env: str, form: Mapping[str, str]) -> tuple[Command, ...]:
    """The command that removes one connection this app declared: the row's ``role``, ``app`` and ``event``."""
    role, other, event = (form.get(key, '').strip() for key in ('role', 'app', 'event'))
    if role == 'calls' and records.NAME_RE.match(other):
        return (Command('app-calls', (app, env, f'--remove={other}')),)
    if role == 'subscribes' and records.SUBSCRIPTION_RE.match(f'{other}.{event}'):
        return (Command('app-events', (app, env, f'--remove-subscribe={other}.{event}')),)
    if role == 'publishes':
        raise ActionError('to stop publishing, edit asyncapi.yaml named by provides.eventsFile; '
                          'publications cannot be removed on the platform')
    if role == 'reaches' and not egress.grant_problem(event):
        return (Command('app-egress', (app, env, f'--remove={event}')),)
    raise ActionError('no such connection to remove: only the app that declared one can remove it')


def allow_flags(form: Mapping[str, str]) -> list[str]:
    """``--add=`` and ``--remove=`` for app-allows, from the form's comma-separated ``add`` and ``remove``."""
    chosen = {field: [name.strip() for name in form.get(field, '').split(',') if name.strip()] for field in ('add', 'remove')}
    if bad := [name for names in chosen.values() for name in names if not ALLOW_RE.match(name)]:
        raise ActionError(f'{", ".join(bad)}: an allowed caller is an app name or team:<team>')
    flags = [f'--{field}={",".join(names)}' for field, names in chosen.items() if names]
    if not flags:
        raise ActionError('name an app or team:<team> to allow or stop allowing')
    return flags


def egress_hosts(form: Mapping[str, str]) -> list[str]:
    """The hosts a form grants (``hosts``, comma-separated), each as app-egress keeps it."""
    hosts = [egress.grant(name) for name in form.get('hosts', '').split(',') if name.strip()]
    if not hosts:
        raise ActionError('name a host, such as api.stripe.com')
    problems = [problem for name in hosts if (problem := egress.grant_problem(name))]
    if problems:
        raise ActionError('; '.join(problems))
    return hosts


def schedule_commands(app: str, env: str, form: Mapping[str, str]) -> tuple[Command, ...]:
    when = ' '.join(form.get('schedule', '').split())
    if problem := schedule_problem(when):
        raise ActionError(problem)
    return (Command('app-schedule', (app, env, f'--schedule={when}')),)


def team_commands(action: str, name: str, form: Mapping[str, str]) -> tuple[Command, ...]:
    description = ' '.join(form.get('description', '').split())
    rename, move = form.get('rename', '').strip(), form.get('move_apps_to', '').strip()
    for label, value in (('team name', name), ('new name', rename), ('team to move apps to', move)):
        if (value or label == 'team name') and not records.NAME_RE.fullmatch(value):
            raise ActionError(f'{label}: lowercase letters, digits and hyphens, at most 40 characters')
    if len(description) > 200:
        raise ActionError('a description is one line of at most 200 characters')
    flags = ([f'--move-apps-to={move}'] if move else []) if action == 'remove' else [f'--description={description}']
    if action == 'edit' and rename and rename != name:
        flags.append(f'--rename={rename}')
    return (Command(f'team-{action}', (name, *flags)),)


def channel_commands(name: str, form: Mapping[str, str], *, remove: bool = False) -> tuple[Command, ...]:
    if not records.NAME_RE.fullmatch(name):
        raise ActionError('team name: lowercase letters, digits and hyphens, at most 40 characters')
    if remove:
        return (Command('team-channel', (name, '--remove')),)
    transport, address = form.get('transport', 'ntfy').strip(), form.get('address', '').strip()
    if problem := channels.address_problem(transport, address):
        raise ActionError(f'channel address: {problem}')
    return (Command('team-channel', (name, f'--transport={transport}'), input=address + '\n'),)


def new_commands(name: str, _env: str, form: Mapping) -> tuple[Command, ...]:
    return (Command('app-new', (name, f'--from-repo={form["repo"]}', '--env=staging',
                                f'--image={form["image"]}', *form['flags'])),)


def promote_commands(app: str, _env: str, form: Mapping[str, str]) -> tuple[Command, ...]:
    if not form.get('image'):
        raise ActionError('refresh the staging app page and review the image before promotion')
    host = checked_host(form.get('host', ''))
    return (Command('app-promote', (app, f'--expect-image={form["image"]}', '--prepare-only',
                                   *([f'--host={host}'] if host else []))),)


@dataclass(frozen=True)
class Action:
    """One kind of console PR, shared by the console (which opens it) and the gate (which merges it):
    the environments its branch may name (none for a team's), the input paths it carries, which of
    their changes it keeps (removal: deletions only), whether the gate may merge it, and the
    commands and input reader/rule, and the words for its PR, job and any conflict."""
    envs: tuple[str, ...]
    owns: Callable[[str, str, str], bool]  # (app or team, env, path)
    commands: Callable[[str, str, Mapping], tuple[Command, ...]]
    read: Callable[[Path, str, str], object]  # semantic inputs, before and after the edit/merge
    rule: Callable[..., str]  # (name, env, paths, before, after, staged) -> manual-merge reason or ''
    title: str  # formatted with name and env; never includes credentials
    job: str = ''  # when different from the action's name
    kind: str = 'console'  # what open_action does for it (promotion, new-app), and in "which are not <app>'s <kind> records"
    again: str = 'make the change again'  # in "close it and <again> from the console"
    gated: bool = True  # False: a person always merges it (remove, teams, channels); the gate skips it
    keeps: str = 'AMD'  # the changes (added, modified, deleted) of its paths the PR carries
    target_env: str = ''  # the instance it changes when that is not the branch's (promotion: prod)


SHA = '[0-9a-f]{7,40}'


def parse(branch: str) -> tuple[str, str, str] | None:
    """``(action, app or team, env)`` of a console branch ``console/<action>-<name>[-<env>]-<commit>``,
    env '' for a team's; None for any other branch. Its env is the branch's, not always the target's."""
    for action, entry in ACTIONS.items():
        envs = f'-({"|".join(entry.envs)})' if entry.envs else '()'
        found = re.fullmatch(rf'console/{re.escape(action)}-([a-z][a-z0-9-]*?){envs}-{SHA}', branch)
        if found and records.NAME_RE.fullmatch(found[1]):
            return action, found[1], found[2]
    return None


def branch(action: str, name: str, env: str, revision: str) -> str:
    """The branch of ``action`` on ``name`` (and ``env``), made from main at ``revision``; refused unless
    parse reads it back as the same."""
    made = f'console/{action}-{name}' + (f'-{env}' if env else '') + f'-{revision}'
    if parse(made) != (action, name, env):
        raise ValueError(f'refusing to push {made!r}: the console pushes only its own action branches')
    return made


def target(action: str, name: str, env: str) -> str:
    """What a change is to: ``<app>/<env>`` (promotion: production) or ``team:<name>``."""
    entry = ACTIONS[action]
    return f'{name}/{entry.target_env or env}' if entry.envs else f'team:{name}'


def gated(branch_name: str) -> bool:
    """Whether the gate takes a PR on this branch, and so may have left its refusal."""
    found = parse(branch_name)
    return bool(found and ACTIONS[found[0]].gated)


def settings_of(root: Path, app: str, env: str) -> Settings | None:
    """An instance's Settings in the tree at ``root``; None when it has none."""
    path = instance_folder(root, app, env) / SETTINGS_FILE
    if not path.is_file():
        return None
    try:
        return Settings.parse(yamlio.load(path.read_text()), f'{app}/{env}: {SETTINGS_FILE}')
    except (RecordError, yaml.YAMLError) as error:
        raise Hold(f'the instance record cannot be read: {error}') from None


REACH = 'changes who can reach it'
STAGING_REVIEW: dict[str, tuple[str, str]] = {
    'exceptions': ('changes its app-policy exceptions', 'changed'),
    'exposure': (REACH, 'changed'), 'host': (REACH, 'changed'), 'issuer': (REACH, 'changed'),
    'allows': ('allows {added} to call it', 'added'),
    'egress': ('grants hosts outside the cluster: {added}', 'added'),
    'calls': ('', 'free'), 'subscriptions': ('', 'free'), 'replicas': ('', 'free'), 'capacity': ('', 'free'), 'schedule': ('', 'free'),
}
"""Each Grants and Choices field's staging review, first match in order: held when ``changed``, or a name ``added``, or ``free``."""


def settings_held(env: str, before: Settings | None, after: Settings | None) -> str:
    """An instance change's one rule: why a person merges it, or '' when it merges itself. ``before`` and
    ``after``: the instance's Settings on main and with the change. Held when it touches production or what the
    platform allocated (compared whole), else as ``STAGING_REVIEW`` says.
    The words follow "this pull request" (console) and "Auto-merge refused: it" (the gate)."""
    if before is None:
        return 'changes an instance main does not have'
    if after is None:
        return "removes the instance's settings record"
    if before == after:
        return ''
    if env != 'staging':
        return 'changes production settings'
    if after.allocations != before.allocations:
        return 'changes what the platform allocated it (egress port, events key or database)'
    for name, (reason, review) in STAGING_REVIEW.items():
        old, new = (getattr(s.grants if hasattr(s.grants, name) else s.choices, name) for s in (before, after))
        if review == 'changed' and old != new:
            return reason
        if review == 'added' and (added := [item for item in new if item not in old]):
            return reason.format(added=', '.join(added))
    return ''


def new_inputs(root: Path, app: str, _env: str) -> tuple[bool, str]:
    exists = (root / 'apps' / app).exists() or any(
        instance_unit_file(root, app, env).exists() for env in ('staging', 'prod'))
    try:
        check_new_app(root, app)
    except Hold as error:
        return exists, str(error)
    return exists, ''


def new_held(_app, _env, _files, before, after, _staged) -> str:
    return 'new-app PR overwrites an existing app or instance' if before[0] else after[1]


def production_inputs(root: Path, app: str, _env: str) -> Release | None:
    try:
        return promotion.release_of(root, app, 'prod')
    except EditError as error:
        raise Hold(f'the production release record cannot be read: {error}') from None


def manual_inputs(_root, _name, _env):
    return None


def manual_held(*_args) -> str:
    return 'needs manual review'


def instance_held(_app, env, _files, before, after, _staged) -> str:
    return settings_held(env, before, after)


def promotion_held(app, _env, files, before, after, staged) -> str:
    return promotion.held(app, files, before, after, staged)


ACTIONS: dict[str, Action] = {
    'new': Action(('staging',), lambda app, _env, path: new_app_records(app, path), new_commands,
                  new_inputs, new_held, 'apps: add {name}/staging', job='new app and repository',
                  kind='new-app', again='create it again'),
    'promote': Action(('staging',), lambda app, _env, path: promotion.records(app, path), promote_commands,
                      production_inputs, promotion_held, 'apps: promote {name}/staging to {name}/prod',
                      kind='promotion', again='promote again', target_env='prod'),
    'scale': Action(('staging', 'prod'), instance_records,
                    lambda app, env, form: (Command('app-scale', (app, env, *scale_flags(form))),),
                    settings_of, instance_held, 'apps: scale {name}/{env}', kind='instance'),
    'schedule': Action(('staging', 'prod'), instance_records, schedule_commands,
                       settings_of, instance_held, 'apps: change when it runs: {name}/{env}', kind='instance'),
    'expose': Action(('staging', 'prod'), instance_records,
                     lambda app, env, form: (Command('app-expose', (app, env, *expose_flags(form))),),
                     settings_of, instance_held, 'apps: change access to {name}/{env}', kind='instance'),
    # Subscribing may give an instance its first NATS key: that PR also carries the encrypted seed (held).
    'connect': Action(('staging', 'prod'), lambda app, env, path: instance_records(app, env, path)
                      or path == f'apps/{app}/{env}/{events.SECRET_FILE}',
                      lambda app, env, form: Connections.from_form(form).adding(app, env),
                      settings_of, instance_held, 'apps: change the connections of {name}/{env}', kind='instance'),
    'disconnect': Action(('staging', 'prod'), instance_records, removing,
                         settings_of, instance_held, 'apps: change the connections of {name}/{env}', kind='instance'),
    'egress': Action(('staging', 'prod'), instance_records,
                     lambda app, env, form: (Command('app-egress', (app, env, f'--add={",".join(egress_hosts(form))}')),),
                     settings_of, instance_held, 'apps: change the hosts outside the cluster of {name}/{env}', kind='instance'),
    'allow': Action(('staging', 'prod'), instance_records,
                    lambda app, env, form: (Command('app-allows', (app, env, *allow_flags(form))),),
                    settings_of, instance_held, 'apps: change who may call {name}/{env}', kind='instance'),
    'remove': Action(('staging', 'prod'), removal_inputs,
                     lambda app, env, _form: (Command('app-remove', (app, env)),),
                     manual_inputs, manual_held, 'apps: remove {name}/{env}', gated=False, keeps='D'),
    **{f'team-{action}': Action((), lambda _name, _env, path: team_inputs(path),
                              lambda name, _env, form, action=action: team_commands(action, name, form),
                              manual_inputs, manual_held, f'teams: {verb} {{name}}', job=f'team {action}', gated=False)
       for action, verb in (('add', 'add'), ('edit', 'change'), ('remove', 'remove'))},
    **{f'team-channel-{action}': Action((), lambda _name, _env, path: channel_inputs(path),
                                      lambda name, _env, form, action=action: channel_commands(name, form, remove=action == 'remove'),
                                      manual_inputs, manual_held, f'teams: {action} the notification channel of {{name}}',
                                      job=f'channel {action}', gated=False)
       for action in ('set', 'remove')},
}
"""Every console Git change: branch/target, command builder, input ownership and semantic merge rule.
Readers return only the facts the rule needs. Console preparation and each newest-main gate retry read
before and after, then judge the same facts; read failures propagate, never authorize a merge."""
INSTANCE_KINDS = tuple(name for name, entry in ACTIONS.items() if entry.kind == 'instance')


def judge(action: str, name: str, env: str, files: list[str], before: object, after: object,
          staged: Callable[[Release], bool] = lambda _release: False) -> str:
    """Why these semantic inputs need manual merge, or ''. Encrypted-file publication checks are separate."""
    entry = ACTIONS[action]
    if other := sorted(path for path in files if not entry.owns(name, env, path)):
        return f"changes {', '.join(other[:5])}, which are not {name}'s {entry.kind} records: a person reviews it"
    return entry.rule(name, env, files, before, after, staged)


def eligible(pr: dict, sha: str, repo: str) -> tuple[str, str, str]:
    """``(action, app, env)`` of a PR this run may merge."""
    found = parse(pr.get('head', {}).get('ref', ''))
    if (pr.get('state') != 'open' or not found or not ACTIONS[found[0]].gated
            or pr.get('base', {}).get('ref') != 'main'
            or pr.get('head', {}).get('sha') != sha
            or pr.get('head', {}).get('repo', {}).get('full_name') != repo
            or 'auto-merge' not in {label['name'] for label in pr.get('labels', [])} or pr.get('draft')):
        raise Skip('PR is held, moved, closed, a fork, or outside the supported console branches')
    return found


def validated(runner: Runner, sha: str, sleep: Callable[[float], None]) -> None:
    """Wait for Validate on the PR head. The label starts this run beside Validate, and leaving while it
    is still running would cost the next run a second runner start before the merge."""
    waited = 0
    while True:
        runs = runner.json(['gh', 'run', 'list', '--workflow', 'validate.yml', '--commit', sha,
                            '--event', 'pull_request', '--limit', '1', '--json', 'databaseId,status,conclusion'])
        if runs and runs[0].get('status') == 'completed':
            if runs[0].get('conclusion') != 'success':
                raise Skip('Validate has not passed on the current PR head')
            if waited:
                print(f'Validate passed after a {waited} s wait', flush=True)
            return
        if waited >= VALIDATE_WAIT:
            raise Skip('Validate has not passed on the current PR head (still running)')
        if not waited:
            print(f'Waiting up to {VALIDATE_WAIT} s for Validate on {sha[:7]}', flush=True)
        sleep(VALIDATE_POLL)
        waited += VALIDATE_POLL


def fetch_head(runner: Runner, root: Path, number: int, sha: str) -> None:
    """Fetch the PR head (apart from main: together, FETCH_HEAD is main's); it must still be ``sha``."""
    runner.run(['git', 'fetch', 'origin', f'pull/{number}/head'], cwd=root)
    if runner.output(['git', 'rev-parse', 'FETCH_HEAD'], cwd=root).strip() != sha:
        raise Skip('PR head changed during fetch')


def merge(runner: Runner, root: Path, number: int, sha: str, repo: str,
          sleep: Callable[[float], None] = time.sleep) -> None:
    pr_path = f'repos/{repo}/pulls/{number}'
    pr = runner.json(['gh', 'api', pr_path])
    action, app, env = eligible(pr, sha, repo)
    validated(runner, sha, sleep)
    files = runner.output(['gh', 'pr', 'diff', str(number), '--repo', repo, '--name-only']).splitlines()
    merge_records(runner, root, number, sha, repo, pr, action, app, files, env)
    runner.run(['git', 'push', 'origin', '--delete', pr['head']['ref']], cwd=root, mutating=True, check=False)


def staging_history(runner: Runner, work: Path, app: str) -> Iterator[Release]:
    """Complete staging Releases selected on main's first-parent history. Git failures propagate."""
    path = (instance_folder(Path(), app, 'staging') / RELEASE_FILE).as_posix()
    revisions = runner.output(['git', 'log', '--first-parent', '--diff-filter=AM',
                               '--format=%H', 'HEAD', '--', path], cwd=work).splitlines()
    for revision in revisions:
        text = runner.output(['git', 'show', f'{revision}:{path}'], cwd=work)
        yield Release.parse(yamlio.load(text), f'{app}/staging at {revision}')


def merge_records(runner: Runner, root: Path, number: int, sha: str, repo: str, pr: dict, action: str,
                  app: str, files: list[str], env: str = 'staging') -> None:
    """Merge the PR's records onto the newest main, apply the action's rule, render, check and push as a
    merge of the PR head (bots.push_main: a push lost to another writer is merged again, ATTEMPTS times)."""
    rule = ACTIONS[action]
    if other := sorted(path for path in files if not rule.owns(app, env, path)):
        raise Hold(f"it changes {', '.join(other[:5])}, which are not {app}'s {rule.kind} records: a person reviews it")

    def change(work: Path, base: str) -> bots.Commit:
        fetch_head(runner, root, number, sha)
        before = rule.read(work, app, env)
        merged = runner.run([*GIT, 'merge', '--no-ff', '--no-commit', sha], cwd=work, check=False)
        if merged.returncode:
            conflicts = runner.output(['git', 'diff', '--name-only', '--diff-filter=U'], cwd=work)
            if conflicts.strip():
                raise Hold(f'it conflicts with main: close it and {rule.again} from the console')
            raise CommandError(merged.args, runner.redact(merged.stderr or merged.stdout), merged.returncode)
        after = rule.read(work, app, env)
        statuses = dict(line.split('\t', 1)[::-1] for line in runner.output(
            ['git', 'diff', '--name-status', '--no-renames', base], cwd=work).splitlines())
        if reason := encrypted_reason(action, app, env, statuses, work):
            raise Hold(f'it {reason}: a person reviews it')
        if reason := judge(action, app, env, files, before, after,
                           lambda release: promotion.staged(release, staging_history(runner, work, app))):
            raise Hold(f'it {reason}: a person reviews it')
        bots.swhurl(runner, work, 'app-sync')
        # GitHub shows the PR merged: its head is the commit's second parent.
        return bots.Commit(f'Merge pull request #{number}: {pr.get("title", "")}', (sha,))

    def ready() -> None:
        """Recheck the person's hold and the exact PR head after the checks."""
        eligible(runner.json(['gh', 'api', f'repos/{repo}/pulls/{number}']), sha, repo)

    bots.push_main(runner, root, change, attempts=ATTEMPTS, ready=ready, git=GIT)


def refuse(runner: Runner, repo: str, number: int, reason: str) -> None:
    """Hand the PR to a person: remove its label and leave the reason, one redacted line, where the
    console's Activity reads it."""
    runner.run(['gh', 'api', '--method', 'DELETE', f'repos/{repo}/issues/{number}/labels/auto-merge'],
               mutating=True, check=False)
    said = ' '.join(reason.split())[:REFUSAL_LIMIT]
    runner.run(['gh', 'api', '--method', 'POST', f'repos/{repo}/issues/{number}/comments', '--input', '-'],
               input=json.dumps({'body': f'{REFUSAL}{said}\n\nReview the changes and merge this yourself, or close it '
                                         'and start again from the console.'}), mutating=True, check=False)


def main(argv=None, runner: Runner | None = None) -> int:
    """Merge the eligible console PRs one at a time. A hold or a failure of one PR is reported and the
    queue goes on; the run fails at the end if any PR was held or failed."""
    import argparse
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('number', type=int, nargs='?')
    parser.add_argument('sha', nargs='?')
    args = parser.parse_args(argv)
    if (args.number is None) != (args.sha is None):
        parser.error('provide both PR number and SHA, or neither to process the eligible queue')
    runner = runner or Runner()
    runner.add_secret(os.environ.get('GH_TOKEN'))
    repo = os.environ.get('GITHUB_REPOSITORY', '')
    if not repo:
        print('Not merging console PRs: GITHUB_REPOSITORY is not set')
        return 1
    try:
        queue = [{'number': args.number, 'headRefOid': args.sha, 'headRefName': 'console/'}] if args.number is not None \
            else runner.json(['gh', 'pr', 'list', '--state', 'open', '--label', 'auto-merge',
                              '--json', 'number,headRefOid,headRefName'])
    except Exception as error:  # noqa: BLE001 - reported, and the run fails
        print(f'Not merging console PRs: {runner.redact(str(error))}')
        return 1
    failed = False
    for item in queue:
        if not item['headRefName'].startswith('console/'):
            continue
        number = item['number']
        try:
            merge(runner, ROOT, number, item['headRefOid'], repo)
        except Skip as error:
            print(f'Not merging #{number}: {error}')
        except Hold as error:
            reason = runner.redact(str(error))
            print(f'Not merging #{number}: {reason}')
            refuse(runner, repo, number, reason)
            failed = True
        except Exception as error:  # noqa: BLE001 - one PR's failure keeps its label; the queue goes on
            print(f'Could not merge #{number} ({type(error).__name__}), the next run tries again: {runner.redact(str(error))}')
            failed = True
    return 1 if failed else 0
