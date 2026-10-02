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
import re
import sys
import time
from pathlib import Path
from urllib.parse import parse_qs, urlparse

from markupsafe import Markup, escape
from starlette.applications import Starlette
from starlette.middleware import Middleware
from starlette.middleware.base import BaseHTTPMiddleware
from starlette.requests import Request
from starlette.responses import PlainTextResponse, RedirectResponse, Response
from starlette.routing import Route
from starlette.templating import Jinja2Templates

from swhurl.apps import contract, ops, repo
from swhurl.console import actions, changes, cluster, logconfig, repos
from swhurl.run import Runner

IDENTITY_HEADER = 'X-Auth-Request-Email'
DEV_IDENTITY = 'operator@localhost (dev)'
LOOPBACK = ('127.0.0.1', '::1', 'localhost')
TEMPLATES = Jinja2Templates(directory=Path(__file__).parent / 'templates')
# A Ready status ('True', 'False', 'Unknown') as a CSS class.
TEMPLATES.env.filters['state'] = lambda status: {'True': 'ok', 'False': 'bad'}.get(status, 'warn')
# Every page names an app instance <app>/<env> (never its Flux unit's name); see cluster.target.
HASH = re.compile(r'\b([0-9a-f]{12})[0-9a-f]{8,}\b')


def short_hashes(text: object) -> Markup:
    """Escape ``text`` and shorten each commit SHA or digest to 12 characters, the full value on hover."""
    return Markup(HASH.sub(lambda m: f'<span class="hash" title="{m[0]}">{m[1]}…</span>', str(escape(text))))


TEMPLATES.env.filters['short'] = short_hashes
TEMPLATES.env.globals['target'] = cluster.target


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
               github: changes.GitHub | None = None, app_repos: repos.AppReposToken | None = None,
               repo_opener: repo.Opener | None = None) -> Starlette:
    """``github`` and ``app_repos`` default to the tokens in the environment (none: the new-app form says so)."""
    jobs = jobs or actions.Jobs(runner)
    github = github or changes.github_from_env(runner)
    app_repos = app_repos or repos.token_from_env(runner)

    def page(request: Request, name: str, status_code: int = 200, **context) -> Response:
        context.update(identity=request.state.identity, path=request.url.path, refused=actions.REFUSED,
                       read_at=dt.datetime.now().astimezone().strftime('%H:%M'),
                       running_jobs=[j for j in jobs.recent() if j.state == 'running'])
        context.setdefault('github', github)
        return TEMPLATES.TemplateResponse(request, name, context, status_code=status_code)

    def reading(request: Request, name: str, read, **context) -> Response:
        try:
            return page(request, name, **context, **read())
        except cluster.ReadError as error:
            return page(request, 'error.html', status_code=502, error=str(error))

    def open_prs() -> tuple[list[changes.PullRequest], str]:
        """The console's open PRs, or why they could not be listed (the page still renders)."""
        if github is None:
            return [], 'No GitHub token is configured, so open pull requests are not listed.'
        try:
            return changes.console_prs(runner, github), ''
        except actions.ActionError as error:
            return [], f'Could not list open pull requests: {error}'

    def overview(request: Request) -> Response:
        def read():
            checks, found = cluster.platform_checks(runner), cluster.units(runner)
            prs, prs_error = open_prs()
            return {'checks': checks, 'units': found, 'apps': cluster.apps(runner), 'problems': cluster.problems(checks, found),
                    'updating': cluster.updating(found),
                    'prs': prs, 'prs_error': prs_error, 'recent': [j for j in jobs.recent() if j.state != 'running'][:5]}
        return reading(request, 'overview.html', read)

    def apps(request: Request) -> Response:
        return reading(request, 'apps.html', lambda: {'apps': cluster.summaries(cluster.apps(runner))})

    def app(request: Request) -> Response:
        found = cluster.instance(request.path_params['app'], request.path_params['env'])
        status = ops.gather_status(runner, found) if found else None
        if status is None and found:
            # Created here but not applied yet: say so instead of "no such app" (its links appear at once).
            label = f'{found.app}/{found.env}'
            pending = [j for j in jobs.recent() if j.unit == label and j.action.startswith('new app')]
            prs, prs_error = open_prs()
            prs = [pr for pr in prs if changes.creates_instance(pr, found.app, found.env)]
            if pending or prs:
                return page(request, 'pending.html', label=label, jobs=pending, prs=prs, prs_error=prs_error)
        if status is None:
            return page(request, 'error.html', status_code=404, error='No such app instance.')
        summary = next((a for a in cluster.summaries(cluster.apps(runner)) if a.app == found.app), None)
        return page(request, 'app.html', status=status, scale_fields=changes.SCALE_FIELDS, github=github,
                    exposures=ops.EXPOSURE_LABELS, exposure_text=changes.EXPOSURE_LABELS, domain=contract.COOKIE_DOMAIN,
                    summary=summary, state=cluster.instance_state(status, summary.envs.get(found.env) if summary else None), unit_jobs=[j for j in jobs.recent() if j.unit in (found.unit, f'{found.app}/{found.env}')])

    def platform(request: Request) -> Response:
        def read():
            checks, found = cluster.platform_checks(runner), cluster.units(runner)
            layers = [(title, text, [u for u in found if u.layer == title])
                      for title, text in [(t, d) for _, t, d in cluster.LAYERS] + [(cluster.OTHER_LAYER, '')]]
            return {'checks': checks, 'units': found, 'problems': cluster.problems(checks, found), 'updating': cluster.updating(found),
                    'layers': [layer for layer in layers if layer[2]], 'flux_section': cluster.FLUX_SECTION}
        return reading(request, 'platform.html', read)

    def unit(request: Request) -> Response:
        def read():
            found = {u.name: u for u in cluster.units(runner)}
            name = request.path_params['unit']
            if name not in found:
                raise LookupError(name)
            return {'u': found[name], 'app': cluster.instance_of_unit(name),
                    'unit_jobs': [j for j in jobs.recent() if j.unit == name]}
        try:
            return reading(request, 'unit.html', read)
        except LookupError:
            return page(request, 'error.html', status_code=404, error='No Flux unit by that name.')

    def moved(target: str):
        return lambda _request: RedirectResponse(target, status_code=301)

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

    questions_cache: dict[str, tuple[float, list[repo.Question]]] = {}

    def stack_questions(stack: str) -> list[repo.Question]:
        """The stack template's questions (its copier.yml on GitHub), read at most every 5 minutes."""
        cached = questions_cache.get(stack)
        if cached is not None and time.monotonic() - cached[0] <= 300:
            return cached[1]
        found = repo.template_questions(stack, **({'opener': repo_opener} if repo_opener else {}))
        questions_cache[stack] = (time.monotonic(), found)
        return found

    def new_repo_form(request: Request, status_code: int = 200, error: str = '', form=None) -> Response:
        form = form or {}
        features, features_error = {}, ''
        for stack in contract.STACKS:
            try:
                features[stack] = stack_questions(stack)
            except repo.RepoError as problem:
                features[stack], features_error = [], str(problem)
        return page(request, 'new_repo.html', status_code=status_code,
                    features=features, features_error=features_error,
                    preset=changes.NEW_REPO, stacks=contract.STACKS, stack_text=contract.STACK_DESCRIPTIONS,
                    owner=contract.APP_OWNER,
                    exposure_text=changes.EXPOSURE_LABELS, domain=contract.COOKIE_DOMAIN, form=form, error=error,
                    github=github, app_repos=app_repos)

    async def new_repo(request: Request) -> Response:
        form = {k: v[0] for k, v in parse_qs((await request.body()).decode(), keep_blank_values=True).items()}
        if github is None or app_repos is None:
            return new_repo_form(request, 409, 'Both GitHub tokens must be configured (console-github Secret).', form)
        try:
            questions = stack_questions(form.get('stack', '').strip()) if form.get('stack', '').strip() in contract.STACKS else []
            name, stack, description, answers, extra = changes.new_repo_args(form, questions)
        except (actions.ActionError, repo.RepoError) as error:
            return new_repo_form(request, 400, str(error), form)
        req = repo.Request(name, stack, contract.APP_OWNER, description, answers)

        def work(job: actions.Job) -> None:
            client = repos.AppRepos(runner, app_repos)
            try:
                image = repos.create_app_repo(runner, client, job, req, **({'opener': repo_opener} if repo_opener else {}))
            finally:
                client.close()
            argv = [name, f'--from-repo={req.repo}', '--env=staging', f'--image={image}', *extra]
            job.link = changes.open_pr(
                runner, github, job, slug=f'new-{name}-staging', title=f'apps: add {name}/staging',
                body=changes.new_repo_body(f'https://github.com/{req.repo}', contract.STACKS[stack], argv),
                change=lambda clone: changes.run_app_new(runner, job, clone, argv), auto_merge=True)
        try:
            job = jobs.submit('new app and repository', f'{name}/staging', request.state.identity, work)
        except actions.ActionError as error:
            return new_repo_form(request, 409, str(error), form)
        return RedirectResponse(f'/jobs/{job.id}', status_code=303)

    def new_app_form(request: Request, status_code: int = 200, error: str = '', form=None) -> Response:
        form = form or {}
        # A returned /new form is always an existing-image tab; a plain GET starts on the new repository tab.
        preset = form.get('preset', 'swhurl-web') if form else request.query_params.get('preset', changes.NEW_REPO)
        if preset not in changes.PRESET_LABELS:
            preset = changes.NEW_REPO
        if preset == changes.NEW_REPO:
            return new_repo_form(request, status_code, error)
        checked = {f: bool(form.get(f)) for f in changes.CHECKBOXES} if form else changes.new_app_checked(preset)
        advanced = [f for _, group in changes.ADVANCED_GROUPS for f in group]
        return page(request, 'new.html', status_code=status_code, fields=changes.NEW_APP_FIELDS,
                    labels={f: label for f, _, label in changes.NEW_APP_FIELDS},
                    choices=changes.CHOICES, checkboxes=changes.CHECKBOXES, otlp_hint=changes.OTLP_HINT,
                    exposure_text=changes.EXPOSURE_LABELS, groups=changes.ADVANCED_GROUPS,
                    # Open when nothing fills it (no preset) or a returned form set something in it.
                    advanced_open=not preset or any(form.get(f) for f in advanced if f not in changes.CHECKBOXES),
                    preset=preset, existing=changes.EXISTING_LABELS, domain=contract.COOKIE_DOMAIN, checked=checked,
                    defaults=changes.new_app_defaults(preset), form=form, error=error, github=github)

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
                runner, github, job, slug=f'new-{name}-{env}', title=f'apps: add {name}/{env}',
                body=changes.new_app_body(name, env, argv),
                change=lambda clone: changes.run_app_new(runner, job, clone, argv), auto_merge=env == 'staging')
        try:
            job = jobs.submit('new app', f'{name}/{env}', request.state.identity, work)
        except actions.ActionError as error:
            return new_app_form(request, 409, str(error), form)
        return RedirectResponse(f'/jobs/{job.id}', status_code=303)

    async def change_app(request: Request) -> Response:
        """Promote, scale, change access to or remove an instance: a PR made by the clone's app-* command."""
        app, env, change = (request.path_params[k] for k in ('app', 'env', 'change'))
        found = cluster.instance(app, env)
        if found is None or change not in ('promote', 'scale', 'expose', 'remove'):
            return page(request, 'error.html', status_code=404, error='No such app instance or change.')
        if github is None:
            return page(request, 'error.html', status_code=409, error='No GitHub token is configured (console-github Secret).')
        form = {k: v[0] for k, v in parse_qs((await request.body()).decode(), keep_blank_values=True).items()}
        try:
            if change == 'promote':
                if env != 'staging':
                    raise actions.ActionError('promote from the staging instance')
                command, argv, target = 'app-promote', [app, '--from=staging', '--to=prod'], f'{app}/prod'
                title, make = f'apps: promote {app}/staging to {app}/prod', f'make app-promote APP={app}'
            elif change == 'scale':
                command, argv, target = 'app-scale', [app, env, *changes.scale_args(form)], f'{app}/{env}'
                title, make = f'apps: scale {app}/{env}', f'make app-scale APP={app} ENV={env} ARGS="{" ".join(argv[2:])}"'
            elif change == 'expose':
                command, argv, target = 'app-expose', [app, env, *changes.expose_args(form)], f'{app}/{env}'
                title, make = f'apps: change access to {app}/{env}', f'make app-expose APP={app} ENV={env} ARGS="{" ".join(argv[2:])}"'
            else:
                command, argv, target = 'app-remove', [app, env], f'{app}/{env}'
                title, make = f'apps: remove {app}/{env}', f'make app-remove APP={app} ENV={env}'
        except actions.ActionError as error:
            return page(request, 'error.html', status_code=400, error=str(error))

        def work(job: actions.Job) -> None:
            job.link = changes.open_pr(
                runner, github, job, slug=f'{change}-{app}-{env}', title=title,
                body=f'Generated by the console with:\n\n    {make}',
                change=lambda clone: changes.run_tool(runner, job, clone, command, argv),
                # Easy to undo: a staging app or a resize. Production only when asked; access changes and removal never.
                auto_merge=change == 'scale' or (change == 'promote' and form.get('auto_merge') == 'on'))
        try:
            job = jobs.submit(change, target, request.state.identity, work)
        except actions.ActionError as error:
            return page(request, 'error.html', status_code=409, error=str(error))
        return RedirectResponse(f'/jobs/{job.id}', status_code=303)

    def activity(request: Request) -> Response:
        prs, prs_error = open_prs()
        return page(request, 'activity.html', jobs=jobs.recent(), prs=prs, prs_error=prs_error)

    def healthz(_request: Request) -> Response:
        return PlainTextResponse('ok\n')

    return Starlette(
        routes=[Route('/', overview), Route('/apps', apps), Route('/apps/{app}/{env}', app),
                Route('/platform', platform), Route('/units', moved('/platform#units')), Route('/units/{unit}', unit),
                Route('/healthz', healthz), Route('/units/{unit}/{action}', start, methods=['POST']),
                Route('/activity', activity), Route('/jobs', moved('/activity')), Route('/jobs/{id:int}', job),
                Route('/new', new_app, methods=['GET', 'POST']), Route('/new/repo', new_repo, methods=['POST']),
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
                host=args.host, port=args.port, log_level='info', log_config=logconfig.CONFIG)
    return 0
