"""swhurl.yaml: what an app says it needs from the platform, kept in the app's own repository.

One file, two lifetimes, and ``FIELDS`` says which each field has (docs/apps.md#swhurlyaml shows the
same table, and a test compares them):

- **seed**: read once, when ``app-new`` creates the staging instance. From then on the instance in
  this repository is the truth; a later edit of the field in the app's repository changes nothing,
  and the command named beside it is how the instance changes.
- **live**: read again from the app's repository, so an edit there reaches the platform.

Every reader goes through ``read`` (GitHub, at a ref) or ``parse`` (text in hand), so the file has
one fetcher and one validation: unknown fields and other versions are refused wherever it is read.
The name, environment, image and host belong to each instance and are never in the file.
"""
from __future__ import annotations

import re
import urllib.error
import urllib.request
from collections.abc import Callable
from dataclasses import dataclass

import yaml

from swhurl import app_alerts
from swhurl.apps.contract import DATABASES, EXPOSURES, TEMPLATE_PORT, TEMPLATE_UID, secret_key_problem

FILE = 'swhurl.yaml'
VERSION = 1
KINDS = ('web', 'worker')
TELEMETRY = ('otlp', 'none')
MAX_BYTES = 512 * 1024
SEED, LIVE = 'seed', 'live'
INSTANCE = "the instance's files"
Opener = Callable[[urllib.request.Request], bytes]


@dataclass(frozen=True)
class Field:
    name: str
    read: str  # SEED or LIVE
    then: str  # seed: what changes the instance afterwards; live: when it is read again


FIELDS = (
    Field('version', LIVE, 'every time the file is read: another version is refused'),
    Field('stack', LIVE, "by the console, from the repository's default branch, for the language it shows"),
    Field('kind', SEED, 'nothing: web or worker is fixed at creation'),
    Field('port', SEED, INSTANCE),
    Field('healthPath', SEED, INSTANCE),
    Field('uid', SEED, INSTANCE),
    Field('telemetry', SEED, INSTANCE),
    Field('autoDeploy', SEED, 'nothing: staging always follows new images'),
    Field('database', SEED, INSTANCE),
    Field('databaseSize', SEED, INSTANCE),
    Field('secrets', SEED, INSTANCE),
    Field('resources', SEED, 'make app-scale'),
    Field('exposure', SEED, 'make app-expose'),
    Field('team', SEED, 'make app-team'),
    Field('alerts', SEED, 'make app-alerts'),
    Field('calls', SEED, 'make app-calls'),
    Field('events', SEED, 'make app-events; what it publishes follows provides.eventsFile once the app has one'),
    Field('provides', LIVE, "by make app-offers, at the commit of every new staging image (the Offers workflow)"),
)


class DeclarationError(ValueError):
    """The file cannot be used: it says why, naming where it was read from."""


class Unreachable(DeclarationError):
    """GitHub could not be asked: nothing is known about the file."""


@dataclass(frozen=True)
class Declaration:
    """A valid swhurl.yaml."""
    seed: dict
    """app-new's defaults, by option name: every seed field, and what the live ones mean for a new instance."""
    stack: str = ''
    api_path: str = ''  # provides.api: where a web app serves its API description
    api_file: str = ''  # provides.apiFile: where the repository keeps its OpenAPI document
    events_file: str = ''  # provides.eventsFile: where it keeps its AsyncAPI document


def _resource_defaults(resources: dict, fail: Callable[[str], DeclarationError]) -> dict:
    defaults: dict = {}
    names = {'cpu': 'cpu', 'memory': 'memory', 'memoryLimit': 'memory_limit'}
    if set(resources) - set(names):
        raise fail(f'resources may set {", ".join(names)} only')
    for key, dest in names.items():
        if resources.get(key) is not None:
            if not isinstance(resources[key], str):
                raise fail(f'resources.{key} must be a quantity string, for example "64Mi"')
            defaults[dest] = resources[key]
    return defaults


def _event_defaults(declared: dict, fail: Callable[[str], DeclarationError]) -> dict:
    defaults: dict = {}
    if set(declared) - {'publish', 'subscribe'}:
        raise fail('events may set publish and subscribe only')
    for key, dest, pattern in (('publish', 'publish', r'[a-z]([a-z0-9-]{0,38}[a-z0-9])?'),
                               ('subscribe', 'subscribe', r'[a-z]([a-z0-9-]{0,38}[a-z0-9])?\.[a-z]([a-z0-9-]{0,38}[a-z0-9])?')):
        names = declared.get(key) or []
        if not isinstance(names, list) or not all(isinstance(n, str) and re.fullmatch(pattern, n) for n in names) \
                or len(set(names)) != len(names):
            raise fail('events.publish lists event names (created); events.subscribe lists <app>.<event> (orders.created)')
        if names:
            defaults[dest] = names
    return defaults


def _provides_defaults(provides: dict, kind: str, fail: Callable[[str], DeclarationError]) -> dict:
    defaults: dict = {}
    if set(provides) - {'api', 'apiFile', 'eventsFile'} or not isinstance(provides.get('api', '/'), str) \
            or not provides.get('api', '/').startswith('/'):
        raise fail('provides may set api, a path such as /openapi.json that the app serves, and apiFile and eventsFile only')
    for key in ('apiFile', 'eventsFile'):
        # Where the app's repository keeps its OpenAPI and AsyncAPI documents: what it offers other apps.
        path = provides.get(key)
        if path is not None and (not isinstance(path, str) or not re.fullmatch(r'[A-Za-z0-9_][A-Za-z0-9_.-]*(/[A-Za-z0-9_][A-Za-z0-9_.-]*)*', path)):
            raise fail(f'provides.{key} is a file in the repository, such as {"openapi.json" if key == "apiFile" else "asyncapi.yaml"}')
    if provides.get('apiFile') or provides.get('eventsFile'):
        defaults['offers'] = True  # app-new reads them from the repository (make app-offers)
    if provides.get('apiFile') and kind != 'web':
        raise fail('provides.apiFile applies to kind web only')
    if provides.get('api'):
        if kind != 'web':
            raise fail('provides.api applies to kind web only')
        defaults['api_path'] = provides['api']
    return defaults


def _call_defaults(calls: list | None, fail: Callable[[str], DeclarationError]) -> dict:
    defaults: dict = {}
    if calls:
        if not all(isinstance(name, str) and re.fullmatch(r'[a-z]([a-z0-9-]{0,38}[a-z0-9])?', name) for name in calls) \
                or len(set(calls)) != len(calls):
            raise fail('calls must be a list of distinct app names')
        defaults['calls'] = calls
    return defaults


def of(doc: object, source: str = FILE) -> Declaration:
    """The declaration a parsed document makes; DeclarationError naming the first problem."""
    def fail(message: str) -> DeclarationError:
        return DeclarationError(f'{source}: {message}')

    if not isinstance(doc, dict):
        raise fail('must be a mapping')
    if doc.get('version') != VERSION:
        raise fail(f'version must be {VERSION} (this platform reads version {VERSION})')
    allowed = {item.name for item in FIELDS}
    unknown = sorted(set(doc) - allowed)
    if unknown:
        raise fail(f'unknown field(s) {", ".join(unknown)}; allowed: {", ".join(sorted(allowed))}')

    def field(key: str, kind: type | tuple, choices: tuple = ()):
        value = doc.get(key)
        if value is None:
            return None
        if not isinstance(value, kind) or isinstance(value, bool) and kind is int:
            raise fail(f'{key} must be {getattr(kind, "__name__", kind)}')
        if choices and value not in choices:
            raise fail(f'{key} must be one of {", ".join(choices)}')
        return value

    kind = field('kind', str, KINDS)
    if not kind:
        raise fail(f'kind is required ({", ".join(KINDS)})')
    defaults: dict = {'kind': kind, 'uid': field('uid', int) or TEMPLATE_UID,
                      'otlp': field('telemetry', str, TELEMETRY) == 'otlp',
                      'auto_deploy': bool(field('autoDeploy', bool)),
                      'exposure': field('exposure', str, EXPOSURES)
                      or ('authenticated-web' if kind == 'web' else 'private')}
    if kind == 'web':
        defaults['port'] = field('port', int) or TEMPLATE_PORT
        defaults['health_path'] = field('healthPath', str)
        if not defaults['health_path']:
            raise fail('healthPath is required for kind web (the app\'s readiness endpoint)')
    elif any(doc.get(k) is not None for k in ('port', 'healthPath')):
        raise fail('port and healthPath apply to kind web only')
    if field('database', str, DATABASES):
        defaults['database'] = doc['database']
        if field('databaseSize', str):
            defaults['database_size'] = doc['databaseSize']
    elif doc.get('databaseSize') is not None:
        raise fail('databaseSize needs database')
    secrets = field('secrets', list)
    if secrets:
        if not all(isinstance(k, str) for k in secrets):
            raise fail('secrets must be environment variable names (A-Z, 0-9, _)')
        if problem := secret_key_problem(secrets):
            raise fail(problem)
        defaults['secret_keys'] = secrets
    defaults.update(_resource_defaults(field('resources', dict) or {}, fail))
    defaults.update(_event_defaults(field('events', dict) or {}, fail))
    provides = field('provides', dict) or {}
    defaults.update(_provides_defaults(provides, kind, fail))
    defaults.update(_call_defaults(field('calls', list), fail))
    if field('team', str):
        defaults['team'] = doc['team']  # checked against the registry by app-new
    if field('alerts', str):
        try:
            app_alerts.thresholds(doc['alerts'])
        except ValueError as error:
            raise fail(f'alerts: {error}') from None
        defaults['alerts'] = doc['alerts']
    return Declaration(defaults, field('stack', str) or '', provides.get('api') or '',
                       provides.get('apiFile') or '', provides.get('eventsFile') or '')


def parse(text: str, source: str = FILE) -> Declaration:
    try:
        return of(yaml.safe_load(text), source)
    except yaml.YAMLError as error:
        raise DeclarationError(f'{source}: not valid YAML: {error}') from None


def _open(request: urllib.request.Request) -> bytes:
    with urllib.request.urlopen(request, timeout=30) as reply:  # noqa: S310 (https only, built below)
        return reply.read(MAX_BYTES + 1)


def read(repository: str, ref: str = '', *, token: str = '', opener: Opener = _open,
         api: str = 'https://api.github.com') -> Declaration | None:
    """The swhurl.yaml of ``OWNER/REPO`` on GitHub at a ref (the default branch without one).

    None when the repository has no such file there, or is private and no token allows it.
    """
    where = f'{repository}@{ref}' if ref else repository
    headers = {'Accept': 'application/vnd.github.raw+json', 'X-GitHub-Api-Version': '2022-11-28', 'User-Agent': 'swhurl-platform'}
    if token:
        headers['Authorization'] = f'Bearer {token}'
    try:
        body = opener(urllib.request.Request(f'{api}/repos/{repository}/contents/{FILE}' + (f'?ref={ref}' if ref else ''), headers=headers))
    except urllib.error.HTTPError as error:
        if error.code == 404:
            return None
        raise Unreachable(f'could not read {FILE} from {where}: {error.code} {error.reason}') from None
    except (urllib.error.URLError, OSError) as error:
        raise Unreachable(f'could not reach GitHub for {where}: {getattr(error, "reason", error)}') from None
    if len(body) > MAX_BYTES:
        raise DeclarationError(f'{where}:{FILE} is larger than {MAX_BYTES // 1024} KiB')
    return parse(body.decode(), f'{where}:{FILE}')
