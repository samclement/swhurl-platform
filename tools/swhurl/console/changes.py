"""The console's change kinds: one table for the pull request the console opens and the branch Activity reads.

Every console change is a pull request on a ``console/<kind>-<name>[-<env>]-<sha>`` branch, and a person
merges every one: the console adds no label and nothing merges a console branch. A kind states the
environments its branch may name, which input paths its pull request may carry (``owns``), which of their
changes it keeps, the commands the clone runs to make it, its pull request title and its job's name.
"""
from __future__ import annotations

import re
from collections.abc import Callable, Mapping
from dataclasses import dataclass
from dataclasses import field as dataclass_field
from pathlib import Path

from swhurl.apps import channels, events
from swhurl.apps.contract import TEAMS_FILE
from swhurl.apps.model import records
from swhurl.apps.model.records import ALLOW_RE
from swhurl.console import v2
from swhurl.console.actions import ActionError


def encrypted_reason(action: str, statuses: Mapping[str, str]) -> str:
    """Why a changed encrypted input needs a person to set its values. A new app's seed is written and
    encrypted by v2's ``create`` itself, so a creation is exempt; a deletion needs no setup."""
    if action == 'new':
        return ''
    encrypted = [p for p, status in statuses.items() if status != 'D' and p.endswith('.sops.yaml')]
    if not encrypted:
        return ''
    if all(Path(p).name == events.SECRET_FILE for p in encrypted):
        return "adds or changes the app's generated encrypted events key"
    return 'needs Secret values to be set on the pull request branch'


def team_inputs(path: str) -> bool:
    """A team or channel change's files: the registry, what v2 renders from it and a channel's encrypted Secret."""
    return path in (str(TEAMS_FILE), str(channels.ROUTES_FILE)) or Path(path).parent == channels.CHANNELS_DIR


@dataclass(frozen=True)
class Command:
    """One command of the clone's own tooling. ``input`` is its standard input: the one way a secret
    reaches it (never an argument, never in a message)."""
    name: str
    argv: tuple[str, ...]
    input: str | None = dataclass_field(default=None, repr=False)
    tool: str = 'python'  # 'v2' runs swhurl-v2's binary; 'python' runs the clone's own swhurl command

    def as_make(self) -> str:
        """The change as the operator would type it, never including a credential."""
        if self.tool == 'v2':
            return ' '.join((v2.BINARY, *self.argv))
        flags = f' ARGS="{" ".join(self.argv[2:])}"' if self.argv[2:] else ''
        return f'make {self.name} APP={self.argv[0]} ENV={self.argv[1]}{flags}'


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


def new_commands(name: str, _env: str, form: Mapping) -> tuple[Command, ...]:
    """v2's creation from the image the new repository published, then the grants the form chose, then the
    whole-tree render. ``create`` writes records only, so the edit and the render are what make the PR carry
    everything v2 derives from them."""
    settings = list(form['flags'])
    team = next((s.partition('=')[2] for s in settings if s.startswith('team=')), '')
    exposure = next((s.partition('=')[2] for s in settings if s.startswith('exposure=')), '')
    create = ['create', '--records', v2.RECORDS, '--app', name, '--team', team, '--image', form['image'],
              '--profile', v2.PROFILE, '--out', v2.OUT, *(['--exposure', exposure] if exposure else [])]
    commands = [Command('create', tuple(create), tool='v2')]
    commands += [Command('edit', tuple(v2.edit_argv(name, 'staging', setting)), tool='v2')
                 for setting in settings if not setting.startswith(('team=', 'exposure='))]
    commands.append(Command('render', tuple(v2.render_argv()), tool='v2'))
    return tuple(commands)


def team_commands(action: str, name: str, form: Mapping[str, str]) -> tuple[Command, ...]:
    """v2's team change for the form's action; v2 judges the registry and the description. An edit states the
    description and then any new name: a team that apps or grants name is not renamed, and v2's refusal says to move
    them with swhurl-v2 edit. The names are checked here too because the branch is named after the team."""
    description = ' '.join(form.get('description', '').split())
    rename = form.get('rename', '').strip()
    for label, value in (('team name', name), ('new name', rename)):
        if (value or label == 'team name') and not records.NAME_RE.fullmatch(value):
            raise ActionError(f'{label}: lowercase letters, digits and hyphens, at most 40 characters')
    if action == 'remove':
        return (Command('team', tuple(v2.team_argv(name, 'remove')), tool='v2'),)
    commands = [Command('team', tuple(v2.team_argv(name, 'add' if action == 'add' else 'describe',
                                                   f'--description={description}')), tool='v2')]
    if action == 'edit' and rename and rename != name:
        commands.append(Command('team', tuple(v2.team_argv(name, 'rename', f'--rename={rename}')), tool='v2'))
    return tuple(commands)


def channel_commands(name: str, form: Mapping[str, str], *, remove: bool = False) -> tuple[Command, ...]:
    """Give a team its channel, the address on v2's standard input, or remove it; v2 judges the address."""
    if not records.NAME_RE.fullmatch(name):
        raise ActionError('team name: lowercase letters, digits and hyphens, at most 40 characters')
    if remove:
        return (Command('team', tuple(v2.team_argv(name, 'drop-channel')), tool='v2'),)
    transport, address = form.get('transport', 'ntfy').strip(), form.get('address', '').strip()
    return (Command('team', tuple(v2.team_argv(name, 'channel', f'--transport={transport}')), input=address + '\n',
                    tool='v2'),)


def v2_commands(app: str, env: str, settings: tuple[str, ...]) -> tuple[Command, ...]:
    """One edit per named change, then the whole-tree render that writes what the records derive."""
    return (*[Command('edit', tuple(v2.edit_argv(app, env, setting)), tool='v2') for setting in settings],
            Command('render', tuple(v2.render_argv()), tool='v2'))


def v2_scale_settings(form: Mapping[str, str]) -> tuple[str, ...]:
    """v2's scale fields: replicas and memory in MiB. CPU requests and limits are not modelled."""
    settings = []
    replicas = form.get('replicas', '').strip()
    if replicas:
        if not re.fullmatch(r'[0-9]+', replicas):
            raise ActionError(f'Replicas {replicas!r} is not a whole number')
        settings.append(f'replicas={int(replicas)}')
    memory = form.get('memory', '').strip()
    if memory:
        found = re.fullmatch(r'([0-9]+)(?:Mi)?', memory)
        if not found:
            raise ActionError(f'Memory {memory!r} is not a whole number of MiB, such as 256 or 256Mi')
        settings.append(f'memoryMiB={int(found[1])}')
    if form.get('cpu', '').strip() or form.get('memory_limit', '').strip():
        raise ActionError('v2 does not model CPU requests or memory limits: change replicas and memory only')
    if not settings:
        raise ActionError('change replicas or memory')
    return tuple(settings)


def v2_exposure_settings(form: Mapping[str, str]) -> tuple[str, ...]:
    exposure = form.get('exposure', '').strip()
    if exposure not in records.EXPOSURES:
        raise ActionError(f'exposure must be one of {", ".join(records.EXPOSURES)}')
    if form.get('host', '').strip():
        raise ActionError('v2 derives the address from the app name and the exposure: it does not model a host')
    return (f'exposure={exposure}',)


def v2_names(value: str, pattern: re.Pattern, label: str) -> tuple[str, ...]:
    names = [name.strip() for name in value.split(',') if name.strip()]
    if bad := [name for name in names if not pattern.fullmatch(name)]:
        raise ActionError(f'{", ".join(bad)}: {label}')
    return tuple(names)


def v2_connection_settings(form: Mapping[str, str]) -> tuple[str, ...]:
    """The whole calls and subscriptions lists a connect or disconnect write: v2 states a declaration whole."""
    calls = v2_names(form.get('calls', ''), records.NAME_RE, 'a call names an app')
    subscriptions = v2_names(form.get('subscriptions', ''), records.SUBSCRIPTION_RE,
                             'a subscription names <app>.<event>')
    settings = []
    if calls or 'calls' in form:
        settings.append(f'calls={",".join(calls)}')
    if subscriptions or 'subscriptions' in form:
        settings.append(f'subscriptions={",".join(subscriptions)}')
    if not settings:
        raise ActionError('choose an app to call or an event to receive')
    return tuple(settings)


def v2_allow_settings(form: Mapping[str, str]) -> tuple[str, ...]:
    allows = v2_names(form.get('allows', ''), ALLOW_RE, 'an allowed caller is an app name or team:<team>')
    return (f'allows={",".join(allows)}',)


def v2_team_settings(form: Mapping[str, str]) -> tuple[str, ...]:
    team = form.get('team', '').strip()
    if not records.NAME_RE.fullmatch(team):
        raise ActionError('choose the team that owns the app')
    return (f'team={team}',)


def v2_remove_commands(app: str, env: str, _form: Mapping[str, str]) -> tuple[Command, ...]:
    return (Command('remove', tuple(v2.remove_argv(app, env)), tool='v2'),
            Command('render', tuple(v2.render_argv()), tool='v2'))


def v2_promote_commands(app: str, _env: str, form: Mapping[str, str]) -> tuple[Command, ...]:
    if not form.get('image'):
        raise ActionError('refresh the staging app page and review the image before promotion')
    return (Command('promote', tuple(v2.promote_argv(app)), tool='v2'),
            Command('render', tuple(v2.render_argv()), tool='v2'))


V2_PLATFORM_OUTPUTS = frozenset({
    'v2-records/calls.json', 'platform/egress/allocation.json', 'platform/egress/configmap.json',
    'platform/egress/networkpolicy.json', 'platform/nats/users.json', 'platform/reloader/values.json',
    'clusters/home/kustomization.yaml', 'platform/auth/kustomization.yaml',
})


def v2_owns(app: str, env: str, path: str, *, envs: tuple[str, ...] | None = None) -> bool:
    """The files a v2 action may carry: its records and what render writes from them, never ``apps/``."""
    if path in V2_PLATFORM_OUTPUTS or path.startswith('platform/auth/'):
        return True
    if path == f'{v2.RECORDS}/{app}/app.json':
        return True
    environments = envs if envs is not None else ((env,) if env else ('staging', 'prod'))
    if any(path.startswith(f'{v2.RECORDS}/{app}/{one}/') for one in environments):
        return True
    if any(path.startswith(f'v2-apps/instances/{app}/{one}/') for one in environments):
        return True
    return any(path == f'clusters/home/app-{app}-{one}.json' for one in environments)


@dataclass(frozen=True)
class Action:
    """One kind of console pull request, shared by the console (which opens it) and Activity (which reads
    its branch): the environments its branch may name (none for a team's), the input paths it carries,
    which of their changes it keeps (removal: deletions only), its command builder, and the words for its
    pull request and job."""
    envs: tuple[str, ...]
    owns: Callable[[str, str, str], bool]  # (app or team, env, path)
    commands: Callable[[str, str, Mapping], tuple[Command, ...]]
    title: str  # formatted with name and env; never includes credentials
    job: str = ''  # when different from the action's name
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


ACTIONS: dict[str, Action] = {
    'new': Action(('staging',), v2_owns, new_commands, 'v2-records: add {name}/staging',
                  job='new app and repository'),
    **{f'team-{action}': Action((), lambda _name, _env, path: team_inputs(path),
                              lambda name, _env, form, action=action: team_commands(action, name, form),
                              f'teams: {verb} {{name}}', job=f'team {action}')
       for action, verb in (('add', 'add'), ('edit', 'change'), ('remove', 'remove'))},
    **{f'team-channel-{action}': Action((), lambda _name, _env, path: team_inputs(path),
                                      lambda name, _env, form, action=action: channel_commands(name, form, remove=action == 'remove'),
                                      f'teams: {action} the notification channel of {{name}}', job=f'channel {action}')
       for action in ('set', 'remove')},
    'v2-scale': Action(('staging', 'prod'), lambda app, env, path: v2_owns(app, env, path),
                       lambda app, env, form: v2_commands(app, env, v2_scale_settings(form)),
                       'apps: scale {name}/{env}'),
    'v2-expose': Action(('staging', 'prod'), lambda app, env, path: v2_owns(app, env, path),
                        lambda app, env, form: v2_commands(app, env, v2_exposure_settings(form)),
                        'apps: change access to {name}/{env}'),
    'v2-connect': Action(('staging', 'prod'), lambda app, env, path: v2_owns(app, env, path),
                         lambda app, env, form: v2_commands(app, env, v2_connection_settings(form)),
                         'apps: change the connections of {name}/{env}'),
    'v2-disconnect': Action(('staging', 'prod'), lambda app, env, path: v2_owns(app, env, path),
                            lambda app, env, form: v2_commands(app, env, v2_connection_settings(form)),
                            'apps: change the connections of {name}/{env}'),
    'v2-allow': Action(('staging', 'prod'), lambda app, env, path: v2_owns(app, env, path),
                       lambda app, env, form: v2_commands(app, env, v2_allow_settings(form)),
                       'apps: change who may call {name}/{env}'),
    # The team is the app's: the edit names no environment and rewrites both, whichever page it was asked from.
    'v2-team': Action(('staging', 'prod'), lambda app, env, path: v2_owns(app, env, path, envs=('staging', 'prod')),
                      lambda app, _env, form: v2_commands(app, '', v2_team_settings(form)),
                      'apps: change the team of {name}'),
    'v2-remove': Action(('staging', 'prod'), lambda app, env, path: v2_owns(app, env, path),
                        v2_remove_commands, 'apps: remove {name}/{env}'),
    'v2-promote': Action(('staging',), lambda app, env, path: v2_owns(app, env, path, envs=('staging', 'prod')),
                         v2_promote_commands, 'apps: promote {name}/staging to {name}/prod',
                         target_env='prod'),
}
"""Every console pull request: branch/target, command builder, input ownership and PR wording. A person
merges every one; the console reads it back by branch with :func:`parse`."""
V2_KINDS = tuple(name for name in ACTIONS if name.startswith('v2-') and name != 'v2-promote')
V2_ACTION = {name.removeprefix('v2-'): name for name in (*V2_KINDS, 'v2-promote')}
