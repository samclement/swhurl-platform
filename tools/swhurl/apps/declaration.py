"""swhurl.yaml: what an app is, needs and offers, kept in the app's own repository.

Each field is one of four kinds, and ``FIELDS`` says which (docs/apps.md#swhurlyaml shows the same table,
and a test compares them; GLOSSARY.md defines the words):

- **identity**: what the app is (``version``, ``stack``, ``kind``, ``description``).
- **need**: what the image needs to run (port, probes, user, telemetry, resources, database, secret names).
- **offer**: what other apps can use (``provides``, ``flags``); AsyncAPI send operations declare publications.
- **grant**: what the platform allows the app (team, exposure, alerts, calls, ``events.subscribe``). It
  belongs to this repository and is refused in the file, naming the command that sets it.

Each supported field is read again from the app's repository. The needs (``Needs``) are written
with every new staging image (edit.deploy_staging); offers are read at that image's commit too.
``kind`` is checked but cannot change. Grant fields are refused.

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

from swhurl import yamlio
from swhurl.apps.contract import DEFAULT_DATABASE_SIZE, TEMPLATE_PORT, TEMPLATE_UID, secret_key_problem
from swhurl.apps.instance import ENVIRONMENTS
from swhurl.apps.model import records

FILE = 'swhurl.yaml'
VERSION = 1
TELEMETRY = ('otlp', 'none')
MAX_BYTES = 512 * 1024
MAX_DESCRIPTION = 200
"""One line, as the New app form and make app-repo take it."""
LIVE, IGNORED, REFUSED = 'live', 'ignored', 'refused'
IDENTITY, NEED, OFFER, GRANT, DROPPED = 'identity', 'need', 'offer', 'grant', 'dropped'
WITH_IMAGE = 'with every new staging image (make app-deploy, the Deploy workflow)'
Opener = Callable[[urllib.request.Request], bytes]


@dataclass(frozen=True)
class Field:
    name: str  # a top-level field, or events.subscribe
    kind: str  # IDENTITY, NEED, OFFER, GRANT or DROPPED
    read: str  # LIVE, IGNORED or REFUSED (every grant)
    then: str  # live: when it is read again; refused: what sets it


FIELDS = (
    Field('version', IDENTITY, LIVE, 'every time the file is read: another version is refused'),
    Field('stack', IDENTITY, LIVE, "by the console, from the repository's default branch, for the language it shows"),
    Field('kind', IDENTITY, LIVE, 'with every new staging image, and checked: web, worker or job is fixed at creation, '
          'and an image that changes it is held'),
    Field('description', IDENTITY, LIVE, WITH_IMAGE + ', as SWHURL_DESCRIPTION; a promotion carries it with the image'),
    Field('port', NEED, LIVE, WITH_IMAGE),
    Field('healthPath', NEED, LIVE, WITH_IMAGE),
    Field('uid', NEED, LIVE, WITH_IMAGE),
    Field('telemetry', NEED, LIVE, WITH_IMAGE),
    Field('resources', NEED, LIVE, WITH_IMAGE + ', as the minimum: make app-scale may set more, never less'),
    Field('database', NEED, LIVE, WITH_IMAGE + ': a new one creates its volume; removing one is refused while it exists'),
    Field('databaseSize', NEED, LIVE, WITH_IMAGE + ', for a new database: a volume keeps the size it was created with'),
    Field('secrets', NEED, LIVE, WITH_IMAGE + ': a new name deploys without its value until make app-secret sets it'),
    Field('provides', OFFER, LIVE, "by make app-offers, at the commit of every new staging image (the Deploy workflow)"),
    Field('flags', OFFER, LIVE, "by the app's own build, which generates a typed accessor for each flag"),
    Field('team', GRANT, REFUSED, 'make app-team'),
    Field('exposure', GRANT, REFUSED, 'make app-expose'),
    Field('alerts', GRANT, REFUSED, 'make app-alerts'),
    Field('calls', GRANT, REFUSED, 'make app-calls'),
    Field('events.subscribe', GRANT, REFUSED, 'make app-events'),
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
class Needs:
    """What the image needs to run that shapes its release, read with every new staging image."""
    kind: str
    port: int | None  # None for a worker
    health_path: str
    uid: int
    otlp: bool
    minimum: dict  # resources the image needs at least, by app-scale option: cpu, memory, memory_limit
    database: str = ''  # sqlite, or '' for none
    database_size: str = ''  # the volume a new database is created with
    secrets: tuple[str, ...] = ()  # the secret variable names it reads


@dataclass(frozen=True)
class Declaration:
    """A valid swhurl.yaml."""
    seed: dict
    """app-new's defaults, by option name: every seed field, and what the live ones mean for a new instance."""
    stack: str = ''
    description: str = ''  # one line saying what the app does
    api_path: str = ''  # provides.api: where a web app serves its API description
    api_file: str = ''  # provides.apiFile: where the repository keeps its OpenAPI document
    events_file: str = ''  # provides.eventsFile: where it keeps its AsyncAPI document
    flags: dict = dataclasses.field(default_factory=dict)  # flags: name -> its declaration, as written

    @property
    def needs(self) -> Needs:
        seed = self.seed
        return Needs(seed['kind'], seed.get('port'), seed.get('health_path') or '', seed['uid'], seed['otlp'],
                     {key: seed[key] for key in RESOURCES if key in seed}, seed.get('database') or '',
                     seed.get('database_size') or (DEFAULT_DATABASE_SIZE if seed.get('database') else ''),
                     tuple(seed.get('secret_keys') or ()))


RESOURCES = {'cpu': 'cpu', 'memory': 'memory', 'memory_limit': 'memoryLimit'}
"""The image's minimum resources: app-scale option -> swhurl.yaml key."""
QUANTITY = {'cpu': re.compile(r'\d+(\.\d+)?m?'), 'memory': re.compile(r'\d+(Ki|Mi|Gi)'), 'memory_limit': re.compile(r'\d+(Ki|Mi|Gi)')}


def _resource_defaults(resources: dict, fail: Callable[[str], DeclarationError]) -> dict:
    defaults: dict = {}
    if set(resources) - set(RESOURCES.values()):
        raise fail(f'resources may set {", ".join(RESOURCES.values())} only')
    for dest, key in RESOURCES.items():
        if resources.get(key) is not None:
            if not isinstance(resources[key], str) or not QUANTITY[dest].fullmatch(resources[key]):
                raise fail(f'resources.{key} must be a quantity string, for example ' + ('"100m"' if dest == 'cpu' else '"64Mi"'))
            defaults[dest] = resources[key]
    return defaults


def _check_events(declared: dict, fail: Callable[[str], DeclarationError]) -> None:
    if 'publish' in declared:
        raise fail('events.publish is no longer supported: declare send operations in asyncapi.yaml and name it '
                   'with provides.eventsFile; remove events.publish')
    if set(declared) - {'subscribe'}:
        raise fail('events may not declare publications here: use provides.eventsFile and asyncapi.yaml')


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

    kind = field('kind', str, records.KINDS)
    if not kind:
        raise fail(f'kind is required ({", ".join(records.KINDS)})')
    description = field('description', str) or ''
    if len(description) > MAX_DESCRIPTION or description != ' '.join(description.split()):
        raise fail(f'description is one line of at most {MAX_DESCRIPTION} characters, without leading, trailing or repeated spaces')
    present = {item.name for item in FIELDS if item.read == REFUSED and doc.get(item.name) is not None}
    if isinstance(doc.get('events'), dict) and doc['events'].get('subscribe') is not None:
        present.add('events.subscribe')
    if refused := [item for item in FIELDS if item.name in present]:
        raise fail(f'{", ".join(item.name for item in refused)}: a grant, set on the platform, not in this file '
                   f'({"; ".join(dict.fromkeys(item.then for item in refused))}, or the console); remove it')
    field('autoDeploy', bool)
    defaults: dict = {'kind': kind, 'uid': field('uid', int) or TEMPLATE_UID,
                      'otlp': field('telemetry', str, TELEMETRY) == 'otlp',
                      'auto_deploy': True,  # autoDeploy is accepted and ignored (checked as a boolean above)
                      'exposure': 'authenticated-web' if kind == 'web' else 'private'}
    if kind == 'web':
        defaults['port'] = field('port', int) or TEMPLATE_PORT
        defaults['health_path'] = field('healthPath', str)
        if not defaults['health_path']:
            raise fail('healthPath is required for kind web (the app\'s readiness endpoint)')
    elif any(doc.get(k) is not None for k in ('port', 'healthPath')):
        raise fail('port and healthPath apply to kind web only')
    if field('database', str, records.DATABASES):
        if kind != 'web':
            raise fail(f'database applies to kind web only: a {kind} keeps what it needs in an API it calls '
                       '(docs/apps.md#app-types)')
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
    _check_events(field('events', dict) or {}, fail)
    provides = field('provides', dict) or {}
    defaults.update(_provides_defaults(provides, kind, fail))
    flags = field('flags', dict) or {}
    for name, flag in flags.items():
        if problem := flag_problem(name, flag):
            raise fail(problem)
    if flags:
        defaults['offers'] = True  # app-new keeps them beside the instance from the start (make app-offers)
    if description:
        defaults['description'] = description
    return Declaration(defaults, field('stack', str) or '', description, provides.get('api') or '',
                       provides.get('apiFile') or '', provides.get('eventsFile') or '', flags)


def parse(text: str, source: str = FILE) -> Declaration:
    try:
        return of(yamlio.load(text), source)
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
