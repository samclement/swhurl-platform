"""``python3 -m swhurl console``: serve the read-only console.

Every page except ``/healthz`` needs the signed-in email that oauth2-proxy
passes through Traefik as ``X-Auth-Request-Email``; without it the answer is
401. ``--dev`` uses a fixed identity instead and is refused on any address
but loopback, so it can never be exposed.
"""
from __future__ import annotations

import argparse
import datetime as dt
import sys
from pathlib import Path

from starlette.applications import Starlette
from starlette.middleware import Middleware
from starlette.middleware.base import BaseHTTPMiddleware
from starlette.requests import Request
from starlette.responses import PlainTextResponse, Response
from starlette.routing import Route
from starlette.templating import Jinja2Templates

from swhurl.apps import ops
from swhurl.console import cluster
from swhurl.run import Runner

IDENTITY_HEADER = 'X-Auth-Request-Email'
DEV_IDENTITY = 'operator@localhost (dev)'
LOOPBACK = ('127.0.0.1', '::1', 'localhost')
TEMPLATES = Jinja2Templates(directory=Path(__file__).parent / 'templates')
# A Ready status ('True', 'False', 'Unknown') as a CSS class.
TEMPLATES.env.filters['state'] = lambda status: {'True': 'ok', 'False': 'bad'}.get(status, 'warn')


class RequireIdentity(BaseHTTPMiddleware):
    def __init__(self, app, dev_identity: str | None = None):
        super().__init__(app)
        self.dev_identity = dev_identity

    async def dispatch(self, request: Request, call_next) -> Response:
        if request.url.path == '/healthz':
            return await call_next(request)
        identity = self.dev_identity or request.headers.get(IDENTITY_HEADER, '').strip()
        if not identity:
            return PlainTextResponse('sign-in required: no identity from oauth2-proxy\n', status_code=401)
        request.state.identity = identity
        return await call_next(request)


def create_app(runner: Runner, *, dev_identity: str | None = None) -> Starlette:
    def page(request: Request, name: str, status_code: int = 200, **context) -> Response:
        context.update(identity=request.state.identity, path=request.url.path,
                       read_at=dt.datetime.now().astimezone().strftime('%H:%M:%S'))
        return TEMPLATES.TemplateResponse(request, name, context, status_code=status_code)

    def reading(request: Request, name: str, read, **context) -> Response:
        try:
            return page(request, name, **context, **read())
        except cluster.ReadError as error:
            return page(request, 'error.html', status_code=502, error=str(error))

    def apps(request: Request) -> Response:
        return reading(request, 'apps.html', lambda: {'rows': cluster.apps(runner)})

    def app(request: Request) -> Response:
        found = cluster.instance(request.path_params['app'], request.path_params['env'])
        status = ops.gather_status(runner, found) if found else None
        if status is None:
            return page(request, 'error.html', status_code=404, error='No such app instance.')
        return page(request, 'app.html', status=status)

    def units(request: Request) -> Response:
        def read():
            found = cluster.units(runner)
            levels = [[u for u in found if u.level == n] for n in range(max((u.level for u in found), default=-1) + 1)]
            return {'levels': levels}
        return reading(request, 'units.html', read)

    def platform(request: Request) -> Response:
        return reading(request, 'platform.html', lambda: {'checks': cluster.platform_checks(runner)})

    def healthz(_request: Request) -> Response:
        return PlainTextResponse('ok\n')

    return Starlette(
        routes=[Route('/', apps), Route('/apps/{app}/{env}', app), Route('/units', units),
                Route('/platform', platform), Route('/healthz', healthz)],
        middleware=[Middleware(RequireIdentity, dev_identity=dev_identity)])


def parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(prog='swhurl console', description=__doc__.split('\n\n')[0])
    p.add_argument('--host', default='0.0.0.0')
    p.add_argument('--port', type=int, default=8080)
    p.add_argument('--dev', action='store_true', help=f'identity {DEV_IDENTITY!r}; loopback only')
    return p


def main(argv: list[str] | None = None) -> int:
    args = parser().parse_args(argv)
    if args.dev and args.host == '0.0.0.0':
        args.host = '127.0.0.1'
    if args.dev and args.host not in LOOPBACK:
        print(f'[ERROR] --dev serves a fixed identity; it only binds to loopback, not {args.host}', file=sys.stderr)
        return 2
    import uvicorn
    uvicorn.run(create_app(Runner(), dev_identity=DEV_IDENTITY if args.dev else None),
                host=args.host, port=args.port, log_level='info')
    return 0
