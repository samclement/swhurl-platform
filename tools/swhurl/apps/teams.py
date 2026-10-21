"""Manage the team registry (apps/teams.yaml) without leaving an app pointing at a team that is gone.

    team-add NAME [--description TEXT]
    team-edit NAME [--description TEXT] [--rename NEW] [--notify CHANNEL]
    team-remove NAME [--move-apps-to OTHER]
    team-channel NAME [--remove]                         give a team its channel, replace its address or remove it
    channel-add NAME [--transport ntfy] [--replace]      the address is read from standard input, never an argument
    channel-remove NAME

A team is a name with a description: the platform.swhurl.com/team label on an app's namespaces
(docs/apps.md#teams-and-isolation). The registry and the apps that name a team must agree, or
make check-apps fails (rule team), so each command changes both in one Git edit:

  - renaming a team relabels every app that belongs to it;
  - removing a team is refused while apps belong to it, unless they are moved to another team
    in the same change;

Telemetry already stored keeps the name it was written with (swhurl.team); only new data follows.

A team's ``notify`` names the one channel that gets its apps' lifecycle messages
(docs/services.md#notification-channels). A channel is one encrypted Secret, written once: these
commands encrypt and never decrypt, and no output repeats an address.
"""
from __future__ import annotations

import argparse
import getpass
import re
import sys
from dataclasses import replace
from pathlib import Path

from ruamel.yaml.comments import CommentedMap

from swhurl import ROOT
from swhurl.apps import channels, cli
from swhurl.apps.contract import TEAMS_FILE
from swhurl.apps.errors import EditError
from swhurl.apps.model import write
from swhurl.apps.model.draft import Entry
from swhurl.apps.model.records import TEAM_GRANT
from swhurl.apps.new import dump
from swhurl.apps.yaml_file import YamlFile, mapping
from swhurl.run import CommandError, Runner

NAME_RE = re.compile(r'^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$')
MAX_DESCRIPTION = 200


def registry(root: Path) -> tuple[YamlFile, dict]:
    document = YamlFile(root / TEAMS_FILE)
    return document, mapping(document.data, 'teams', create=True)


def checked_name(name: str) -> str:
    if not NAME_RE.match(name or ''):
        raise EditError('a team name is a DNS label: lowercase letters, digits and hyphens, at most 40 characters')
    return name


def checked_description(text: str) -> str:
    text = ' '.join((text or '').split())
    if len(text) > MAX_DESCRIPTION:
        raise EditError(f'a description is one line of at most {MAX_DESCRIPTION} characters')
    return text


def entry(description: str) -> CommentedMap:
    found = CommentedMap()
    if description:
        found['description'] = description
    return found


def prepare(root: Path, document: YamlFile | None = None, operations: dict | None = None,
            known: dict[str, str] | None = None) -> write.Prepared:
    """Plan against the proposed team/channel registry, not yesterday's routes on disk."""
    document = document or registry(root)[0]
    wanted = {'channels': channels.channels(root) if known is None else known,
              'teams': {team: list(value['notify']) for team, value in document.data['teams'].items()
                        if (value or {}).get('notify')}}
    planned = write.prepare(root, operations or {}, routes=wanted if (root / channels.NOTIFY_DIR).is_dir() else None)
    if document.path.read_text() != document.render():
        planned.writes[document.path] = document.render()
    return planned


def checked_channels(root: Path, names: list[str]) -> list[str]:
    known = channels.channels(root)
    unknown = [name for name in names if name not in known]
    if unknown:
        raise EditError(f'no channel {", ".join(unknown)}; channels: {", ".join(known) or "none"} (make channel-add)')
    return list(dict.fromkeys(names))


def add(root: Path, name: str, description: str = '') -> None:
    document, teams = registry(root)
    if checked_name(name) in teams:
        raise EditError(f'team {name} already exists')
    teams[name] = entry(checked_description(description))
    write.apply(root, prepare(root, document))
    print(f'[OK] added team {name}')


def change(root: Path, name: str, description: str | None = None, rename: str | None = None,
           notify: list[str] | None = None) -> None:
    """Change a team's description, name or channels. A rename relabels its apps and rewrites every
    team:<old> grant in the same edit."""
    document, teams = registry(root)
    if name not in teams:
        raise EditError(f'no team {name}; registered: {", ".join(teams)}')
    if description is None and not rename and notify is None:
        raise EditError('give --description, --rename and/or --notify')
    if notify is not None:
        notify = checked_channels(root, notify)
        if len(notify) > 1:
            raise EditError(f'a team notifies one channel, not {len(notify)}: choose one of {", ".join(notify)}')
        current = teams[name] if isinstance(teams[name], dict) else entry('')
        if notify:
            current['notify'] = notify
        else:
            current.pop('notify', None)
        teams[name] = current
    if description is not None:
        description = checked_description(description)
        current = teams[name] if isinstance(teams[name], dict) else entry('')
        if description:
            current['description'] = description
        else:
            current.pop('description', None)
        teams[name] = current
    apps: list[str] = []
    operations = {}
    if rename and rename != name:
        if checked_name(rename) in teams:
            raise EditError(f'team {rename} already exists; to merge the two, remove {name} with --move-apps-to {rename}')
        position = list(teams).index(name)
        teams.insert(position, rename, teams.pop(name))
        world, _ = write.read(root)
        before, after = f'{TEAM_GRANT}{name}', f'{TEAM_GRANT}{rename}'

        def update(value: Entry) -> Entry:
            allows = tuple(dict.fromkeys(after if grant == before else grant for grant in value.settings.grants.allows))
            return replace(value, app=replace(value.app, team=rename) if value.app.team == name else value.app,
                           settings=replace(value.settings, grants=replace(value.settings.grants, allows=allows)))
        operations = {each: write.Update(update) for each, value in world.items()
                      if value.app.team == name or before in value.settings.grants.allows}
        apps = sorted({each.app for each in operations if world[each].app.team == name})
    write.apply(root, prepare(root, document, operations))
    print(f'[OK] team {name}' + (f' renamed to {rename}; relabelled {len(apps)} app(s)' if rename and rename != name else ' updated'))


def remove(root: Path, name: str, move_apps_to: str | None = None) -> None:
    """Remove a team. Its apps must go somewhere first: refuse, or move them in the same edit."""
    document, teams = registry(root)
    if name not in teams:
        raise EditError(f'no team {name}; registered: {", ".join(teams)}')
    world, _ = write.read(root)
    apps = sorted({each.app for each, value in world.items() if value.app.team == name})
    if apps and not move_apps_to:
        raise EditError(f'team {name} still owns {", ".join(apps)}: move them with --move-apps-to <team>, '
                        'or each with make app-team, then remove it')
    if move_apps_to is not None and (move_apps_to == name or move_apps_to not in teams):
        raise EditError(f'--move-apps-to must name another registered team: {", ".join(t for t in teams if t != name)}')
    granted = sorted(str(each) for each, value in world.items() if f'{TEAM_GRANT}{name}' in value.settings.grants.allows)
    if granted:
        raise EditError(f'team:{name} is allowed to call {", ".join(granted)}: remove those grants first '
                        f'(make app-allows APP=<app> ENV=<env> ARGS="--remove team:{name}")')
    operations = {each: write.Update(lambda value: replace(value, app=replace(value.app, team=move_apps_to)))
                  for each, value in world.items() if value.app.team == name}
    del teams[name]
    write.apply(root, prepare(root, document, operations))
    print(f'[OK] removed team {name}' + (f'; moved {", ".join(apps)} to {move_apps_to}' if apps else ''))


def listed_channels(root: Path) -> tuple[YamlFile, list]:
    document = YamlFile(root / channels.NOTIFY_DIR / 'kustomization.yaml')
    resources = document.data.get('resources')
    if not isinstance(resources, list):
        raise EditError(f'{document.path}: expected a resources list')
    return document, resources


def set_decryption(root: Path, prepared: write.Prepared, encrypted: bool) -> None:
    """The notify unit decrypts exactly while it holds an encrypted file (make check-repo requires the two to agree)."""
    path = root / channels.UNIT_FILE
    if not path.is_file():
        return
    unit = YamlFile(path)
    spec = mapping(unit.data, 'spec')
    if encrypted and 'decryption' not in spec:
        spec.insert(list(spec).index('path') + 1, 'decryption', {'provider': 'sops', 'secretRef': {'name': 'sops-age'}})
    elif not encrypted and 'decryption' in spec:
        del spec['decryption']
    else:
        return
    prepared.writes[path] = unit.render()


def channel_add(root: Path, name: str, address: str, transport: str = 'ntfy', replace: bool = False,
                runner: Runner | None = None, *, registry_document: YamlFile | None = None) -> None:
    """Write a channel's encrypted Secret. Encrypt only: an existing channel is replaced whole, never read."""
    if not channels.NAME_RE.match(name or ''):
        raise EditError('a channel name is a DNS label: lowercase letters, digits and hyphens, at most 40 characters')
    problem = channels.address_problem(transport, address)
    if problem:
        raise EditError(problem)
    path = root / channels.channel_file(name)
    if path.exists() and not replace:
        raise EditError(f'channel {name} already exists; --replace gives it a new address')
    if replace and not path.exists():
        raise EditError(f'no channel {name} to replace; channels: {", ".join(channels.channels(root)) or "none"}')
    document, resources = listed_channels(root)
    before = path.read_text() if path.exists() else None
    known = channels.channels(root) | {name: transport}
    prepared = prepare(root, registry_document, known=known)
    if path.name not in resources:
        resources.append(path.name)
        prepared.writes[document.path] = document.render()
    set_decryption(root, prepared, True)
    # Encrypt from stdin: neither the address nor an incomplete multi-file operation reaches disk.
    text = dump([{'apiVersion': 'v1', 'kind': 'Secret',
                  'metadata': {'name': f'{channels.SECRET_PREFIX}{name}', 'namespace': channels.NAMESPACE,
                               'labels': {channels.TRANSPORT: transport}},
                  'type': 'Opaque', 'stringData': {channels.ADDRESS_KEY: address}}])
    try:
        encrypted = (runner or Runner()).run(
            ['sops', '--encrypt', '--filename-override', str(path.relative_to(root)), '--input-type', 'yaml',
             '--output-type', 'yaml', '/dev/stdin'], input=text, cwd=root).stdout
    except CommandError:
        raise EditError(f'could not SOPS-encrypt {path.name}; nothing was changed') from None
    if 'sops:' not in encrypted or 'ENC[' not in encrypted:
        raise EditError(f'{path.name} was not encrypted; nothing was changed')
    prepared.writes[path] = encrypted
    write.apply(root, prepared)
    print(f'[OK] channel {name} ({transport}) {"replaced" if before is not None else "added"}; its address is not shown again')


def channel_remove(root: Path, name: str, *, registry_document: YamlFile | None = None) -> None:
    path = root / channels.channel_file(name)
    if not path.exists():
        raise EditError(f'no channel {name}; channels: {", ".join(channels.channels(root)) or "none"}')
    registry_document = registry_document or registry(root)[0]
    users = [team for team, value in registry_document.data['teams'].items() if name in (value or {}).get('notify', [])]
    if users:
        raise EditError(f'channel {name} is notified by {", ".join(users)}: change those teams first '
                        '(make team-edit NAME=<team> ARGS="--notify <another channel>")')
    document, resources = listed_channels(root)
    known = channels.channels(root)
    del known[name]
    prepared = prepare(root, registry_document, known=known)
    if path.name in resources:
        resources.remove(path.name)
        prepared.writes[document.path] = document.render()
    prepared.deletions.append(path)
    set_decryption(root, prepared, any(file != path for file in (root / channels.NOTIFY_DIR).glob('*.sops.yaml')))
    write.apply(root, prepared)
    print(f'[OK] removed channel {name}')


def team_channel(root: Path, team: str, address: str | None, transport: str = 'ntfy',
                 runner: Runner | None = None) -> None:
    """Give ``team`` its one channel, or a new address for the one it has; ``address`` None removes it.

    The everyday command: the channel is named after the team, so nobody has to name or pick one. A
    channel another team also notifies stays when this team lets go of it."""
    document, registered = registry(root)
    if team not in registered:
        raise EditError(f'no team {team}; registered: {", ".join(registered)}')
    current = ((registered[team] or {}).get('notify') or [None])[0]
    if address is None:
        if not current:
            raise EditError(f'team {team} has no channel to remove')
        registered[team].pop('notify')
        if any(current in (value or {}).get('notify', []) for value in registered.values()):
            write.apply(root, prepare(root, document))
        else:
            channel_remove(root, current, registry_document=document)
    else:
        chosen = current or team
        if not current:
            registered[team] = registered[team] or entry('')
            registered[team]['notify'] = [chosen]
        channel_add(root, chosen, address, transport, replace=(root / channels.channel_file(chosen)).exists(),
                    runner=runner, registry_document=document)


def read_address() -> str:
    """One line from standard input; a terminal is prompted without echo."""
    return (getpass.getpass('Channel address (not shown): ') if sys.stdin.isatty() else sys.stdin.readline()).strip()


def main_add(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl team-add', description='Register a team in apps/teams.yaml')
    p.add_argument('name')
    p.add_argument('--description', default='')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return cli.run(lambda: add(args.root.resolve(), args.name, args.description), False)


def main_edit(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl team-edit', description='Change a team\'s description or name (a rename relabels its apps)')
    p.add_argument('name')
    p.add_argument('--description')
    p.add_argument('--rename', metavar='NEW')
    p.add_argument('--notify', metavar='CHANNEL', type=lambda s: [c.strip() for c in s.split(',') if c.strip()],
                   help='the channel that gets its apps\' lifecycle messages; an empty value clears it')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return cli.run(lambda: change(args.root.resolve(), args.name, args.description, args.rename, args.notify), False)


def main_team_channel(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl team-channel',
                                description='Give a team its notification channel (address from standard input), or remove it')
    p.add_argument('name')
    p.add_argument('--remove', action='store_true', help='remove the team\'s channel instead')
    p.add_argument('--transport', default='ntfy', choices=channels.TRANSPORTS)
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return cli.run(lambda: team_channel(args.root.resolve(), args.name, None if args.remove else read_address(),
                                         args.transport, runner), False)


def main_channel_add(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl channel-add',
                                description='Add a notification channel; its address is read from standard input')
    p.add_argument('name')
    p.add_argument('--transport', default='ntfy', choices=channels.TRANSPORTS)
    p.add_argument('--replace', action='store_true', help='give an existing channel a new address')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return cli.run(lambda: channel_add(args.root.resolve(), args.name, read_address(), args.transport,
                                        args.replace, runner), False)


def main_channel_remove(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl channel-remove', description='Remove a channel no team notifies')
    p.add_argument('name')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return cli.run(lambda: channel_remove(args.root.resolve(), args.name), False)


def main_remove(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl team-remove', description='Remove a team that owns no app, or move its apps first')
    p.add_argument('name')
    p.add_argument('--move-apps-to', metavar='TEAM')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return cli.run(lambda: remove(args.root.resolve(), args.name, args.move_apps_to), False)
