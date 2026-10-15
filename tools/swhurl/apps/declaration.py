"""swhurl.yaml: what an app is, needs and offers, kept in the app's own repository.

Each field is one of four kinds, and ``FIELDS`` says which (docs/apps.md#swhurlyaml shows the same table,
and a test compares them; GLOSSARY.md defines the words):

- **identity**: what the app is (``version``, ``stack``, ``kind``).
- **need**: what the image needs to run (port, probes, user, telemetry, resources, database, secret names).
- **offer**: what other apps can use (``provides``, ``flags``, ``events.publish``).
- **grant**: what the platform allows the app (team, exposure, alerts, calls, ``events.subscribe``). It
  belongs to this repository; ``app-new`` still takes one from the file at creation.

Separately, each field is read at one of two times today:

- **seed**: read once, when ``app-new`` creates the staging instance. From then on the instance in
  this repository is the truth; a later edit of the field in the app's repository changes nothing,
  and the command named beside it is how the instance changes.
- **live**: read again from the app's repository, so an edit there reaches the platform.

``autoDeploy`` is accepted and ignored: staging always follows new images.

Every reader goes through ``read`` (GitHub, at a ref) or ``parse`` (text in hand), so the file has
one fetcher and one validation: unknown fields and other versions are refused wherever it is read.
The name, environment, image and host belong to each instance and are never in the file.
"""
from __future__ import annotations

import dataclasses
import math
import re
import urllib.error
import urllib.request
from collections.abc import Callable
from dataclasses import dataclass

import yaml

from swhurl import app_alerts
from swhurl.apps.contract import DATABASES, EXPOSURES, TEMPLATE_PORT, TEMPLATE_UID, secret_key_problem
from swhurl.apps.instance import ENVIRONMENTS

FILE = 'swhurl.yaml'
VERSION = 1
KINDS = ('web', 'worker')
TELEMETRY = ('otlp', 'none')
MAX_BYTES = 512 * 1024
SEED, LIVE, IGNORED = 'seed', 'live', 'ignored'
IDENTITY, NEED, OFFER, GRANT, DROPPED = 'identity', 'need', 'offer', 'grant', 'dropped'
INSTANCE = "the instance's files"
Opener = Callable[[urllib.request.Request], bytes]


@dataclass(frozen=True)
class Field:
    name: str  # a top-level field, or events.publish and events.subscribe, which differ in kind
    kind: str  # IDENTITY, NEED, OFFER, GRANT or DROPPED
    read: str  # SEED, LIVE or IGNORED
    then: str  # seed: what changes the instance afterwards; live: when it is read again


FIELDS = (
    Field('version', IDENTITY, LIVE, 'every time the file is read: another version is refused'),
    Field('stack', IDENTITY, LIVE, "by the console, from the repository's default branch, for the language it shows"),
    Field('kind', IDENTITY, SEED, 'nothing: web or worker is fixed at creation'),
    Field('port', NEED, SEED, INSTANCE),
    Field('healthPath', NEED, SEED, INSTANCE),
    Field('uid', NEED, SEED, INSTANCE),
    Field('telemetry', NEED, SEED, INSTANCE),
    Field('resources', NEED, SEED, 'make app-scale'),
    Field('database', NEED, SEED, INSTANCE),
    Field('databaseSize', NEED, SEED, INSTANCE),
    Field('secrets', NEED, SEED, INSTANCE),
    Field('provides', OFFER, LIVE, "by make app-offers, at the commit of every new staging image (the Offers workflow)"),
    Field('flags', OFFER, LIVE, "by the app's own build, which generates a typed accessor for each flag"),
    Field('events.publish', OFFER, SEED, 'make app-events; once the app has provides.eventsFile, that file instead'),
    Field('team', GRANT, SEED, 'make app-team'),
    Field('exposure', GRANT, SEED, 'make app-expose'),
    Field('alerts', GRANT, SEED, 'make app-alerts'),
    Field('calls', GRANT, SEED, 'make app-calls'),
    Field('events.subscribe', GRANT, SEED, 'make app-events'),
    Field('autoDeploy', DROPPED, IGNORED, 'nothing: staging always follows new images'),
)
FLAG_NAME = re.compile(r'[a-z][a-z0-9]*(-[a-z][a-z0-9]*)*')
"""A flag's name: kebab case where each part starts with a letter, so its camelCase accessor is unique."""
FLAG_TYPES = {'boolean': (bool,), 'integer': (int,), 'number': (int, float), 'string': (str,)}
INT_RANGE = (-2**31, 2**31 - 1)
"""An integer flag is an Int in Kotlin: 32 bits."""


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
    flags: dict = dataclasses.field(default_factory=dict)  # flags: name -> its declaration, as written


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


def flag_problem(name: str, flag: object) -> str:
    """Why one declared flag cannot be used, or ''. The templates' generators apply the same rules."""
    if not isinstance(name, str) or len(name) > 40 or not FLAG_NAME.fullmatch(name):
        return f'flag name {name!r} must be kebab case, each part starting with a letter (new-checkout), at most 40 characters'
    if not isinstance(flag, dict):
        return f'flags.{name} must be a mapping with type and default'
    kind = flag.get('type')
    if kind not in FLAG_TYPES:
        return f'flags.{name}.type must be one of {", ".join(FLAG_TYPES)}'
    allowed = {'type', 'description', 'default', *ENVIRONMENTS} | ({'min', 'max'} if kind in ('integer', 'number') else set()) \
        | ({'enum'} if kind == 'string' else set())
    if unknown := sorted(set(flag) - allowed):
        return f'flags.{name} may not set {", ".join(unknown)} (a {kind} flag sets {", ".join(sorted(allowed))})'
    if not isinstance(flag.get('description', ''), str):
        return f'flags.{name}.description must be text'
    if 'default' not in flag:
        return f'flags.{name}.default is required'

    def typed(value: object) -> bool:
        if not isinstance(value, FLAG_TYPES[kind]) or isinstance(value, bool) and kind != 'boolean':
            return False
        if kind == 'integer':
            return INT_RANGE[0] <= value <= INT_RANGE[1]
        return kind != 'number' or math.isfinite(value)
    for bound in ('min', 'max'):
        if bound in flag and not (isinstance(flag[bound], (int, float)) and not isinstance(flag[bound], bool)
                                  and (kind == 'number' or isinstance(flag[bound], int))):
            return f'flags.{name}.{bound} must be a{"n integer" if kind == "integer" else " number"}'
    if 'min' in flag and 'max' in flag and flag['min'] > flag['max']:
        return f'flags.{name}.min is above its max'
    enum = flag.get('enum')
    if enum is not None and (not isinstance(enum, list) or not enum or not all(isinstance(v, str) for v in enum)
                             or len(set(enum)) != len(enum)):
        return f'flags.{name}.enum must list distinct strings'
    for key in ('default', *ENVIRONMENTS):
        if key not in flag:
            continue
        value = flag[key]
        if not typed(value):
            return f'flags.{name}.{key} must be a{"n" if kind == "integer" else ""} {kind}, not {value!r}'
        if kind in ('integer', 'number') and not (flag.get('min', value) <= value <= flag.get('max', value)):
            return f'flags.{name}.{key} is outside min and max'
        if enum is not None and value not in enum:
            return f'flags.{name}.{key} must be one of {", ".join(enum)}'
    return ''


def of(doc: object, source: str = FILE) -> Declaration:
    """The declaration a parsed document makes; DeclarationError naming the first problem."""
    def fail(message: str) -> DeclarationError:
        return DeclarationError(f'{source}: {message}')

    if not isinstance(doc, dict):
        raise fail('must be a mapping')
    if doc.get('version') != VERSION:
        raise fail(f'version must be {VERSION} (this platform reads version {VERSION})')
    allowed = {item.name.split('.')[0] for item in FIELDS}
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
    field('autoDeploy', bool)
    defaults: dict = {'kind': kind, 'uid': field('uid', int) or TEMPLATE_UID,
                      'otlp': field('telemetry', str, TELEMETRY) == 'otlp',
                      'auto_deploy': True,  # autoDeploy is accepted and ignored (checked as a boolean above)
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
    flags = field('flags', dict) or {}
    for name, flag in flags.items():
        if problem := flag_problem(name, flag):
            raise fail(problem)
    if flags:
        defaults['offers'] = True  # app-new keeps them beside the instance from the start (make app-offers)
    return Declaration(defaults, field('stack', str) or '', provides.get('api') or '',
                       provides.get('apiFile') or '', provides.get('eventsFile') or '', flags)


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
