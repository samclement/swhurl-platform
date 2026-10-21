"""Edit generated app instances (Git edits only, like app-new): promote, scale, remove.

    app-promote APP   create production from staging or update its image
    app-scale APP ENV [--replicas N] [--cpu Q] [--memory Q] [--memory-limit Q]
    app-expose APP ENV --exposure E [--host H]     change who can reach an instance
    app-team APP --team T                          move an app (every environment) to a registered team
    app-calls APP ENV [--add A,B] [--remove C]     change which apps an instance may call
    app-egress APP ENV [--add H[:P],...] [--remove H[:P]]   change which hosts outside the cluster an instance may reach
    app-events APP ENV [--subscribe APP.EVENT,...] [--add-subscribe APP.EVENT] ...   change the events an instance receives
    app-offers APP ENV | --all ENV                 read what an app offers (its API and event documents) from its repository
    app-deploy [APP] [--refresh]                   bring staging to the newest image, with what it needs and offers
    app-secret APP ENV NAME                        set one secret value, read from standard input (needs the age key)
    app-sync                                       rewrite what the platform derives in every instance
    app-remove APP ENV                             delete the instance's files and unregister its unit

Each edits files under the repository root and checks the result against the
app policy; commit and push to apply. Handwritten YAML retains its comments,
quotes and key order. Unsupported custom structures are refused explicitly.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import sys
from concurrent.futures import ThreadPoolExecutor
from dataclasses import replace
from pathlib import Path

from swhurl import ROOT, app_alerts, platform
from swhurl.apps import (
    declaration,
    egress,
    events,
    images,
    offers,
    ops,
    promotion,
)
from swhurl.apps.contract import (
    AUTO_DEPLOY_TAG_PATTERN,
    EXPOSURES,
    JOB_SCHEDULE,
    JOB_TIME_ZONE,
    RETAINED_STORAGE_CLASS,
    SECRET_FILE,
    app_secret,
    default_host,
    image_policy_name,
    in_cookie_domain,
    over_quota,
    quantity,
    schedule_problem,
    secret_key_problem,
    stored_secrets,
    teams,
)
from swhurl.apps.instance import ENVIRONMENTS, Instance, instance_folder
from swhurl.apps.model import sync as model_sync
from swhurl.apps.model import workload
from swhurl.apps.model.calls import admits
from swhurl.apps.model.draft import APP_SECRET, EVENTS_SECRET, Entry
from swhurl.apps.model.events import keyed as events_keyed
from swhurl.apps.model.exposure import ROUTED
from swhurl.apps.model.records import ALLOW_RE, TEAM_GRANT, Image, Release, Resources, Storage
from swhurl.apps.model.workload import DEFAULT_RESOURCES
from swhurl.apps.new import (
    GenerationError,
    check_generated,
    dump,
    write_encrypted_secret,
)
from swhurl.apps.release import (
    change_release,
    describe_offers,
    image_declaration,
    image_source,
    instance_dir,
    new_nats_key,
    offer_files,
    offered,
    read_offers,
    set_offers,
)
from swhurl.apps.yaml_file import EditError, read
from swhurl.run import CommandError, Runner

CPU_RE = re.compile(r'^\d+(\.\d+)?m?$')
MEMORY_RE = re.compile(r'^\d+(Ki|Mi|Gi)$')
MAX_REPLICAS = 10


def set_calls(root: Path, app: str, env: str, *, add: list[str] | None = None, remove: list[str] | None = None) -> Path:
    """Change which apps ``app/env`` calls (its Settings). A call connects once the callee allows it
    (set_allows); until then it renders nothing and waits."""
    add, remove = add or [], remove or []
    if not add and not remove:
        raise EditError('give --add and/or --remove with app names')
    instance = instance_dir(root, app, env)
    world, _ = model_sync.read(root)
    current = list(world[Instance(app, env)].settings.calls)
    for callee in add:
        if callee == app:
            raise EditError('an app does not declare calls to itself')
        if callee in current:
            raise EditError(f'{app}/{env} already calls {callee}')
        found = world.get(Instance.named(callee, env))
        if found is None:
            instance_dir(root, callee, env)  # says which instance is missing
        if found.release.port is None:
            raise EditError(f'{callee} is a worker: it has no address to call')
    for callee in remove:
        if callee not in current:
            raise EditError(f'{app}/{env} does not call {callee}')
    now = tuple([callee for callee in current if callee not in remove] + add)
    model_sync.change(root, Instance(app, env), lambda entry: replace(entry, settings=replace(entry.settings, calls=now)))
    print(f'[OK] {app}/{env} calls: {", ".join(now) or "nothing"}')
    team = world[Instance(app, env)].app.team
    for callee in add:
        if not admits(world[Instance(callee, env)], app, team):
            print(f'[INFO] {app} -> {callee} waits for {callee} to allow it: make app-allows APP={callee} ENV={env} '
                  f'ARGS="--add {app}" (or team:{team})')
    return instance


def set_allows(root: Path, app: str, env: str, *, add: list[str] | None = None, remove: list[str] | None = None) -> Path:
    """Change who may call ``app/env`` (its Settings): app names, or team:<team> for every app of a team.
    A caller is connected when it also calls this app; the callers' side is rendered with it."""
    add, remove = add or [], remove or []
    if not add and not remove:
        raise EditError('give --add and/or --remove with app names or team:<team>')
    instance = instance_dir(root, app, env)
    world, _ = model_sync.read(root)
    current = list(world[Instance(app, env)].settings.allows)
    registered = teams(root)
    for name in add:
        if not ALLOW_RE.match(name):
            raise EditError(f'{name!r} is not an app name or team:<team>')
        if name == app:
            raise EditError('an app does not allow itself')
        if name.startswith(TEAM_GRANT) and name.removeprefix(TEAM_GRANT) not in registered:
            raise EditError(f'unknown team {name.removeprefix(TEAM_GRANT)!r}; registered in apps/teams.yaml: {", ".join(registered)}')
        if name in current:
            raise EditError(f'{app}/{env} already allows {name}')
    for name in remove:
        if name not in current:
            raise EditError(f'{app}/{env} does not allow {name}')
    now = tuple([name for name in current if name not in remove] + list(dict.fromkeys(add)))
    model_sync.change(root, Instance(app, env), lambda entry: replace(entry, settings=replace(entry.settings, allows=now)))
    print(f'[OK] {app}/{env} allows: {", ".join(now) or "nobody"}')
    return instance


def set_egress(root: Path, app: str, env: str, *, add: list[str] | None = None, remove: list[str] | None = None) -> Path:
    """Change which hosts outside the cluster ``app/env`` may reach (its Settings); the proxy's
    configuration, which follows from every instance's grants, is rendered with it."""
    add, remove = [egress.grant(name) for name in add or []], [egress.grant(name) for name in remove or []]
    if not add and not remove:
        raise EditError('give --add and/or --remove with host names')
    problems = [problem for name in add if (problem := egress.grant_problem(name))]
    if problems:
        raise EditError('; '.join(problems))
    instance = instance_dir(root, app, env)
    world, platform = model_sync.read(root)
    if not platform.egress:
        raise EditError(f'{egress.PROXY_FILE} is missing: this tree has no egress proxy')
    now = changed_list('hosts', list(world[Instance(app, env)].settings.egress), None, add, remove)
    within = egress.covered(now)
    if within:
        raise EditError(f'a wildcard grant already covers {", ".join(within)}')
    model_sync.change(root, Instance(app, env),
                      lambda entry: replace(entry, settings=replace(entry.settings, egress=tuple(now))))
    print(f'[OK] {app}/{env} may reach: {", ".join(now) or "nothing outside the cluster"}')
    return instance


def changed_list(what: str, current: list[str], replace: list[str] | None, add: list[str], remove: list[str]) -> list[str] | None:
    """A list after a change: replaced whole, or with names added and removed (as app-calls does); None if untouched."""
    if replace is not None:
        if add or remove:
            raise EditError(f'--{what} replaces the list: do not give --add-{what} or --remove-{what} with it')
        return replace
    if not add and not remove:
        return None
    present = [name for name in add if name in current]
    absent = [name for name in remove if name not in current]
    if present or absent:
        raise EditError('; '.join(filter(None, [present and f'already there: {", ".join(present)}',
                                                absent and f'not there to remove: {", ".join(absent)}'])))
    return [name for name in current if name not in remove] + add


def set_events(root: Path, app: str, env: str, *, subscribe: list[str] | None = None,
               add_subscribe: list[str] | None = None, remove_subscribe: list[str] | None = None,
               runner: Runner | None = None) -> Path:
    """Grant the ``<app>.<event>`` pairs ``app/env`` receives: replace the list, or add/remove names.
    Publications belong to the image's AsyncAPI document, never to this command."""
    instance, each = instance_dir(root, app, env), Instance(app, env)
    world, _ = model_sync.read(root)
    entry = world[each]
    subscribe = changed_list('subscribe', list(entry.settings.subscriptions), subscribe,
                             add_subscribe or [], remove_subscribe or [])
    if subscribe is None:
        raise EditError('give --subscribe (an empty value clears it), --add-subscribe or --remove-subscribe; '
                        'to publish, edit asyncapi.yaml named by provides.eventsFile')
    bad = [name for name in subscribe if not events.SUBSCRIPTION_RE.match(name)]
    if bad or len(set(subscribe)) != len(subscribe):
        raise EditError('subscriptions are <app>.<event> (orders.created), each once'
                        + (f': {", ".join(bad)}' if bad else ''))
    subscribed = tuple(subscribe)
    created = ''
    if (entry.release.publishes or subscribed) and not events_keyed(entry):
        created = new_nats_key(instance, app, env, runner=runner)

    def update(entry: Entry) -> Entry:
        allocations = replace(entry.settings.allocations, events_key=created) if created else entry.settings.allocations
        return replace(entry, settings=replace(entry.settings, subscriptions=subscribed, allocations=allocations),
                       encrypted=entry.encrypted | ({EVENTS_SECRET} if created else set()))
    try:
        model_sync.change(root, each, update)
    except EditError:
        if created:
            (instance / events.SECRET_FILE).unlink(missing_ok=True)
        raise
    print(f'[OK] {app}/{env} publishes: {", ".join(entry.release.publishes) or "nothing"}; '
          f'subscribes to: {", ".join(subscribed) or "nothing"}')
    return instance


def newest_image(root: Path, app: str, *, opener: offers.Opener = offers._open) -> images.Image:
    """The image ``app``'s staging ImagePolicy picks from the registry now."""
    instance_dir(root, app, 'staging')
    world, _ = model_sync.read(root)
    image = world[Instance(app, 'staging')].release.image
    if image.repository != workload.platform_image(app):
        raise EditError(f'{app}/staging does not follow platform images')
    try:
        return images.newest(images.Policy(image_policy_name(app), image.repository.removeprefix('ghcr.io/'),
                                           re.compile(AUTO_DEPLOY_TAG_PATTERN), 'run'), opener=opener)
    except (images.ImageError, OSError) as error:
        raise EditError(str(error)) from None


RESOURCE_FIELDS = (('cpu', 'requests', 'cpu'), ('memory', 'requests', 'memory'), ('memory_limit', 'limits', 'memory'))
"""Each resource the image may need at least (declaration.RESOURCES) and where it is in the container."""


def with_needs(entry: Entry, image: Image, declared: declaration.Declaration, app: str) -> tuple[Entry, list[str]]:
    """The records with a new image and what it needs: its port and health path (web), the user it runs as,
    telemetry, a new database's volume, the secret names it reads (a new one arrives without a value),
    resources at its minimum or the instance's capacity, whichever is larger, its description and its stack. Returns them and what changed.
    A need the instance cannot take is refused (EditError)."""
    needs, old = declared.needs, entry.release
    if old.kind != needs.kind:
        raise EditError(f'kind: {needs.kind}, but the instance is a {"web app" if old.kind == "web" else old.kind}: '
                        'web, worker or job is fixed at creation (create a new app for the other kind)')
    web = needs.kind == 'web'
    release = replace(old, image=image, port=needs.port if web else None, health_path=needs.health_path if web else '',
                      uid=needs.uid, telemetry=needs.otlp, database=needs.database or '', secrets=tuple(needs.secrets),
                      description=declared.description, stack=declared.stack,
                      resources=Resources(*(needs.minimum.get(field, '') for field, _, _ in RESOURCE_FIELDS)))
    return take_release(entry, release, app, needs.database_size, 'swhurl.yaml')


def take_release(entry: Entry, release: Release, app: str, database_size: str, said: str) -> tuple[Entry, list[str]]:
    """``entry`` running ``release``, an image and what it needs (a new staging image's, or staging's for a
    promotion), with what that changes in the instance's Settings: a new database gets a retained volume of
    ``database_size`` and one writer. Returns the records and what changed. Refused (EditError, naming
    ``said``, where the need was declared): a database the instance would lose, a volume of another size, and a
    new uid for files already on a volume."""
    old, settings = entry.release, entry.settings
    changes: list[str] = []
    if release.port != old.port:
        changes.append(f'port {old.port} -> {release.port}')
    if release.health_path != old.health_path:
        changes.append(f'health path {old.health_path} -> {release.health_path}')
    if release.uid != old.uid:
        if old.database:
            raise EditError(f'uid {release.uid}: {app} keeps a volume whose files belong to uid {old.uid}, '
                            'and a new image does not change who owns them (keep the uid, or move the data by hand)')
        changes.append(f'uid {old.uid} -> {release.uid}')
    if release.telemetry != old.telemetry:
        changes.append(f'telemetry {"otlp" if release.telemetry else "none"}')
    allocations, replicas = settings.allocations, settings.replicas
    if release.database and not old.database:
        # As app-new makes it: a retained volume, one writer, stop-first updates (rule single-writer).
        allocations = replace(allocations, database=Storage(database_size, RETAINED_STORAGE_CLASS))
        replicas = None
        changes.append(f'database {release.database} on a new {database_size} volume')
    elif old.database and not release.database:
        raise EditError(f'database: none, but {app} keeps its database volume, and a push never deletes data: keep '
                        f'database in {said}, or remove the data on purpose (make destroy-data, docs/operations.md#lifecycle)')
    elif release.database and allocations.database and allocations.database.size != database_size:
        raise EditError(f'databaseSize {database_size}, but the volume was created with {allocations.database.size}, '
                        'and a volume keeps its size: declare that size again')
    if release.secrets != old.secrets:
        changes.append('secret names ' + ', '.join([f'+{n}' for n in release.secrets if n not in old.secrets]
                                                   + [f'-{n}' for n in old.secrets if n not in release.secrets]))
    if release.description != old.description:
        changes.append('description')
    # The instance's own capacity stays: resources render as the larger of it and the image's minimum.
    found = replace(entry, release=release, settings=replace(settings, replicas=replicas, allocations=allocations))
    changes += at_minimum(entry, found)
    return found, changes


def at_minimum(before: Entry, after: Entry) -> list[str]:
    """What the image's minimum (``after``'s release) changes in the instance's resources, which render as
    the larger of it and the instance's capacity. Refused when the instance cannot run it."""
    running, resources = workload.resources(before), workload.resources(after)
    changes = [f'{section}.{key} {running[section][key]} -> {resources[section][key]} (the image\'s minimum)'
               for _, section, key in RESOURCE_FIELDS if resources[section][key] != running[section][key]]
    request, limit = resources['requests']['memory'], resources['limits']['memory']
    if quantity(request) > quantity(limit):
        raise EditError(f'resources: a memory request of {request} is above the memory limit {limit}; declare memoryLimit too')
    count = 1 if after.settings.replicas is None else after.settings.replicas
    if over := over_quota([(count + (not after.release.database), [resources])]):
        minimum = {field: value for field, _, _ in RESOURCE_FIELDS if (value := getattr(after.release.resources, field))}
        raise EditError(f"resources: the image's minimum {minimum} at {count} replica(s) "
                        f'exceeds the namespace quota for {", ".join(over)}')
    return changes


def deploy_staging(root: Path, app: str, *, token: str = '', opener: offers.Opener = offers._open,
                   runner: Runner | None = None, found: images.Image | None = None, refresh: bool = False) -> bool:
    """Staging runs the newest image its ImagePolicy accepts (``found``, or read now), with what that
    image needs and offers: the tag and digest from the registry (images.py), then its swhurl.yaml read
    at its commit, whose needs become its Release (with_needs) and whose offers are kept beside it.
    An image already deployed is not read again unless ``refresh``. A refused need or offer holds the
    image: nothing is written. Returns whether anything changed."""
    found = found or newest_image(root, app, opener=opener)
    instance, each = instance_dir(root, app, 'staging'), Instance(app, 'staging')
    world, _ = model_sync.read(root)
    entry = world[each]
    previous = entry.release.image
    moved = (previous.tag, previous.digest) != (found.tag, found.digest)
    if not refresh and not moved and offers.IMAGE_COMMIT.match(found.tag) and entry.release.offers == offers.commit_of(found.tag):
        print(f'[OK] {app}/staging runs its newest image {found.tag}; its needs and offers were read at {entry.release.offers}')
        return False
    image = replace(previous, tag=found.tag, digest=found.digest)
    repository, commit = image_source(image, app, 'staging')
    declared = image_declaration(image, app, 'staging', token=token, opener=opener)
    files, described, served = read_offers(repository, commit, declared, token=token, opener=opener)
    updated, needed = with_needs(entry, image, declared, app)
    updated = replace(updated, release=offered(updated.release, commit, files, described, served))
    planned = offer_files(instance / offers.OFFERS_DIR, repository, commit, files)
    changed = change_release(root, each, model_sync.Update(lambda _: updated), planned, runner=runner)
    describe_offers(app, 'staging', commit, files, described, declared, changed)
    if moved:
        print(f'[OK] {app}/staging: image {previous.tag} -> {found.tag}')
    if needed:
        print(f'[OK] {app}/staging needs: {", ".join(needed)}')
    missing = [name for name in declared.needs.secrets if name not in stored_secrets(
        (instance / SECRET_FILE).read_text() if (instance / SECRET_FILE).is_file() else '')]
    if missing:
        print(f'[WARN] {app}/staging: value missing for {", ".join(missing)} (the app runs without it): '
              f'make app-secret APP={app} ENV=staging NAME={missing[0]}', file=sys.stderr)
    return changed or moved or bool(needed)


def set_secret(root: Path, app: str, env: str, name: str, value: str, *, runner: Runner | None = None) -> Path:
    """Set one value of ``app/env``'s Secret (SECRET_FILE), encrypting it as it is written: the value goes
    to sops on standard input, never on a command line or unencrypted to disk. A name the image does not
    declare yet is accepted, so a value can be set before the push that needs it."""
    if problem := secret_key_problem([name]):
        raise EditError(problem)
    if not value:
        raise EditError(f'no value for {name}: give it on standard input')
    each = Instance.named(app, env)
    world, _ = model_sync.read(root)
    if each is None or each not in world:
        instance_dir(root, app, env)  # raises, naming what is missing
    instance, declared = each.folder(root), world[each].release.secrets
    # Rendered before the value is written: the instance's files must be what its records render now. An
    # instance with records and no rendered files yet (a first promotion's branch, which carries only
    # records) gets the encrypted file alone: main renders it when the branch is merged.
    prepared = model_sync.prepare(root, {each: model_sync.Update(lambda entry: replace(entry, encrypted=entry.encrypted | {APP_SECRET}))}) \
        if (instance / 'helmrelease.yaml').is_file() else None
    path, runner = instance / SECRET_FILE, runner or Runner()
    relative = path.relative_to(root).as_posix()  # .sops.yaml's rules match this path
    try:
        if path.is_file():
            if 'stringData' not in read(path):
                raise EditError(f'{relative} keeps its values under data, not stringData: set {name} with sops by hand')
            runner.run(['sops', 'set', '--value-stdin', relative, f'["stringData"]["{name}"]'], input=json.dumps(value), cwd=root)
        else:
            text = dump([{'apiVersion': 'v1', 'kind': 'Secret',
                          'metadata': {'name': app_secret(app), 'namespace': Instance(app, env).namespace},
                          'type': 'Opaque', 'stringData': {name: value}}])
            encrypted = runner.run(['sops', '--encrypt', '--filename-override', relative, '--input-type', 'yaml',
                                    '--output-type', 'yaml', '/dev/stdin'], input=text, cwd=root).stdout
            if 'sops:' not in encrypted or 'ENC[' not in encrypted:
                raise EditError(f'sops did not encrypt {relative}; nothing was written')
            path.write_text(encrypted)
    except CommandError as error:
        raise EditError(f'sops could not set {name} in {relative} (it needs the age key: age.agekey or SOPS_AGE_KEY_FILE): '
                        f'{error}') from None
    if prepared:
        model_sync.apply(root, prepared)
    print(f'[OK] {app}/{env}: {name} set (value not shown)')
    if name not in declared:
        print(f'[INFO] {name} is not among the secrets its image declares yet: the app receives it all the same')
    return instance


def app_instances(root: Path, app: str) -> list[Instance]:
    """The app's instances, staging first; refused when it has none."""
    world, _ = model_sync.read(root)
    found = [each for env in ENVIRONMENTS if (each := Instance(app, env)) in world]
    if not found:
        instance_dir(root, app, ENVIRONMENTS[0])  # raises, naming what is missing
    return found


def set_team(root: Path, app: str, name: str) -> Path:
    """Move every environment of an app to a team (its App record, rendered into each namespace)."""
    if name not in teams(root):
        raise EditError(f'unknown team {name!r}; registered in apps/teams.yaml: {", ".join(teams(root))}')
    found = app_instances(root, app)
    if not model_sync.change(root, found, lambda entry: replace(entry, app=replace(entry.app, team=name))):
        raise EditError(f'{app} already belongs to {name}')
    print(f'[OK] {app}: team {name} ({", ".join(each.env for each in found)})')
    return found[0].folder(root)


def set_alerts(root: Path, app: str, changes: dict[str, str] | None = None, *, none: bool = False,
               default: bool = False) -> Path:
    """Change an app's default alerts in every environment: a threshold or ``none`` per alert (``changes``),
    no alert at all, or back to the defaults. The dashboard job replaces the alerts within a minute."""
    found = app_instances(root, app)
    if sum((bool(changes), none, default)) != 1:
        raise EditError('give thresholds (--error-logs, --errors), or --none, or --default')
    world, _ = model_sync.read(root)
    try:
        settings = app_alerts.thresholds(world[found[0]].app.alerts)
        for key, value in (changes or {}).items():
            settings |= {key: app_alerts.thresholds(f'{key}={value}')[key]}
    except ValueError as error:
        raise EditError(str(error)) from None
    if none:
        spec = app_alerts.OFF
    elif default:
        spec = ''
    else:
        spec = app_alerts.spec_of(settings)
    if not model_sync.change(root, found, lambda entry: replace(entry, app=replace(entry.app, alerts=spec))):
        raise EditError(f'{app} already has those alert settings')
    described = ', '.join(f'{key} {"none" if value is None else f">= {value}"}' for key, value in app_alerts.thresholds(spec).items())
    print(f'[OK] {app}: alerts {described} ({", ".join(each.env for each in found)})')
    return found[0].folder(root)


def staging_offers(root: Path, app: str, staging: Entry, target: Path) -> dict[Path, str | None]:
    """Production's offers documents for staging's image: staging's, read for that image."""
    folder = instance_folder(root, app, 'staging') / offers.OFFERS_DIR
    try:
        found = offers.read(instance_folder(root, app, 'staging'))
    except offers.OfferError as error:
        raise EditError(f'{app}/staging: {error}') from None
    tag = staging.release.image.tag
    if found and offers.IMAGE_COMMIT.match(tag) and offers.commit_of(tag) != found.commit:
        raise EditError(f"{app}/staging runs {tag} but its offers were read at {found.commit}: read them for this image first "
                        f'(make app-offers APP={app} ENV=staging; the Deploy workflow does it with each new image)')
    files = {name: (folder / name).read_text() for name in filter(None, (found.api, found.events, found.flags))} if found else {}
    return offer_files(target / offers.OFFERS_DIR, found.repository if found else '', found.commit if found else '', files)


def promote(root: Path, app: str, source: str = 'staging', target: str = 'prod', *,
            expect_image: str | None = None, host: str | None = None, runner=None) -> Path:
    """Production runs staging's release (docs/apps.md#promote-to-production). A later promotion copies
    staging's Release whole, the image's minimum resources too (refused when production's quota cannot
    hold them), and renders it with production's own Settings; a first one creates production's records
    from staging's (promotion.first_production)."""
    if (source, target) != ('staging', 'prod'):
        raise EditError('promotion must differ between environments and always goes staging → prod')
    world, platform = model_sync.read(root)
    decision = promotion.select(root, world, app)
    if expect_image is not None and decision.image != expect_image:
        raise EditError(f'{app}/staging image changed since it was reviewed; refresh staging and review it again')
    if decision.outcome == 'same':
        raise EditError(f'{app}/prod already runs this digest; same image in both')
    staging, production = world[Instance(app, 'staging')], world.get(Instance(app, 'prod'))
    each, folder = Instance(app, 'prod'), instance_folder(root, app, 'prod')
    if decision.outcome == 'update':
        if host:
            raise EditError('--host applies only to first production; change existing routes with app-expose')
        if decision.rollback and (moved := [name for name, a, b in (
                ('database', production.release.database, staging.release.database),
                ('uid', production.release.uid, staging.release.uid)) if a != b]):
            raise EditError(f'{app}/staging runs an older image with another {" and ".join(moved)} than production: a rollback '
                            'never changes what a volume holds or who owns it (promote a newer image instead)')
        size = staging.settings.allocations.database.size if staging.settings.allocations.database else ''
        promoted, changed = take_release(production, staging.release, app, size, f'{app}/staging')
        change_release(root, each, model_sync.Update(lambda _: promoted), staging_offers(root, app, staging, folder), runner=runner)
        print(f'[OK] {app}/prod: image {decision.image} (from staging)' + (' [rollback]' if decision.rollback else ''))
        if changed:
            print(f'[OK] {app}/prod: {", ".join(changed)}')
        if added := [name for name in staging.release.secrets if name not in production.release.secrets]:
            print(f'[INFO] {app}/prod reads new secret names {", ".join(added)}: set their values before merging, in your own '
                  f'terminal: make app-secret APP={app} ENV=prod NAME=<name>')
        egress_difference(app, staging, production)
        return folder
    first = promotion.first_production(root, world, app, host=host)
    model_sync.guard(root, world, platform, [Instance(app, 'staging')])
    # Production's own credentials, made before rendering: placeholders for the Secret, and its NATS key.
    folder.mkdir(parents=True)
    try:
        if first.secret_keys:
            try:
                write_encrypted_secret(folder, app_secret(app), each.namespace, first.secret_keys, runner=runner)
            except GenerationError as error:
                raise EditError(str(error)) from None
        change_release(root, each, model_sync.Create(first.entry), staging_offers(root, app, staging, folder), runner=runner,
                       keyed=events.has_nats(root))
    except Exception:
        shutil.rmtree(folder, ignore_errors=True)
        raise
    print(f'[OK] created {app}/prod from reviewed staging settings (independent storage and credentials)')
    if first.secret_keys:
        print(f'[INFO] Set production secrets before merging, in your own terminal: make app-secret APP={app} ENV=prod NAME=<name> '
              '(make check-secrets lists the placeholders left)')
    egress_difference(app, staging, first.entry)
    return folder


def egress_difference(app: str, staging: Entry, production: Entry) -> None:
    """Say which hosts staging may reach and production may not: a promotion never grants them."""
    missing = [name for name in staging.settings.egress if name not in production.settings.egress]
    if missing:
        print(f'[INFO] staging may reach {", ".join(missing)}; production may not. A promotion never widens that: '
              f'make app-egress APP={app} ENV=prod ARGS="--add {",".join(missing)}"')


def scale(root: Path, app: str, env: str, *, replicas: int | None = None, cpu: str | None = None,
          memory: str | None = None, memory_limit: str | None = None) -> Path:
    """Change replicas or the capacity in the instance's Settings, and nothing else; never resources below
    the image's minimum (its Release, written with each image), or below the platform's default where the
    image states none."""
    if replicas is None and not (cpu or memory or memory_limit):
        raise EditError('give at least one of --replicas, --cpu, --memory, --memory-limit')
    if replicas is not None and not 0 <= replicas <= MAX_REPLICAS:
        raise EditError(f'--replicas must be 0 to {MAX_REPLICAS}')
    requested = {'cpu': cpu, 'memory': memory, 'memory_limit': memory_limit}
    for field, value in requested.items():
        flag = f'--{field.replace("_", "-")}'
        if value and not (CPU_RE if field == 'cpu' else MEMORY_RE).match(value):
            raise EditError(f'{flag} {value!r} is not a quantity like ' + ('100m or 0.5' if field == 'cpu' else '64Mi'))
    instance, each = instance_dir(root, app, env), Instance(app, env)
    world, _ = model_sync.read(root)
    entry = world[each]
    minimum = {field: value for field in requested if (value := getattr(entry.release.resources, field))}
    below = [f'--{field.replace("_", "-")} {value} (at least {minimum[field]})'
             for field, value in requested.items()
             if value and field in minimum and quantity(value) < quantity(minimum[field])]
    if below:
        raise EditError(f"below the image's minimum, from resources in its {declaration.FILE}: {', '.join(below)}")
    below = [f'--{field.replace("_", "-")} {value} (at least {getattr(DEFAULT_RESOURCES, field)})'
             for field, value in requested.items()
             if value and field not in minimum and quantity(value) < quantity(getattr(DEFAULT_RESOURCES, field))]
    if below:
        raise EditError(f"below the platform's default resources: {', '.join(below)}")
    if replicas is not None and entry.release.kind == 'job':
        raise EditError(f'{app}/{env} is a job: it runs one pod per run, on its schedule (make app-schedule); '
                        'only its resources can be changed')
    running = workload.resources(entry)
    effective = {'cpu': running['requests']['cpu'], 'memory': running['requests']['memory'],
                 'memory_limit': running['limits']['memory']}
    changes = []
    current_replicas = 1 if entry.settings.replicas is None else entry.settings.replicas
    if replicas is not None and replicas != current_replicas:
        changes.append(f'replicas {replicas}')
    else:
        replicas = entry.settings.replicas
    labels = {'cpu': 'requests.cpu', 'memory': 'requests.memory', 'memory_limit': 'limits.memory'}
    changes += [f'{labels[field]} {value}' for field, value in requested.items() if value and value != effective[field]]
    if not changes:
        raise EditError(f'{app}/{env} already has those settings')
    # The capacity chosen here; resources render as the larger of it and the image's minimum (or the default).
    capacity = Resources(*(requested[field] or effective[field] for field in requested))
    model_sync.change(root, each, lambda entry: replace(
        entry, settings=replace(entry.settings, replicas=replicas, capacity=capacity)))
    print(f'[OK] {app}/{env}: {", ".join(changes)}')
    return instance


def schedule(root: Path, app: str, env: str, when: str) -> Path:
    """Change when a job runs: five cron fields in its time zone (Etc/UTC)."""
    if problem := schedule_problem(when):
        raise EditError(problem)
    instance, each = instance_dir(root, app, env), Instance(app, env)
    world, _ = model_sync.read(root)
    entry = world[each]
    if entry.release.kind != 'job':
        raise EditError(f'{app}/{env} is not a job: only a job runs on a schedule')
    previous = entry.settings.schedule or JOB_SCHEDULE
    if previous == when:
        raise EditError(f'{app}/{env} already runs on {when!r}')
    model_sync.change(root, each, lambda entry: replace(
        entry, settings=replace(entry.settings, schedule='' if when == JOB_SCHEDULE else when)))
    print(f'[OK] {app}/{env}: schedule {previous!r} -> {when!r} ({JOB_TIME_ZONE})')
    return instance


def resolve_host(app: str, env: str, exposure: str, host: str | None, current_host: str | None) -> str | None:
    """Choose a route host and enforce the shared sign-in cookie boundary."""
    if exposure == 'authenticated-web':
        host = host or (current_host if current_host and in_cookie_domain(current_host) else default_host(app, env))
        if not in_cookie_domain(host):
            raise EditError(f'signed-in hosts must be under {default_host("x", "prod")[2:]} (shared sign-in cookie)')
    elif exposure == 'public':
        host = host or (current_host if current_host and not in_cookie_domain(current_host) else None)
        if not host:
            raise EditError('a public instance needs --host, outside the sign-in cookie domain')
        if in_cookie_domain(host):
            raise EditError(f'public hosts must be outside {default_host("x", "prod")[2:]}: the sign-in cookie would reach them')
    elif host:
        raise EditError('a private instance has no route; drop --host')
    return host


def expose(root: Path, app: str, env: str, exposure: str, host: str | None = None) -> Path:
    """Change who can reach an instance (its Settings): the route, sign-in, Namespace label, NetworkPolicy
    and unit dependencies are rendered from it."""
    instance, each = instance_dir(root, app, env), Instance(app, env)
    world, _ = model_sync.read(root)
    entry = world[each]
    current = entry.settings.exposure
    if exposure != 'private' and entry.release.kind != 'web':
        raise EditError(f'{app}/{env} is a {entry.release.kind} (no Service): it can only be private')
    current_host = (entry.settings.host or default_host(app, env)) if current in ROUTED else None
    host = resolve_host(app, env, exposure, host, current_host)
    if (exposure, host) == (current, current_host):
        raise EditError(f'{app}/{env} is already {exposure}' + (f' at {host}' if host else ''))
    stored = '' if host in (None, default_host(app, env)) else host
    model_sync.change(root, each, lambda entry: replace(entry, settings=replace(
        entry.settings, exposure=exposure, host=stored, issuer=entry.settings.issuer if host else '')))
    print(f'[OK] {app}/{env}: {current} -> {exposure}' + (f' at https://{host}' if host else ' (no route)'))
    return instance


def remove(root: Path, app: str, env: str) -> None:
    instance_dir(root, app, env)
    world, _ = model_sync.read(root)
    keeps_data = world[Instance(app, env)].settings.allocations.database is not None
    prepared = model_sync.prepare(root, {Instance(app, env): model_sync.Remove()})
    for path in model_sync.apply(root, prepared):
        print(f'[OK] changed {path.relative_to(root)}')
    if keeps_data:
        print(f'[WARN] {app}-{env} has a retained volume: Flux keeps its namespace and claim after removal. '
              'Delete the data only on purpose: make destroy-data (docs/operations.md#lifecycle)')


def run(action, check_dir: bool) -> int:
    """Run an edit; on success check the edited instance against the app policy."""
    try:
        result = action()
    except EditError as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 2
    if check_dir and result:
        return check_generated(result)
    print('[INFO] Next: make check-apps, then commit, push and make flux-reconcile')
    return 0


def main_promote(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-promote', description='Copy an app image (tag and digest) between environments')
    p.add_argument('app')
    p.add_argument('--expect-image', help='refuse if the source no longer names this exact repository, tag and digest')
    p.add_argument('--host', help='distinct production hostname for first public promotion')
    p.add_argument('--prepare-only', action='store_true', help=argparse.SUPPRESS)
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    runner = runner or Runner()

    def changed(applied: str, newest: str) -> list[str] | None:
        """The paths that differ between two commits in this checkout's history."""
        root = args.root.resolve()
        if not (root / '.git').exists():
            return None
        found = runner.run(['git', '-C', root, 'diff', '--name-only', applied, newest], check=False)
        return None if found.returncode else found.stdout.splitlines()

    def change():
        root = args.root.resolve()
        world, _ = model_sync.read(root)
        decision = promotion.select(root, world, args.app)
        if args.expect_image and args.expect_image != decision.image:
            raise EditError('staging image changed since it was reviewed; refresh and review it again')
        if decision.outcome == 'same':
            raise EditError('production already runs this digest; same image in both')
        if not args.prepare_only:  # console checks once, after preparing scratch files and before publication
            source_instance = Instance.named(args.app, 'staging')
            status = promotion.settled(ops.gather_status(runner, source_instance), changed) if source_instance else None
            problem = promotion.source_problem(status, decision.image)
            if problem:
                raise EditError(problem)
        return promote(root, args.app, expect_image=args.expect_image, host=args.host, runner=runner)

    try:
        return run(change, True)
    except CommandError as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 1


def main_scale(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-scale', description='Change replicas or resources of an app instance')
    p.add_argument('app')
    p.add_argument('env', choices=ENVIRONMENTS)
    p.add_argument('--replicas', type=int)
    p.add_argument('--cpu')
    p.add_argument('--memory')
    p.add_argument('--memory-limit')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)

    def change():
        return scale(args.root.resolve(), args.app, args.env, replicas=args.replicas, cpu=args.cpu,
                     memory=args.memory, memory_limit=args.memory_limit)
    return run(change, True)


def main_schedule(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-schedule', description='Change when a job runs')
    p.add_argument('app')
    p.add_argument('env', choices=ENVIRONMENTS)
    p.add_argument('--schedule', required=True, help='five cron fields in Etc/UTC, for example "*/10 * * * *", or @hourly, @daily')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: schedule(args.root.resolve(), args.app, args.env, args.schedule), True)


def main_expose(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-expose', description='Change who can reach an app instance')
    p.add_argument('app')
    p.add_argument('env', choices=ENVIRONMENTS)
    p.add_argument('--exposure', required=True, choices=EXPOSURES,
                   help='private: no route; authenticated-web: Google sign-in; public: no sign-in, host outside the domain')
    p.add_argument('--host', help='default for authenticated-web: the current host if signed-in, else the derived one')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: expose(args.root.resolve(), args.app, args.env, args.exposure, args.host), True)


def main_team(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-team', description='Move an app to a team registered in apps/teams.yaml')
    p.add_argument('app')
    p.add_argument('--team', required=True)
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: set_team(args.root.resolve(), args.app, args.team), True)


def main_alerts(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-alerts', description='Change an app\'s default alerts: their thresholds, or none')
    p.add_argument('app')
    for preset in app_alerts.PRESETS:
        p.add_argument(f'--{preset.key}', metavar='N|none', help=f'{preset.label}: fire at N in {preset.interval} '
                                                                  f'(default {preset.threshold}), or none')
    p.add_argument('--none', action='store_true', help='no default alerts for this app')
    p.add_argument('--default', action='store_true', help='back to the defaults')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    changes = {preset.key: value for preset in app_alerts.PRESETS
               if (value := getattr(args, preset.key.replace('-', '_'))) is not None}
    return run(lambda: set_alerts(args.root.resolve(), args.app, changes, none=args.none, default=args.default), True)


def main_calls(argv: list[str] | None = None) -> int:
    def names(text: str) -> list[str]:
        return [name.strip() for name in text.split(',') if name.strip()]
    p = argparse.ArgumentParser(prog='swhurl app-calls', description='Change which apps an instance may call')
    p.add_argument('app')
    p.add_argument('env', choices=ENVIRONMENTS)
    p.add_argument('--add', type=names, default=[], help='comma-separated apps to call (each needs this environment)')
    p.add_argument('--remove', type=names, default=[], help='comma-separated apps to stop calling')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: set_calls(args.root.resolve(), args.app, args.env, add=args.add, remove=args.remove), True)


def main_allows(argv: list[str] | None = None) -> int:
    def names(text: str) -> list[str]:
        return [name.strip() for name in text.split(',') if name.strip()]
    p = argparse.ArgumentParser(prog='swhurl app-allows', description='Change which apps may call an instance')
    p.add_argument('app')
    p.add_argument('env', choices=ENVIRONMENTS)
    p.add_argument('--add', type=names, default=[], help='comma-separated app names or team:<team> to allow')
    p.add_argument('--remove', type=names, default=[], help='comma-separated entries to stop allowing')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: set_allows(args.root.resolve(), args.app, args.env, add=args.add, remove=args.remove), True)


def main_egress(argv: list[str] | None = None) -> int:
    def names(text: str) -> list[str]:
        return [name.strip() for name in text.split(',') if name.strip()]
    p = argparse.ArgumentParser(prog='swhurl app-egress', description='Change which hosts outside the cluster an instance may reach')
    p.add_argument('app')
    p.add_argument('env', choices=ENVIRONMENTS)
    p.add_argument('--add', type=names, default=[], help=f'comma-separated host[:port] (port {egress.DEFAULT_PORT} '
                                                         'by default; *.example.com is any name under example.com)')
    p.add_argument('--remove', type=names, default=[], help='comma-separated host[:port] to stop granting')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: set_egress(args.root.resolve(), args.app, args.env, add=args.add, remove=args.remove), True)


def main_events(argv: list[str] | None = None) -> int:
    def names(text: str) -> list[str]:
        return [name.strip() for name in text.split(',') if name.strip()]
    p = argparse.ArgumentParser(prog='swhurl app-events', description='Set the events an instance receives; '
                                'publications come from asyncapi.yaml named by provides.eventsFile')
    p.add_argument('app')
    p.add_argument('env', choices=ENVIRONMENTS)
    p.add_argument('--subscribe', type=names, help='comma-separated <app>.<event> it receives ("" for none)')
    p.add_argument('--add-subscribe', type=names, default=[], help='comma-separated <app>.<event> to start receiving')
    p.add_argument('--remove-subscribe', type=names, default=[], help='comma-separated <app>.<event> to stop receiving')
    p.add_argument('--root', type=Path, default=ROOT)
    argv = sys.argv[1:] if argv is None else argv
    if any(arg.partition('=')[0] in ('--publish', '--add-publish', '--remove-publish') for arg in argv):
        p.error('publishing flags are no longer supported: edit asyncapi.yaml named by swhurl.yaml provides.eventsFile')
    args = p.parse_args(argv)
    return run(lambda: set_events(args.root.resolve(), args.app, args.env, subscribe=args.subscribe,
                                  add_subscribe=args.add_subscribe, remove_subscribe=args.remove_subscribe), True)


def main_offers(argv: list[str] | None = None, opener: offers.Opener = offers._open) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-offers',
                                description="Read what an app offers (its API and event documents) from its repository at its image's commit")
    p.add_argument('app', nargs='?')
    p.add_argument('env', nargs='?', choices=ENVIRONMENTS)
    p.add_argument('--all', choices=ENVIRONMENTS, metavar='ENV', help='every instance of this environment that an app repository publishes')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    if bool(args.all) == bool(args.app and args.env):
        p.error('give APP and ENV, or --all ENV')
    root, token = args.root.resolve(), os.environ.get('GITHUB_TOKEN', '')
    if not args.all:
        # A change is checked against the app policy: run() takes the instance's folder, not set_offers' answer.
        return run(lambda: set_offers(root, args.app, args.env, token=token, opener=opener) and instance_dir(root, args.app, args.env), True)
    failed = 0
    try:
        world, _ = model_sync.read(root)
    except EditError as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 2
    for each, entry in world.items():
        if each.env != args.all:
            continue
        app, image = each.app, entry.release.image
        if not image.repository.startswith('ghcr.io/') or not offers.IMAGE_COMMIT.match(image.tag):
            continue  # not built from an app repository (a fixture, a pinned third-party image)
        try:
            set_offers(root, app, args.all, token=token, opener=opener)
        except EditError as error:  # the others are still read; this one keeps what it had
            failed += 1
            print(f'[ERROR] {app}/{args.all}: {error}', file=sys.stderr)
    return 1 if failed else 0


def main_deploy(argv: list[str] | None = None, opener: offers.Opener = offers._open) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-deploy',
                                description="Bring staging to each app's newest image, with what that image needs and offers")
    p.add_argument('app', nargs='?', help='one app (default: every staging instance with an ImagePolicy)')
    p.add_argument('--refresh', action='store_true',
                   help="read swhurl.yaml again for an image staging already runs (after a change to how the platform applies it)")
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    root, token = args.root.resolve(), os.environ.get('GITHUB_TOKEN', '')
    if args.app:
        return run(lambda: deploy_staging(root, args.app, token=token, opener=opener, refresh=args.refresh)
                   and instance_dir(root, args.app, 'staging'), True)
    try:
        world, _ = model_sync.read(root)
    except EditError as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 2
    deployed = [each.app for each, entry in world.items() if each.env == 'staging'
                and entry.release.image.repository == workload.platform_image(each.app)]

    def newest(app: str) -> images.Image | EditError:
        try:
            return newest_image(root, app, opener=opener)
        except EditError as error:
            return error
    with ThreadPoolExecutor(max_workers=8) as pool:  # registry reads only; the edits below run one at a time
        found = dict(zip(deployed, pool.map(newest, deployed), strict=True))
    failed = 0
    for app in deployed:
        try:
            if isinstance(found[app], EditError):
                raise found[app]
            deploy_staging(root, app, token=token, opener=opener, found=found[app], refresh=args.refresh)
        except EditError as error:  # the others still deploy; this one keeps its image
            failed += 1
            print(f'[ERROR] {app}/staging: {error}', file=sys.stderr)
    return 1 if failed else 0


def main_secret(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-secret', description='Set one secret value of an app instance '
                                '(read from standard input, or asked for without echo); needs the age key')
    p.add_argument('app')
    p.add_argument('env', choices=ENVIRONMENTS)
    p.add_argument('name', help='the variable name, such as API_TOKEN')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    if runner is None:  # the age key as make check-secrets finds it
        key_file = os.environ.get('SOPS_AGE_KEY_FILE') or str(ROOT / platform.AGE_KEY)
        if not Path(key_file).is_file():
            print(f'[ERROR] age key not found at {key_file}; set SOPS_AGE_KEY_FILE', file=sys.stderr)
            return 2
        runner = Runner(env={'SOPS_AGE_KEY_FILE': key_file})
    if sys.stdin.isatty():
        import getpass
        value = getpass.getpass(f'{args.name} for {args.app}/{args.env} (not shown): ')
    else:
        value = sys.stdin.read().removesuffix('\n')
    return run(lambda: set_secret(args.root.resolve(), args.app, args.env, args.name, value, runner=runner), True)


def nats_keys(root: Path, *, runner: Runner | None = None) -> list[Instance]:
    """Give every instance without one its own NATS key (docs/apps.md#events); the instances that got one."""
    world, _ = model_sync.read(root)
    found = [each for each, entry in world.items() if not events_keyed(entry)]
    for each in found:
        change_release(root, each, model_sync.Update(lambda entry: entry), {}, runner=runner, keyed=True)
    return found


def sync(root: Path) -> None:
    changed = model_sync.sync(root)
    for path in changed:
        print(f'[OK] rewrote {path.relative_to(root)}')
    if not changed:
        print('[OK] every instance already has what the platform derives')


def main_sync(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-sync', description='Rewrite what the platform derives in every instance')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    def keys_then_sync():
        root = args.root.resolve()
        if events.has_nats(root):  # every instance has its own NATS key; one made before that gets it here
            for each in nats_keys(root):
                print(f'[OK] {each}: created its NATS key ({events.SECRET_FILE})')
        return sync(root)
    return run(keys_then_sync, False)


def main_remove(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-remove', description='Delete an app instance from Git (Flux uninstalls it)')
    p.add_argument('app')
    p.add_argument('env', choices=ENVIRONMENTS)
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: remove(args.root.resolve(), args.app, args.env), False)
