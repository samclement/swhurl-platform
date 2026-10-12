"""The notify service: accepts a message about an app and delivers it to the channels of the app's team.

    POST /v1/notify      Authorization: Bearer <service-account token for the audience "notify">
    {"app": "orders", "env": "staging", "title": "...", "message": "...", "priority": "high",
     "click": "https://...", "key": "an id that makes a retry harmless"}

    POST /v1/alert       from ClickStack's webhook, which proves itself with a shared secret in the body
    {"secret": "...", "title": "🚨 swhurl:orders:error-logs", "body": "Group: ...", "link": "https://...", "state": "ALERT"}

The caller is whoever Kubernetes says the token belongs to (a TokenReview for this audience only);
nothing in the body is trusted for identity. An app sends as itself: ``app`` and ``env`` come from
its token, its title is prefixed with them, and it is limited to ``RATE`` messages an hour. The
senders in ``PLATFORM_SENDERS`` say which app a message is about. An accepted message becomes one
outbox entry per channel of that app's team and is answered 202; entries are checkpointed in a
ConfigMap before any is sent and delivered at least once (docs/services.md#notify-service).

It reads the routes, the app's namespace label and the channel addresses when a message arrives and
when it is sent: nothing is cached, so a new channel works as soon as Flux has applied it.
"""
from __future__ import annotations

import argparse
import base64
import hashlib
import hmac
import json
import logging
import logging.config
import re
import threading
import time
import uuid
from collections import deque

import uvicorn
from starlette.applications import Starlette
from starlette.concurrency import run_in_threadpool
from starlette.requests import Request
from starlette.responses import JSONResponse, PlainTextResponse, Response
from starlette.routing import Route

from swhurl import app_alerts, statestore
from swhurl.apps import channels
from swhurl.apps.contract import TEAM
from swhurl.apps.instance import Instance
from swhurl.console.logconfig import CONFIG
from swhurl.run import CommandError, Runner

from .delivery import deliver
from .errors import NotificationError
from .notice import PRIORITIES, Notice, Ntfy, Sender

AUDIENCE = 'notify'
REVIEW = ['kubectl', 'create', '--validate=false', '-f', '-', '-o', 'json']
OUTBOX_NAME = 'notify-outbox'
PLATFORM_SENDERS = {'system:serviceaccount:console:notification-check'}
"""Who may name the app a message is about: the notification checker."""
APP_SENDER = re.compile(r'^system:serviceaccount:(?P<namespace>[a-z0-9-]+):(?P<app>[a-z0-9-]+)$')
"""An app's own service account, ``<app>`` in ``<app>-<env>``: it sends about itself only."""
RATE = (30, 3600)  # messages an app instance may send, per this many seconds
MAX_BODY, MAX_TITLE, MAX_MESSAGE, MAX_KEY = 16_384, 250, 3_500, 300
MAX_PENDING = 500
MAX_OUTBOX_BYTES = 700_000
HISTORY = 7200  # seconds a delivered entry's key is remembered, so a repeated request is not sent twice
INTERVAL = 30  # seconds between delivery passes when nothing new arrives

log = logging.getLogger(__name__)


class Refused(Exception):
    def __init__(self, status: int, reason: str):
        super().__init__(reason)
        self.status, self.reason = status, reason


def caller(runner: Runner, token: str) -> str:
    """The identity Kubernetes gives ``token`` for this service's audience; 401 for any other token."""
    review = {'apiVersion': 'authentication.k8s.io/v1', 'kind': 'TokenReview',
              'spec': {'token': token, 'audiences': [AUDIENCE]}}
    try:
        # The reply repeats the token, so it is never logged; the request goes on standard input.
        # Client-side validation would list CRDs, which this ServiceAccount may not: the API validates.
        status = runner.json(REVIEW, input=json.dumps(review),
                             secret_output=True)['status']
    except (CommandError, KeyError, TypeError, ValueError):
        raise Refused(503, 'cannot check the token with Kubernetes') from None
    if not status.get('authenticated') or AUDIENCE not in (status.get('audiences') or []):
        raise Refused(401, f'a service-account token for the audience {AUDIENCE} is required')
    return str((status.get('user') or {}).get('username', ''))


def sender(who: str) -> tuple[str, str] | None:
    """``(app, env)`` when ``who`` is an app's own service account, None for a platform sender; 403 for anyone else."""
    if who in PLATFORM_SENDERS:
        return None
    found = APP_SENDER.match(who)
    instance = Instance.of_namespace(found['namespace']) if found else None
    if instance and instance.app == found['app']:
        return instance.app, instance.env
    log.warning('Refused a message from %s', who)
    raise Refused(403, 'this caller may not send notifications')


class RateLimit:
    """At most RATE[0] accepted messages per app instance in any RATE[1] seconds (kept in memory: a
    restart forgets, which only ever allows more)."""

    def __init__(self, clock=time.time):
        self.clock, self.sent, self.lock = clock, {}, threading.Lock()

    def take(self, app: str, env: str) -> None:
        now = self.clock()
        with self.lock:
            times = self.sent.setdefault((app, env), deque())
            while times and times[0] <= now - RATE[1]:
                times.popleft()
            if len(times) >= RATE[0]:
                raise Refused(429, f'{app}/{env} has sent {RATE[0]} messages in the last hour; try again later')
            times.append(now)


def message(body: bytes, app_sender: tuple[str, str] | None = None) -> dict:
    """The validated request: app, env and an outbox notice without a channel yet. An app's message is
    about itself (``app_sender``, from its token) and needs no key."""
    try:
        doc = json.loads(body)
        if not isinstance(doc, dict):
            raise ValueError
    except ValueError:
        raise Refused(400, 'the body must be one JSON object') from None
    unknown = sorted(set(doc) - {'app', 'env', 'title', 'message', 'priority', 'click', 'key'})
    if unknown:
        raise Refused(400, f'unknown field {", ".join(unknown)}')
    text = {field: doc.get(field, '') for field in ('app', 'env', 'title', 'message', 'click', 'key')}
    if not all(isinstance(value, str) for value in text.values()):
        raise Refused(400, 'app, env, title, message, click and key are strings')
    if app_sender:
        if (text['app'] or app_sender[0], text['env'] or app_sender[1]) != app_sender:
            raise Refused(403, 'an app sends about itself only')
        text['app'], text['env'] = app_sender
        text['key'] = f"app:{text['app']}:{text['env']}:{text['key'] or uuid.uuid4().hex}"
        text['title'] = f"{text['app']}/{text['env']}: {text['title'].strip()}" if text['title'].strip() else ''
    if not Instance.named(text['app'], text['env']):
        raise Refused(400, 'app must be an app name and env staging or prod')
    if not text['title'].strip() or len(text['title']) > MAX_TITLE or len(text['message']) > MAX_MESSAGE:
        raise Refused(400, f'a title of at most {MAX_TITLE} characters is required; a message is at most {MAX_MESSAGE}')
    if doc.get('priority', 'normal') not in PRIORITIES:
        raise Refused(400, f'priority is one of {", ".join(PRIORITIES)}')
    if text['click'] and not text['click'].startswith('https://') or len(text['click']) > 500:
        raise Refused(400, 'click must be an https URL of at most 500 characters')
    if not text['key'] or len(text['key']) > MAX_KEY:
        raise Refused(400, f'key is required (at most {MAX_KEY} characters): the same key is delivered once')
    notice = Notice(text['title'].strip(), text['message'], doc.get('priority', 'normal') == 'high', text['click'])
    return {'app': text['app'], 'env': text['env'], 'key': text['key'], 'notice': notice}


ALERT_SECRET = 'notify-alerts'
"""The Secret (key ``secret``) ClickStack's webhook must repeat: HyperDX cannot present a token."""
GROUP = re.compile(r"k8s\.namespace\.name'\):(?P<namespace>[a-z0-9-]+)")
"""The namespace an alert fired for, from the "Group:" line HyperDX writes for a tile grouped by namespace."""


def alert_secret(runner: Runner) -> str:
    try:
        secrets = runner.json(['kubectl', '-n', channels.NAMESPACE, 'get', 'secrets', '-o', 'json'],
                              secret_output=True)['items']
        found = next(base64.b64decode(s['data']['secret']).decode() for s in secrets if s['metadata']['name'] == ALERT_SECRET)
    except (CommandError, KeyError, TypeError, StopIteration, ValueError):
        raise Refused(503, 'the alert webhook secret is not set up') from None
    runner.add_secret(found)
    return found


def alert(runner: Runner, body: bytes) -> dict:
    """A ClickStack alert as a request for the outbox: which app and environment it is about (from the
    alert's name and the group it fired for), a title in the platform's words and HyperDX's own detail."""
    try:
        doc = json.loads(body)
        text = {field: doc.get(field, '') for field in ('secret', 'title', 'body', 'link', 'state')}
        if not all(isinstance(value, str) for value in text.values()):
            raise ValueError
    except (ValueError, AttributeError):
        raise Refused(400, 'the body must be one JSON object of strings') from None
    if not text['secret'] or not hmac.compare_digest(text['secret'].encode(), alert_secret(runner).encode()):
        raise Refused(401, 'the alert webhook secret is required')
    named, group = app_alerts.NAME.search(text['title']), GROUP.search(text['body'])
    instance = Instance.of_namespace(group['namespace']) if named and group else None
    if not instance or instance.app != named['app']:
        raise Refused(400, 'not an alert of an app: its name is swhurl:<app>:<preset> and its group the app\'s namespace')
    app, env, firing = instance.app, instance.env, text['state'] == 'ALERT'
    label = app_alerts.LABELS.get(named['preset'], named['preset'])
    detail = '\n'.join(line for line in text['body'].splitlines() if line.strip() and not line.startswith('Group:'))
    link = text['link'] if text['link'].startswith('https://') and len(text['link']) <= 500 else ''
    notice = Notice(f"{app}/{env} {'alert' if firing else 'recovered'}: {label}", detail[:MAX_MESSAGE], firing, link)
    key = f"alert:{app}:{env}:{named['preset']}:{text['state']}:{hashlib.sha256(text['body'].encode()).hexdigest()[:16]}"
    return {'app': app, 'env': env, 'key': key, 'notice': notice}


def team_channels(runner: Runner, app: str, env: str) -> list[str]:
    """The channels of the team that owns ``app``'s namespace; none for an unknown app or a team without any."""
    get = ['kubectl', '-n', channels.NAMESPACE, 'get']
    try:
        namespace = runner.json(['kubectl', 'get', 'namespace', Instance(app, env).namespace, '--ignore-not-found', '-o', 'json']) or {}
        routes = next((json.loads(c['data'][channels.ROUTES_KEY]) for c in runner.json([*get, 'configmaps', '-o', 'json'])['items']
                       if c['metadata']['name'] == channels.ROUTES_NAME), {})
    except (CommandError, KeyError, TypeError, ValueError):
        raise Refused(503, 'cannot read the team routes') from None
    team = ((namespace.get('metadata') or {}).get('labels') or {}).get(TEAM)
    return list(routes.get('teams', {}).get(team, []))


def addresses(runner: Runner) -> dict[str, str]:
    """Each ntfy channel's address, from the channel Secrets; registered as secrets and never logged."""
    try:
        secrets = runner.json(['kubectl', '-n', channels.NAMESPACE, 'get', 'secrets', '-o', 'json'],
                              secret_output=True)['items']
    except (CommandError, KeyError, TypeError):
        raise NotificationError('cannot read the channel Secrets') from None
    found = {}
    for secret in secrets:
        name = secret['metadata']['name']
        encoded = (secret.get('data') or {}).get(channels.ADDRESS_KEY)
        if (name.startswith(channels.SECRET_PREFIX) and encoded
                and (secret['metadata'].get('labels') or {}).get(channels.TRANSPORT) == 'ntfy'):
            found[name.removeprefix(channels.SECRET_PREFIX)] = base64.b64decode(encoded).decode()
            runner.add_secret(found[name.removeprefix(channels.SECRET_PREFIX)])
    return found


class Outbox:
    """Accepted entries, checkpointed in one ConfigMap key before anything is sent."""

    def __init__(self, runner: Runner, sender: Sender | None = None, clock=time.time):
        self.runner, self.sender, self.clock = runner, sender or Ntfy(), clock
        self.lock = threading.Lock()
        self.wake = threading.Event()
        self.state: dict | None = None

    def load(self) -> dict:
        if self.state is None:
            raw = statestore.read_key(self.runner, namespace=channels.NAMESPACE, name=OUTBOX_NAME, key='state.json')
            state = json.loads(raw) if raw else {'version': 1, 'seen': {}, 'pending': []}
            if state.get('version') != 1 or not isinstance(state.get('seen'), dict) or not isinstance(state.get('pending'), list):
                raise NotificationError('the outbox is invalid; refusing to reset delivery history')
            self.state = state
        return self.state

    def save(self, runner: Runner, state: dict) -> None:
        raw = json.dumps(state, separators=(',', ':'))
        if len(raw.encode()) > MAX_OUTBOX_BYTES:
            raise NotificationError('the outbox exceeds its bounded size')
        statestore.write_key(runner, namespace=channels.NAMESPACE, name=OUTBOX_NAME, key='state.json', raw=raw,
                             field_manager='notify')

    def accept(self, request: dict, names: list[str]) -> list[str]:
        """Queue one entry per channel; an entry already queued or delivered under the same key is not repeated."""
        now = self.clock()
        with self.lock:
            try:
                state = self.load()
                queued = [name for name in names
                          if (key := f"{request['key']}@{name}") not in state['seen']
                          and not any(entry['key'] == key for entry in state['pending'])]
                if len(state['pending']) + len(queued) > MAX_PENDING:
                    raise Refused(503, 'the outbox is full; try again later')
                if queued:
                    added = [request['notice'].fields() | {'key': f"{request['key']}@{name}", 'channel': name, 'queued': now,
                                                  'app': request['app'], 'env': request['env']} for name in queued]
                    state['pending'] += added
                    try:
                        self.save(self.runner, state)
                    except (CommandError, NotificationError):
                        del state['pending'][-len(added):]
                        raise
            except (CommandError, NotificationError, ValueError):
                self.state = None  # read it again next time rather than trust a copy that may be behind
                raise Refused(503, 'cannot save to the outbox') from None
        if queued:
            self.wake.set()
        return queued

    def deliver(self) -> None:
        now = self.clock()
        with self.lock:
            state = self.load()
            state['seen'] = {key: until for key, until in state['seen'].items() if until > now}
            if not state['pending']:
                return
            found: dict[str, str] = {}

            def send(entry: dict) -> None:
                if not found:
                    found.update(addresses(self.runner))
                address = found.get(entry['channel'])
                if not address:
                    raise NotificationError('no ntfy channel Secret with that name in the cluster')
                self.sender(address, Notice.of(entry))
            try:
                deliver(self.runner, state, send, now, save=self.save, history=HISTORY)
            except CommandError:
                self.state = None
                raise

    def run(self, stop: threading.Event) -> None:
        """Deliver whenever something is accepted, and every INTERVAL for what could not be sent."""
        while not stop.is_set():
            self.wake.wait(INTERVAL)
            self.wake.clear()
            try:
                self.deliver()
            except (CommandError, NotificationError, ValueError) as error:
                log.error('Delivery pass failed: %s', self.runner.redact(str(error))
                          if isinstance(error, NotificationError) else type(error).__name__)


def create_app(runner: Runner, outbox: Outbox, limit: RateLimit | None = None) -> Starlette:
    limit = limit or RateLimit()

    def refuse(error: Refused) -> Response:
        return JSONResponse({'error': error.reason}, status_code=error.status)

    def notify(request: Request, body: bytes) -> Response:
        scheme, _, token = request.headers.get('authorization', '').partition(' ')
        if scheme.lower() != 'bearer' or not token.strip():
            raise Refused(401, f'a service-account token for the audience {AUDIENCE} is required')
        who = caller(runner, token.strip())
        app_sender = sender(who)
        wanted = message(body, app_sender)
        if app_sender:
            limit.take(*app_sender)
        queued = outbox.accept(wanted, team_channels(runner, wanted['app'], wanted['env']))
        log.info('Accepted "%s" about %s/%s from %s for %d channel(s)', wanted['notice'].title, wanted['app'],
                 wanted['env'], who.rsplit(':', 1)[-1], len(queued))
        return JSONResponse({'channels': queued}, status_code=202)

    def from_clickstack(_request: Request, body: bytes) -> Response:
        wanted = alert(runner, body)
        queued = outbox.accept(wanted, team_channels(runner, wanted['app'], wanted['env']))
        log.info('Accepted "%s" about %s/%s from ClickStack for %d channel(s)', wanted['notice'].title, wanted['app'],
                 wanted['env'], len(queued))
        return JSONResponse({'channels': queued}, status_code=202)

    def endpoint(handler):
        async def run(request: Request) -> Response:
            body = await request.body()
            if len(body) > MAX_BODY:
                return refuse(Refused(413, f'a request is at most {MAX_BODY} bytes'))
            try:
                return await run_in_threadpool(handler, request, body)
            except Refused as error:
                return refuse(error)
        return run

    def healthz(_request: Request) -> Response:
        return PlainTextResponse('ok\n')

    return Starlette(routes=[Route('/v1/notify', endpoint(notify), methods=['POST']),
                             Route('/v1/alert', endpoint(from_clickstack), methods=['POST']), Route('/healthz', healthz)])


def main(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    parser = argparse.ArgumentParser(prog='swhurl notify-serve', description=__doc__.splitlines()[0])
    parser.add_argument('--host', default='0.0.0.0')
    parser.add_argument('--port', type=int, default=8080)
    args = parser.parse_args(argv)
    logging.config.dictConfig(CONFIG)
    runner = runner or Runner.from_environment()
    outbox = Outbox(runner)
    stop = threading.Event()
    threading.Thread(target=outbox.run, args=(stop,), daemon=True, name='outbox').start()
    outbox.wake.set()  # send what a previous pod left behind
    try:
        uvicorn.run(create_app(runner, outbox), host=args.host, port=args.port, log_level='info', log_config=CONFIG)
    finally:
        stop.set()
    return 0
