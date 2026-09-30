"""``python3 -m swhurl console``: serve the read-only console.

Every page except ``/healthz`` needs the signed-in email that oauth2-proxy
passes through Traefik as ``X-Auth-Request-Email``; without it the answer is
401. ``--dev`` uses a fixed identity instead and is refused on any address
but loopback, so it can never be exposed. A POST (the only way to start an
action) must also come from a page of the console itself (its ``Origin``
names this host), so another site cannot submit one with the sign-in cookie.
"""
from __future__ import annotations

import argparse
import datetime as dt
import sys
from pathlib import Path
from urllib.parse import parse_qs, urlparse

from starlette.applications import Starlette
from starlette.middleware import Middleware
from starlette.middleware.base import BaseHTTPMiddleware
from starlette.requests import Request
from starlette.responses import PlainTextResponse, RedirectResponse, Response
from starlette.routing import Route
from starlette.templating import Jinja2Templates

from swhurl.apps import ops
from swhurl.console import actions, changes, cluster
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
        if request.method not in ('GET', 'HEAD') and urlparse(request.headers.get('origin', '')).netloc != request.headers.get('host'):
            return PlainTextResponse('refused: the request did not come from a console page\n', status_code=403)
        request.state.identity = identity
        return await call_next(request)


def create_app(runner: Runner, *, dev_identity: str | None = None, jobs: actions.Jobs | None = None,
               github: changes.GitHub | None = None) -> Starlette:
    """``github`` defaults to the token in the environment (none: the new-app form says so)."""
    jobs = jobs or actions.Jobs(runner)
    github = github or changes.github_from_env(runner)

    def page(request: Request, name: str, status_code: int = 200, **context) -> Response:
        context.update(identity=request.state.identity, path=request.url.path, refused=actions.REFUSED,
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
        return page(request, 'app.html', status=status, scale_fields=changes.SCALE_FIELDS, github=github)

    def units(request: Request) -> Response:
        def read():
            found = cluster.units(runner)
            levels = [[u for u in found if u.level == n] for n in range(max((u.level for u in found), default=-1) + 1)]
            return {'levels': levels}
        return reading(request, 'units.html', read)

    def platform(request: Request) -> Response:
        return reading(request, 'platform.html', lambda: {'checks': cluster.platform_checks(runner)})

    def start(request: Request) -> Response:
        try:
            job = jobs.start(request.path_params['action'], request.path_params['unit'], request.state.identity)
        except actions.ActionError as error:
            return page(request, 'error.html', status_code=409, error=str(error))
        except cluster.ReadError as error:
            return page(request, 'error.html', status_code=502, error=str(error))
        return RedirectResponse(f'/jobs/{job.id}', status_code=303)

    def job(request: Request) -> Response:
        found = jobs.get(request.path_params['id'])
        if found is None:
            return page(request, 'error.html', status_code=404, error='No such job (jobs are kept in memory only).')
        return page(request, 'job.html', job=found)

    def new_app_form(request: Request, status_code: int = 200, error: str = '', form=None) -> Response:
        return page(request, 'new.html', status_code=status_code, fields=changes.NEW_APP_FIELDS,
                    choices=changes.CHOICES, checkboxes=changes.CHECKBOXES, otlp_hint=changes.OTLP_HINT,
                    defaults=changes.new_app_defaults(), form=form or {}, error=error, github=github)

    async def new_app(request: Request) -> Response:
        if request.method == 'GET':
            return new_app_form(request)
        # A plain URL-encoded form; parse_qs avoids a multipart dependency.
        form = {k: v[0] for k, v in parse_qs((await request.body()).decode(), keep_blank_values=True).items()}
        if github is None:
            return new_app_form(request, 409, 'No GitHub token is configured (console-github Secret).', form)
        try:
            name, env, argv = changes.new_app_args(form)
        except actions.ActionError as error:
            return new_app_form(request, 400, str(error), form)

        def work(job: actions.Job) -> None:
            job.link = changes.open_pr(
                runner, github, job, slug=f'new-{name}-{env}', title=f'apps: add {name} {env}',
                body=f'Generated by the console with:\n\n    make app-new NAME={name} ARGS="{" ".join(argv[1:])}"',
                change=lambda clone: changes.run_app_new(runner, job, clone, argv))
        try:
            job = jobs.submit('new app', f'{name}/{env}', request.state.identity, work)
        except actions.ActionError as error:
            return new_app_form(request, 409, str(error), form)
        return RedirectResponse(f'/jobs/{job.id}', status_code=303)

    async def change_app(request: Request) -> Response:
        """Promote, scale or remove an instance: a PR made by the clone's app-promote/app-scale/app-remove."""
        app, env, change = (request.path_params[k] for k in ('app', 'env', 'change'))
        found = cluster.instance(app, env)
        if found is None or change not in ('promote', 'scale', 'remove'):
            return page(request, 'error.html', status_code=404, error='No such app instance or change.')
        if github is None:
            return page(request, 'error.html', status_code=409, error='No GitHub token is configured (console-github Secret).')
        form = {k: v[0] for k, v in parse_qs((await request.body()).decode(), keep_blank_values=True).items()}
        try:
            if change == 'promote':
                if env != 'staging':
                    raise actions.ActionError('promote from the staging instance')
                command, argv, target = 'app-promote', [app, '--from=staging', '--to=prod'], f'{app}/prod'
                title, make = f'apps: promote {app} staging to prod', f'make app-promote APP={app}'
            elif change == 'scale':
                command, argv, target = 'app-scale', [app, env, *changes.scale_args(form)], f'{app}/{env}'
                title, make = f'apps: scale {app} {env}', f'make app-scale APP={app} ENV={env} ARGS="{" ".join(argv[2:])}"'
            else:
                command, argv, target = 'app-remove', [app, env], f'{app}/{env}'
                title, make = f'apps: remove {app} {env}', f'make app-remove APP={app} ENV={env}'
        except actions.ActionError as error:
            return page(request, 'error.html', status_code=400, error=str(error))

        def work(job: actions.Job) -> None:
            job.link = changes.open_pr(
                runner, github, job, slug=f'{change}-{app}-{env}', title=title,
                body=f'Generated by the console with:\n\n    {make}',
                change=lambda clone: changes.run_tool(runner, job, clone, command, argv))
        try:
            job = jobs.submit(change, target, request.state.identity, work)
        except actions.ActionError as error:
            return page(request, 'error.html', status_code=409, error=str(error))
        return RedirectResponse(f'/jobs/{job.id}', status_code=303)

    def job_list(request: Request) -> Response:
        return page(request, 'jobs.html', jobs=jobs.recent())

    def healthz(_request: Request) -> Response:
        return PlainTextResponse('ok\n')

    return Starlette(
        routes=[Route('/', apps), Route('/apps/{app}/{env}', app), Route('/units', units),
                Route('/platform', platform), Route('/healthz', healthz),
                Route('/units/{unit}/{action}', start, methods=['POST']),
                Route('/jobs', job_list), Route('/jobs/{id:int}', job),
                Route('/new', new_app, methods=['GET', 'POST']),
                Route('/apps/{app}/{env}/{change}', change_app, methods=['POST'])],
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
