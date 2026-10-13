"""``python3 -m swhurl console``: serve the console.

Every page except ``/healthz`` needs the signed-in email that oauth2-proxy
passes through Traefik as ``X-Auth-Request-Email``; without it the answer is
401. ``--dev`` uses a fixed identity instead and is refused on any address
but loopback, so it can never be exposed. A POST (the only way to start an
action) must also come from a page of the console itself (its ``Origin``
names this host), so another site cannot submit one with the sign-in cookie.
"""
from __future__ import annotations

import argparse
import asyncio
import contextlib
import datetime as dt
import hashlib
import json
import logging
import os
import re
import sys
import time
from collections import Counter
from pathlib import Path
from urllib.parse import parse_qs, urlencode, urlparse

from markupsafe import Markup, escape
from starlette.applications import Starlette
from starlette.middleware import Middleware
from starlette.middleware.base import BaseHTTPMiddleware
from starlette.requests import Request
from starlette.responses import HTMLResponse, PlainTextResponse, RedirectResponse, Response, StreamingResponse
from starlette.routing import Mount, Route
from starlette.staticfiles import StaticFiles
from starlette.templating import Jinja2Templates

from swhurl import ROOT, dashboards, kube
from swhurl.apps import contract, declaration, new, ops, repo
from swhurl.apps import promotion as gate
from swhurl.apps.instance import ENVIRONMENTS, Instance
from swhurl.apps.state import verdict
from swhurl.apps.yaml_file import EditError
from swhurl.console import actions, catalogue, changes, cluster, jobstore, logconfig, promotion, repos
from swhurl.run import CommandError, Runner, SharedReads

IDENTITY_HEADER = 'X-Auth-Request-Email'
DEV_IDENTITY = 'operator@localhost (dev)'
LOOPBACK = ('127.0.0.1', '::1', 'localhost')
TEMPLATES = Jinja2Templates(directory=Path(__file__).parent / 'templates')
STATIC = Path(__file__).parent / 'static'
# In every asset URL, so a new console image is never served with an old stylesheet from a browser's cache.
ASSETS = hashlib.sha256(b''.join(p.read_bytes() for p in sorted(STATIC.rglob('*')) if p.is_file())).hexdigest()[:12]
TEMPLATES.env.globals['assets'] = ASSETS
# Pages that only show state re-read themselves; pages with forms never change under you.
SELF_REFRESHING = {'overview.html', 'apps.html', 'platform.html', 'activity.html'}
# A Ready status ('True', 'False', 'Unknown') as a CSS class.
TEMPLATES.env.filters['state'] = lambda status: {'True': 'ok', 'False': 'bad'}.get(status, 'warn')
# Every page names an app instance <app>/<env> (never its Flux unit's name); see cluster.target.
HASH = re.compile(r'\b([0-9a-f]{12})[0-9a-f]{8,}\b')
STACK_NAMES = {'typescript': 'TypeScript', 'kotlin': 'Kotlin'}  # a stack as the language it is
STREAM_POLL = 0.25
KEEPALIVE = 15.0


def short_hashes(text: object) -> Markup:
    """Escape ``text`` and shorten each commit SHA or digest to 12 characters, the full value on hover."""
    return Markup(HASH.sub(lambda m: f'<span class="hash" title="{m[0]}">{m[1]}…</span>', str(escape(text))))


TEMPLATES.env.filters['short'] = short_hashes
TEMPLATES.env.globals['target'] = cluster.target
TEMPLATES.env.globals['default_host'] = contract.default_host


def logs_url(app: str) -> str:
    """HyperDX, filtered to one app's logs and traces."""
    return f'https://clickstack.{contract.COOKIE_DOMAIN}/search?' + urlencode({'where': f'ServiceName:"{app}"', 'whereLanguage': 'lucene'})


TEMPLATES.env.globals['logs_url'] = logs_url


def dashboard_urls(runner: Runner) -> dict[str, str]:
    """Each app's HyperDX dashboard, from the record the dashboard job keeps (``console/app-dashboards``).
    Empty when it cannot be read: the pages then leave the link out."""
    return {app: f'https://clickstack.{contract.COOKIE_DOMAIN}/dashboards/{ident}' for app, ident in dashboards.recorded(runner).items()}


def ago(when: dt.datetime | str, now: dt.datetime | None = None) -> str:
    """A time as how long ago it was (``3 h ago``); text that is not a time is returned as it is."""
    now = now or dt.datetime.now(dt.UTC)
    if isinstance(when, str):
        try:
            if len(when) == 10:  # a date alone: no hour to count from
                days = (now.date() - dt.date.fromisoformat(when)).days
                return {0: 'today', 1: 'yesterday'}.get(days, f'{days} days ago')
            when = dt.datetime.fromisoformat(when.replace('Z', '+00:00'))
        except ValueError:
            return when
    if when.tzinfo is None:
        when = when.replace(tzinfo=dt.UTC)
    seconds = (now - when).total_seconds()
    if seconds < 90:
        return 'just now'
    if seconds < 90 * 60:
        return f'{round(seconds / 60)} min ago'
    if seconds < 36 * 3600:
        return f'{round(seconds / 3600)} h ago'
    return f'{round(seconds / 86400)} days ago'


TEMPLATES.env.filters['ago'] = ago


def follow(url: str) -> Markup:
    """htmx attributes for something that says a job is running: re-read the page it is on shortly,
    so the row it sits in shows what the job did without anyone pressing Refresh."""
    return Markup(f'hx-get="{escape(url)}" hx-trigger="load delay:10s" hx-select="main" hx-target="main" hx-swap="outerHTML"')


TEMPLATES.env.globals['follow'] = follow
TEMPLATES.env.globals['merge_mode'] = promotion.merge_mode


def took(job: actions.Job) -> str:
    """How long a finished job ran."""
    seconds = round((job.finished - job.started).total_seconds()) if job.finished else 0
    return f'{seconds} s' if seconds < 90 else f'{round(seconds / 60)} min'


TEMPLATES.env.filters['took'] = took


def in_place(request: Request) -> bool:
    """Whether a button in a list sent this (htmx): its answer then replaces the button, so you stay on the list."""
    return request.headers.get('hx-request') == 'true'


def started(request: Request, job: actions.Job) -> Response:
    """The answer to starting a job: in a list, a link to it where the button was (and the page reads
    itself again shortly); from a plain form, the job's page."""
    if not in_place(request):
        return RedirectResponse(f'/jobs/{job.id}', status_code=303)
    here = urlparse(request.headers.get('hx-current-url', ''))
    back = here.path + (f'?{here.query}' if here.query else '') if here.path.startswith('/') else '/'
    return HTMLResponse(f'<a class="started" href="/jobs/{job.id}" {follow(back)}><span class="mark updating" aria-hidden="true">'
                        f'</span> {escape(job.action)} started · job #{job.id}</a>')


def refusal(error: Exception) -> Response:
    """Why a button in a list did nothing, where the button was (htmx swaps only a 2xx answer)."""
    return HTMLResponse(f'<span class="bad" role="alert">{escape(error)}</span>')


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


def job_store(runner: Runner, dev_identity: str | None) -> jobstore.JobStore | None:
    """Jobs are remembered outside the pod only by the console in the cluster: a local --dev console
    reads the same cluster, and must not write there or be taken for a pod that went away."""
    pod = os.environ.get('HOSTNAME', '')
    return jobstore.JobStore(runner, pod) if os.environ.get('KUBERNETES_SERVICE_HOST') and pod and not dev_identity else None


def create_app(runner: Runner, *, dev_identity: str | None = None, jobs: actions.Jobs | None = None,
               github: changes.GitHub | None = None, app_repos: repos.AppReposToken | None = None,
               repo_opener: repo.Opener | None = None, documents: catalogue.Reader | None = None) -> Starlette:
    """``github`` and ``app_repos`` default to the tokens in the environment (none: the new-app form says so)."""
    jobs = jobs or actions.Jobs(runner, store=job_store(runner, dev_identity))
    github = github or changes.github_from_env(runner)
    app_repos = app_repos or repos.token_from_env(runner)
    # From here every handler reads through this: within one page (``reading`` and the app page) a
    # read runs once and independent reads run at once; a POST and a job get the runner unchanged.
    runner = SharedReads(runner)

    def platform_file(path: str) -> str | None:
        """A file of this repository's main branch, through the console's GitHub token."""
        api = changes.GitHubAPI(runner, github)
        try:
            reply = api.client.get(f'/contents/{path}', params={'ref': 'main'}, headers={'Accept': 'application/vnd.github.raw+json'})
        finally:
            api.client.close()
        if reply.status_code == 404:
            return None
        reply.raise_for_status()
        return reply.text

    def local_file(path: str) -> str | None:
        return (ROOT / path).read_text() if (ROOT / path).is_file() else None

    # What apps offer is described in Git (apps/<app>/<env>/offers/): through GitHub, or without a token
    # from this checkout if it has one (console --dev); the image has no apps/, so then not at all.
    document_source = documents
    if not document_source:
        if github:
            document_source = platform_file
        elif (ROOT / 'apps').is_dir():
            document_source = local_file
        else:
            document_source = None
    described = catalogue.Documents(document_source)

    def offer_text(row: cluster.AppRow, event: str = '') -> str:
        """What an app's own documents say about its API, or about one of its events ('' if nothing)."""
        found = described.of(row.instance.app, row.instance.env, row.connections.offers)
        if event:
            return next((e.description for e in found.events if e.name == event), '')
        return found.api.description if found.api else ''

    def page(request: Request, name: str, status_code: int = 200, **context) -> Response:
        context.update(identity=request.state.identity, path=request.url.path, refused=actions.REFUSED,
                       read_at=dt.datetime.now().astimezone().strftime('%H:%M'),
                       running_jobs=[j for j in jobs.recent() if j.state == 'running'])
        context.setdefault('github', github)
        context.setdefault('offer_text', offer_text)
        context.update(auto_refresh=name in SELF_REFRESHING and status_code == 200,
                       refresh_url=request.url.path + (f'?{request.url.query}' if request.url.query else ''))
        return TEMPLATES.TemplateResponse(request, name, context, status_code=status_code)

    def reading(request: Request, name: str, read, **context) -> Response:
        try:
            with runner.once():
                found = read()
            return page(request, name, **context, **found)
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
            checks, found, (prs, prs_error), rows = runner.together(
                lambda: cluster.platform_checks(runner), lambda: cluster.units(runner), open_prs, lambda: cluster.apps(runner))
            return {'checks': checks, 'units': found, 'apps': rows, 'problems': cluster.problems(checks, found),
                    'updating': cluster.updating(found),
                    'prs': prs, 'prs_error': prs_error, 'recent': [j for j in jobs.recent() if j.state != 'running'][:5]}
        return reading(request, 'overview.html', read)

    def apps(request: Request) -> Response:
        """Every app as a list (optionally one team's) or, with ?view=map, one environment as a map of who calls whom."""
        view, team = request.query_params.get('view', 'list'), request.query_params.get('team', '')
        env = request.query_params.get('env', 'staging')
        attention = request.query_params.get('show') == 'attention'

        def read():
            rows = cluster.apps(runner)
            summaries = cluster.summaries(rows)
            teams = sorted({summary.team for summary in summaries if summary.team})
            shown = [summary for summary in summaries if not team or summary.team == team]
            # What needs you comes first; within each group, by name.
            shown.sort(key=lambda summary: (all(row.state.key == 'healthy' for row in summary.envs.values()), summary.app))
            states = [row.state.key for summary in shown for row in summary.envs.values()]
            tally = {'healthy': states.count('healthy'), 'updating': states.count('updating'), 'apps': len(shown),
                     'attention': len(states) - states.count('healthy') - states.count('updating')}
            if attention:
                shown = [summary for summary in shown if any(row.state.key != 'healthy' for row in summary.envs.values())]
            return {'apps': shown, 'teams': teams, 'tally': tally, 'attention': attention, 'team': team,
                    'stacks': stacks([summary.app for summary in shown]) if view != 'map' else {},
                    'stack_names': STACK_NAMES, 'owner': contract.APP_OWNER,
                    'dashboards': dashboard_urls(runner) if view != 'map' else {}, 'view': 'map' if view == 'map' else 'list', 'env': env if env in ENVIRONMENTS else 'staging',
                    'map': cluster.app_map([row for row in rows if not team or row.team == team],
                                           env if env in ENVIRONMENTS else 'staging') if view == 'map' else None}
        return reading(request, 'apps.html', read)

    def catalogue_page(request: Request) -> Response:
        """What the apps of one environment offer each other: APIs and events, described where the app describes them."""
        env = request.query_params.get('env', 'staging')
        env = env if env in ENVIRONMENTS else 'staging'
        kind, team = request.query_params.get('kind', ''), request.query_params.get('team', '')

        def read():
            rows = cluster.apps(runner, states=False)
            found = catalogue.entries(rows, env, described)
            shown = [e for e in found if (not kind or e.kind == kind) and (not team or e.team == team)]
            # An app that only uses is listed while no kind is chosen: it offers neither.
            grouped = [g for g in catalogue.groups(rows, env, shown) if (g.entries or not kind) and (not team or g.team == team)]
            return {'entries': shown, 'groups': grouped, 'team_of': {g.app: g.team for g in catalogue.groups(rows, env, [])},
                    'teams': sorted({row.team for row in rows if row.instance.env == env and row.team}), 'env': env,
                    'kind': kind if kind in ('api', 'event') else '', 'team': team, 'owner': contract.APP_OWNER}
        return reading(request, 'catalogue.html', read)

    def read_app(request: Request) -> Response:
        found = Instance.named(request.path_params['app'], request.path_params['env'])
        status, rows = runner.together(lambda: ops.gather_status(runner, found),
                                       lambda: cluster.apps(runner)) if found else (None, [])
        if status is None and found:
            # Created here but not applied yet: say so instead of "no such app" (its links appear at once).
            label = f'{found.app}/{found.env}'
            pending = [j for j in jobs.recent() if j.unit == label and j.action.startswith('new app')]
            prs, prs_error = open_prs()
            prs = [pr for pr in prs if changes.creates_instance(pr, found.app, found.env)]
            prs += [pr for pr in open_prs()[0] if found.env == 'prod' and promotion.matching(pr, found.app)]
            in_git = False
            if not pending and not prs and github:
                in_git = promotion.main_tree(runner, github, lambda root: found.folder(root).exists())
            if pending or prs or in_git:
                return page(request, 'pending.html', label=label, jobs=pending, prs=prs, prs_error=prs_error, in_git=in_git)
        if status is None:
            return page(request, 'error.html', status_code=404, error='No such app instance.')
        summary = next((a for a in cluster.summaries(rows) if a.app == found.app), None)
        promotion_problem = gate.source_problem(promotion.settled_status(runner, github, status), status.desired_image) if found.env == 'staging' else ''
        row = summary.envs.get(found.env) if summary else None
        return page(request, 'app.html', status=status, team=(row.team if row else '') or ops.team_of(runner, found),
                    connections=row.connections if row else cluster.Connections(), connectable=connectable(found.env, found.app), logs=logs_url(found.app), dashboard=dashboard_urls(runner).get(found.app, ''),
                    stack=stacks([found.app]).get(found.app, ''), stack_names=STACK_NAMES, owner=contract.APP_OWNER,
                    scale_fields=changes.SCALE_FIELDS, github=github,
                    promotion_problem=promotion_problem,
                    exposures=ops.EXPOSURE_LABELS, exposure_text=changes.EXPOSURE_LABELS, domain=contract.COOKIE_DOMAIN,
                    summary=summary, state=verdict(status), unit_jobs=[j for j in jobs.recent() if j.unit in (found.unit, f'{found.app}/{found.env}')])

    def app(request: Request) -> Response:
        try:
            with runner.once():
                return read_app(request)
        except (CommandError, cluster.ReadError, actions.ActionError, EditError) as error:
            return page(request, 'error.html', status_code=502, error=str(error))

    def platform(request: Request) -> Response:
        def read():
            checks, found = runner.together(lambda: cluster.platform_checks(runner), lambda: cluster.units(runner))
            layers = [(title, text, [u for u in found if u.layer == title])
                      for title, text in [(t, d) for _, t, d in cluster.LAYERS] + [(cluster.OTHER_LAYER, '')]]
            revisions = Counter(u.revision for u in found if u.revision).most_common(1)
            return {'checks': checks, 'units': found, 'problems': cluster.problems(checks, found), 'updating': cluster.updating(found),
                    'layers': [layer for layer in layers if layer[2]],
                    'groups': [(section, cluster.condensed(group, ago)) for section, group in checks.sections
                               if section != cluster.FLUX_SECTION],
                    # What nearly every unit has applied: said once, so a row shows a revision only when it differs.
                    'revision': revisions[0][0] if revisions else ''}
        return reading(request, 'platform.html', read)

    def unit(request: Request) -> Response:
        def read():
            found = {u.name: u for u in cluster.units(runner)}
            name = request.path_params['unit']
            if name not in found:
                raise LookupError(name)
            return {'u': found[name], 'app': Instance.of_unit(name),
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
        except (actions.ActionError, cluster.ReadError) as error:
            if in_place(request):
                return refusal(error)
            return page(request, 'error.html', status_code=409 if isinstance(error, actions.ActionError) else 502, error=str(error))
        return started(request, job)

    def job(request: Request) -> Response:
        found = jobs.get(request.path_params['id'])
        if found is None:
            return page(request, 'error.html', status_code=404, error='No such job (only the newest jobs are kept).')
        return page(request, 'job.html', job=found, can_resume=bool(resumable(found) and github and app_repos))

    async def job_events(request: Request) -> Response:
        found = jobs.get(request.path_params['id'])
        if found is None or not found.live:  # another pod's job: its page re-reads itself instead
            return PlainTextResponse('No such job here.\n', status_code=404)

        # EventSource supplies Last-Event-ID on reconnect. The query is the
        # initial page's already-rendered line count.
        raw = request.headers.get('last-event-id')
        if raw is None:
            raw = request.query_params.get('after', '0')
        try:
            sent = max(0, int(raw))
        except (TypeError, ValueError):
            sent = 0
        sent = min(sent, len(found.lines))

        async def stream():
            nonlocal sent
            last_activity = time.monotonic()
            while True:
                finished = found.finished is not None
                new = found.lines[sent:]
                for line in new:
                    sent += 1
                    body = json.dumps({'html': str(short_hashes(line))}, ensure_ascii=False)
                    yield f'id: {sent}\nevent: line\ndata: {body}\n\n'
                    last_activity = time.monotonic()
                if finished and not new:
                    body = json.dumps({'state': found.state,
                                       'finished': found.finished.strftime('%H:%M:%S'), 'link': found.link})
                    yield f'event: done\ndata: {body}\n\n'
                    return
                idle = time.monotonic() - last_activity
                if idle >= KEEPALIVE:
                    yield ': keep-alive\n\n'
                    last_activity = time.monotonic()
                await asyncio.sleep(min(STREAM_POLL, max(0.001, KEEPALIVE - (time.monotonic() - last_activity))))

        return StreamingResponse(stream(), media_type='text/event-stream',
                                 headers={'Cache-Control': 'no-cache', 'X-Accel-Buffering': 'no'})

    questions_cache: dict[str, tuple[float, list[repo.Question]]] = {}

    def stack_questions(stack: str) -> list[repo.Question]:
        """The stack template's questions (its copier.yml on GitHub), read at most every 5 minutes."""
        cached = questions_cache.get(stack)
        if cached is not None and time.monotonic() - cached[0] <= 300:
            return cached[1]
        found = repo.template_questions(stack, **({'opener': repo_opener} if repo_opener else {}))
        questions_cache[stack] = (time.monotonic(), found)
        return found

    teams_cache: list[tuple[float, dict[str, str]]] = []

    def platform_teams() -> tuple[dict[str, str], str]:
        """``(registered teams, problem)``, read from main at most every 5 minutes; none on a problem."""
        if teams_cache and time.monotonic() - teams_cache[0][0] <= 300:
            return teams_cache[0][1], ''
        if github is None:
            return {}, 'No GitHub token is configured, so the team registry on main cannot be read.'
        try:
            found = changes.platform_teams(runner, github)
        except actions.ActionError as problem:
            return {}, str(problem)
        teams_cache[:] = [(time.monotonic(), found)]
        return found, ''

    channels_cache: list[tuple[float, tuple[dict[str, str], dict[str, list[str]]]]] = []

    def platform_channels() -> tuple[dict[str, str], dict[str, list[str]]]:
        """``(each channel's transport, each team's channels)`` from main, at most every 5 minutes; none
        when they cannot be read. Names only: the console never holds an address."""
        if channels_cache and time.monotonic() - channels_cache[0][0] <= 300:
            return channels_cache[0][1]
        if github is None:
            return {}, {}
        try:
            found = changes.platform_channels(runner, github)
        except actions.ActionError:
            return {}, {}
        channels_cache[:] = [(time.monotonic(), found)]
        return found

    stacks_cache: dict[str, tuple[float, str]] = {}

    def stacks(apps: list[str]) -> dict[str, str]:
        """Each app's stack (its language), from the swhurl.yaml on its repository's default branch: kept for an hour, since an app does
        not change language, and a failure for 5 minutes, after which the rest are not asked for either
        (GitHub being away must not make the page wait once per app)."""
        reachable = True
        for app in apps:
            cached = stacks_cache.get(app)
            if cached and time.monotonic() - cached[0] <= (3600 if cached[1] else 300):
                continue
            found = ''
            if reachable:
                try:
                    opener = {'opener': lambda request: repo_opener(request)[1]} if repo_opener else {}
                    declared = declaration.read(f'{contract.APP_OWNER}/{app}', **opener)
                    found = declared.stack if declared else ''
                except declaration.Unreachable:
                    reachable = False
                except declaration.DeclarationError:
                    pass  # a file the platform would refuse names no stack
            stacks_cache[app] = (time.monotonic(), found)
        return {app: stacks_cache[app][1] for app in apps}

    def teams_page(request: Request, status_code: int = 200, error: str = '') -> Response:
        """Every team with the apps that belong to it, and the changes that keep the two in step."""
        def read():
            registered, registry_error = platform_teams()
            owned: dict[str, dict[str, dict[str, cluster.AppRow]]] = {}  # team, app, environment
            for row in cluster.apps(runner):
                owned.setdefault(row.team, {}).setdefault(row.instance.app, {})[row.instance.env] = row
            # Apps whose team is not registered (or that carry none) are shown too: they are what a
            # careless change would leave behind, and the list says how to bring them back.
            unregistered = {team: apps for team, apps in owned.items() if team not in registered}
            if registry_error:
                # The registry is unknown, so nothing can be called unregistered: show the teams the
                # apps name instead of raising a false alarm.
                registered = registered | {team: '' for team in owned if team}
                unregistered = {team: apps for team, apps in owned.items() if not team}
            latest: dict[str, actions.Job] = {}  # each team's newest job, shown in its row
            for job in jobs.recent():
                if job.unit.startswith('team:'):
                    latest.setdefault(job.unit[5:], job)
            channels, team_channels = platform_channels()
            return {'teams': registered, 'owned': owned, 'unregistered': unregistered, 'registry_error': registry_error,
                    'channels': channels, 'team_channels': team_channels,
                    'error': error, 'team_jobs': latest,
                    'stacks': stacks(sorted({app for apps in owned.values() for app in apps})),
                    'dashboards': dashboard_urls(runner),
                    'stack_names': STACK_NAMES}
        return reading(request, 'teams.html', read, status_code=status_code)

    async def change_team(request: Request) -> Response:
        """Add, edit or remove a team: a PR made by the clone's team-* command, which refuses anything
        that would leave an app without a registered team."""
        action = request.path_params['action']
        if github is None:
            return page(request, 'error.html', status_code=409, error='No GitHub token is configured (console-github Secret).')
        form = {k: v[0] for k, v in parse_qs((await request.body()).decode(), keep_blank_values=True).items()}
        try:
            change = changes.Change.to_team(action, form)
        except actions.ActionError as error:
            return teams_page(request, 400, str(error))
        return propose(request, change, then=(teams_cache, channels_cache))  # the next page reads the registry again

    async def change_channel(request: Request) -> Response:
        """Give a team its notification channel (or a new address for it), or remove it: a PR made by
        the clone's team-channel command. The address goes to that command's standard input and is
        redacted from every message; it is never rendered back, logged or put in the pull request."""
        action = request.path_params['action']
        if github is None:
            return page(request, 'error.html', status_code=409, error='No GitHub token is configured (console-github Secret).')
        form = {k: v[0] for k, v in parse_qs((await request.body()).decode(), keep_blank_values=True).items()}
        runner.add_secret(form.get('address', '').strip())
        try:
            change = changes.Change.to_channel(action, form)
        except actions.ActionError as error:
            return teams_page(request, 400, str(error))
        return propose(request, change, then=(channels_cache,))

    def connectable(env: str = 'staging', app: str = '') -> tuple[list[cluster.AppRow], list[tuple[str, cluster.AppRow]], str]:
        """What an app can connect to in ``env``: the web apps it could call, each published
        ``<app>.<event>`` with its publisher, and why they could not be read (the form still renders).
        For an existing ``app``, without itself and what it is connected to already."""
        try:
            rows = [row for row in cluster.apps(runner, states=False) if row.instance.env == env]
        except cluster.ReadError as problem:
            return [], [], str(problem)
        mine = next((row.connections for row in rows if row.instance.app == app), cluster.Connections())
        others = [row for row in rows if row.instance.app != app]
        return ([row for row in others if row.connections.address and row.instance.app not in mine.calls],
                [(name, row) for row in others for event in row.connections.publishes
                 if (name := f'{row.instance.app}.{event}') not in mine.subscribes], '')

    def new_repo_form(request: Request, status_code: int = 200, error: str = '', form=None) -> Response:
        form = form or {}
        teams, teams_error = platform_teams()
        callable_apps, offered_events, connections_error = connectable()
        features, features_error = {}, ''
        for stack in contract.STACKS:
            try:
                features[stack] = stack_questions(stack)
            except repo.RepoError as problem:
                features[stack], features_error = [], str(problem)
        return page(request, 'new_repo.html', status_code=status_code,
                    features=features, features_error=features_error,
                    stacks=contract.STACKS, stack_names=STACK_NAMES, stack_text=contract.STACK_DESCRIPTIONS,
                    owner=contract.APP_OWNER, teams=teams, teams_error=teams_error,
                    team_channels=platform_channels()[1],
                    callable_apps=callable_apps, offered_events=offered_events, connections_error=connections_error,
                    chosen_calls=[n for n in form.get('calls', '').split(',') if n],
                    chosen_events=[n for n in form.get('subscribe', '').split(',') if n],
                    exposure_text=changes.EXPOSURE_LABELS, domain=contract.COOKIE_DOMAIN, form=form, error=error,
                    github=github, app_repos=app_repos)

    async def new_repo(request: Request) -> Response:
        fields = parse_qs((await request.body()).decode(), keep_blank_values=True)
        form = {k: v[0] for k, v in fields.items()}
        for ticked in ('calls', 'subscribe'):  # one checkbox per app or event
            form[ticked] = ','.join(value for value in fields.get(ticked, []) if value)
        if github is None or app_repos is None:
            return new_repo_form(request, 409, 'Both GitHub tokens must be configured (console-github Secret).', form)
        try:
            questions = stack_questions(form.get('stack', '').strip()) if form.get('stack', '').strip() in contract.STACKS else []
            callable_apps, offered_events, problem = connectable()
            if problem and (form['calls'] or form['subscribe']):
                raise actions.ActionError(f'cannot check the apps and events chosen: {problem}')
            chosen = changes.NewApp.from_form(form, questions, platform_teams()[0], {row.instance.app for row in callable_apps},
                                              {name for name, _ in offered_events})
        except (actions.ActionError, repo.RepoError) as error:
            return new_repo_form(request, 400, str(error), form)
        try:
            job = jobs.submit('new app and repository', chosen.target, request.state.identity,
                              new_app_work(chosen, create=True), resume=chosen.record)
        except actions.ActionError as error:
            return new_repo_form(request, 409, str(error), form)
        return RedirectResponse(f'/jobs/{job.id}', status_code=303)

    def new_app_work(chosen: changes.NewApp, *, create: bool):
        """Create the app's repository (or, finishing an earlier job, find the one it created), wait for
        its image and open the platform pull request that adds its staging instance."""
        def work(job: actions.Job) -> None:
            client = repos.AppRepos(runner, app_repos)
            opener = {'opener': repo_opener} if repo_opener else {}
            try:
                image = (repos.create_app_repo(runner, client, job, chosen.request, **opener) if create
                         else repos.finish_app_repo(client, job, chosen.request, **opener))
            finally:
                client.close()
            job.link = chosen.change(image).open(runner, github, job)
        return work

    def resumable(found: actions.Job) -> dict | None:
        """What is needed to finish ``found``, if it is a new-app job that ended without its pull request
        and no later job has taken its place."""
        told = found.resume or {}
        if found.state not in ('failed', 'interrupted') or found.link or told.get('kind') != changes.NEW_APP:
            return None
        later = any(j.unit == found.unit and j.id > found.id and j.state in ('running', 'succeeded') for j in jobs.recent())
        return None if later else told

    def resume_job(request: Request) -> Response:
        """Finish a new-app job that stopped after creating its repository: the same choices, no second repository."""
        found = jobs.get(request.path_params['id'])
        told = resumable(found) if found else None
        if told is None:
            return page(request, 'error.html', status_code=404, error='No such job, or nothing of it is left to finish.')
        if github is None or app_repos is None:
            return page(request, 'error.html', status_code=409, error='Both GitHub tokens must be configured (console-github Secret).')
        try:
            job = jobs.submit('finish new app', found.unit, request.state.identity,
                              new_app_work(changes.NewApp.from_record(told), create=False), resume=dict(told))
        except (KeyError, TypeError, ValueError):
            return page(request, 'error.html', status_code=409, error='The record of that job cannot be used to finish it; '
                        'add the app with the make app-new line in docs/apps.md.')
        except actions.ActionError as error:
            return page(request, 'error.html', status_code=409, error=str(error))
        return RedirectResponse(f'/jobs/{job.id}', status_code=303)

    def review_promotion(request: Request) -> Response:
        app = request.path_params['app']
        found = Instance.named(app, 'staging')
        if found is None or github is None:
            return page(request, 'error.html', status_code=409, error='Staging and a GitHub token are required.')
        try:
            existing = [pr for pr in changes.console_prs(runner, github) if promotion.matching(pr, app)]
            if existing:
                return page(request, 'promotion.html', review={'app': app}, prs=existing)
            status = promotion.staging_status(runner, github, found)
            if status is None:
                raise actions.ActionError('staging is missing; deploy staging before promoting')
            reviewed = promotion.main_tree(runner, github, lambda root: promotion.review(root, app, status))
            return page(request, 'promotion.html', review=reviewed, prs=existing)
        except (EditError, new.GenerationError, actions.ActionError) as error:
            return page(request, 'error.html', status_code=409, error=str(error))
        except (CommandError, cluster.ReadError) as error:
            return page(request, 'error.html', status_code=502, error=str(error))

    async def control_promotion(request: Request) -> Response:
        if github is None:
            return page(request, 'error.html', status_code=409, error='No GitHub token is configured.')
        number, action = request.path_params['number'], request.path_params['action']
        def work(job):
            job.link = promotion.control(runner, github, number, action)
            job.lines.append('PR held for manual review.' if action == 'hold' else
                             'Automatic merge requested; validation and merge verification still apply.')
        try:
            job = jobs.submit(action + ' promotion PR', f'PR #{number}', request.state.identity, work)
        except actions.ActionError as error:
            return refusal(error) if in_place(request) else page(request, 'error.html', status_code=409, error=str(error))
        return started(request, job)

    async def change_app(request: Request) -> Response:
        """Promote, scale, change access to, change the connections of or remove an instance: a PR made by the clone's app-* commands."""
        app, env, change = (request.path_params[k] for k in ('app', 'env', 'change'))
        found = Instance.named(app, env)
        if found is None or change != 'promote' and change not in changes.INSTANCE_CHANGES:
            return page(request, 'error.html', status_code=404, error='No such app instance or change.')
        if github is None:
            return page(request, 'error.html', status_code=409, error='No GitHub token is configured (console-github Secret).')
        fields = parse_qs((await request.body()).decode(), keep_blank_values=True)
        form = {k: v[0] for k, v in fields.items()}
        try:
            if change == 'promote':
                return promote(request, found, form)
            if change == 'connect':
                for ticked in ('calls', 'subscribe'):  # one checkbox per app or event
                    form[ticked] = ','.join(value for value in fields.get(ticked, []) if value)
            proposed = changes.Change.to_instance(change, found, form)  # malformed: refused before reading the cluster
            if change == 'connect' and proposed.touches_others:
                callable_apps, offered_events, problem = connectable(env, app)
                if problem:
                    raise actions.ActionError(f'cannot check the apps and events chosen: {problem}')
                changes.Connections.from_form(form).check_offered({row.instance.app for row in callable_apps},
                                                                  {name for name, _ in offered_events})
        except (actions.ActionError, EditError, new.GenerationError) as error:
            return page(request, 'error.html', status_code=400, error=str(error))
        except CommandError as error:
            return page(request, 'error.html', status_code=502, error=str(error))
        return propose(request, proposed)

    def promote(request: Request, found: Instance, form: dict[str, str]) -> Response:
        """Open the pull request for a reviewed promotion, or point at the one already open for the same image."""
        app = found.app
        if found.env != 'staging':
            raise actions.ActionError('promote from the staging instance')
        image = form.get('image', '')
        changes.checked_host(form.get('host', ''))
        if not image or not form.get('configuration') or not form.get('target'):
            raise actions.ActionError('refresh the staging app page and review the image before promotion')
        if not promotion.existing(runner, github, app, image):
            problem = gate.source_problem(promotion.staging_status(runner, github, found), image)
            if problem:
                raise actions.ActionError(problem)

        def work(job: actions.Job) -> None:
            previous = promotion.existing(runner, github, app, image)
            if previous:
                job.link = previous.url
                job.lines.append('Reusing the existing promotion pull request')
                return
            staging = promotion.staging_status(runner, github, found)
            problem = gate.source_problem(staging, image)
            if problem:
                raise actions.ActionError(problem)
            job.link = promotion.open_pr(runner, github, job, app, form, staging)
        try:
            job = jobs.submit('promote', f'{app}/prod', request.state.identity, work)
        except actions.ActionError as error:
            return page(request, 'error.html', status_code=409, error=str(error))
        return RedirectResponse(f'/jobs/{job.id}', status_code=303)

    def propose(request: Request, change: changes.Change, *, then: tuple = ()) -> Response:
        """Run ``change`` as a job that opens its pull request, then clear the ``then`` caches it made stale."""
        def work(job: actions.Job) -> None:
            job.link = change.open(runner, github, job)
            for cache in then:
                cache.clear()
        try:
            job = jobs.submit(change.kind, change.target, request.state.identity, work)
        except actions.ActionError as error:
            return page(request, 'error.html', status_code=409, error=str(error))
        return RedirectResponse(f'/jobs/{job.id}', status_code=303)

    async def use_offer(request: Request) -> Response:
        """The catalogue's Use: the app chosen there connects, as its own Connect to an app would."""
        chosen = parse_qs((await request.body()).decode()).get('app', [''])[0]
        if not chosen:
            return page(request, 'error.html', status_code=400, error='Choose the app that will use it.')
        request.path_params.update(app=chosen, change='connect')
        return await change_app(request)

    def activity(request: Request) -> Response:
        prs, prs_error = open_prs()
        return page(request, 'activity.html', jobs=jobs.recent(), prs=prs, prs_error=prs_error)

    def healthz(_request: Request) -> Response:
        return PlainTextResponse('ok\n')

    @contextlib.asynccontextmanager
    async def lifespan(_app):
        """On the way down (a new image, a changed token), finish the running jobs before the pod goes."""
        yield
        running = [j for j in jobs.recent() if j.live and j.state == 'running']
        if running:
            logging.getLogger('swhurl.console.jobs').info('Stopping: waiting for %d running job(s) to finish', len(running))
        if not await asyncio.to_thread(jobs.drain):
            logging.getLogger('swhurl.console.jobs').error('Stopping with jobs still running: they will show as interrupted')

    return Starlette(
        routes=[Route('/', overview), Route('/apps', apps), Route('/apps/{app}/{env}', app), Route('/services', moved('/apps?view=map')), Route('/catalogue', catalogue_page), Route('/catalogue/{env}/use', use_offer, methods=['POST']),
                Route('/teams', teams_page), Route('/teams/{action}', change_team, methods=['POST']),
                Route('/team-channel/{action}', change_channel, methods=['POST']),
                Route('/platform', platform), Route('/units', moved('/platform#units')), Route('/units/{unit}', unit),
                Route('/healthz', healthz), Route('/units/{unit}/{action}', start, methods=['POST']),
                Route('/apps/{app}/staging/promotion', review_promotion),
                Route('/promotion-prs/{number:int}/{action}', control_promotion, methods=['POST']),
                Route('/activity', activity), Route('/jobs', moved('/activity')), Route('/jobs/{id:int}', job),
                Route('/jobs/{id:int}/events', job_events), Route('/jobs/{id:int}/resume', resume_job, methods=['POST']),
                Route('/new', new_repo_form), Route('/new/repo', new_repo, methods=['POST']),
                Mount('/static', StaticFiles(directory=STATIC), name='static'),
                Route('/apps/{app}/{env}/{change}', change_app, methods=['POST'])],
        middleware=[Middleware(RequireIdentity, dev_identity=dev_identity)], lifespan=lifespan)


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
    # Open job pages (event streams) are cut after 20 seconds of a shutdown; the jobs themselves are waited for (lifespan).
    uvicorn.run(create_app(kube.runner(), dev_identity=DEV_IDENTITY if args.dev else None),
                host=args.host, port=args.port, log_level='info', log_config=logconfig.CONFIG, timeout_graceful_shutdown=20)
    return 0
