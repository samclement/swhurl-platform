"""Manage the team registry (apps/teams.yaml) without leaving an app pointing at a team that is gone.

    team-add NAME [--description TEXT]
    team-edit NAME [--description TEXT] [--rename NEW]
    team-remove NAME [--move-apps-to OTHER]

A team is a name with a description: the platform.swhurl.com/team label on an app's namespaces
(docs/apps.md#teams-and-isolation). The registry and the apps that name a team must agree, or
make check-apps fails (rule team), so each command changes both in one Git edit:

  - renaming a team relabels every app that belongs to it;
  - removing a team is refused while apps belong to it, unless they are moved to another team
    in the same change;
  - the default team cannot be removed or renamed: new apps without a team go to it.

Telemetry already stored keeps the name it was written with (swhurl.team); only new data follows.
"""
from __future__ import annotations

import argparse
import re
from pathlib import Path

from ruamel.yaml.comments import CommentedMap

from swhurl import ROOT
from swhurl.apps import derive, edit
from swhurl.apps.contract import DEFAULT_TEAM, ENVIRONMENTS, TEAM, TEAMS_FILE
from swhurl.apps.yaml_file import EditError, YamlFile, mapping

NAME_RE = re.compile(r'^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$')
MAX_DESCRIPTION = 200


def registry(root: Path) -> tuple[YamlFile, dict]:
    document = YamlFile(root / TEAMS_FILE)
    return document, mapping(document.data, 'teams', create=True)


def apps_of(root: Path) -> dict[str, list[str]]:
    """Every team an app names in Git, with its apps: registered or not."""
    found: dict[str, set[str]] = {}
    for env in ENVIRONMENTS:
        for app in derive.instances_in(root, env):
            labels = mapping(mapping(YamlFile(root / 'apps' / app / env / 'namespace.yaml').data, 'metadata'), 'labels')
            found.setdefault(str(labels.get(TEAM, '')), set()).add(app)
    return {team: sorted(apps) for team, apps in found.items()}


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


def relabel(root: Path, apps: list[str], team: str) -> None:
    for app in apps:
        edit.set_team(root, app, team)


def add(root: Path, name: str, description: str = '') -> None:
    document, teams = registry(root)
    if checked_name(name) in teams:
        raise EditError(f'team {name} already exists')
    teams[name] = entry(checked_description(description))
    document.save()
    print(f'[OK] added team {name}')


def change(root: Path, name: str, description: str | None = None, rename: str | None = None) -> None:
    """Change a team's description, its name, or both. A rename relabels its apps in the same edit."""
    document, teams = registry(root)
    if name not in teams:
        raise EditError(f'no team {name}; registered: {", ".join(teams)}')
    if description is None and not rename:
        raise EditError('give --description and/or --rename')
    if description is not None:
        description = checked_description(description)
        current = teams[name] if isinstance(teams[name], dict) else entry('')
        if description:
            current['description'] = description
        else:
            current.pop('description', None)
        teams[name] = current
    apps: list[str] = []
    if rename and rename != name:
        if name == DEFAULT_TEAM:
            raise EditError(f'{name} is the default team for new apps and cannot be renamed')
        if checked_name(rename) in teams:
            raise EditError(f'team {rename} already exists; to merge the two, remove {name} with --move-apps-to {rename}')
        position = list(teams).index(name)
        teams.insert(position, rename, teams.pop(name))
        apps = apps_of(root).get(name, [])
    document.save()
    relabel(root, apps, rename or name)
    print(f'[OK] team {name}' + (f' renamed to {rename}; relabelled {len(apps)} app(s)' if rename and rename != name else ' updated'))


def remove(root: Path, name: str, move_apps_to: str | None = None) -> None:
    """Remove a team. Its apps must go somewhere first: refuse, or move them in the same edit."""
    document, teams = registry(root)
    if name not in teams:
        raise EditError(f'no team {name}; registered: {", ".join(teams)}')
    if name == DEFAULT_TEAM:
        raise EditError(f'{name} is the default team for new apps and cannot be removed')
    apps = apps_of(root).get(name, [])
    if apps and not move_apps_to:
        raise EditError(f'team {name} still owns {", ".join(apps)}: move them with --move-apps-to <team>, '
                        'or each with make app-team, then remove it')
    if move_apps_to is not None and (move_apps_to == name or move_apps_to not in teams):
        raise EditError(f'--move-apps-to must name another registered team: {", ".join(t for t in teams if t != name)}')
    relabel(root, apps, move_apps_to or '')  # while the old team is still registered: nothing is ever unowned
    del teams[name]
    document.save()
    print(f'[OK] removed team {name}' + (f'; moved {", ".join(apps)} to {move_apps_to}' if apps else ''))


def main_add(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl team-add', description='Register a team in apps/teams.yaml')
    p.add_argument('name')
    p.add_argument('--description', default='')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return edit.run(lambda: add(args.root.resolve(), args.name, args.description), False)


def main_edit(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl team-edit', description='Change a team\'s description or name (a rename relabels its apps)')
    p.add_argument('name')
    p.add_argument('--description')
    p.add_argument('--rename', metavar='NEW')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return edit.run(lambda: change(args.root.resolve(), args.name, args.description, args.rename), False)


def main_remove(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl team-remove', description='Remove a team that owns no app, or move its apps first')
    p.add_argument('name')
    p.add_argument('--move-apps-to', metavar='TEAM')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return edit.run(lambda: remove(args.root.resolve(), args.name, args.move_apps_to), False)
