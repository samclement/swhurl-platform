"""Edit generated app instances (Git edits only, like app-new): promote, scale, remove.

    app-promote APP   create production from staging or update its image
    app-scale APP ENV [--replicas N] [--cpu Q] [--memory Q] [--memory-limit Q]
    app-expose APP ENV --exposure E [--host H]     change who can reach an instance
    app-team APP --team T                          move an app (every environment) to a registered team
    app-calls APP ENV [--add A,B] [--remove C]     change which apps an instance may call
    app-events APP ENV [--publish A,B] [--subscribe APP.EVENT,...] [--add-subscribe APP.EVENT] ...   change the events an instance publishes and receives
    app-offers APP ENV | --all ENV                 read what an app offers (its API and event documents) from its repository
    app-sync                                       rewrite what the platform derives in every instance
    app-remove APP ENV                             delete the instance's files and unregister its unit

Each edits files under the repository root and checks the result against the
app policy; commit and push to apply. Handwritten YAML retains its comments,
quotes and key order. Unsupported custom structures are refused explicitly.
"""
from __future__ import annotations

import argparse
import os
import re
import shutil
import sys
from pathlib import Path

from swhurl import ROOT, app_alerts
from swhurl.apps import events, helmrelease, nkeys, offers, ops, promotion
from swhurl.apps.calling import (
    API_VARIABLE,
    callee_address,
    callee_variable,
    declared_calls,
)
from swhurl.apps.contract import (
    AUTH_MIDDLEWARE,
    ENVIRONMENTS,
    EXPOSURE,
    EXPOSURES,
    MANIFEST_FILE,
    TEAM,
    default_host,
    in_cookie_domain,
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
from swhurl.apps.new import (
    check_generated,
    depends_on,
    dump,
    ingress_values,
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
    secret = instance / events.SECRET_FILE
    created = False
    if active and not (variables.get(events.NKEY_VARIABLE) and secret.is_file()):
        # The seed goes from memory into a file that sops encrypts in place: it is never printed.
        seed, public = nkeys.generate()
        secret.write_text(dump([{'apiVersion': 'v1', 'kind': 'Secret',
                                 'metadata': {'name': events.secret_name(app), 'namespace': f'{app}-{env}'},
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
        created = True
    document.save()
    try:
        sync_env(root, env)
    except EditError:
        document.path.write_text(before)
        if created:
            secret.unlink(missing_ok=True)
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
        source = {'repository': repository, 'commit': commit}
        source |= {key: name for key, name in (('api', next((n for n in files if n != offers.EVENTS_NAME), '')),
                                               ('events', offers.EVENTS_NAME if offers.EVENTS_NAME in files else '')) if name}
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


def set_offers(root: Path, app: str, env: str, *, token: str = '', opener: offers.Opener = offers._open,
               runner: Runner | None = None) -> bool:
    """Read what ``app/env`` offers from its repository, at the commit its image was built from: the
    documents its swhurl.yaml names (provides.apiFile, provides.eventsFile). Returns whether anything changed."""
    instance = instance_dir(root, app, env)
    image = helmrelease.image(YamlFile(instance / 'helmrelease.yaml').data)
    repository = str(image.get('repository', ''))
    if not repository.startswith('ghcr.io/'):
        raise EditError(f'{app}/{env} runs {repository or "no image"}: only an app published from its own GitHub repository has offers')
    repository = repository.removeprefix('ghcr.io/')
    try:
        commit = offers.commit_of(str(image.get('tag', '')))
        manifest = offers.fetch(repository, commit, MANIFEST_FILE, token=token, opener=opener)
        if manifest is None:
            raise offers.OfferError(f'{repository}@{commit} has no {MANIFEST_FILE} (or the repository is private: set GITHUB_TOKEN)')
        provides = (offers._document(manifest, MANIFEST_FILE).get('provides') or {})
        files, described = {}, None
        for key, kind in (('apiFile', 'api'), ('eventsFile', 'events')):
            path = provides.get(key)
            if not path:
                continue
            text = offers.fetch(repository, commit, str(path), token=token, opener=opener)
            if text is None:
                raise offers.OfferError(f'{MANIFEST_FILE} names {path} (provides.{key}), which {repository}@{commit} does not have')
            if kind == 'api':
                offers.check_api(text, str(path))
                files['openapi' + (Path(str(path)).suffix or '.yaml')] = text
            else:
                described = offers.events(text, str(path))
                files[offers.EVENTS_NAME] = text
    except offers.OfferError as error:
        raise EditError(str(error)) from None
    served = provides.get('api')
    served = served if isinstance(served, str) and re.fullmatch(r'/[A-Za-z0-9_./-]*', served) else ''
    changed = write_offers(root, app, env, repository, commit, files, described, api_path=served, runner=runner)
    what = ', '.join(filter(None, [next((f'its API ({n})' for n in files if n != offers.EVENTS_NAME), ''),
                                   described is not None and f'events: {", ".join(e.name for e in described) or "none"}']))
    print(f'[OK] {app}/{env} at {commit} offers {what or "nothing"}' + ('' if changed else ' (unchanged)'))
    return changed


def carry_offers(root: Path, app: str, *, runner: Runner | None = None) -> bool:
    """Production offers what the image it now runs offers: staging's documents, read for that image."""
    try:
        found = offers.read(root / 'apps' / app / 'staging')
    except offers.OfferError as error:
        raise EditError(f'{app}/staging: {error}') from None
    if found:
        tag = str(helmrelease.image(YamlFile(root / 'apps' / app / 'staging' / 'helmrelease.yaml').data).get('tag', ''))
        if offers.IMAGE_COMMIT.match(tag) and offers.commit_of(tag) != found.commit:
            raise EditError(f"{app}/staging runs {tag} but its offers were read at {found.commit}: read them for this image first "
                            f'(make app-offers APP={app} ENV=staging; the Offers workflow does it after each new image)')
    folder = root / 'apps' / app / 'staging' / offers.OFFERS_DIR
    files = {name: (folder / name).read_text() for name in filter(None, (found.api, found.events))} if found else {}
    served = str(helmrelease.variables(YamlFile(root / 'apps' / app / 'staging' / 'helmrelease.yaml').data).get(API_VARIABLE, ''))
    return write_offers(root, app, 'prod', found.repository if found else '', found.commit if found else '', files,
                        list(found.described) if found and found.described is not None else None,
                        api_path=served if found else '', runner=runner)


def set_team(root: Path, app: str, name: str) -> Path:
    """Move every environment of an app to a team, and give each its quota and NetworkPolicy if it lacks them."""
    if name not in teams(root):
        raise EditError(f'unknown team {name!r}; registered in apps/teams.yaml: {", ".join(teams(root))}')
    found = [env for env in ENVIRONMENTS if (root / 'apps' / app / env / 'helmrelease.yaml').is_file()]
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
    return root / 'apps' / app / found[0]


def set_alerts(root: Path, app: str, changes: dict[str, str] | None = None, *, none: bool = False,
               default: bool = False) -> Path:
    """Change an app's default alerts in every environment: a threshold or ``none`` per alert (``changes``),
    no alert at all, or back to the defaults. The dashboard job replaces the alerts within a minute."""
    found = [env for env in ENVIRONMENTS if (root / 'apps' / app / env / 'namespace.yaml').is_file()]
    if not found:
        instance_dir(root, app, ENVIRONMENTS[0])  # raises, naming what is missing
    if sum((bool(changes), none, default)) != 1:
        raise EditError('give thresholds (--error-logs, --errors), or --none, or --default')
    namespaces = [YamlFile(root / 'apps' / app / env / 'namespace.yaml') for env in found]
    current = next((str(annotations[app_alerts.ANNOTATION]) for n in namespaces
                    if app_alerts.ANNOTATION in (annotations := mapping(n.data, 'metadata').get('annotations') or {})), '')
    try:
        settings = app_alerts.thresholds(current)
        for key, value in (changes or {}).items():
            settings |= {key: app_alerts.thresholds(f'{key}={value}')[key]}
    except ValueError as error:
        raise EditError(str(error)) from None
    spec = app_alerts.OFF if none else '' if default else app_alerts.spec_of(settings)
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
    return root / 'apps' / app / found[0]


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
                   if not (root / 'apps' / callee / target / 'helmrelease.yaml').is_file()]
        if missing:
            raise EditError(f'{app} calls {", ".join(missing)}, which has no production instance: promote that first')
        result = promotion.create(root, app, src, host=host, runner=runner)
        sync_env(root, target)  # production's own callers and callees
        carry_offers(root, app, runner=runner)
        print(f'[OK] created {app}/prod from reviewed staging settings (independent storage and credentials)')
        if container(src).get('envFrom'):
            print(f'[INFO] Set production secrets with sops apps/{app}/prod/secret.sops.yaml before merging')
        return result
    if host:
        raise EditError('--host applies only to first production; change existing routes with app-expose')
    document = YamlFile(root / 'apps' / app / 'prod/helmrelease.yaml')
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
    return root / 'apps' / app / 'prod'


def scale(root: Path, app: str, env: str, *, replicas: int | None = None, cpu: str | None = None,
          memory: str | None = None, memory_limit: str | None = None) -> Path:
    if replicas is None and not (cpu or memory or memory_limit):
        raise EditError('give at least one of --replicas, --cpu, --memory, --memory-limit')
    if replicas is not None and not 0 <= replicas <= MAX_REPLICAS:
        raise EditError(f'--replicas must be 0 to {MAX_REPLICAS}')
    for value, pattern, flag in ((cpu, CPU_RE, '--cpu'), (memory, MEMORY_RE, '--memory'),
                                 (memory_limit, MEMORY_RE, '--memory-limit')):
        if value and not pattern.match(value):
            raise EditError(f'{flag} {value!r} is not a quantity like ' + ('100m or 0.5' if flag == '--cpu' else '64Mi'))
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


def expose(root: Path, app: str, env: str, exposure: str, host: str | None = None) -> Path:
    """Change who can reach an instance: its route, sign-in, Namespace label and unit dependencies."""
    instance = instance_dir(root, app, env)
    document = YamlFile(instance / 'helmrelease.yaml')
    app_values = values(document.data)
    ingress = mapping(app_values, 'ingress') if 'ingress' in app_values else None
    route = mapping(ingress, 'main') if ingress and 'main' in ingress else None
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
    current_host = route['hosts'][0]['host'] if route else None
    namespace_path, unit_path = instance / 'namespace.yaml', root / 'clusters/home' / f'app-{app}-{env}.yaml'
    namespace, unit = YamlFile(namespace_path), YamlFile(unit_path)
    labels = mapping(mapping(namespace.data, 'metadata'), 'labels')
    unit_spec = mapping(unit.data, 'spec')
    current = labels.get(EXPOSURE)
    if exposure != 'private' and 'service' not in app_values:
        raise EditError(f'{app}/{env} is a worker (no Service): it can only be private')
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
    if (exposure, host) == (current, current_host):
        raise EditError(f'{app}/{env} is already {exposure}' + (f' at {host}' if host else ''))
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
    labels[EXPOSURE] = exposure
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
    unit_file = root / 'clusters/home' / f'app-{app}-{env}.yaml'
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
    unwatch_namespace(root, f'{app}-{env}')
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
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    runner = runner or Runner()

    def change():
        root = args.root.resolve()
        source, target = promotion.read(root, args.app)
        decision = promotion.decide(source, target)
        if args.expect_image and args.expect_image != decision.image:
            raise EditError('staging image changed since it was reviewed; refresh and review it again')
        if decision.outcome == 'same':
            raise EditError('production already runs this digest; same image in both')
        status = ops.gather_status(runner, ops.Instance(args.app, 'staging'))
        problem = promotion.source_problem(status, decision.image, status.applied_revision if status else '')
        if problem:
            raise EditError(problem)
        return promote(root, args.app, expect_image=args.expect_image,
                       expect_config=args.expect_config, host=args.host, runner=runner)

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
    return run(lambda: scale(args.root.resolve(), args.app, args.env, replicas=args.replicas, cpu=args.cpu,
                             memory=args.memory, memory_limit=args.memory_limit), True)


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
        return run(lambda: set_offers(root, args.app, args.env, token=token, opener=opener), True)
    failed = 0
    for app in instances_in(root, args.all):
        image = helmrelease.image(YamlFile(root / 'apps' / app / args.all / 'helmrelease.yaml').data)
        if not str(image.get('repository', '')).startswith('ghcr.io/') or not offers.IMAGE_COMMIT.match(str(image.get('tag', ''))):
            continue  # not built from an app repository (a fixture, a pinned third-party image)
        try:
            set_offers(root, app, args.all, token=token, opener=opener)
        except EditError as error:  # the others are still read; this one keeps what it had
            failed += 1
            print(f'[ERROR] {app}/{args.all}: {error}', file=sys.stderr)
    return 1 if failed else 0


def main_sync(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-sync', description='Rewrite what the platform derives in every instance')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: sync(args.root.resolve()), False)


def main_remove(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-remove', description='Delete an app instance from Git (Flux uninstalls it)')
    p.add_argument('app')
    p.add_argument('env', choices=ENVIRONMENTS)
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: remove(args.root.resolve(), args.app, args.env), False)
