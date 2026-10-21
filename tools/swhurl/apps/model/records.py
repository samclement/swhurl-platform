"""App, Release and Settings: plain values, parsed strictly from the documents that will hold them.

Standard library only (no ruamel.yaml, no I/O), so the offline policy check may import it. ``parse``
takes the document as loaded YAML and refuses an unknown version or field; ``document`` gives it
back. Optional fields are left out of a document when empty, so a record and its document round-trip.
"""
from __future__ import annotations

import re
from dataclasses import dataclass, field

VERSION = 1
APP_FILE, RELEASE_FILE, SETTINGS_FILE = 'app.yaml', 'release.yaml', 'instance.yaml'
RECORD_FILES = frozenset({RELEASE_FILE, SETTINGS_FILE})
"""An instance folder's record files: parsed strictly, never rendered or applied."""
KINDS = ('web', 'worker', 'job')
EXPOSURES = ('private', 'authenticated-web', 'public')
DATABASES = ('sqlite',)
NAME_RE = re.compile(r'^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$')
SUBSCRIPTION_RE = re.compile(r'^[a-z]([a-z0-9-]{0,38}[a-z0-9])?\.[a-z]([a-z0-9-]{0,38}[a-z0-9])?$')
TEAM_GRANT = 'team:'
ALLOW_RE = re.compile(r'^(team:)?[a-z]([a-z0-9-]{0,38}[a-z0-9])?$')
"""An ``allows`` entry: an app's name, or ``team:<team>`` for every app of that team (no wildcard)."""


class RecordError(ValueError):
    """A record document that cannot be used: the message names the field."""


def _fields(doc: object, where: str, required: tuple[str, ...], optional: tuple[str, ...]) -> dict:
    if not isinstance(doc, dict):
        raise RecordError(f'{where}: expected a mapping')
    unknown = sorted(set(doc) - set(required) - set(optional))
    if unknown:
        raise RecordError(f'{where}: unknown field {", ".join(unknown)}')
    missing = [name for name in required if name not in doc]
    if missing:
        raise RecordError(f'{where}: {", ".join(missing)} is required')
    return doc


def _version(doc: dict, where: str) -> None:
    if doc.get('version') != VERSION:
        raise RecordError(f'{where}: version must be {VERSION}')


def _text(doc: dict, name: str, where: str, default: str = '') -> str:
    value = doc.get(name, default)
    if not isinstance(value, str):
        raise RecordError(f'{where}: {name} must be text')
    return value


def _texts(doc: dict, name: str, where: str, pattern: re.Pattern | None = None) -> tuple[str, ...]:
    value = doc.get(name, [])
    if not isinstance(value, list) or not all(isinstance(item, str) for item in value):
        raise RecordError(f'{where}: {name} must be a list of text')
    bad = [item for item in value if pattern and not pattern.match(item)]
    if bad:
        raise RecordError(f'{where}: {name}: not allowed: {", ".join(bad)}')
    return tuple(value)


def _number(doc: dict, name: str, where: str) -> int | None:
    value = doc.get(name)
    if value is not None and (not isinstance(value, int) or isinstance(value, bool) or value < 0):
        raise RecordError(f'{where}: {name} must be a whole number')
    return value


def _choice(value: str, choices: tuple[str, ...], name: str, where: str) -> str:
    if value not in choices:
        raise RecordError(f'{where}: {name} must be one of {", ".join(choices)}')
    return value


def _compact(doc: dict) -> dict:
    """Leave out what is empty, so an optional field absent and empty read the same."""
    return {key: value for key, value in doc.items() if value not in (None, '', [], {}, ())}


@dataclass(frozen=True)
class Resources:
    """CPU request, memory request and memory limit, as Kubernetes quantities; '' for none."""
    cpu: str = ''
    memory: str = ''
    memory_limit: str = ''

    @classmethod
    def parse(cls, doc: object, where: str) -> Resources:
        doc = _fields(doc, where, (), ('cpu', 'memory', 'memoryLimit'))
        return cls(_text(doc, 'cpu', where), _text(doc, 'memory', where), _text(doc, 'memoryLimit', where))

    def document(self) -> dict:
        return _compact({'cpu': self.cpu, 'memory': self.memory, 'memoryLimit': self.memory_limit})

    def __bool__(self) -> bool:
        return bool(self.cpu or self.memory or self.memory_limit)


@dataclass(frozen=True)
class App:
    """``apps/<app>/app.yaml``: who owns the app and its alert settings (app_alerts' spec; '' is the defaults)."""
    team: str
    alerts: str = ''

    @classmethod
    def parse(cls, doc: object, where: str = 'app.yaml') -> App:
        doc = _fields(doc, where, ('version', 'team'), ('alerts',))
        _version(doc, where)
        team = _text(doc, 'team', where)
        if not NAME_RE.match(team):
            raise RecordError(f'{where}: team must be a team name')
        return cls(team, _text(doc, 'alerts', where))

    def document(self) -> dict:
        return {'version': VERSION, **_compact({'team': self.team, 'alerts': self.alerts})}


@dataclass(frozen=True)
class Image:
    repository: str
    tag: str = ''
    digest: str = ''

    def document(self) -> dict:
        return _compact({'repository': self.repository, 'tag': self.tag, 'digest': self.digest})


@dataclass(frozen=True)
class Release:
    """``release.yaml``: the selected image and what it declares it needs and offers. Grants nothing."""
    image: Image
    kind: str
    uid: int
    stack: str = ''
    command: tuple[str, ...] = ()
    port: int | None = None  # web only
    health_path: str = ''  # web only
    telemetry: bool = False
    resources: Resources = Resources()  # the image's minimum; empty: the platform's default
    database: str = ''
    secrets: tuple[str, ...] = ()
    description: str = ''
    api_path: str = ''
    publishes: tuple[str, ...] = ()
    offers: str = ''  # the commit its offered documents were read at

    FIELDS = ('stack', 'command', 'port', 'healthPath', 'telemetry', 'resources', 'database', 'secrets',
              'description', 'apiPath', 'publishes', 'offers')

    @classmethod
    def parse(cls, doc: object, where: str = 'release.yaml') -> Release:
        doc = _fields(doc, where, ('version', 'image', 'kind', 'uid'), cls.FIELDS)
        _version(doc, where)
        image = _fields(doc['image'], f'{where}: image', ('repository',), ('tag', 'digest'))
        kind = _choice(_text(doc, 'kind', where), KINDS, 'kind', where)
        port = _number(doc, 'port', where)
        health = _text(doc, 'healthPath', where)
        if kind == 'web' and not (port and health):
            raise RecordError(f'{where}: a web image needs port and healthPath')
        if kind != 'web' and (port or health):
            raise RecordError(f'{where}: only a web image has port and healthPath')
        telemetry = doc.get('telemetry', False)
        if not isinstance(telemetry, bool):
            raise RecordError(f'{where}: telemetry must be true or false')
        database = _text(doc, 'database', where)
        if database:
            _choice(database, DATABASES, 'database', where)
        return cls(Image(*(_text(image, key, where) for key in ('repository', 'tag', 'digest'))), kind,
                   _number(doc, 'uid', where) or 0, _text(doc, 'stack', where), _texts(doc, 'command', where), port, health,
                   telemetry, Resources.parse(doc.get('resources', {}), f'{where}: resources'), database,
                   _texts(doc, 'secrets', where), _text(doc, 'description', where), _text(doc, 'apiPath', where),
                   _texts(doc, 'publishes', where, NAME_RE), _text(doc, 'offers', where))

    def document(self) -> dict:
        return {'version': VERSION, 'image': self.image.document(), 'kind': self.kind, 'uid': self.uid, **_compact({
            'stack': self.stack, 'command': list(self.command), 'port': self.port, 'healthPath': self.health_path,
            'telemetry': self.telemetry or None, 'resources': self.resources.document(), 'database': self.database,
            'secrets': list(self.secrets), 'description': self.description, 'apiPath': self.api_path,
            'publishes': list(self.publishes), 'offers': self.offers})}


@dataclass(frozen=True)
class Storage:
    size: str
    storage_class: str


@dataclass(frozen=True)
class Allocations:
    """Values chosen once and kept by every render: never chosen by rendering itself."""
    egress_port: int | None = None
    events_key: str = ''  # the NATS public key; its seed is in the instance's events.sops.yaml
    database: Storage | None = None

    @classmethod
    def parse(cls, doc: object, where: str) -> Allocations:
        doc = _fields(doc, where, (), ('egressPort', 'eventsKey', 'database'))
        storage = None
        if 'database' in doc:
            found = _fields(doc['database'], f'{where}: database', ('size', 'storageClass'), ())
            storage = Storage(_text(found, 'size', where), _text(found, 'storageClass', where))
        return cls(_number(doc, 'egressPort', where), _text(doc, 'eventsKey', where), storage)

    def document(self) -> dict:
        database = {'size': self.database.size, 'storageClass': self.database.storage_class} if self.database else None
        return _compact({'egressPort': self.egress_port, 'eventsKey': self.events_key, 'database': database})


@dataclass(frozen=True)
class Grants:
    """Reviewed permissions for this environment; relations retain their existing semantics."""
    exposure: str = 'private'
    calls: tuple[str, ...] = ()
    allows: tuple[str, ...] = ()  # <app> or team:<team>, this environment only
    subscriptions: tuple[str, ...] = ()  # <app>.<event>
    egress: tuple[str, ...] = ()  # host:port grants
    exceptions: str = ''  # reviewed '<rule>=<reason>', comma-separated (docs/apps.md#the-app-policy)

    @property
    def subscribed(self) -> list[tuple[str, str]]:
        """``(publishing app, event)`` pairs."""
        return [tuple(item.split('.', 1)) for item in self.subscriptions]  # type: ignore[misc]


@dataclass(frozen=True)
class Choices:
    """Explicit environment configuration, independent of the selected Release."""
    host: str = ''  # '' is the platform's address for a signed-in app
    issuer: str = ''  # '' is the platform's certificate issuer
    replicas: int | None = None
    capacity: Resources = Resources()  # the image's minimum still wins when higher
    schedule: str = ''  # a job's; '' is the platform's default


@dataclass(frozen=True)
class Settings:
    """``instance.yaml``: parsed parts, serialized in the existing flat version-1 document."""
    grants: Grants = field(default_factory=Grants)
    choices: Choices = field(default_factory=Choices)
    allocations: Allocations = field(default_factory=Allocations)

    FIELDS = ('exposure', 'host', 'issuer', 'calls', 'allows', 'subscriptions', 'egress', 'replicas', 'capacity',
              'schedule', 'exceptions', 'allocations')

    @classmethod
    def parse(cls, doc: object, where: str = 'instance.yaml') -> Settings:
        doc = _fields(doc, where, ('version',), cls.FIELDS)
        _version(doc, where)
        return cls(Grants(_choice(_text(doc, 'exposure', where, 'private'), EXPOSURES, 'exposure', where),
                          _texts(doc, 'calls', where, NAME_RE), _texts(doc, 'allows', where, ALLOW_RE),
                          _texts(doc, 'subscriptions', where, SUBSCRIPTION_RE), _texts(doc, 'egress', where),
                          _text(doc, 'exceptions', where)),
                   Choices(_text(doc, 'host', where), _text(doc, 'issuer', where), _number(doc, 'replicas', where),
                           Resources.parse(doc.get('capacity', {}), f'{where}: capacity'), _text(doc, 'schedule', where)),
                   Allocations.parse(doc.get('allocations', {}), f'{where}: allocations'))

    def document(self) -> dict:
        grants, choices = self.grants, self.choices
        return {'version': VERSION, **_compact({
            'exposure': grants.exposure, 'host': choices.host, 'issuer': choices.issuer, 'calls': list(grants.calls),
            'allows': list(grants.allows), 'subscriptions': list(grants.subscriptions), 'egress': list(grants.egress),
            'replicas': choices.replicas, 'capacity': choices.capacity.document(), 'schedule': choices.schedule,
            'exceptions': grants.exceptions, 'allocations': self.allocations.document()})}
