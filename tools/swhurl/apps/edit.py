"""App operations: read inputs, change records, prepare and write derived files.

Git edits only; commit and push to apply. CLI handling lives in cli.py, pure
record transformations in transitions.py, and the write pipeline in model/write.py.
"""
from __future__ import annotations

import json
import re
import sys
from dataclasses import dataclass, replace
from pathlib import Path

from swhurl import app_alerts
from swhurl.apps import declaration, egress, images, offers, promotion, transitions
from swhurl.apps.contract import (
    AUTO_DEPLOY_TAG_PATTERN,
    JOB_SCHEDULE,
    JOB_TIME_ZONE,
    SECRET_FILE,
    app_secret,
    default_host,
    image_policy_name,
    in_cookie_domain,
    quantity,
    schedule_problem,
    stored_secrets,
    teams,
)
from swhurl.apps.defaults import secret_key_problem
from swhurl.apps.errors import EditError
from swhurl.apps.instance import ENVIRONMENTS, Instance, instance_folder
from swhurl.apps.model import relations, workload, write
from swhurl.apps.model.draft import APP_SECRET, Entry
from swhurl.apps.model.exposure import ROUTED
from swhurl.apps.model.records import TEAM_GRANT, Resources
from swhurl.apps.model.workload import DEFAULT_RESOURCES
from swhurl.apps.release import (
    change_release,
    describe_offers,
    image_declaration,
    image_source,
    instance_dir,
    offer_files,
    offered,
    read_offers,
)
from swhurl.apps.yaml_file import read
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
    world, _ = write.read(root)
    now = tuple(transitions.changed_list('calls', list(world[Instance(app, env)].settings.grants.calls), None, add, remove))
    write.change(root, Instance(app, env), lambda entry: replace(entry, settings=replace(
        entry.settings, grants=replace(entry.settings.grants, calls=now))))
    print(f'[OK] {app}/{env} calls: {", ".join(now) or "nothing"}')
    world, _ = write.read(root)
    for relation in relations.evaluate(Instance(app, env), world):
        if relation.target in add and relation.pending:
            print(f'[INFO] {app} -> {relation.target} {relation.pending.replace("waiting", "waits", 1)}')
    return instance


def set_allows(root: Path, app: str, env: str, *, add: list[str] | None = None, remove: list[str] | None = None) -> Path:
    """Change who may call ``app/env`` (its Settings): app names, or team:<team> for every app of a team.
    A caller is connected when it also calls this app; the callers' side is rendered with it."""
    add, remove = add or [], remove or []
    if not add and not remove:
        raise EditError('give --add and/or --remove with app names or team:<team>')
    instance = instance_dir(root, app, env)
    world, _ = write.read(root)
    current = list(world[Instance(app, env)].settings.grants.allows)
    registered = teams(root)
    for name in add:
        if name == app:
            raise EditError('an app does not allow itself')
        if name.startswith(TEAM_GRANT) and name.removeprefix(TEAM_GRANT) not in registered:
            raise EditError(f'unknown team {name.removeprefix(TEAM_GRANT)!r}; registered in apps/teams.yaml: {", ".join(registered)}')
    now = tuple(transitions.changed_list('allows', current, None, add, remove))
    write.change(root, Instance(app, env), lambda entry: replace(entry, settings=replace(
        entry.settings, grants=replace(entry.settings.grants, allows=now))))
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
    world, platform = write.read(root)
    if not platform.egress:
        raise EditError(f'{egress.DIRECTORY} is missing: this tree has no egress proxy')
    now = transitions.changed_list('hosts', list(world[Instance(app, env)].settings.grants.egress), None, add, remove)
    within = egress.covered(now)
    if within:
        raise EditError(f'a wildcard grant already covers {", ".join(within)}')
    write.change(root, Instance(app, env),
                 lambda entry: replace(entry, settings=replace(
                     entry.settings, grants=replace(entry.settings.grants, egress=tuple(now)))))
    print(f'[OK] {app}/{env} may reach: {", ".join(now) or "nothing outside the cluster"}')
    return instance


def set_events(root: Path, app: str, env: str, *, subscribe: list[str] | None = None,
               add_subscribe: list[str] | None = None, remove_subscribe: list[str] | None = None,
               runner: Runner | None = None) -> Path:
    """Grant the ``<app>.<event>`` pairs ``app/env`` receives: replace the list, or add/remove names.
    Publications belong to the image's AsyncAPI document, never to this command."""
    instance, each = instance_dir(root, app, env), Instance(app, env)
    world, _ = write.read(root)
    entry = world[each]
    subscribe = transitions.changed_list('subscribe', list(entry.settings.grants.subscriptions), subscribe,
                             add_subscribe or [], remove_subscribe or [])
    if subscribe is None:
        raise EditError('give --subscribe (an empty value clears it), --add-subscribe or --remove-subscribe; '
                        'to publish, edit asyncapi.yaml named by provides.eventsFile')
    subscribed = tuple(subscribe)
    write.change(root, each, lambda entry: replace(entry, settings=replace(
        entry.settings, grants=replace(entry.settings.grants, subscriptions=subscribed))), runner=runner)
    print(f'[OK] {app}/{env} publishes: {", ".join(entry.release.publishes) or "nothing"}; '
          f'subscribes to: {", ".join(subscribed) or "nothing"}')
    return instance


def newest_image(root: Path, app: str, *, opener: offers.Opener = offers._open) -> images.Image:
    """The image ``app``'s staging ImagePolicy picks from the registry now."""
    instance_dir(root, app, 'staging')
    world, _ = write.read(root)
    image = world[Instance(app, 'staging')].release.image
    if image.repository != workload.platform_image(app):
        raise EditError(f'{app}/staging does not follow platform images')
    try:
        return images.newest(images.Policy(image_policy_name(app), image.repository.removeprefix('ghcr.io/'),
                                           re.compile(AUTO_DEPLOY_TAG_PATTERN), 'run'), opener=opener)
    except (images.ImageError, OSError) as error:
        raise EditError(str(error)) from None


def deploy_staging(root: Path, app: str, *, token: str = '', opener: offers.Opener = offers._open,
                   runner: Runner | None = None, found: images.Image | None = None, refresh: bool = False) -> bool:
    """Staging runs the newest image its ImagePolicy accepts (``found``, or read now), with what that
    image needs and offers: the tag and digest from the registry (images.py), then its swhurl.yaml read
    at its commit, whose needs become its Release (transitions.with_needs) and whose offers are kept beside it.
    An image already deployed is not read again unless ``refresh``. A refused need or offer holds the
    image: nothing is written. Returns whether anything changed."""
    found = found or newest_image(root, app, opener=opener)
    instance, each = instance_dir(root, app, 'staging'), Instance(app, 'staging')
    world, _ = write.read(root)
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
    updated, needed = transitions.with_needs(entry, image, declared, app)
    updated = replace(updated, release=offered(updated.release, commit, files, described, served))
    planned = offer_files(instance / offers.OFFERS_DIR, repository, commit, files)
    changed = change_release(root, each, write.Update(lambda _: updated, declared.needs.database_size), planned, runner=runner)
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
    world, _ = write.read(root)
    if each is None or each not in world:
        instance_dir(root, app, env)  # raises, naming what is missing
    instance, declared = each.folder(root), world[each].release.secrets
    # Rendered before the value is written: the instance's files must be what its records render now. An
    # instance with records and no rendered files yet (a first promotion's branch, which carries only
    # records) gets the encrypted file alone: main renders it when the branch is merged.
    path, runner = instance / SECRET_FILE, runner or Runner()
    relative = path.relative_to(root).as_posix()  # .sops.yaml's rules match this path
    pending = {} if path.is_file() else {APP_SECRET: write.encrypt_secret(
        root, path, app_secret(app), each.namespace, {name: value}, runner)}
    prepared = write.prepare(root, {each: write.Update(lambda entry: replace(entry, pending=pending))}) \
        if (instance / 'helmrelease.yaml').is_file() else None
    runner.add_secret(value)
    try:
        if path.is_file():
            if 'stringData' not in read(path):
                raise EditError(f'{relative} keeps its values under data, not stringData: set {name} with sops by hand')
            runner.run(['sops', 'set', '--value-stdin', relative, f'["stringData"]["{name}"]'], input=json.dumps(value), cwd=root)
        elif prepared is None:  # records-only branch: main renders once it is merged
            path.write_text(pending[APP_SECRET])
    except CommandError as error:
        raise EditError(f'sops could not set {name} in {relative} (it needs the age key: age.agekey or SOPS_AGE_KEY_FILE): '
                        f'{error}') from None
    if prepared:
        write.apply(root, prepared)
    print(f'[OK] {app}/{env}: {name} set (value not shown)')
    if name not in declared:
        print(f'[INFO] {name} is not among the secrets its image declares yet: the app receives it all the same')
    return instance


def app_instances(root: Path, app: str) -> list[Instance]:
    """The app's instances, staging first; refused when it has none."""
    world, _ = write.read(root)
    found = [each for env in ENVIRONMENTS if (each := Instance(app, env)) in world]
    if not found:
        instance_dir(root, app, ENVIRONMENTS[0])  # raises, naming what is missing
    return found


def set_team(root: Path, app: str, name: str) -> Path:
    """Move every environment of an app to a team (its App record, rendered into each namespace)."""
    if name not in teams(root):
        raise EditError(f'unknown team {name!r}; registered in apps/teams.yaml: {", ".join(teams(root))}')
    found = app_instances(root, app)
    if not write.change(root, found, lambda entry: replace(entry, app=replace(entry.app, team=name))):
        raise EditError(f'{app} already belongs to {name}')
    print(f'[OK] {app}: team {name} ({", ".join(each.env for each in found)})')
    return found[0].folder(root)


@dataclass(frozen=True)
class Thresholds:
    changes: dict[str, str]  # a threshold or none per alert, by preset key; the others keep what was set before


@dataclass(frozen=True)
class NoAlerts:
    pass


@dataclass(frozen=True)
class DefaultAlerts:
    pass


def set_alerts(root: Path, app: str, choice: Thresholds | NoAlerts | DefaultAlerts) -> Path:
    """Change an app's default alerts in every environment: thresholds, no alert at all, or back to the
    defaults. The dashboard job replaces the alerts within a minute."""
    found = app_instances(root, app)
    world, _ = write.read(root)
    if isinstance(choice, NoAlerts):
        spec = app_alerts.OFF
    elif isinstance(choice, DefaultAlerts):
        spec = ''
    else:
        try:
            settings = app_alerts.thresholds(world[found[0]].app.alerts)
            for key, value in choice.changes.items():
                settings |= {key: app_alerts.thresholds(f'{key}={value}')[key]}
        except ValueError as error:
            raise EditError(str(error)) from None
        spec = app_alerts.spec_of(settings)
    if not write.change(root, found, lambda entry: replace(entry, app=replace(entry.app, alerts=spec))):
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
    world, platform = write.read(root)
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
        promoted, changed = transitions.take_release(production, staging.release, app, size, f'{app}/staging')
        change_release(root, each, write.Update(lambda _: promoted, size), staging_offers(root, app, staging, folder), runner=runner)
        print(f'[OK] {app}/prod: image {decision.image} (from staging)' + (' [rollback]' if decision.rollback else ''))
        if changed:
            print(f'[OK] {app}/prod: {", ".join(changed)}')
        if added := [name for name in staging.release.secrets if name not in production.release.secrets]:
            print(f'[INFO] {app}/prod reads new secret names {", ".join(added)}: set their values before merging, in your own '
                  f'terminal: make app-secret APP={app} ENV=prod NAME=<name>')
        egress_difference(app, staging, production)
        return folder
    first = promotion.first_production(root, world, app, host=host)
    write.guard(root, world, platform, [Instance(app, 'staging')])
    # Prepare placeholders in memory too: a later key/render/routes refusal leaves no files behind.
    entry = first.entry
    if first.secret_keys:
        ciphertext = write.encrypt_secret(root, folder / SECRET_FILE, app_secret(app), each.namespace,
                                          dict.fromkeys(first.secret_keys, 'REPLACE_ME'), runner or Runner())
        entry = replace(entry, pending={APP_SECRET: ciphertext})
    change_release(root, each, write.Create(entry, first.database_size), staging_offers(root, app, staging, folder), runner=runner)
    print(f'[OK] created {app}/prod from reviewed staging settings (independent storage and credentials)')
    if first.secret_keys:
        print(f'[INFO] Set production secrets before merging, in your own terminal: make app-secret APP={app} ENV=prod NAME=<name> '
              '(make check-secrets lists the placeholders left)')
    egress_difference(app, staging, first.entry)
    return folder


def egress_difference(app: str, staging: Entry, production: Entry) -> None:
    """Say which hosts staging may reach and production may not: a promotion never grants them."""
    missing = [name for name in staging.settings.grants.egress if name not in production.settings.grants.egress]
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
    world, _ = write.read(root)
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
    current_replicas = 1 if entry.settings.choices.replicas is None else entry.settings.choices.replicas
    if replicas is not None and replicas != current_replicas:
        changes.append(f'replicas {replicas}')
    else:
        replicas = entry.settings.choices.replicas
    labels = {'cpu': 'requests.cpu', 'memory': 'requests.memory', 'memory_limit': 'limits.memory'}
    changes += [f'{labels[field]} {value}' for field, value in requested.items() if value and value != effective[field]]
    if not changes:
        raise EditError(f'{app}/{env} already has those settings')
    # The capacity chosen here; resources render as the larger of it and the image's minimum (or the default).
    capacity = Resources(*(requested[field] or effective[field] for field in requested))
    write.change(root, each, lambda entry: replace(
        entry, settings=replace(entry.settings, choices=replace(entry.settings.choices, replicas=replicas, capacity=capacity))))
    print(f'[OK] {app}/{env}: {", ".join(changes)}')
    return instance


def schedule(root: Path, app: str, env: str, when: str) -> Path:
    """Change when a job runs: five cron fields in its time zone (Etc/UTC)."""
    if problem := schedule_problem(when):
        raise EditError(problem)
    instance, each = instance_dir(root, app, env), Instance(app, env)
    world, _ = write.read(root)
    entry = world[each]
    if entry.release.kind != 'job':
        raise EditError(f'{app}/{env} is not a job: only a job runs on a schedule')
    previous = entry.settings.choices.schedule or JOB_SCHEDULE
    if previous == when:
        raise EditError(f'{app}/{env} already runs on {when!r}')
    write.change(root, each, lambda entry: replace(
        entry, settings=replace(entry.settings, choices=replace(entry.settings.choices, schedule='' if when == JOB_SCHEDULE else when))))
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
    world, _ = write.read(root)
    entry = world[each]
    current = entry.settings.grants.exposure
    if exposure != 'private' and entry.release.kind != 'web':
        raise EditError(f'{app}/{env} is a {entry.release.kind} (no Service): it can only be private')
    current_host = (entry.settings.choices.host or default_host(app, env)) if current in ROUTED else None
    host = resolve_host(app, env, exposure, host, current_host)
    if (exposure, host) == (current, current_host):
        raise EditError(f'{app}/{env} is already {exposure}' + (f' at {host}' if host else ''))
    stored = '' if host in (None, default_host(app, env)) else host
    write.change(root, each, lambda entry: replace(entry, settings=replace(
        entry.settings, grants=replace(entry.settings.grants, exposure=exposure),
        choices=replace(entry.settings.choices, host=stored, issuer=entry.settings.choices.issuer if host else ''))))
    print(f'[OK] {app}/{env}: {current} -> {exposure}' + (f' at https://{host}' if host else ' (no route)'))
    return instance


def remove(root: Path, app: str, env: str) -> None:
    instance_dir(root, app, env)
    world, _ = write.read(root)
    keeps_data = world[Instance(app, env)].settings.allocations.database is not None
    prepared = write.prepare(root, {Instance(app, env): write.Remove()})
    for path in write.apply(root, prepared):
        print(f'[OK] changed {path.relative_to(root)}')
    if keeps_data:
        print(f'[WARN] {app}-{env} has a retained volume: Flux keeps its namespace and claim after removal. '
              'Delete the data only on purpose: make destroy-data (docs/operations.md#lifecycle)')


def sync(root: Path, *, rotate: Instance | None = None, runner: Runner | None = None) -> None:
    changed = write.apply(root, write.prepare(root, {rotate: write.Rotate()} if rotate else {}, runner=runner))
    for path in changed:
        print(f'[OK] rewrote {path.relative_to(root)}')
    if not changed:
        print('[OK] every instance already has what the platform derives')
