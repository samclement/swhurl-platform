"""What an app offers other apps: its API and event descriptions (docs/apps.md#what-an-app-offers).

An app's repository may keep an OpenAPI document for its routes and an AsyncAPI document for the
events it publishes, named in its swhurl.yaml (provides.apiFile, provides.eventsFile). app-offers
(edit.py) reads both at the commit an instance's image was built from and keeps them beside the
instance, where the offline checks and the console read them:

    apps/<app>/<env>/offers/source.yaml      where they came from: repository, commit, file names
    apps/<app>/<env>/offers/asyncapi.yaml    the events, as the repository has them
    apps/<app>/<env>/offers/openapi.<ext>    the API, as the repository has it

The AsyncAPI document is the declaration of what the app publishes: SWHURL_EVENTS_PUBLISH on the
instance is derived from it (rule offers). Nothing here edits an instance or imports what does, so
the policy check can use it.
"""
from __future__ import annotations

import re
import urllib.error
import urllib.request
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path

import yaml

OFFERS_DIR = 'offers'
SOURCE_FILE = 'source.yaml'
EVENTS_NAME = 'asyncapi.yaml'
ANNOTATION = 'platform.swhurl.com/offers'
"""On the instance's HelmRelease: the commit its offers were read at. The console sees from it that
the app describes itself, without a variable that would restart the app when it changes."""
IMAGE_COMMIT = re.compile(r'^[0-9]+-([0-9a-f]{7,40})$')
"""An app image's tag is <run>-<commit>: the commit says which revision of the repository it is."""
EVENT_RE = re.compile(r'^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$')
FILE_RE = re.compile(r'^[A-Za-z0-9_][A-Za-z0-9_.-]*(/[A-Za-z0-9_][A-Za-z0-9_.-]*)*$')
MAX_BYTES = 512 * 1024
Opener = Callable[[urllib.request.Request], bytes]


class OfferError(Exception):
    pass


def _open(request: urllib.request.Request) -> bytes:
    with urllib.request.urlopen(request, timeout=30) as reply:  # noqa: S310 (https only, built below)
        return reply.read(MAX_BYTES + 1)


def commit_of(tag: str) -> str:
    match = IMAGE_COMMIT.match(tag or '')
    if not match:
        raise OfferError(f'image tag {tag!r} is not <run>-<commit>, so the commit it was built from is unknown')
    return match[1]


def fetch(repository: str, commit: str, path: str, *, token: str = '', opener: Opener = _open,
          api: str = 'https://api.github.com') -> str | None:
    """One file of a GitHub repository at a commit; None if the repository has no such file there."""
    if not FILE_RE.match(path):
        raise OfferError(f'{path!r} is not a file in the repository')
    headers = {'Accept': 'application/vnd.github.raw+json', 'X-GitHub-Api-Version': '2022-11-28',
               'User-Agent': 'swhurl-app-offers'}
    if token:
        headers['Authorization'] = f'Bearer {token}'
    try:
        body = opener(urllib.request.Request(f'{api}/repos/{repository}/contents/{path}?ref={commit}', headers=headers))
    except urllib.error.HTTPError as error:
        if error.code == 404:
            return None
        raise OfferError(f'could not read {path} from {repository}@{commit}: {error.code} {error.reason}') from None
    except urllib.error.URLError as error:
        raise OfferError(f'could not reach GitHub for {repository}: {error.reason}') from None
    if len(body) > MAX_BYTES:
        raise OfferError(f'{path} in {repository} is larger than {MAX_BYTES // 1024} KiB')
    return body.decode()


def _document(text: str, name: str) -> dict:
    try:
        doc = yaml.safe_load(text)
    except yaml.YAMLError as error:
        raise OfferError(f'{name} is not valid YAML or JSON: {str(error).splitlines()[0]}') from None
    if not isinstance(doc, dict):
        raise OfferError(f'{name} must be a mapping')
    return doc


def _settled(doc: dict, node: object, name: str) -> dict:
    """``node`` with one local reference ("#/components/...") followed."""
    if isinstance(node, dict) and isinstance(node.get('$ref'), str):
        found: object = doc
        for key in node['$ref'].removeprefix('#/').split('/'):
            found = found.get(key) if isinstance(found, dict) else None
        if not isinstance(found, dict):
            raise OfferError(f'{name}: {node["$ref"]} points at nothing')
        return found
    return node if isinstance(node, dict) else {}


@dataclass(frozen=True)
class Event:
    name: str
    description: str = ''
    payload: dict | None = None  # its JSON Schema
    example: object = None


def events(text: str, name: str = EVENTS_NAME) -> list[Event]:
    """The events an AsyncAPI 3 document says its app sends, as the stack templates' helpers read it:
    a channel's address is the event's name, and with an ``operations`` section only the channels an
    operation sends to count. Raises OfferError for a document the platform cannot use."""
    doc = _document(text, name)
    if not str(doc.get('asyncapi', '')).startswith('3.'):
        raise OfferError(f'{name} must be an AsyncAPI 3 document (asyncapi: 3.x)')
    channels = doc.get('channels') or {}
    operations = [op for op in (doc.get('operations') or {}).values() if isinstance(op, dict)]
    if not isinstance(channels, dict):
        raise OfferError(f'{name}: channels must be a mapping')
    sends = {(op.get('channel') or {}).get('$ref') for op in operations if op.get('action') == 'send'}
    found = []
    for key, channel in channels.items():
        if operations and f'#/channels/{key}' not in sends:
            continue
        channel = _settled(doc, channel, name)
        event = str(channel.get('address') or key)
        if not EVENT_RE.match(event):
            raise OfferError(f'{name}: channel {key} has the address {event!r}; an event is a lowercase name such as created')
        if event in [e.name for e in found]:
            raise OfferError(f'{name}: {event} is described twice')
        messages = list((channel.get('messages') or {}).values())
        message = _settled(doc, messages[0], name) if messages else {}
        payload = _settled(doc, message.get('payload'), name) if message.get('payload') else None
        examples = message.get('examples') or []
        example = examples[0].get('payload') if examples and isinstance(examples[0], dict) else None
        found.append(Event(event, str(channel.get('description') or message.get('summary') or ''), payload, example))
    return found


def check_api(text: str, name: str) -> dict:
    """An OpenAPI 3 document as a mapping; OfferError if it is not one."""
    doc = _document(text, name)
    if not str(doc.get('openapi', '')).startswith('3.') or not isinstance(doc.get('paths'), dict):
        raise OfferError(f'{name} must be an OpenAPI 3 document (openapi: 3.x, with paths)')
    return doc


METHODS = ('get', 'put', 'post', 'delete', 'patch', 'head', 'options')


@dataclass(frozen=True)
class Api:
    title: str = ''
    description: str = ''
    operations: tuple[tuple[str, str, str], ...] = ()  # (METHOD, path, summary)


def api(text: str, name: str) -> Api:
    """What an OpenAPI document says its app answers: each path and method with its summary."""
    doc = check_api(text, name)
    info = doc.get('info') if isinstance(doc.get('info'), dict) else {}
    operations = [(method.upper(), str(path), str(operation.get('summary') or operation.get('description') or ''))
                  for path, item in doc['paths'].items() if isinstance(item, dict)
                  for method, operation in item.items() if method in METHODS and isinstance(operation, dict)]
    return Api(str(info.get('title') or ''), str(info.get('description') or ''), tuple(operations))


@dataclass(frozen=True)
class Source:
    """offers/source.yaml: where an instance's documents came from and what their files are called."""
    repository: str
    commit: str
    api: str = ''  # file name in the directory, '' without an API description
    events: str = ''

    @classmethod
    def parse(cls, text: str) -> Source:
        doc = _document(text, f'{OFFERS_DIR}/{SOURCE_FILE}')
        return cls(*(str(doc.get(key) or '') for key in ('repository', 'commit', 'api', 'events')))

    def document(self) -> dict:
        """What the file holds: a document without one is not named."""
        return {key: value for key, value in vars(self).items() if value}


@dataclass(frozen=True)
class Offers:
    """What an instance keeps in its offers directory."""
    repository: str
    commit: str
    api: str = ''  # file name in the directory, '' without an API description
    events: str = ''
    described: tuple[Event, ...] | None = None  # None without an events document


def read(instance: Path) -> Offers | None:
    """An instance's offers as they are on disk; None if it has none. OfferError if they are broken."""
    folder = instance / OFFERS_DIR
    if not (folder / SOURCE_FILE).is_file():
        return None
    source = Source.parse((folder / SOURCE_FILE).read_text())
    api, event_file = source.api, source.events
    for name in filter(None, (api, event_file)):
        if '/' in name or not (folder / name).is_file():
            raise OfferError(f'{OFFERS_DIR}/{SOURCE_FILE} names {name}, which is not in {OFFERS_DIR}/')
    if api:
        check_api((folder / api).read_text(), f'{OFFERS_DIR}/{api}')
    described = tuple(events((folder / event_file).read_text(), f'{OFFERS_DIR}/{event_file}')) if event_file else None
    return Offers(source.repository, source.commit, api, event_file, described)


def problems(instance: Path, published: list[str], annotation: str = '') -> list[str]:
    """Rule offers: what the instance publishes is what its events document describes, and the
    release names the commit the documents came from."""
    try:
        offers = read(instance)
    except OfferError as error:
        return [f'offers: {error}; make app-offers reads them again']
    if offers is None:
        return [f'offers: the release is annotated {ANNOTATION} but the instance keeps no {OFFERS_DIR}/'] if annotation else []
    found = []
    if annotation != offers.commit:
        found.append(f'offers: the release says its offers are from {annotation or "nowhere"}, {OFFERS_DIR}/{SOURCE_FILE} says {offers.commit}')
    if offers.described is not None and [e.name for e in offers.described] != published:
        found.append(f'offers: publishes {", ".join(published) or "nothing"}, but {OFFERS_DIR}/{offers.events} describes '
                     f'{", ".join(e.name for e in offers.described) or "nothing"}')
    return [f'{problem}; make app-offers APP={instance.parent.name} ENV={instance.name} rewrites it' for problem in found]
