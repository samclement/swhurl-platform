"""The notify service: accepts a message about an app and delivers it to the channels of the app's team.

    POST /v1/notify      Authorization: Bearer <service-account token for the audience "notify">
    {"app": "orders", "env": "staging", "title": "...", "message": "...", "priority": "high",
     "click": "https://...", "key": "an id that makes a retry harmless"}

The caller is whoever Kubernetes says the token belongs to (a TokenReview for this audience only);
nothing in the body is trusted for identity. Only the senders in ``PLATFORM_SENDERS`` are accepted,
and they say which app a message is about. An accepted message becomes one outbox entry per channel
of that app's team and is answered 202; entries are checkpointed in a ConfigMap before any is sent
and delivered at least once (docs/services.md#notify-service).

It reads the routes, the app's namespace label and the channel addresses when a message arrives and
when it is sent: nothing is cached, so a new channel works as soon as Flux has applied it.
"""
from __future__ import annotations

import argparse
import base64
import json
import logging
import logging.config
import threading
import time

import uvicorn
from starlette.applications import Starlette
from starlette.concurrency import run_in_threadpool
from starlette.requests import Request
from starlette.responses import JSONResponse, PlainTextResponse, Response
from starlette.routing import Route

from swhurl import statestore
from swhurl.apps import channels
from swhurl.apps.contract import TEAM
from swhurl.console.logconfig import CONFIG
from swhurl.run import CommandError, Runner

from .delivery import deliver, publish
from .errors import NotificationError

AUDIENCE = 'notify'
OUTBOX_NAME = 'notify-outbox'
PLATFORM_SENDERS = {'system:serviceaccount:console:notification-check'}
"""Who may send, and name the app a message is about: the notification checker."""
MAX_BODY, MAX_TITLE, MAX_MESSAGE, MAX_KEY = 16_384, 250, 3_500, 300
MAX_PENDING = 500
MAX_OUTBOX_BYTES = 700_000
HISTORY = 7200  # seconds a delivered entry's key is remembered, so a repeated request is not sent twice
INTERVAL = 30  # seconds between delivery passes when nothing new arrives
PRIORITIES = {'normal': 3, 'high': 4}
NAME = channels.NAME_RE

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
        status = runner.json(['kubectl', 'create', '-f', '-', '-o', 'json'], input=json.dumps(review),
                             secret_output=True)['status']
    except (CommandError, KeyError, TypeError, ValueError):
        raise Refused(503, 'cannot check the token with Kubernetes') from None
    if not status.get('authenticated') or AUDIENCE not in (status.get('audiences') or []):
        raise Refused(401, f'a service-account token for the audience {AUDIENCE} is required')
    return str((status.get('user') or {}).get('username', ''))


def message(body: bytes) -> dict:
    """The validated request: app, env and an outbox notice without a channel yet."""
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
    if not NAME.match(text['app']) or text['env'] not in ('staging', 'prod'):
        raise Refused(400, 'app must be an app name and env staging or prod')
    if not text['title'].strip() or len(text['title']) > MAX_TITLE or len(text['message']) > MAX_MESSAGE:
        raise Refused(400, f'a title of at most {MAX_TITLE} characters is required; a message is at most {MAX_MESSAGE}')
    if doc.get('priority', 'normal') not in PRIORITIES:
        raise Refused(400, f'priority is one of {", ".join(PRIORITIES)}')
    if text['click'] and not text['click'].startswith('https://') or len(text['click']) > 500:
        raise Refused(400, 'click must be an https URL of at most 500 characters')
    if not text['key'] or len(text['key']) > MAX_KEY:
        raise Refused(400, f'key is required (at most {MAX_KEY} characters): the same key is delivered once')
    high = doc.get('priority', 'normal') == 'high'
    notice = {'title': text['title'].strip(), 'message': text['message'], 'priority': PRIORITIES['high' if high else 'normal'],
              'tags': ['warning' if high else 'white_check_mark']}
    if text['click']:
        notice['click'] = text['click']
    return {'app': text['app'], 'env': text['env'], 'key': text['key'], 'notice': notice}


def team_channels(runner: Runner, app: str, env: str) -> list[str]:
    """The channels of the team that owns ``app``'s namespace; none for an unknown app or a team without any."""
    get = ['kubectl', '-n', channels.NAMESPACE, 'get']
    try:
        namespace = runner.json(['kubectl', 'get', 'namespace', f'{app}-{env}', '--ignore-not-found', '-o', 'json']) or {}
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

    def __init__(self, runner: Runner, sender=publish, clock=time.time):
        self.runner, self.sender, self.clock = runner, sender, clock
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
                    added = [request['notice'] | {'key': f"{request['key']}@{name}", 'channel': name, 'queued': now,
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

            def send(notice: dict) -> None:
                if not found:
                    found.update(addresses(self.runner))
                address = found.get(notice['channel'])
                if not address:
                    raise NotificationError('no ntfy channel Secret with that name in the cluster')
                self.sender(address, notice)
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


def create_app(runner: Runner, outbox: Outbox) -> Starlette:
    def refuse(error: Refused) -> Response:
        return JSONResponse({'error': error.reason}, status_code=error.status)

    def notify(request: Request, body: bytes) -> Response:
        scheme, _, token = request.headers.get('authorization', '').partition(' ')
        if scheme.lower() != 'bearer' or not token.strip():
            raise Refused(401, f'a service-account token for the audience {AUDIENCE} is required')
        who = caller(runner, token.strip())
        if who not in PLATFORM_SENDERS:
            log.warning('Refused a message from %s', who)
            raise Refused(403, 'this caller may not send notifications')
        wanted = message(body)
        queued = outbox.accept(wanted, team_channels(runner, wanted['app'], wanted['env']))
        log.info('Accepted "%s" about %s/%s from %s for %d channel(s)', wanted['notice']['title'], wanted['app'],
                 wanted['env'], who.rsplit(':', 1)[-1], len(queued))
        return JSONResponse({'channels': queued}, status_code=202)

    async def endpoint(request: Request) -> Response:
        body = await request.body()
        if len(body) > MAX_BODY:
            return refuse(Refused(413, f'a request is at most {MAX_BODY} bytes'))
        try:
            return await run_in_threadpool(notify, request, body)
        except Refused as error:
            return refuse(error)

    def healthz(_request: Request) -> Response:
        return PlainTextResponse('ok\n')

    return Starlette(routes=[Route('/v1/notify', endpoint, methods=['POST']), Route('/healthz', healthz)])


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
