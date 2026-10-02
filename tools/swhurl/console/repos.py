"""New app repositories: render a stack's template and create the repository through GitHub's API.

The console's second token (``APP_REPOS_TOKEN`` in the ``console-github`` Secret) covers every
repository of its owner, because GitHub lets a fine-grained token create repositories only then;
it could therefore change or delete any of them. :class:`AppRepos` is the only code that uses it,
and it can only:

  - read whether a repository exists and read its workflow runs;
  - create a new public repository (``POST /user/repos``);
  - write the first commit to a repository it created in this job, and nothing else.

It has no method that deletes, and refuses every write to a repository it did not just create
(tested). The token travels only in the ``Authorization`` header and is redacted from all output.
"""
from __future__ import annotations

import base64
import os
import shutil
import tempfile
import time
from collections.abc import Callable, Mapping
from dataclasses import dataclass
from dataclasses import field as dataclass_field
from pathlib import Path
from typing import Any

import httpx

from swhurl import platform
from swhurl.apps import repo
from swhurl.apps.contract import APP_OWNER, APP_WORKFLOW, STACKS
from swhurl.console.actions import ActionError, Job
from swhurl.run import Runner

FIRST_RUN_TIMEOUT = 600.0  # seconds: the first build is about two minutes
POLL = 5.0


@dataclass(frozen=True)
class AppReposToken:
    token: str
    owner: str = APP_OWNER
    api: str = 'https://api.github.com'
    transport: httpx.BaseTransport | None = dataclass_field(default=None, compare=False, repr=False)  # tests


def token_from_env(runner: Runner, env: Mapping[str, str] | None = None) -> AppReposToken | None:
    env = os.environ if env is None else env
    token = env.get(platform.APP_REPOS_TOKEN_KEY, '').strip()
    if token in ('', 'REPLACE_ME'):
        return None
    runner.add_secret(token)
    return AppReposToken(token)


class AppRepos:
    """The few calls a new app repository needs; writes only to repositories created by this object."""

    def __init__(self, runner: Runner, config: AppReposToken):
        runner.add_secret(config.token)
        self.runner, self.owner = runner, config.owner
        self.created: set[str] = set()
        self.client = httpx.Client(
            base_url=config.api, transport=config.transport, timeout=60,
            headers={'Authorization': f'Bearer {config.token}', 'Accept': 'application/vnd.github+json',
                     'X-GitHub-Api-Version': '2022-11-28', 'User-Agent': 'swhurl-console'})

    def close(self) -> None:
        self.client.close()

    def _call(self, method: str, path: str, body: dict[str, Any] | None = None, ok: tuple[int, ...] = ()) -> httpx.Response:
        try:
            reply = self.client.request(method, path, json=body)
        except httpx.HTTPError as error:
            raise ActionError(f'GitHub {method} {path} failed: {self.runner.redact(str(error))}') from None
        if reply.status_code >= 300 and reply.status_code not in ok:
            try:
                message = reply.json().get('message', '')
            except ValueError:
                message = reply.text[:200]
            raise ActionError(f'GitHub {method} {path} returned {reply.status_code}: {self.runner.redact(message)}')
        return reply

    def _writable(self, name: str) -> str:
        if name not in self.created:
            raise ActionError(f'refusing to write to {self.owner}/{name}: the console writes only to a repository '
                              'it created in this job')
        return f'/repos/{self.owner}/{name}'

    def exists(self, name: str) -> bool:
        return self._call('GET', f'/repos/{self.owner}/{name}', ok=(404,)).status_code != 404

    def create(self, name: str, description: str) -> str:
        """A new public repository with one initial commit (the Git API cannot write to an empty one)."""
        reply = self._call('POST', '/user/repos', {'name': name, 'description': description, 'private': False,
                                                   'auto_init': True, 'has_wiki': False, 'has_projects': False})
        self.created.add(name)
        return reply.json()['html_url']

    def push_first_commit(self, name: str, root: Path, message: str, *, sleep: Callable[[float], None]) -> str:
        """Replace the initial commit's tree with every file under ``root``; return the new commit."""
        base = self._writable(name)
        head = ''
        for _ in range(10):  # the initial commit can take a moment to appear
            reply = self._call('GET', f'{base}/git/ref/heads/main', ok=(404, 409))
            if reply.status_code == 200:
                head = reply.json()['object']['sha']
                break
            sleep(1)
        if not head:
            raise ActionError(f'{self.owner}/{name} has no main branch to commit to')
        entries = []
        for path in sorted(p for p in root.rglob('*') if p.is_file()):
            content = base64.b64encode(path.read_bytes()).decode()
            blob = self._call('POST', f'{base}/git/blobs', {'content': content, 'encoding': 'base64'}).json()['sha']
            entries.append({'path': path.relative_to(root).as_posix(), 'type': 'blob', 'sha': blob,
                            'mode': '100755' if os.access(path, os.X_OK) else '100644'})
        tree = self._call('POST', f'{base}/git/trees', {'tree': entries}).json()['sha']
        commit = self._call('POST', f'{base}/git/commits',
                            {'message': message, 'tree': tree, 'parents': [head]}).json()['sha']
        self._call('PATCH', f'{base}/git/refs/heads/main', {'sha': commit})
        return commit

    def run_for(self, name: str, commit: str) -> dict | None:
        """The app workflow's run for ``commit``, if it has started."""
        runs = self._call('GET', f'/repos/{self.owner}/{name}/actions/runs?head_sha={commit}&per_page=20').json()
        return next((r for r in runs.get('workflow_runs') or [] if r.get('name') == APP_WORKFLOW), None)


def create_app_repo(runner: Runner, repos: AppRepos, job: Job, req: repo.Request, *,
                    sleep: Callable[[float], None] = time.sleep, clock: Callable[[], float] = time.monotonic,
                    opener: repo.Opener = repo._open) -> str:
    """Render, create, push and wait for the first image; return ``REPO:<run>-<sha>@sha256:…``."""
    req.check()
    if repos.exists(req.name):
        raise ActionError(f'{req.repo} already exists on GitHub; choose another name (nothing was created)')
    scratch = Path(tempfile.mkdtemp(prefix='app-repo-'))
    try:
        work = scratch / req.name
        job.lines.append(f'Rendering {STACKS[req.stack]} ({req.stack}) for {req.name}')
        repo.render(runner, req, work)
        files = sorted(p.relative_to(work).as_posix() for p in work.rglob('*') if p.is_file())
        job.lines += [f'A {p}' for p in files]
        if runner.dry_run:
            job.lines.append(f'Dry run: would create {req.repo} with {len(files)} file(s)')
            return f'{req.image}:<run>-<sha>@sha256:<digest>'
        url = repos.create(req.name, req.description or f'A swhurl app ({req.stack})')
        job.lines.append(f'Created {url}')
        commit = repos.push_first_commit(req.name, work, f'Start {req.name} from {STACKS[req.stack]}\n\n'
                                         f'Requested-by: {job.identity}\n', sleep=sleep)
        job.lines.append(f'Pushed {commit[:7]}; waiting for its {APP_WORKFLOW} run (checks, build, publish)')
    finally:
        shutil.rmtree(scratch, ignore_errors=True)

    deadline, state = clock() + FIRST_RUN_TIMEOUT, ''
    while True:
        run = repos.run_for(req.name, commit)
        now = f'run {run["run_number"]} {run["status"]}' if run else 'waiting for the run to start'
        if now != state:
            job.lines.append(now)
            state = now
        if run and run['status'] == 'completed':
            if run['conclusion'] != 'success':
                raise ActionError(f'the first {APP_WORKFLOW} run ended {run["conclusion"]}: {run["html_url"]}. '
                                  'Fix the app and push; then add it from the Web app tab with the image that run prints')
            break
        if clock() > deadline:
            raise ActionError(f'no finished {APP_WORKFLOW} run after {FIRST_RUN_TIMEOUT / 60:.0f} minutes: '
                              f'https://github.com/{req.repo}/actions. When it publishes, add the app from the '
                              'Web app tab with the image the run prints')
        sleep(POLL)
    tag = f'{run["run_number"]}-{commit[:7]}'
    try:
        image = f'{req.image}:{tag}@{repo.image_digest(req.image, tag, opener)}'
    except repo.RepoError as error:
        raise ActionError(str(error)) from None
    job.lines.append(f'Published {image}')
    return image
