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
import functools
import hashlib
import logging
import os
import sys
import time
from contextvars import ContextVar
from dataclasses import dataclass, replace
from pathlib import Path
from urllib.parse import urlencode, urlparse

from starlette.applications import Starlette
from starlette.middleware import Middleware
from starlette.middleware.base import BaseHTTPMiddleware
from starlette.requests import Request
from starlette.responses import PlainTextResponse, RedirectResponse, Response
from starlette.routing import Mount
from starlette.staticfiles import StaticFiles
from starlette.templating import Jinja2Templates

from swhurl import ROOT, kube
from swhurl.apps import contract, declaration, repo
from swhurl.apps.instance import Instance
from swhurl.console import (
    actions,
    catalogue,
    cluster,
    flagstore,
    jobstore,
    logconfig,
    merge,
    promotion,
    proposal,
    repos,
    stages,
)
from swhurl.console import github as gh
from swhurl.console.context import Context, follow, logs_url, short_hashes
from swhurl.console.reading import LOG as TIMING
from swhurl.console.reading import Files
from swhurl.console.routes import activity, instance, new_app, pages, teams
from swhurl.run import Runner, SharedReads

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
KEPT = 60  # merged trees and merged pull requests kept between Activity reads; the oldest goes first
# A Ready status ('True', 'False', 'Unknown') as a CSS class.
TEMPLATES.env.filters['state'] = lambda status: {'True': 'ok', 'False': 'bad'}.get(status, 'warn')
TEMPLATES.env.filters['short'] = short_hashes
# Every page names an app instance <app>/<env> (never its Flux unit's name); see cluster.target.
TEMPLATES.env.globals['target'] = cluster.target
TEMPLATES.env.globals['default_host'] = contract.default_host
TEMPLATES.env.globals['logs_url'] = logs_url
TEMPLATES.env.globals['follow'] = follow
TEMPLATES.env.globals['merge_mode'] = promotion.merge_mode


def ungranted_url(instance: Instance) -> str:
    """HyperDX, filtered to the egress proxy's lines for one instance's requests that no grant covers."""
    return f'https://clickstack.{contract.COOKIE_DOMAIN}/search?' + urlencode(
        {'where': f'ServiceName:"egress" AND "instance={instance.namespace} granted=-"', 'whereLanguage': 'lucene'})


TEMPLATES.env.globals['ungranted_url'] = ungranted_url


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


def took(job: actions.Job) -> str:
    """How long a finished job ran."""
    seconds = round((job.finished - job.started).total_seconds()) if job.finished else 0
    return f'{seconds} s' if seconds < 90 else f'{round(seconds / 60)} min'


TEMPLATES.env.filters['took'] = took


@dataclass(frozen=True)
class ChangeRow:
    """One change on Activity: a job with what became of its pull request, or an open pull request
    whose job is no longer remembered."""
    id: str
    track: stages.Track
    job: actions.Job | None = None
    pr: gh.PullRequest | None = None  # its pull request while it is open


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
               github: gh.GitHub | None = None, app_repos: repos.AppReposToken | None = None,
               repo_opener: repo.Opener | None = None, documents: catalogue.Reader | None = None,
               flags: flagstore.FlagStore | None = None) -> Starlette:
    """``github`` and ``app_repos`` default to the tokens in the environment (none: the new-app form says so),
    ``flags`` to the console's NATS key (none: the flag form says so)."""
    flags = flags or flagstore.FlagStore.from_env()
    jobs = jobs or actions.Jobs(runner, store=job_store(runner, dev_identity))
    github = github or gh.github_from_env(runner)
    app_repos = app_repos or repos.token_from_env(runner)
    # From here every handler reads through this: within one page (``reading`` and the app page) a
    # read runs once and independent reads run at once; a POST and a job get the runner unchanged.
    runner = SharedReads(runner)

    def platform_file(path: str) -> str | None:
        """A file of this repository's main branch, through the console's GitHub token."""
        api = gh.GitHubAPI(runner, github)
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
    file_source = document_source
    request_files: ContextVar[Files | None] = ContextVar('request_files', default=None)
    if file_source is not None:
        def document_source(path: str) -> str | None:
            scoped = request_files.get()
            return scoped(path) if scoped is not None else file_source(path)
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

    def open_prs() -> tuple[list[gh.PullRequest], str]:
        """The console's open PRs, or why they could not be listed (the page still renders)."""
        if github is None:
            return [], 'No GitHub token is configured, so open pull requests are not listed.'
        try:
            return gh.console_prs(runner, github), ''
        except actions.ActionError as error:
            return [], f'Could not list open pull requests: {error}'

    def pull_view(pr: gh.PullRequest) -> stages.Pull:
        auto = gh.AUTO_MERGE_LABEL in pr.labels
        reason = '' if auto else promotion.merge_mode(pr).removeprefix('Manual merge required').removeprefix(': ')
        return stages.Pull(pr.number, pr.url, 'open', pr.checks, pr.checks_url or pr.url, auto,
                           reason.split('. ')[0].rstrip('.'))

    # Facts that never change once read, kept across page reads; never progress verdicts.
    renders: dict[str, dict[str, stages.Target]] = {}  # a commit's render targets
    merged: dict[int, stages.Pull] = {}  # merged pull requests (a closed one can be reopened)

    def keep(cache: dict, key, value) -> None:
        if len(cache) >= KEPT:
            cache.pop(next(iter(cache)))  # the oldest
        cache[key] = value

    def pull_of(number: int, url: str, read: bool) -> stages.Pull:
        if number in merged:
            return merged[number]
        if not read or github is None:
            return stages.Pull(number, url)
        try:
            state, when, commit = gh.pull_state(runner, github, number)
        except actions.ActionError:
            return stages.Pull(number, url)
        found = stages.Pull(number, url, state, when=when, commit=commit)
        if state == 'merged':
            keep(merged, number, found)
        return found

    def targets(commit: str) -> dict[str, stages.Target] | None:
        """A commit's render targets, or None when they could not be read (not kept: read again next time)."""
        if not commit or github is None:
            return None
        TIMING.info('render targets cache=%s revision=%s', 'hit' if commit in renders else 'miss', commit[:12])
        if commit not in renders:
            try:
                keep(renders, commit, stages.render_targets(runner, github, commit))
            except actions.ActionError:
                return None
        return renders.get(commit)

    def main_targets() -> tuple[str, dict[str, stages.Target] | None]:
        try:
            head = gh.main_commit(runner, github) if github is not None else ''
        except actions.ActionError:
            return '', None
        return head, targets(head)

    def cluster_rows() -> dict[str, cluster.AppRow] | None:
        try:
            return {f'{row.instance.app}/{row.instance.env}': row for row in cluster.apps(runner, include_removing=True)}
        except cluster.ReadError:
            return None

    def progress_of(job: actions.Job, merged: dict[str, stages.Target] | None, rows: dict[str, cluster.AppRow] | None,
                    main: dict[str, stages.Target] | None) -> stages.Progress:
        """How far ``job``'s merged change has got (stages.progress); records the first time it is seen at its end."""
        def at(tree: dict[str, stages.Target] | None) -> stages.Target | None:
            return None if tree is None else tree.get(job.unit, stages.Target())  # missing from a read tree: absent
        live = None if rows is None else rows.get(job.unit, stages.ABSENT)
        found = stages.progress(job.action, at(merged), at(main), live, job.reached)
        if found.state in stages.REACHED:
            jobs.reach(job)
        return found

    def change_rows(found: list[actions.Job], *, others: bool) -> tuple[list[ChangeRow], str]:
        """Each job as a change with its whole path, and with ``others`` the open pull requests no
        remembered job opened. Also why open pull requests could not be listed, if they could not.
        Independent reads (pull requests, then the cluster, main and merged trees) run at once."""
        proposed = {job.id: stages.number(job) for job in found if job.state == 'succeeded'}
        proposed = {job_id: number for job_id, number in proposed.items() if number}
        prs, prs_error = open_prs() if others or proposed else ([], '')
        is_open = {pr.number: pr for pr in prs}
        pulls = {job_id: pull_view(is_open[number]) for job_id, number in proposed.items() if number in is_open}
        closed = [job for job in found if job.id in proposed and job.id not in pulls]
        read = not prs_error and github is not None
        pulls |= dict(zip((job.id for job in closed), runner.together(
            *(functools.partial(pull_of, proposed[job.id], job.link, read) for job in closed)), strict=True))

        def lands(job: actions.Job) -> bool:
            """A merged change to an instance: where it ends is on the cluster."""
            pull = pulls.get(job.id)
            return bool(pull and pull.state == 'merged' and stages.end_label(job.action, job.unit) != 'On main')

        states: dict[str, cluster.AppRow] | None = {}
        current = None
        trees: dict[str, dict[str, stages.Target] | None] = {}
        landing = [job for job in found if lands(job)]
        if landing:
            commits = sorted({pulls[job.id].commit for job in landing})
            states, (_, current), *read_trees = runner.together(
                cluster_rows, main_targets, *(functools.partial(targets, commit) for commit in commits))
            trees = dict(zip(commits, read_trees, strict=True))
        rows = []
        for job in found:
            pull = pulls.get(job.id)
            landed = progress_of(job, trees.get(pull.commit), states, current) if lands(job) else None
            track = stages.track(job, pull and replace(pull, when=ago(pull.when) if pull.when else ''), landed)
            rows.append(ChangeRow(f'job-{job.id}', track, job, is_open.get(proposed.get(job.id))))
        if others:
            covered = set(proposed.values())
            rows += [ChangeRow(f'pr-{pr.number}', stages.pull_track(pull_view(pr), pr.branch), pr=pr)
                     for pr in prs if pr.number not in covered]
        return rows, prs_error

    teams_cache: list[tuple[float, dict[str, str]]] = []

    def platform_teams() -> tuple[dict[str, str], str]:
        """``(registered teams, problem)``, read from main at most every 5 minutes; none on a problem."""
        if teams_cache and time.monotonic() - teams_cache[0][0] <= 300:
            return teams_cache[0][1], ''
        if github is None:
            return {}, 'No GitHub token is configured, so the team registry on main cannot be read.'
        try:
            found = gh.platform_teams(runner, github)
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
            found = gh.platform_channels(runner, github)
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

    def resumable(found: actions.Job) -> dict | None:
        """What is needed to finish ``found``, if it is a new-app job that ended without its pull request
        and no later job has taken its place."""
        told = found.resume or {}
        if found.state not in ('failed', 'interrupted') or found.link or told.get('kind') != proposal.NEW_APP:
            return None
        later = any(j.unit == found.unit and j.id > found.id and j.state in ('running', 'succeeded') for j in jobs.recent())
        return None if later else told

    def propose(request: Request, action: str, name: str, env: str, commands: tuple[merge.Command, ...], *,
                then: tuple = (), auto_merge: bool = True) -> Response:
        """Run the action as a job that opens its pull request, then clear the ``then`` caches it made stale."""
        def work(job: actions.Job) -> None:
            job.link = proposal.open_action(runner, github, job, action, name, env, commands, auto_merge=auto_merge)
            for cache in then:
                cache.clear()
        try:
            job = jobs.submit(merge.ACTIONS[action].job or action, merge.target(action, name, env), request.state.identity, work)
        except actions.ActionError as error:
            return page(request, 'error.html', status_code=409, error=str(error))
        return RedirectResponse(f'/jobs/{job.id}', status_code=303)

    @contextlib.asynccontextmanager
    async def lifespan(_app):
        """On the way down (a new image, a changed token), finish the running jobs before the pod goes."""
        yield
        running = [j for j in jobs.recent() if j.live and j.state == 'running']
        if running:
            logging.getLogger('swhurl.console.jobs').info('Stopping: waiting for %d running job(s) to finish', len(running))
        if not await asyncio.to_thread(jobs.drain):
            logging.getLogger('swhurl.console.jobs').error('Stopping with jobs still running: they will show as interrupted')

    ctx = Context(runner, github, jobs, flags, app_repos, repo_opener, described, document_source, file_source, platform_file,
                  request_files, page, reading, open_prs, change_rows, stacks, platform_teams, platform_channels, teams_cache,
                  channels_cache, connectable, propose, resumable)
    return Starlette(
        routes=[*pages.routes(ctx), *instance.routes(ctx), *teams.routes(ctx), *activity.routes(ctx), *new_app.routes(ctx),
                Mount('/static', StaticFiles(directory=STATIC), name='static')],
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
