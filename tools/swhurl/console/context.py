"""What the console's route groups share: ``Context``, which ``server.create_app`` builds once, and the
helpers more than one group uses."""
from __future__ import annotations

import re
from collections.abc import Callable
from contextvars import ContextVar
from dataclasses import dataclass
from urllib.parse import urlencode, urlparse

from markupsafe import Markup, escape
from starlette.requests import Request
from starlette.responses import HTMLResponse, RedirectResponse, Response

from swhurl import dashboards
from swhurl.apps import contract, repo
from swhurl.console import actions, catalogue, cluster, flagstore, repos
from swhurl.console.github import GitHub, PullRequest
from swhurl.console.reading import Files
from swhurl.run import Runner, SharedReads

HASH = re.compile(r'\b([0-9a-f]{12})[0-9a-f]{8,}\b')
STACK_NAMES = {'typescript': 'TypeScript', 'kotlin': 'Kotlin'}  # a stack as the language it is


def short_hashes(text: object) -> Markup:
    """Escape ``text`` and shorten each commit SHA or digest to 12 characters, the full value on hover."""
    return Markup(HASH.sub(lambda m: f'<span class="hash" title="{m[0]}">{m[1]}…</span>', str(escape(text))))


def logs_url(app: str) -> str:
    """HyperDX, filtered to one app's logs and traces."""
    return f'https://clickstack.{contract.COOKIE_DOMAIN}/search?' + urlencode({'where': f'ServiceName:"{app}"', 'whereLanguage': 'lucene'})


def dashboard_urls(runner: Runner) -> dict[str, str]:
    """Each app's HyperDX dashboard, from the record the dashboard job keeps (``console/app-dashboards``).
    Empty when it cannot be read: the pages then leave the link out."""
    return {app: f'https://clickstack.{contract.COOKIE_DOMAIN}/dashboards/{ident}' for app, ident in dashboards.recorded(runner).items()}


def follow(url: str) -> Markup:
    """htmx attributes for something that says a job is running: re-read the page it is on shortly,
    so the row it sits in shows what the job did without anyone pressing Refresh."""
    return Markup(f'hx-get="{escape(url)}" hx-trigger="load delay:10s" hx-select="main" hx-target="main" hx-swap="outerHTML"')


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


def moved(target: str):
    return lambda _request: RedirectResponse(target, status_code=301)


@dataclass(frozen=True)
class Context:
    """What ``create_app`` gives every route group: the clients, the reads one page shares through ``runner``
    and the caches and helpers more than one group needs. The caches live as long as the app."""
    runner: SharedReads
    github: GitHub | None
    jobs: actions.Jobs
    flags: flagstore.FlagStore | None
    app_repos: repos.AppReposToken | None
    repo_opener: repo.Opener | None
    described: catalogue.Documents
    document_source: Callable[[str], str | None] | None
    file_source: Callable[[str], str | None] | None
    platform_file: Callable[[str], str | None]
    request_files: ContextVar[Files | None]
    page: Callable[..., Response]
    reading: Callable[..., Response]
    open_prs: Callable[[], tuple[list[PullRequest], str]]
    change_rows: Callable[..., tuple[list, str]]  # server.ChangeRow
    stacks: Callable[[list[str]], dict[str, str]]
    platform_teams: Callable[[], tuple[dict[str, str], str]]
    platform_channels: Callable[[], tuple[dict[str, str], dict[str, list[str]]]]
    teams_cache: list
    channels_cache: list
    connectable: Callable[..., tuple[list[cluster.AppRow], list[tuple[str, cluster.AppRow]], str]]
    propose: Callable[..., Response]
    resumable: Callable[[actions.Job], dict | None]
