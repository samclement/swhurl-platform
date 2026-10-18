"""Read the records back from today's manifests: the shadow's input until the record files exist.

Offline: the image's swhurl.yaml is not read. Its resource minimum is taken to be whatever the
instance runs with when that differs from the default, which holds while no instance has been scaled
(docs/plan.md section 24, stage 0 inventory). Removed at the cutover, with the manifests as inputs.
"""
from __future__ import annotations

from pathlib import Path

from swhurl import app_alerts, yamlio
from swhurl.apps import egress, events, helmrelease, offers
from swhurl.apps.calling import API_VARIABLE, DESCRIPTION_VARIABLE, declared_calls, signing_keys
from swhurl.apps.contract import (
    DATABASE_PATH_VARIABLE,
    EXCEPTIONS,
    EXPOSURE,
    JOB_SCHEDULE,
    SECRET_FILE,
    SECRET_NAMES,
    TEAM,
    default_host,
)
from swhurl.apps.instance import Instance, instances
from swhurl.apps.model.draft import APP_SECRET, EVENTS_SECRET, Entry, Platform, World
from swhurl.apps.model.exposure import DEFAULT_ISSUER
from swhurl.apps.model.records import Allocations, App, Image, Release, Resources, Settings, Storage
from swhurl.apps.model.workload import DEFAULT_RESOURCES


def platform(root: Path, base: Path = Path('apps')) -> Platform:
    def key(path: Path) -> str:
        return (root / path).read_text().strip() if (root / path).is_file() else ''
    return Platform(signing_keys(root), (root / egress.PROXY_FILE).parent.is_dir(),
                    key(events.CONTROLLER_KEY_FILE), key(events.CONSOLE_KEY_FILE), base)


def _annotations(document: dict) -> dict:
    return (document.get('metadata') or {}).get('annotations') or {}


def read(folder: Path, each: Instance) -> Entry:
    namespace = yamlio.load((folder / 'namespace.yaml').read_text())
    release = yamlio.load((folder / 'helmrelease.yaml').read_text())
    labels = (namespace.get('metadata') or {}).get('labels') or {}
    variables = helmrelease.variables(release)
    container = helmrelease.container(release)
    kind = helmrelease.kind(release)
    image = container.get('image') or {}
    requests = (container.get('resources') or {}).get('requests') or {}
    limits = (container.get('resources') or {}).get('limits') or {}
    running = Resources(str(requests.get('cpu', '')), str(requests.get('memory', '')), str(limits.get('memory', '')))
    probe = ((container.get('probes') or {}).get('readiness') or {}).get('spec') or {}
    pod = ((helmrelease.values(release).get('defaultPodOptions') or {}).get('securityContext') or {})
    database = 'sqlite' if DATABASE_PATH_VARIABLE in variables else ''
    annotations = _annotations(release)
    ingress = (helmrelease.values(release).get('ingress') or {}).get('main') or {}
    hosts = helmrelease.hosts(release)
    issuer = (ingress.get('annotations') or {}).get('cert-manager.io/cluster-issuer', '')
    workload = helmrelease.workload(release)
    replicas = workload.get('replicas')
    data = (helmrelease.values(release).get('persistence') or {}).get('data')
    subscriptions = [name for name in str(variables.get(events.SUBSCRIBE_VARIABLE, '')).split(',') if name]
    schedule = helmrelease.schedule(release)
    found = Release(
        image=Image(str(image.get('repository', '')), str(image.get('tag', '')), str(image.get('digest', ''))),
        kind=kind, uid=int(pod.get('runAsUser', 0)), command=tuple(str(part) for part in container.get('command') or ()),
        port=helmrelease.port(release) if kind == 'web' else None,
        health_path=str(((probe.get('httpGet') or {}).get('path')) or '') if kind == 'web' else '',
        telemetry='OTEL_EXPORTER_OTLP_ENDPOINT' in variables,
        resources=Resources() if running == DEFAULT_RESOURCES else running, database=database,
        secrets=tuple(name for name in str(annotations.get(SECRET_NAMES, '')).split(',') if name),
        description=str(variables.get(DESCRIPTION_VARIABLE, '')), api_path=str(variables.get(API_VARIABLE, '')),
        publishes=tuple(events.published(variables)), offers=str(annotations.get(offers.ANNOTATION, '')))
    settings = Settings(
        exposure=str(labels.get(EXPOSURE, 'private')),
        host=hosts[0] if hosts and hosts[0] != default_host(each.app, each.env) else '',
        issuer=issuer if issuer and issuer != DEFAULT_ISSUER else '',
        calls=tuple(declared_calls(variables)), subscriptions=tuple(subscriptions), egress=tuple(egress.granted(release)),
        replicas=None if database and replicas == 1 else replicas,
        schedule=schedule if schedule != JOB_SCHEDULE else '', exceptions=str(annotations.get(EXCEPTIONS, '')),
        allocations=Allocations(egress.port_of(variables), str(variables.get(events.NKEY_VARIABLE, '')),
                                Storage(str(data.get('size', '')), str(data.get('storageClass', ''))) if data else None))
    app = App(str(labels.get(TEAM, '')), str(_annotations(namespace).get(app_alerts.ANNOTATION, '')))
    encrypted = frozenset(name for name, file in ((EVENTS_SECRET, events.SECRET_FILE), (APP_SECRET, SECRET_FILE))
                          if (folder / file).is_file())
    return Entry(app, found, settings, encrypted)


def world(root: Path, base: Path = Path('apps')) -> tuple[World, list[str]]:
    """Every instance's records under ``root``, and why any could not be read or its environments disagree on the app."""
    found: World = {}
    problems: list[str] = []
    for each in instances(root / base):
        try:
            found[each] = read(each.folder(root, base), each)
        except (OSError, ValueError, TypeError, KeyError, AttributeError) as error:
            problems.append(f'{each}: cannot read its records: {error}')
    apps: dict[str, set[App]] = {}
    for each, entry in found.items():
        apps.setdefault(each.app, set()).add(entry.app)
    problems += [f'{app}: its environments disagree on team or alerts' for app, seen in sorted(apps.items()) if len(seen) > 1]
    return found, problems
