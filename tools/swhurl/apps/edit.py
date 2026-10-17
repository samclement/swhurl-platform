"""Edit generated app instances (Git edits only, like app-new): promote, scale, remove.

    app-promote APP   create production from staging or update its image
    app-scale APP ENV [--replicas N] [--cpu Q] [--memory Q] [--memory-limit Q]
    app-expose APP ENV --exposure E [--host H]     change who can reach an instance
    app-team APP --team T                          move an app (every environment) to a registered team
    app-calls APP ENV [--add A,B] [--remove C]     change which apps an instance may call
    app-egress APP ENV [--add H[:P],...] [--remove H[:P]]   change which hosts outside the cluster an instance may reach
    app-events APP ENV [--publish A,B] [--subscribe APP.EVENT,...] [--add-subscribe APP.EVENT] ...   change the events an instance publishes and receives
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
from pathlib import Path

from swhurl import ROOT, app_alerts, platform
from swhurl.apps import declaration, egress, events, helmrelease, images, nkeys, offers, ops, policy, promotion
from swhurl.apps.calling import (
    API_VARIABLE,
    callee_address,
    callee_variable,
    declared_calls,
)
from swhurl.apps.contract import (
    AUTH_MIDDLEWARE,
    DATA_MOUNT,
    DATABASE_PATH_VARIABLE,
    EXPOSURE,
    EXPOSURES,
    IMAGE_AUTOMATION_FILE,
    OTLP_HOST_IP,
    RELOAD,
    RETAINED_STORAGE_CLASS,
    SECRET_FILE,
    SECRET_NAMES,
    SQLITE_PATH,
    TEAM,
    app_secret,
    declared_secrets,
    default_host,
    in_cookie_domain,
    otlp_variables,
    over_quota,
    quantity,
    secret_key_problem,
    stored_secrets,
    teams,
)
from swhurl.apps.derive import (
    callers_of,
    container,
    controller,
    instance_dir,
    instances_in,
    isolation_edits,
    service_port,
    subscribers_of,
    sync,
    sync_env,
    values,
)
from swhurl.apps.instance import ENVIRONMENTS, Instance, instance_folder, instance_unit_file
from swhurl.apps.new import (
    check_generated,
    depends_on,
    dump,
    ingress_values,
    watch_namespace,
)
from swhurl.apps.yaml_file import EditError, YamlFile, mapping
from swhurl.run import CommandError, Runner

PRUNE_DISABLED = 'kustomize.toolkit.fluxcd.io/prune'
CPU_RE = re.compile(r'^\d+(\.\d+)?m?$')
MEMORY_RE = re.compile(r'^\d+(Ki|Mi|Gi)$')
MAX_REPLICAS = 10


def set_calls(root: Path, app: str, env: str, *, add: list[str] | None = None, remove: list[str] | None = None) -> Path:
    """Change which apps ``app/env`` may call: its side here, the callees' side through sync_env."""
    add, remove = add or [], remove or []
    if not add and not remove:
        raise EditError('give --add and/or --remove with app names')
    instance = instance_dir(root, app, env)
    document = YamlFile(instance / 'helmrelease.yaml')
    variables = mapping(container(document.data), 'env', create=True)
    current = declared_calls(variables)
    for callee in add:
        if callee == app:
            raise EditError('an app does not declare calls to itself')
        if callee in current:
            raise EditError(f'{app}/{env} already calls {callee}')
        port = service_port(values(YamlFile(instance_dir(root, callee, env) / 'helmrelease.yaml').data))
        if port is None:
            raise EditError(f'{callee} is a worker: it has no address to call')
        variables[callee_variable(callee)] = callee_address(callee, port)
    for callee in remove:
        if callee not in current:
            raise EditError(f'{app}/{env} does not call {callee}')
        del variables[callee_variable(callee)]
    before = document.path.read_text()
    document.save()
    try:
        sync_env(root, env)
    except EditError:
        document.path.write_text(before)
        raise
    now = declared_calls(mapping(container(YamlFile(instance / 'helmrelease.yaml').data), 'env'))
    print(f'[OK] {app}/{env} calls: {", ".join(now) or "nothing"}')
    return instance


def set_egress(root: Path, app: str, env: str, *, add: list[str] | None = None, remove: list[str] | None = None) -> Path:
    """Change which hosts outside the cluster ``app/env`` may reach (its release's annotation), and the
    proxy's configuration that follows from every instance's grants."""
    add, remove = [egress.grant(name) for name in add or []], [egress.grant(name) for name in remove or []]
    if not add and not remove:
        raise EditError('give --add and/or --remove with host names')
    problems = [problem for name in add if (problem := egress.grant_problem(name))]
    if problems:
        raise EditError('; '.join(problems))
    instance = instance_dir(root, app, env)
    if egress.proxy_file(root) is None:
        raise EditError(f'{egress.PROXY_FILE} is missing: this tree has no egress proxy')
    document = YamlFile(instance / 'helmrelease.yaml')
    current = egress.granted(document.data)
    now = changed_list('hosts', current, None, add, remove)
    within = egress.covered(now)
    if within:
        raise EditError(f'a wildcard grant already covers {", ".join(within)}')
    annotations = mapping(mapping(document.data, 'metadata'), 'annotations', create=True)
    if now:
        annotations[egress.ANNOTATION] = ','.join(now)
    else:
        del annotations[egress.ANNOTATION]
        if not annotations:
            del document.data['metadata']['annotations']
    before = document.path.read_text()
    document.save()
    try:
        sync_env(root, env)
    except EditError:
        document.path.write_text(before)
        raise
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


def nats_key(root: Path, app: str, env: str, *, runner: Runner | None = None) -> bool:
    """Give an instance its own NATS key if it has none (docs/apps.md#events): the seed in its encrypted
    Secret, the public key on its release. Whether one was made; sync_env wires what follows."""
    instance = instance_dir(root, app, env)
    document = YamlFile(instance / 'helmrelease.yaml')
    variables = mapping(container(document.data), 'env', create=True)
    secret = instance / events.SECRET_FILE
    if variables.get(events.NKEY_VARIABLE) and secret.is_file():
        return False
    # The seed goes from memory into a file that sops encrypts in place: it is never printed.
    seed, public = nkeys.generate()
    secret.write_text(dump([{'apiVersion': 'v1', 'kind': 'Secret',
                             'metadata': {'name': events.secret_name(app), 'namespace': Instance(app, env).namespace},
                             'type': 'Opaque', 'stringData': {events.SEED_VARIABLE: seed}}]))
    try:
        (runner or Runner()).run(['sops', '--encrypt', '--in-place', str(secret)])
    except CommandError as error:
        secret.unlink(missing_ok=True)
        raise EditError(f'could not SOPS-encrypt {secret.name}; nothing was changed: {error}') from None
    if 'sops:' not in secret.read_text():
        secret.unlink()
        raise EditError(f'{secret.name} was not encrypted; removed it')
    variables[events.NKEY_VARIABLE] = public
    document.save()
    return True


def set_events(root: Path, app: str, env: str, *, publish: list[str] | None = None, subscribe: list[str] | None = None,
               add_publish: list[str] | None = None, remove_publish: list[str] | None = None,
               add_subscribe: list[str] | None = None, remove_subscribe: list[str] | None = None,
               runner: Runner | None = None) -> Path:
    """Change the events ``app/env`` publishes and/or the ``<app>.<event>`` pairs it receives: each list
    replaced whole (None: unchanged, an empty list: none) or with names added and removed. The first
    time, the instance gets its own NATS key; everything else follows in sync_env."""
    now = helmrelease.variables(YamlFile(instance_dir(root, app, env) / 'helmrelease.yaml').data)
    publish = changed_list('publish', events.published(now), publish, add_publish or [], remove_publish or [])
    subscribe = changed_list('subscribe', ['.'.join(pair) for pair in events.subscribed(now)], subscribe,
                             add_subscribe or [], remove_subscribe or [])
    if publish is None and subscribe is None:
        raise EditError('give --publish and/or --subscribe (an empty value clears it), or --add-publish, '
                        '--remove-publish, --add-subscribe, --remove-subscribe')
    bad = [name for name in publish or [] if not events.EVENT_RE.match(name)]
    bad += [name for name in subscribe or [] if not events.SUBSCRIPTION_RE.match(name)]
    if bad or len(set(publish or [])) != len(publish or []) or len(set(subscribe or [])) != len(subscribe or []):
        raise EditError('events are lowercase names (created), subscriptions <app>.<event> (orders.created), each once'
                        + (f': {", ".join(bad)}' if bad else ''))
    instance = instance_dir(root, app, env)
    document = YamlFile(instance / 'helmrelease.yaml')
    variables = mapping(container(document.data), 'env', create=True)
    for key, value in ((events.PUBLISH_VARIABLE, publish), (events.SUBSCRIBE_VARIABLE, subscribe)):
        if value:
            variables[key] = ','.join(value)
        elif value is not None:
            variables.pop(key, None)
    active = bool(events.published(variables) or events.subscribed(variables))
    before = document.path.read_text()
    document.save()
    try:
        created = active and nats_key(root, app, env, runner=runner)
    except EditError:
        document.path.write_text(before)
        raise
    try:
        sync_env(root, env)
    except EditError:
        document.path.write_text(before)
        if created:
            (instance / events.SECRET_FILE).unlink(missing_ok=True)
        raise
    now = helmrelease.variables(YamlFile(instance / 'helmrelease.yaml').data)
    print(f'[OK] {app}/{env} publishes: {", ".join(events.published(now)) or "nothing"}; '
          f'subscribes to: {", ".join(".".join(pair) for pair in events.subscribed(now)) or "nothing"}')
    return instance


def write_offers(root: Path, app: str, env: str, repository: str, commit: str, files: dict[str, str],
                 described: list[offers.Event] | None, *, api_path: str = '', runner: Runner | None = None) -> bool:
    """Keep an instance's offers (``files``: name in the offers directory -> text) and what follows from
    them: the release's annotation, and with an events document the events it publishes. Returns whether
    anything changed. Without files the directory and annotation go; what it publishes is left as it is.
    ``api_path`` is where a web app says it serves its description (provides.api): kept as SWHURL_API_PATH.
    A refusal (an event another app still receives is gone) leaves everything as it was."""
    instance = instance_dir(root, app, env)
    folder = instance / offers.OFFERS_DIR
    release = instance / 'helmrelease.yaml'
    before = {path: path.read_bytes() for path in [release, *(sorted(folder.iterdir()) if folder.is_dir() else [])]}
    if files:
        source = offers.Source(repository, commit, next((n for n in files if n not in (offers.EVENTS_NAME, offers.FLAGS_NAME)), ''),
                               offers.EVENTS_NAME if offers.EVENTS_NAME in files else '',
                               offers.FLAGS_NAME if offers.FLAGS_NAME in files else '').document()
        wanted = {offers.SOURCE_FILE: '# Read from the app\'s repository by make app-offers: do not edit.\n' + dump([source]), **files}
        folder.mkdir(exist_ok=True)
        for path in folder.iterdir():
            if path.name not in wanted:
                path.unlink()
        for name, text in wanted.items():
            if not (folder / name).is_file() or (folder / name).read_text() != text:
                (folder / name).write_text(text)
    elif folder.is_dir():
        shutil.rmtree(folder)
    document = YamlFile(release)
    metadata = mapping(document.data, 'metadata')
    annotations = metadata.get('annotations') or {}
    if files and annotations.get(offers.ANNOTATION) != commit:
        mapping(metadata, 'annotations', create=True)[offers.ANNOTATION] = commit
        document.save()
    elif not files and offers.ANNOTATION in annotations:
        del annotations[offers.ANNOTATION]
        if not annotations:
            del metadata['annotations']
        document.save()
    variables = mapping(container(document.data), 'env', create=True)
    if api_path and service_port(values(document.data)) is not None and variables.get(API_VARIABLE) != api_path:
        variables[API_VARIABLE] = api_path
        document.save()
    names = [event.name for event in described] if described is not None else None
    if names is not None and names != events.published(helmrelease.variables(document.data)):
        try:
            set_events(root, app, env, publish=names, runner=runner)
        except EditError:
            if folder.is_dir():
                shutil.rmtree(folder)
            for path, content in before.items():
                path.parent.mkdir(exist_ok=True)
                path.write_bytes(content)
            raise
    after = {path: path.read_bytes() for path in [release, *(sorted(folder.iterdir()) if folder.is_dir() else [])]}
    return after != before


def image_source(release: dict, app: str, env: str) -> tuple[str, str]:
    """``(OWNER/REPO, commit)`` the image a release runs was built from."""
    repository = str(helmrelease.image(release).get('repository', ''))
    if not repository.startswith('ghcr.io/'):
        raise EditError(f'{app}/{env} runs {repository or "no image"}: only an app published from its own GitHub repository has offers')
    try:
        return repository.removeprefix('ghcr.io/'), offers.commit_of(str(helmrelease.image(release).get('tag', '')))
    except offers.OfferError as error:
        raise EditError(str(error)) from None


def image_declaration(release: dict, app: str, env: str, *, token: str = '',
                      opener: offers.Opener = offers._open) -> declaration.Declaration:
    """The swhurl.yaml of the image a release runs, read at the commit it was built from."""
    repository, commit = image_source(release, app, env)
    try:
        declared = declaration.read(repository, commit, token=token, opener=opener)
    except declaration.DeclarationError as error:
        raise EditError(str(error)) from None
    if declared is None:
        raise EditError(f'{repository}@{commit} has no {declaration.FILE} (or the repository is private: set GITHUB_TOKEN)')
    return declared


def set_offers(root: Path, app: str, env: str, *, token: str = '', opener: offers.Opener = offers._open,
               runner: Runner | None = None, declared: declaration.Declaration | None = None) -> bool:
    """Read what ``app/env`` offers from its repository, at the commit its image was built from: the
    documents its swhurl.yaml names (provides, the file's live field: declaration.py); ``declared`` is
    that file when the caller has read it already. Returns whether anything changed."""
    release = YamlFile(instance_dir(root, app, env) / 'helmrelease.yaml').data
    repository, commit = image_source(release, app, env)
    declared = declared or image_declaration(release, app, env, token=token, opener=opener)
    try:
        files, described = {}, None
        for key, kind, path in (('apiFile', 'api', declared.api_file), ('eventsFile', 'events', declared.events_file)):
            if not path:
                continue
            text = offers.fetch(repository, commit, str(path), token=token, opener=opener)
            if text is None:
                raise offers.OfferError(f'{declaration.FILE} names {path} (provides.{key}), which {repository}@{commit} does not have')
            if kind == 'api':
                offers.check_api(text, str(path))
                files['openapi' + (Path(str(path)).suffix or '.yaml')] = text
            else:
                described = offers.events(text, str(path))
                files[offers.EVENTS_NAME] = text
        if declared.flags:  # what the console's flag form is built from (docs/apps.md#feature-flags)
            files[offers.FLAGS_NAME] = json.dumps(declared.flags, indent=2) + '\n'
    except (offers.OfferError, declaration.DeclarationError) as error:
        raise EditError(str(error)) from None
    served = declared.api_path if re.fullmatch(r'/[A-Za-z0-9_./-]*', declared.api_path) else ''
    changed = write_offers(root, app, env, repository, commit, files, described, api_path=served, runner=runner)
    what = ', '.join(filter(None, [next((f'its API ({n})' for n in files if n not in (offers.EVENTS_NAME, offers.FLAGS_NAME)), ''),
                                   described is not None and f'events: {", ".join(e.name for e in described) or "none"}',
                                   declared.flags and f'flags: {", ".join(declared.flags)}']))
    print(f'[OK] {app}/{env} at {commit} offers {what or "nothing"}' + ('' if changed else ' (unchanged)'))
    return changed


def newest_image(root: Path, app: str, *, opener: offers.Opener = offers._open) -> images.Image:
    """The image ``app``'s staging ImagePolicy picks from the registry now."""
    try:
        return images.newest(images.policy(instance_dir(root, app, 'staging') / IMAGE_AUTOMATION_FILE), opener=opener)
    except (images.ImageError, OSError) as error:
        raise EditError(str(error)) from None


PROBES = ('readiness', 'liveness', 'startup')
SCALED = (('cpu', 'requests', 'cpu'), ('memory', 'requests', 'memory'), ('memory_limit', 'limits', 'memory'))
"""Each resource app-scale sets (its option, by declaration.RESOURCES) and where it is in the container."""


def secret_reference(main: dict, workload: dict, app: str) -> None:
    """The app's own Secret as variables (envFrom), optional so the app starts before its first value, and
    Reloader restarting it when a value changes."""
    name = app_secret(app)
    refs = main.get('envFrom')
    found = next((ref for ref in refs or [] if (ref.get('secretRef') or {}).get('name') == name), None)
    if found is None:
        if refs is None:
            main['envFrom'] = []
        main['envFrom'].append({'secretRef': {'name': name, 'optional': True}})
    elif found['secretRef'].get('optional') is not True:
        found['secretRef']['optional'] = True
    annotations = workload.get('annotations') or {}
    reloads = [n for n in str(annotations.get(RELOAD, '')).split(',') if n]
    if name not in reloads:
        mapping(workload, 'annotations', create=True)[RELOAD] = ','.join([*reloads, name])


def apply_needs(release: dict, app: str, needs: declaration.Needs) -> list[str]:
    """Write what the image needs into its release (in place): the port and health path of a web app
    (Service and probes), the user it runs as, its telemetry variables, a new database's volume, the
    secret names it reads (a new one arrives without a value), and resources raised to the image's
    minimum. Returns what changed. A need the instance cannot take is refused (EditError) and
    the release is left part-changed: the caller restores it."""
    app_values, main, workload = values(release), container(release), controller(release)
    changes: list[str] = []
    port = service_port(app_values)
    if (port is None) != (needs.kind == 'worker'):
        raise EditError(f'kind: {needs.kind}, but the instance is a {"worker" if port is None else "web app"}: '
                        'web or worker is fixed at creation (create a new app for the other kind)')
    if needs.port is not None and port != needs.port:
        app_values['service']['main']['ports']['http']['port'] = needs.port
        changes.append(f'port {port} -> {needs.port}')
    paths = set()
    for name in PROBES if needs.port is not None else ():
        check = (((main.get('probes') or {}).get(name) or {}).get('spec') or {}).get('httpGet')
        if check is None:
            continue
        paths.add(check.get('path'))
        for key, value in (('path', needs.health_path), ('port', needs.port)):
            if check.get(key) != value:
                check[key] = value
    if paths - {needs.health_path}:
        changes.append(f'health path {", ".join(sorted(map(str, paths - {needs.health_path})))} -> {needs.health_path}')
    security = mapping(mapping(app_values, 'defaultPodOptions'), 'securityContext')
    if security.get('runAsUser') != needs.uid:
        if 'data' in (app_values.get('persistence') or {}):
            raise EditError(f'uid {needs.uid}: {app} keeps a volume whose files belong to uid {security.get("runAsUser")}, '
                            'and a new image does not change who owns them (keep the uid, or move the data by hand)')
        changes.append(f'uid {security.get("runAsUser")} -> {needs.uid}')
        for key in ('runAsUser', 'runAsGroup', 'fsGroup'):
            security[key] = needs.uid
    variables = mapping(main, 'env', create=True)
    telemetry = otlp_variables(app)
    if egress.NO_PROXY_VARIABLE in variables:  # the proxy's settings use HOST_IP too (make app-sync)
        telemetry.pop(OTLP_HOST_IP)
    if needs.otlp and any(key not in variables for key in telemetry):
        for key, value in telemetry.items():
            variables.setdefault(key, value)
        changes.append('telemetry otlp')
    elif not needs.otlp and any(key in variables for key in telemetry):
        for key in telemetry:
            variables.pop(key, None)
        changes.append('telemetry none')
    persistence = mapping(app_values, 'persistence', create=True)
    if needs.database and DATABASE_PATH_VARIABLE not in variables:
        if 'data' in persistence:
            raise EditError(f'database: {needs.database}, but {app} already has a volume of its own: add the database by hand')
        # As app-new makes it: a retained volume, one writer, stop-first updates (rule single-writer).
        persistence['data'] = {'type': 'persistentVolumeClaim', 'storageClass': RETAINED_STORAGE_CLASS,
                               'accessMode': 'ReadWriteOnce', 'size': needs.database_size, 'retain': True,
                               'globalMounts': [{'path': DATA_MOUNT}]}
        variables[DATABASE_PATH_VARIABLE] = SQLITE_PATH
        if 'replicas' in workload:
            workload['replicas'] = 1
        else:
            workload.insert(0, 'replicas', 1)
        workload['strategy'] = 'Recreate'
        changes.append(f'database {needs.database} on a new {needs.database_size} volume')
    elif not needs.database and DATABASE_PATH_VARIABLE in variables:
        raise EditError(f'database: none, but {app} keeps its database volume, and a push never deletes data: keep '
                        'database in swhurl.yaml, or remove the data on purpose (make destroy-data, docs/operations.md#lifecycle)')
    elif needs.database and str(persistence['data'].get('size')) != needs.database_size:
        raise EditError(f'databaseSize {needs.database_size}, but the volume was created with {persistence["data"].get("size")}, '
                        'and a volume keeps its size: declare that size again')
    named = declared_secrets(release)
    if list(needs.secrets) != named:
        annotations = mapping(mapping(release, 'metadata'), 'annotations', create=True)
        if needs.secrets:
            annotations[SECRET_NAMES] = ','.join(needs.secrets)
        else:
            del annotations[SECRET_NAMES]
            if not annotations:
                del release['metadata']['annotations']
        changes.append('secret names ' + ', '.join([f'+{n}' for n in needs.secrets if n not in named]
                                                   + [f'-{n}' for n in named if n not in needs.secrets]))
    if needs.secrets:
        secret_reference(main, workload, app)
    resources = mapping(main, 'resources', create=True)
    for field, section, key in SCALED:
        minimum, current = needs.minimum.get(field), (resources.get(section) or {}).get(key)
        if minimum is not None and (current is None or quantity(current) < quantity(minimum)):
            mapping(resources, section, create=True)[key] = minimum
            changes.append(f'{section}.{key} {current} -> {minimum} (the image\'s minimum)')
    requests, limits = resources.get('requests') or {}, resources.get('limits') or {}
    if 'memory' in requests and 'memory' in limits and quantity(requests['memory']) > quantity(limits['memory']):
        raise EditError(f'resources: a memory request of {requests["memory"]} is above the memory limit {limits["memory"]}; '
                        'declare memoryLimit too')
    rolling = (workload.get('strategy') or 'RollingUpdate') != 'Recreate'
    if over := over_quota([(workload.get('replicas', 1) + rolling, [resources])]):
        raise EditError(f"resources: the image's minimum {needs.minimum} at {workload.get('replicas', 1)} replica(s) "
                        f'exceeds the namespace quota for {", ".join(over)}')
    return changes


def deploy_staging(root: Path, app: str, *, token: str = '', opener: offers.Opener = offers._open,
                   runner: Runner | None = None, found: images.Image | None = None, refresh: bool = False) -> bool:
    """Staging runs the newest image its ImagePolicy accepts (``found``, or read now), with what that
    image needs and offers: the tag and digest from the registry (images.py), then its swhurl.yaml read
    at its commit, whose needs are written into the release (apply_needs) and whose offers beside it.
    An image already deployed is not read again unless ``refresh``. A refused need or offer holds the
    image: the instance keeps what it had. Returns whether anything changed."""
    found = found or newest_image(root, app, opener=opener)
    instance = instance_dir(root, app, 'staging')
    release = instance / 'helmrelease.yaml'
    touched = [release, instance / 'namespace.yaml', root / 'platform/reloader/helmrelease.yaml']
    before = {path: path.read_bytes() for path in touched if path.is_file()}
    document = YamlFile(release)
    image = promotion.image_of(document.data)
    moved = (image.get('tag'), image.get('digest')) != (found.tag, found.digest)
    read_at = ((document.data.get('metadata') or {}).get('annotations') or {}).get(offers.ANNOTATION)
    if not refresh and not moved and offers.IMAGE_COMMIT.match(found.tag) and read_at == offers.commit_of(found.tag):
        print(f'[OK] {app}/staging runs its newest image {found.tag}; its needs and offers were read at {read_at}')
        return False
    previous, port = image.get('tag'), service_port(values(document.data))
    image['tag'], image['digest'] = found.tag, found.digest
    try:
        declared = image_declaration(document.data, app, 'staging', token=token, opener=opener)
        needed = apply_needs(document.data, app, declared.needs)
        document.save()
        if 'data' in (values(document.data).get('persistence') or {}):
            namespace = YamlFile(instance / 'namespace.yaml')
            metadata = mapping(namespace.data, 'metadata')
            if (metadata.get('annotations') or {}).get(PRUNE_DISABLED) != 'disabled':
                # The namespace, and with it the claim, stays when the unit goes (destroy-data deletes it).
                mapping(metadata, 'annotations', create=True)[PRUNE_DISABLED] = 'disabled'
                namespace.save()
        if declared.needs.secrets:
            watch_namespace(root, Instance(app, 'staging').namespace)
        production = instance_folder(root, app, 'prod')
        if (production / 'helmrelease.yaml').is_file() and (drift := policy.drift([production, instance])):
            raise EditError(f"{'; '.join(drift)}: a promotion does not carry what an image needs yet, so a need "
                            'may change only for an app without production')
        changed = set_offers(root, app, 'staging', token=token, opener=opener, runner=runner, declared=declared)
        if service_port(values(YamlFile(release).data)) != port:
            sync_env(root, 'staging')  # its callers' address and its NetworkPolicy
    except EditError:
        for path, content in before.items():
            path.write_bytes(content)
        raise
    if moved:
        print(f'[OK] {app}/staging: image {previous} -> {found.tag}')
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
    instance = instance_dir(root, app, env)
    path, runner = instance / SECRET_FILE, runner or Runner()
    relative = path.relative_to(root).as_posix()  # .sops.yaml's rules match this path
    try:
        if path.is_file():
            if 'stringData' not in YamlFile(path).data:
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
    document = YamlFile(instance / 'helmrelease.yaml')
    secret_reference(container(document.data), controller(document.data), app)
    document.save()
    kustomization = YamlFile(instance / 'kustomization.yaml')
    if SECRET_FILE not in kustomization.data['resources']:
        kustomization.data['resources'].insert(1, SECRET_FILE)
        kustomization.save()
    unit = YamlFile(instance_unit_file(root, app, env))
    if 'decryption' not in mapping(unit.data, 'spec'):
        unit.data['spec']['decryption'] = {'provider': 'sops', 'secretRef': {'name': 'sops-age'}}
        unit.save()
    watch_namespace(root, Instance(app, env).namespace)
    print(f'[OK] {app}/{env}: {name} set (value not shown)')
    if name not in declared_secrets(document.data):
        print(f'[INFO] {name} is not among the secrets its image declares yet: the app receives it all the same')
    return instance


def carry_offers(root: Path, app: str, *, runner: Runner | None = None) -> bool:
    """Production offers what the image it now runs offers: staging's documents, read for that image."""
    try:
        found = offers.read(instance_folder(root, app, 'staging'))
    except offers.OfferError as error:
        raise EditError(f'{app}/staging: {error}') from None
    if found:
        tag = str(helmrelease.image(YamlFile(instance_folder(root, app, 'staging') / 'helmrelease.yaml').data).get('tag', ''))
        if offers.IMAGE_COMMIT.match(tag) and offers.commit_of(tag) != found.commit:
            raise EditError(f"{app}/staging runs {tag} but its offers were read at {found.commit}: read them for this image first "
                            f'(make app-offers APP={app} ENV=staging; the Deploy workflow does it with each new image)')
    folder = instance_folder(root, app, 'staging') / offers.OFFERS_DIR
    files = {name: (folder / name).read_text() for name in filter(None, (found.api, found.events, found.flags))} if found else {}
    served = str(helmrelease.variables(YamlFile(instance_folder(root, app, 'staging') / 'helmrelease.yaml').data).get(API_VARIABLE, ''))
    return write_offers(root, app, 'prod', found.repository if found else '', found.commit if found else '', files,
                        list(found.described) if found and found.described is not None else None,
                        api_path=served if found else '', runner=runner)


def set_team(root: Path, app: str, name: str) -> Path:
    """Move every environment of an app to a team, and give each its quota and NetworkPolicy if it lacks them."""
    if name not in teams(root):
        raise EditError(f'unknown team {name!r}; registered in apps/teams.yaml: {", ".join(teams(root))}')
    found = [env for env in ENVIRONMENTS if (instance_folder(root, app, env) / 'helmrelease.yaml').is_file()]
    if not found:
        instance_dir(root, app, ENVIRONMENTS[0])  # raises, naming what is missing
    edits: list[tuple[Path, str]] = []
    for env in found:
        instance = instance_dir(root, app, env)
        namespace = YamlFile(instance / 'namespace.yaml')
        labels = mapping(mapping(namespace.data, 'metadata'), 'labels')
        if labels.get(TEAM) != name:
            labels[TEAM] = name
            edits.append((namespace.path, namespace.render()))
        wanted = isolation_edits(instance, app, env, labels.get(EXPOSURE), YamlFile(instance / 'helmrelease.yaml').data)
        edits += [(path, text) for path, text in wanted if not path.exists() or path.read_text() != text]
    if not edits:
        raise EditError(f'{app} already belongs to {name}')
    for path, text in edits:
        path.write_text(text)
    print(f'[OK] {app}: team {name} ({", ".join(found)})')
    return instance_folder(root, app, found[0])


def set_alerts(root: Path, app: str, changes: dict[str, str] | None = None, *, none: bool = False,
               default: bool = False) -> Path:
    """Change an app's default alerts in every environment: a threshold or ``none`` per alert (``changes``),
    no alert at all, or back to the defaults. The dashboard job replaces the alerts within a minute."""
    found = [env for env in ENVIRONMENTS if (instance_folder(root, app, env) / 'namespace.yaml').is_file()]
    if not found:
        instance_dir(root, app, ENVIRONMENTS[0])  # raises, naming what is missing
    if sum((bool(changes), none, default)) != 1:
        raise EditError('give thresholds (--error-logs, --errors), or --none, or --default')
    namespaces = [YamlFile(instance_folder(root, app, env) / 'namespace.yaml') for env in found]
    current = next((str(annotations[app_alerts.ANNOTATION]) for n in namespaces
                    if app_alerts.ANNOTATION in (annotations := mapping(n.data, 'metadata').get('annotations') or {})), '')
    try:
        settings = app_alerts.thresholds(current)
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
    edits = 0
    for namespace in namespaces:
        metadata = mapping(namespace.data, 'metadata')
        annotations = metadata.get('annotations') or {}
        if str(annotations.get(app_alerts.ANNOTATION, '')) == spec:
            continue
        if spec:
            mapping(metadata, 'annotations', create=True)[app_alerts.ANNOTATION] = spec
        else:
            del annotations[app_alerts.ANNOTATION]
            if not annotations:
                del metadata['annotations']
        namespace.save()
        edits += 1
    if not edits:
        raise EditError(f'{app} already has those alert settings')
    described = ', '.join(f'{key} {"none" if value is None else f">= {value}"}' for key, value in app_alerts.thresholds(spec).items())
    print(f'[OK] {app}: alerts {described} ({", ".join(found)})')
    return instance_folder(root, app, found[0])


def promote(root: Path, app: str, source: str = 'staging', target: str = 'prod', *,
            expect_image: str | None = None, expect_config: str | None = None,
            host: str | None = None, runner=None) -> Path:
    if (source, target) != ('staging', 'prod'):
        raise EditError('promotion must differ between environments and always goes staging → prod')
    src, dst = promotion.read(root, app)
    decision = promotion.decide(src, dst)
    if expect_image is not None and decision.image != expect_image:
        raise EditError(f'{app}/staging image changed since it was reviewed; refresh staging and review it again')
    if expect_config is not None and promotion.fingerprint(root, app, 'staging') != expect_config:
        raise EditError('staging configuration changed since it was reviewed; refresh and review it again')
    if decision.outcome == 'same':
        raise EditError(f'{app}/prod already runs this digest; same image in both')
    if decision.outcome == 'create':
        missing = [callee for callee in declared_calls(helmrelease.variables(src))
                   if not (instance_folder(root, callee, target) / 'helmrelease.yaml').is_file()]
        if missing:
            raise EditError(f'{app} calls {", ".join(missing)}, which has no production instance: promote that first')
        result = promotion.create(root, app, src, host=host, runner=runner)
        if events.has_nats(root):
            nats_key(root, app, target, runner=runner)  # production's own identity, never staging's
        sync_env(root, target)  # production's own callers and callees
        carry_offers(root, app, runner=runner)
        print(f'[OK] created {app}/prod from reviewed staging settings (independent storage and credentials)')
        if (instance_folder(root, app, target) / 'secret.sops.yaml').is_file():  # not the events key, which is made here
            print(f'[INFO] Set production secrets before merging, in your own terminal: make app-secret APP={app} ENV=prod NAME=<name> '
                  '(make check-secrets lists the placeholders left)')
        egress_difference(app, src, YamlFile(instance_folder(root, app, target) / 'helmrelease.yaml').data)
        return result
    if host:
        raise EditError('--host applies only to first production; change existing routes with app-expose')
    document = YamlFile(instance_folder(root, app, 'prod') / 'helmrelease.yaml')
    current = mapping(container(document.data), 'image')
    image = promotion.image_of(src)
    current.update({k: str(image[k]) for k in ('tag', 'digest') if k in image})
    if 'tag' not in image:
        current.pop('tag', None)
    before = document.path.read_text()
    document.save()
    try:
        carry_offers(root, app, runner=runner)  # what this image offers; refused if production still uses what it dropped
    except EditError:
        document.path.write_text(before)
        raise
    print(f'[OK] {app}/prod: image {decision.image} (from staging)' + (' [rollback]' if decision.rollback else ''))
    egress_difference(app, src, document.data)
    return instance_folder(root, app, 'prod')


def egress_difference(app: str, staging: dict, production: dict) -> None:
    """Say which hosts staging may reach and production may not: a promotion never grants them."""
    missing = [name for name in egress.granted(staging) if name not in egress.granted(production)]
    if missing:
        print(f'[INFO] staging may reach {", ".join(missing)}; production may not. A promotion never widens that: '
              f'make app-egress APP={app} ENV=prod ARGS="--add {",".join(missing)}"')


def image_minimum(root: Path, app: str, env: str, *, token: str = '', opener: offers.Opener = offers._open) -> dict:
    """The resources the image ``app/env`` runs needs at least (``resources`` in its swhurl.yaml), by
    app-scale option; empty for an image no app repository built. Unreachable GitHub is a warning:
    the next image raises anything below its minimum."""
    release = YamlFile(instance_dir(root, app, env) / 'helmrelease.yaml').data
    image = helmrelease.image(release)
    if not str(image.get('repository', '')).startswith('ghcr.io/') or not offers.IMAGE_COMMIT.match(str(image.get('tag', ''))):
        return {}
    try:
        return image_declaration(release, app, env, token=token, opener=opener).needs.minimum
    except EditError as error:
        print(f"[WARN] could not read the image's minimum resources, so they are not checked: {error}", file=sys.stderr)
        return {}


def scale(root: Path, app: str, env: str, *, replicas: int | None = None, cpu: str | None = None,
          memory: str | None = None, memory_limit: str | None = None, minimum: dict | None = None) -> Path:
    """Change replicas or resources; never resources below ``minimum``, what the image needs (image_minimum)."""
    if replicas is None and not (cpu or memory or memory_limit):
        raise EditError('give at least one of --replicas, --cpu, --memory, --memory-limit')
    if replicas is not None and not 0 <= replicas <= MAX_REPLICAS:
        raise EditError(f'--replicas must be 0 to {MAX_REPLICAS}')
    for value, pattern, flag in ((cpu, CPU_RE, '--cpu'), (memory, MEMORY_RE, '--memory'),
                                 (memory_limit, MEMORY_RE, '--memory-limit')):
        if value and not pattern.match(value):
            raise EditError(f'{flag} {value!r} is not a quantity like ' + ('100m or 0.5' if flag == '--cpu' else '64Mi'))
    below = [f'--{field.replace("_", "-")} {value} (at least {(minimum or {})[field]})'
             for field, value in (('cpu', cpu), ('memory', memory), ('memory_limit', memory_limit))
             if value and field in (minimum or {}) and quantity(value) < quantity(minimum[field])]
    if below:
        raise EditError(f"below the image's minimum, from resources in its {declaration.FILE}: {', '.join(below)}")
    instance = instance_dir(root, app, env)
    document = YamlFile(instance / 'helmrelease.yaml')
    main = controller(document.data)
    resources = mapping(container(document.data), 'resources', create=True) if cpu or memory or memory_limit else None
    changes = []
    if replicas is not None and main.get('replicas', 1) != replicas:
        if 'replicas' in main:
            main['replicas'] = replicas
        else:
            main.insert(0, 'replicas', replicas)
        changes.append(f'replicas {replicas}')
    for section, key, value in (('requests', 'cpu', cpu), ('requests', 'memory', memory), ('limits', 'memory', memory_limit)):
        if value and mapping(resources, section, create=True).get(key) != value:
            resources[section][key] = value
            changes.append(f'{section}.{key} {value}')
    if not changes:
        raise EditError(f'{app}/{env} already has those settings')
    document.save()
    print(f'[OK] {app}/{env}: {", ".join(changes)}')
    return instance


def validate_main_route(route: dict | None) -> str | None:
    """Refuse ambiguous or shared route nodes and return the existing host."""
    if route:
        hosts, tls = route.get('hosts'), route.get('tls')
        if (not isinstance(hosts, list) or len(hosts) != 1 or not isinstance(hosts[0], dict)
                or not isinstance(hosts[0].get('host'), str)
                or not isinstance(tls, list) or len(tls) != 1 or not isinstance(tls[0], dict)
                or tls[0].get('hosts') != [hosts[0]['host']]):
            raise EditError('main ingress: expected one host and matching TLS entry; edit custom routes by hand')
        # These sequences are edited in place, so shared values are ambiguous too.
        for value in (hosts, hosts[0], tls, tls[0], tls[0]['hosts']):
            if value.yaml_anchor() or getattr(value, 'merge', None):
                raise EditError('main ingress uses a YAML anchor or merge; edit shared settings by hand')
    return route['hosts'][0]['host'] if route else None


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


def update_route(app_values: dict, route: dict | None, app: str, exposure: str, host: str | None) -> None:
    """Edit the existing route in place, preserving custom settings and YAML nodes."""
    issuer = ((route or {}).get('annotations') or {}).get('cert-manager.io/cluster-issuer', 'letsencrypt-prod')
    ingress = mapping(app_values, 'ingress', create=True)
    if exposure == 'private':
        if any(key != 'main' for key in ingress):
            raise EditError('private exposure would leave additional ingress routes; edit custom routes by hand')
        ingress.pop('main', None)
        if not ingress:
            app_values.pop('ingress')
    elif route:
        route['hosts'][0]['host'] = host
        route['tls'][0]['hosts'][0] = host
        annotations = mapping(route, 'annotations', create=True)
        key = 'traefik.ingress.kubernetes.io/router.middlewares'
        middleware_value = annotations.get(key, '')
        if not isinstance(middleware_value, str):
            raise EditError(f'{key}: expected a comma-separated middleware string')
        middlewares = [m.strip() for m in middleware_value.split(',') if m.strip()]
        middlewares = [m for m in middlewares if m != AUTH_MIDDLEWARE]
        if exposure == 'authenticated-web':
            middlewares.insert(0, AUTH_MIDDLEWARE)
        if middlewares:
            annotations[key] = ','.join(middlewares)
        else:
            annotations.pop(key, None)
    else:
        ingress['main'] = ingress_values(app, host, exposure, issuer)


def update_dependencies(unit_spec: dict, exposure: str) -> None:
    """Keep custom Flux dependencies while updating the exposure dependencies."""
    dependencies = unit_spec.setdefault('dependsOn', [])
    if not isinstance(dependencies, list) or any(not isinstance(d, dict) or 'name' not in d for d in dependencies):
        raise EditError('dependsOn: expected a list of named Flux dependencies')
    if getattr(dependencies, 'yaml_anchor', lambda: None)():
        raise EditError('dependsOn uses a YAML anchor; edit shared settings by hand')
    if exposure != 'authenticated-web':
        for index in reversed(range(len(dependencies))):
            if dependencies[index]['name'] == 'platform-oauth2-proxy':
                del dependencies[index]
    for name in depends_on(exposure):
        if not any(d['name'] == name for d in dependencies):
            dependencies.append({'name': name})


def expose(root: Path, app: str, env: str, exposure: str, host: str | None = None) -> Path:
    """Change who can reach an instance: its route, sign-in, Namespace label and unit dependencies."""
    instance = instance_dir(root, app, env)
    document = YamlFile(instance / 'helmrelease.yaml')
    app_values = values(document.data)
    ingress = mapping(app_values, 'ingress') if 'ingress' in app_values else None
    route = mapping(ingress, 'main') if ingress and 'main' in ingress else None
    current_host = validate_main_route(route)
    namespace_path, unit_path = instance / 'namespace.yaml', instance_unit_file(root, app, env)
    namespace, unit = YamlFile(namespace_path), YamlFile(unit_path)
    labels = mapping(mapping(namespace.data, 'metadata'), 'labels')
    unit_spec = mapping(unit.data, 'spec')
    current = labels.get(EXPOSURE)
    if exposure != 'private' and 'service' not in app_values:
        raise EditError(f'{app}/{env} is a worker (no Service): it can only be private')
    host = resolve_host(app, env, exposure, host, current_host)
    if (exposure, host) == (current, current_host):
        raise EditError(f'{app}/{env} is already {exposure}' + (f' at {host}' if host else ''))
    update_route(app_values, route, app, exposure, host)
    labels[EXPOSURE] = exposure
    update_dependencies(unit_spec, exposure)
    # Render every file before writing any, so unsupported YAML cannot cause a
    # partially saved exposure edit. Policy still validates the resulting files.
    edits = [(file.path, file.render()) for file in (document, namespace, unit)]
    edits += isolation_edits(instance, app, env, exposure, document.data)
    for path, text in edits:
        path.write_text(text)
    print(f'[OK] {app}/{env}: {current} -> {exposure}' + (f' at https://{host}' if host else ' (no route)'))
    return instance


def remove(root: Path, app: str, env: str) -> None:
    instance = instance_dir(root, app, env)
    callers = callers_of(root, app, env)
    if callers:
        raise EditError(f'{app}/{env} is called by {", ".join(callers)}: remove those calls first '
                        f'(make app-calls APP=<caller> ENV={env} ARGS="--remove {app}")')
    subscribers = subscribers_of(root, app, env)
    if subscribers:
        raise EditError(f'{app}/{env} publishes events that {", ".join(subscribers)} subscribe to: '
                        f'remove those subscriptions first (make app-events APP=<subscriber> ENV={env})')
    namespace_file = instance / 'namespace.yaml'
    keeps_data = namespace_file.exists() and PRUNE_DISABLED in namespace_file.read_text()
    unit_file = instance_unit_file(root, app, env)
    shutil.rmtree(instance)
    print(f'[OK] removed {instance.relative_to(root)}/')
    if not any((root / 'apps' / app).iterdir()):
        (root / 'apps' / app).rmdir()
    if unit_file.exists():
        unit_file.unlink()
        print(f'[OK] removed {unit_file.relative_to(root)}')
    registry = root / 'clusters/home/kustomization.yaml'
    lines = registry.read_text().splitlines(keepends=True)
    kept = [line for line in lines if line.strip() != f'- {unit_file.name}']
    if kept != lines:
        registry.write_text(''.join(kept))
        print(f'[OK] unregistered {unit_file.name} in clusters/home/kustomization.yaml')
    unwatch_namespace(root, Instance(app, env).namespace)
    for path in sync_env(root, env):  # the apps it called no longer admit it
        print(f'[OK] rewrote {path.relative_to(root)}')
    if keeps_data:
        print(f'[WARN] {app}-{env} has a retained volume: Flux keeps its namespace and claim after removal. '
              'Delete the data only on purpose: make destroy-data (docs/operations.md#lifecycle)')


def unwatch_namespace(root: Path, namespace: str) -> None:
    path = root / 'platform/reloader/helmrelease.yaml'
    if not path.exists():
        return
    text = path.read_text()
    match = re.search(r'^(\s*namespaces:\s*)\[([^\]]*)\]', text, re.M)
    current = [n.strip() for n in match[2].split(',') if n.strip()] if match else []
    if namespace in current:
        current.remove(namespace)
        path.write_text(text[:match.start()] + f'{match[1]}[{", ".join(current)}]' + text[match.end():])
        print(f'[OK] Reloader no longer watches {namespace}')


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
    p.add_argument('--expect-config', help='refuse if the reviewed staging configuration hash changed')
    p.add_argument('--host', help='distinct production hostname for first public promotion')
    p.add_argument('--source-unchanged', metavar='APPLIED..NEWEST', default='',
                   help="the caller has checked that staging's own files are the same at these two commits "
                        '(the console, whose copy of the repository has no history to compare)')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    runner = runner or Runner()

    def changed(applied: str, newest: str) -> list[str] | None:
        """The paths that differ between two commits, from the caller's word or this checkout's history."""
        if args.source_unchanged == f'{applied}..{newest}':
            return []
        root = args.root.resolve()
        if not (root / '.git').exists():
            return None
        found = runner.run(['git', '-C', root, 'diff', '--name-only', applied, newest], check=False)
        return None if found.returncode else found.stdout.splitlines()

    def change():
        root = args.root.resolve()
        source, target = promotion.read(root, args.app)
        decision = promotion.decide(source, target)
        if args.expect_image and args.expect_image != decision.image:
            raise EditError('staging image changed since it was reviewed; refresh and review it again')
        if decision.outcome == 'same':
            raise EditError('production already runs this digest; same image in both')
        source_instance = Instance.named(args.app, 'staging')
        status = promotion.settled(ops.gather_status(runner, source_instance), changed) if source_instance else None
        problem = promotion.source_problem(status, decision.image)
        if problem:
            raise EditError(problem)
        return promote(root, args.app, expect_image=args.expect_image,
                       expect_config=args.expect_config, host=args.host, runner=runner)

    try:
        return run(change, True)
    except CommandError as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 1


def main_scale(argv: list[str] | None = None, opener: offers.Opener = offers._open) -> int:
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
        root = args.root.resolve()
        lowered = args.cpu or args.memory or args.memory_limit
        minimum = image_minimum(root, args.app, args.env, token=os.environ.get('GITHUB_TOKEN', ''), opener=opener) if lowered else {}
        return scale(root, args.app, args.env, replicas=args.replicas, cpu=args.cpu,
                     memory=args.memory, memory_limit=args.memory_limit, minimum=minimum)
    return run(change, True)


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
    p = argparse.ArgumentParser(prog='swhurl app-events', description='Set the events an instance publishes and receives')
    p.add_argument('app')
    p.add_argument('env', choices=ENVIRONMENTS)
    p.add_argument('--publish', type=names, help='comma-separated events this app publishes ("" for none)')
    p.add_argument('--subscribe', type=names, help='comma-separated <app>.<event> it receives ("" for none)')
    p.add_argument('--add-publish', type=names, default=[], help='comma-separated events to start publishing')
    p.add_argument('--remove-publish', type=names, default=[], help='comma-separated events to stop publishing')
    p.add_argument('--add-subscribe', type=names, default=[], help='comma-separated <app>.<event> to start receiving')
    p.add_argument('--remove-subscribe', type=names, default=[], help='comma-separated <app>.<event> to stop receiving')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: set_events(args.root.resolve(), args.app, args.env, publish=args.publish, subscribe=args.subscribe,
                                  add_publish=args.add_publish, remove_publish=args.remove_publish,
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
    for app in instances_in(root, args.all):
        image = helmrelease.image(YamlFile(instance_folder(root, app, args.all) / 'helmrelease.yaml').data)
        if not str(image.get('repository', '')).startswith('ghcr.io/') or not offers.IMAGE_COMMIT.match(str(image.get('tag', ''))):
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
    deployed = [app for app in instances_in(root, 'staging')  # built from an app repository: not a fixture or a pinned image
                if (instance_folder(root, app, 'staging') / IMAGE_AUTOMATION_FILE).is_file()]

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


def main_sync(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-sync', description='Rewrite what the platform derives in every instance')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    def keys_then_sync():
        root = args.root.resolve()
        if events.has_nats(root):  # every instance has its own NATS key; one made before that gets it here
            for env in ENVIRONMENTS:
                for app in instances_in(root, env):
                    if nats_key(root, app, env):
                        print(f'[OK] {app}/{env}: created its NATS key ({events.SECRET_FILE})')
        return sync(root)
    return run(keys_then_sync, False)


def main_remove(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-remove', description='Delete an app instance from Git (Flux uninstalls it)')
    p.add_argument('app')
    p.add_argument('env', choices=ENVIRONMENTS)
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: remove(args.root.resolve(), args.app, args.env), False)
