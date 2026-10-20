"""Shared staging → production decisions. Git decides existence; live status decides readiness."""
from __future__ import annotations

import dataclasses
import re
from collections.abc import Callable, Iterable
from dataclasses import dataclass
from pathlib import Path

from swhurl.apps import contract, ops, state
from swhurl.apps.instance import NAME_RE, Instance, instance_folder, instance_unit_file
from swhurl.apps.model.draft import APP_SECRET, Entry, World
from swhurl.apps.model.records import RELEASE_FILE, Allocations, RecordError, Release, Settings, Storage
from swhurl.apps.yaml_file import EditError, mapping
from swhurl.apps.yaml_file import read as read_yaml
from swhurl.yamlio import load


@dataclass(frozen=True)
class Decision:
    outcome: str  # create, update, same
    image: str
    previous: str = ''
    rollback: bool = False


def image_of(release: dict) -> dict:
    values = mapping(mapping(release, 'spec'), 'values')
    controller = mapping(mapping(values, 'controllers'), 'main')
    return mapping(mapping(mapping(controller, 'containers'), 'main'), 'image')


def decide(source: dict, target: dict | None) -> Decision:
    image = image_of(source)
    if not image.get('digest'):
        raise EditError('staging has no image digest; promote only a digest-pinned image')
    reference = ops.image_reference(image)
    if target is None:
        return Decision('create', reference)
    previous = image_of(target)
    if previous.get('repository') != image.get('repository'):
        raise EditError('production uses a different image repository; change the repository by hand')
    if previous.get('digest') == image['digest']:
        return Decision('same', reference, ops.image_reference(previous))
    return Decision('update', reference, ops.image_reference(previous), older(image.get('tag', ''), previous.get('tag', '')))


def older(tag: str, than: str) -> bool:
    """Whether image ``tag`` was built before ``than``: both ``<run>-<sha>`` tags, with an earlier run."""
    runs = [re.fullmatch(contract.AUTO_DEPLOY_TAG_PATTERN, each) for each in (tag, than)]
    return bool(all(runs) and int(runs[0]['run']) < int(runs[1]['run']))


def read(root: Path, app: str) -> tuple[dict, dict | None]:
    if not NAME_RE.fullmatch(app):
        raise EditError('APP must be a DNS label')
    base = root / 'apps' / app
    source = read_yaml(base / 'staging/helmrelease.yaml')
    target_dir = base / 'prod'
    unit = instance_unit_file(root, app, 'prod')
    if target_dir.exists() or unit.exists():
        # A partial target is a problem to repair, never evidence that production is missing.
        target = read_yaml(target_dir / 'helmrelease.yaml')
    else:
        target = None
    return source, target


def records(app: str, path: str) -> bool:
    """Whether ``path`` is one of the records a promotion of ``app`` writes: its App record, and production's
    Release, Settings, encrypted files and offers documents. Everything else is rendered from them on main."""
    prod = f'apps/{app}/prod/'
    return path == f'apps/{app}/app.yaml' or (path.startswith(prod) and (
        path.removeprefix(prod) in (RELEASE_FILE, 'instance.yaml') or path.startswith(f'{prod}offers/')
        or ('/' not in path.removeprefix(prod) and path.endswith('.sops.yaml'))))


def automatic(app: str, path: str) -> bool:
    """The records a promotion may change and still merge itself: production's Release and offers documents."""
    return path == f'apps/{app}/prod/{RELEASE_FILE}' or path.startswith(f'apps/{app}/prod/offers/')


def release_of(root: Path, app: str, env: str) -> Release | None:
    """An instance's Release record in the tree at ``root``; None when it has none."""
    path = instance_folder(root, app, env) / RELEASE_FILE
    if not path.is_file():
        return None
    try:
        return Release.parse(load(path.read_text()), f'{app}/{env}: {RELEASE_FILE}')
    except (RecordError, ValueError) as error:
        raise EditError(str(error)) from None


REVIEWED = (('publishes', 'publishes', 'what it publishes'), ('port', 'port', 'its port'),
            ('database', 'database', 'its database'), ('secrets', 'secrets', 'its secret names'))
"""The Release fields production must be ready for: a promotion that changes one is reviewed."""


def held(app: str, changed: Iterable[str], before: Release | None, after: Release | None,
         staged: Callable[[Release], bool]) -> str:
    """Promotion's one rule: why a person merges this promotion of ``app``, or '' when it merges itself.
    ``changed``: the paths it changes; ``before`` and ``after``: production's Release on main and with
    it merged; ``staged(release)``: whether that exact Release was selected in staging on main.
    The words follow "this pull request" (console) and "Auto-merge refused:" (the gate)."""
    changed = list(changed)
    if before is None:
        return 'creates production: a person reviews its address, credentials and settings'
    if after is None:
        return "removes production's release record"
    if other := sorted(path for path in changed if not automatic(app, path)):
        return f"changes {', '.join(other[:5])}, which a promotion of the image alone does not"
    if after.image.repository != before.image.repository:
        return 'changes the image repository'
    if older(after.image.tag, before.image.tag):
        return 'promotes an older image than production runs (a rollback)'
    if moved := [words for _, field, words in REVIEWED if getattr(after, field) != getattr(before, field)]:
        return f"changes {', '.join(moved)} in production (set any new secret value in production first)"
    if not (after.image.digest and staged(after)):
        return f'promotes a release that does not match staging\'s history ({after.image.digest or "no digest"})'
    return ''


def sha(revision: str) -> str:
    """The commit of a Flux revision (``main@sha1:<commit>``)."""
    return revision.rsplit(':', 1)[-1]


def touches(instance: Instance, paths: Iterable[str]) -> bool:
    """Whether any of ``paths`` is one of the instance's own files: its folder or its unit file."""
    root = Path()
    folder, unit = instance.folder(root).as_posix() + '/', instance.unit_file(root).as_posix()
    return any(p == unit or p.startswith(folder) for p in paths)


def settled(status: ops.InstanceStatus | None,
            changed: Callable[[str, str], Iterable[str] | None]) -> ops.InstanceStatus | None:
    """``status`` with ``own_change`` answered, when its unit is behind Git's newest revision: any commit to main
    puts every unit behind for a few seconds, and only one that changes the instance's own files (its folder,
    its unit file) is a change it waits for. ``changed(applied, newest)`` gives the paths that differ between
    the two commits, or None when it cannot tell, which leaves the question open (and the instance not current)."""
    if status is None or status.applied or not (status.desired_revision and status.applied_revision):
        return status
    paths = changed(sha(status.applied_revision), sha(status.desired_revision))
    if paths is None:
        return status
    return dataclasses.replace(status, own_change=touches(status.instance, paths))


def source_problem(status: ops.InstanceStatus | None, image: str) -> str:
    """Why staging cannot be promoted now, or ''. The image pins what was reviewed. ``status`` should be
    ``settled``, or any commit to main refuses."""
    if status is None:
        return 'staging no longer exists; refresh the app page'
    if status.desired_image != image:
        return 'staging changed since this page was loaded; refresh and review it again'
    if status.desired_revision and status.applied_revision and not status.current:
        if status.own_change:
            return ('a change to staging is in Git and Flux has not applied it yet; wait a few seconds, refresh '
                    'and review it again')
        # Whether the unapplied commits change this app could not be read: any commit to main is "newer" until applied here.
        return ('Flux is still applying the latest commit on main to staging (a change landed moments ago, perhaps '
                "another app's); wait a few seconds, refresh and promote again")
    return '' if state.runs(status) else 'staging must be healthy and running the reviewed digest before promotion'


@dataclass(frozen=True)
class FirstProduction:
    """What a first promotion creates production with, before its own key and placeholders exist."""
    entry: Entry
    secret_keys: list[str]  # each gets a REPLACE_ME placeholder: production never runs without a value
    host: str  # its address, '' for a private app (or a public one still without --host, in a preview)


def first_production(root: Path, world: World, app: str, *, host: str | None = None,
                     preview: bool = False) -> FirstProduction:
    """Production from staging's records: the same release (the image and what it needs), staging's
    team, alerts, exposure, issuer, calls, allows (a proposal, reviewed in the held PR), capacity, replicas, schedule and reviewed exceptions; its own
    address, an empty database of the same size, and no subscriptions, egress grants or credentials of
    staging's (docs/apps.md#promote-to-production). ``preview``: a public app may have no --host yet."""
    staging = world[Instance(app, 'staging')]
    if not staging.app.team:
        raise EditError('staging names no team (its namespace label); set it with make app-team before promoting')
    missing = [callee for callee in staging.settings.calls if Instance(callee, 'prod') not in world]
    if missing:
        raise EditError(f'{app} calls {", ".join(missing)}, which has no production instance: promote that first')
    exposure = staging.settings.exposure
    if host and exposure != 'public':
        raise EditError('--host applies only to first promotion of a public app')
    if exposure == 'public':
        staged = staging.settings.host
        if host and contract.in_cookie_domain(host):
            raise EditError(f'public hosts must be outside {contract.default_host("x", "prod")[2:]}: '
                            'the sign-in cookie would reach them')
        if not preview and (not host or host == staged):
            raise EditError('first public promotion needs a distinct production --host')
    instance = instance_folder(root, app, 'staging')
    keys = []
    if staging.release.secrets or APP_SECRET in staging.encrypted:
        # Every name the image needs and every value staging has: never the values themselves.
        stored = contract.stored_secrets((instance / contract.SECRET_FILE).read_text()) if APP_SECRET in staging.encrypted else []
        keys = list(dict.fromkeys([*staging.release.secrets, *stored]))
        if not keys or contract.secret_key_problem(keys):
            raise EditError('staging app Secret needs supported environment variable keys')
    database = staging.settings.allocations.database
    settings = Settings(exposure=exposure, host=(host or '') if exposure == 'public' else '', issuer=staging.settings.issuer,
                        calls=staging.settings.calls, allows=staging.settings.allows, replicas=staging.settings.replicas, capacity=staging.settings.capacity,
                        schedule=staging.settings.schedule, exceptions=staging.settings.exceptions,
                        allocations=Allocations(database=Storage(database.size, contract.RETAINED_STORAGE_CLASS) if database else None))
    if exposure == 'public':
        address = host or ''
    else:
        address = contract.default_host(app, 'prod') if exposure != 'private' else ''
    return FirstProduction(Entry(staging.app, staging.release, settings, frozenset({APP_SECRET}) if keys else frozenset()),
                           keys, address)
