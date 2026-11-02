"""Manage the team registry (apps/teams.yaml) without leaving an app pointing at a team that is gone.

    team-add NAME [--description TEXT]
    team-edit NAME [--description TEXT] [--rename NEW] [--notify CHANNEL]
    team-remove NAME [--move-apps-to OTHER]
    team-channel NAME [--remove]                         give a team its channel, replace its address or remove it
    channel-add NAME [--transport ntfy] [--replace]      the address is read from standard input, never an argument
    channel-remove NAME

A team is a name with a description: the platform.swhurl.com/team label on an app's namespaces
(docs/apps.md#teams-and-isolation). The registry and the apps that name a team must agree, or
make check-apps fails (rule team). v2 writes those records, so these commands cannot move an app or
rewrite a grant, and they refuse while one names the team:

  - renaming a team is refused while a v2 record names it or allows team:<name>;
  - removing a team is refused while one does;

and the refusal names the v2 edit that does it (swhurl-v2 edit --set team=… / --set allows=…).

Telemetry already stored keeps the name it was written with (swhurl.team); only new data follows.

A team's ``notify`` names the one channel that gets its apps' lifecycle messages
(docs/services.md#notification-channels). A channel is one encrypted Secret, written once: these
commands encrypt and never decrypt, and no output repeats an address.
"""
from __future__ import annotations

import argparse
import getpass
import json
import sys
from pathlib import Path

from ruamel.yaml.comments import CommentedMap

from swhurl import ROOT, yamlio
from swhurl.apps import channels
from swhurl.apps.contract import TEAMS_FILE
from swhurl.apps.errors import EditError
from swhurl.apps.model import records, write
from swhurl.apps.model.records import TEAM_GRANT
from swhurl.apps.yaml_file import YamlFile, mapping
from swhurl.run import CommandError, Runner

MAX_DESCRIPTION = 200
RECORDS = Path('v2-records')  # v2's records: where an app's team and every allows grant live


def registry(root: Path) -> tuple[YamlFile, dict]:
    document = YamlFile(root / TEAMS_FILE)
    return document, mapping(document.data, 'teams', create=True)


def checked_name(name: str) -> str:
    if not records.NAME_RE.match(name or ''):
        raise EditError('a team name is a DNS label: lowercase letters, digits and hyphens, at most 40 characters')
    return name


def named_by_records(root: Path, team: str) -> tuple[list[str], list[str]]:
    """The apps whose own record names ``team``, and the instances whose ``allows`` grants it.

    Read from v2's records, the only place a team is stated: v1 rendered files no longer exist, so reading
    them found nothing and both refusals below could not fire (docs/v2-status.md#remaining-work).
    """
    named, granted = [], []
    for path in sorted((root / RECORDS).glob('*/app.json')):
        if _record(path).get('team') == team:
            named.append(path.parent.name)
    for path in sorted((root / RECORDS).glob('*/*/instance.json')):
        if f'{TEAM_GRANT}{team}' in (_record(path).get('allows') or []):
            granted.append(f'{path.parent.parent.name}/{path.parent.name}')
    return named, granted


def _record(path: Path) -> dict:
    try:
        found = json.loads(path.read_text())
    except (OSError, ValueError) as error:
        raise EditError(f'{path} cannot be read ({error}), so the team commands cannot tell what names a team') from None
    if not isinstance(found, dict):
        raise EditError(f'{path} is not a record: expected a JSON object')
    return found


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
    """Change a team's description, name or channels. A rename is refused while a v2 app names the team
    or a v2 record allows it: moving them is swhurl-v2 edit's, which writes the records."""
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
    if rename and rename != name:
        if checked_name(rename) in teams:
            raise EditError(f'team {rename} already exists; a rename cannot merge two teams: move the apps '
                            f'with swhurl-v2 edit --set team={rename}, then remove {name}')
        named, granted = named_by_records(root, name)
        if named:
            raise EditError(f'team {name} is named by {", ".join(named)} in {RECORDS} and a rename moves nothing: '
                            f'move them first with swhurl-v2 edit --set team={rename}')
        if granted:
            raise EditError(f'{TEAM_GRANT}{name} is allowed to call {", ".join(granted)}, so a rename would leave '
                            f'that grant naming a team that is gone: remove it first (swhurl-v2 edit --set allows=…)')
        position = list(teams).index(name)
        teams.insert(position, rename, teams.pop(name))
    write.apply(root, prepare(root, document))
    print(f'[OK] team {name}' + (f' renamed to {rename}' if rename and rename != name else ' updated'))


def remove(root: Path, name: str) -> None:
    """Remove a team nothing names. Its apps and grants must go first, and v2's edit is what moves them."""
    document, teams = registry(root)
    if name not in teams:
        raise EditError(f'no team {name}; registered: {", ".join(teams)}')
    named, granted = named_by_records(root, name)
    if named:
        raise EditError(f'team {name} is named by {", ".join(named)} in {RECORDS}: move them first with '
                        'swhurl-v2 edit --set team=<another team>, then remove it')
    if granted:
        raise EditError(f'{TEAM_GRANT}{name} is allowed to call {", ".join(granted)}: remove those grants first '
                        '(swhurl-v2 edit --set allows=… on each), then remove it')
    del teams[name]
    write.apply(root, prepare(root, document))
    print(f'[OK] removed team {name}')


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
    if not records.NAME_RE.match(name or ''):
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
    text = yamlio.dump_all([{'apiVersion': 'v1', 'kind': 'Secret',
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


def run(action, check_dir: bool = False) -> int:
    """Run one team edit; a refused edit is one line and exit 2."""
    try:
        action()
    except EditError as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 2
    print('[INFO] Next: make check-apps, then commit, push and make flux-reconcile')
    return 0


def read_address() -> str:
    """One line from standard input; a terminal is prompted without echo."""
    return (getpass.getpass('Channel address (not shown): ') if sys.stdin.isatty() else sys.stdin.readline()).strip()


def main_add(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl team-add', description='Register a team in apps/teams.yaml')
    p.add_argument('name')
    p.add_argument('--description', default='')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: add(args.root.resolve(), args.name, args.description), False)


def main_edit(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl team-edit', description='Change a team\'s description, name or channel (a rename is refused while an app names it)')
    p.add_argument('name')
    p.add_argument('--description')
    p.add_argument('--rename', metavar='NEW')
    p.add_argument('--notify', metavar='CHANNEL', type=lambda s: [c.strip() for c in s.split(',') if c.strip()],
                   help='the channel that gets its apps\' lifecycle messages; an empty value clears it')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: change(args.root.resolve(), args.name, args.description, args.rename, args.notify), False)


def main_team_channel(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl team-channel',
                                description='Give a team its notification channel (address from standard input), or remove it')
    p.add_argument('name')
    p.add_argument('--remove', action='store_true', help='remove the team\'s channel instead')
    p.add_argument('--transport', default='ntfy', choices=channels.TRANSPORTS)
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: team_channel(args.root.resolve(), args.name, None if args.remove else read_address(),
                                         args.transport, runner), False)


def main_channel_add(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl channel-add',
                                description='Add a notification channel; its address is read from standard input')
    p.add_argument('name')
    p.add_argument('--transport', default='ntfy', choices=channels.TRANSPORTS)
    p.add_argument('--replace', action='store_true', help='give an existing channel a new address')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: channel_add(args.root.resolve(), args.name, read_address(), args.transport,
                                        args.replace, runner), False)


def main_channel_remove(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl channel-remove', description='Remove a channel no team notifies')
    p.add_argument('name')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: channel_remove(args.root.resolve(), args.name), False)


def main_remove(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl team-remove', description='Remove a team nothing names')
    p.add_argument('name')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: remove(args.root.resolve(), args.name), False)
