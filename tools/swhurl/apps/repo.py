"""Create an app's GitHub repository from a stack's template and wait for its first image.

    make app-repo NAME=<app> [STACK=typescript] [OWNER=samclement] [DESCRIPTION="..."]

1. refuses if OWNER/NAME already exists;
2. renders the stack's Copier template (uvx copier) into a scratch directory, commits it;
3. creates the public repository with your ``gh`` login and pushes over SSH (your usual Git access;
   ``gh``'s token needs no ``workflow`` scope);
4. waits for the repository's first Container run (checks, build, smoke test, publish);
5. reads the image's digest from GHCR (anonymously, as the cluster pulls) and prints the
   ``make app-new … --from-repo`` line that adds the app to staging.

DRY_RUN=true checks the name and that the repository does not exist, and prints the plan.
"""
from __future__ import annotations

import json
import os
import shutil
import sys
import tempfile
import time
import urllib.error
import urllib.request
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path

from swhurl.apps.contract import APP_OWNER, APP_WORKFLOW, COPIER, STACKS
from swhurl.apps.new import NAME_RE
from swhurl.run import CommandError, Runner

MANIFEST_TYPES = ', '.join((
    'application/vnd.oci.image.index.v1+json', 'application/vnd.docker.distribution.manifest.list.v2+json',
    'application/vnd.oci.image.manifest.v1+json', 'application/vnd.docker.distribution.manifest.v2+json'))
Opener = Callable[[urllib.request.Request], tuple[dict, bytes]]


class RepoError(Exception):
    pass


def _open(request: urllib.request.Request) -> tuple[dict, bytes]:
    with urllib.request.urlopen(request, timeout=30) as reply:  # noqa: S310 (https only, built below)
        return dict(reply.headers), reply.read()


@dataclass
class Request:
    name: str
    stack: str = 'typescript'
    owner: str = APP_OWNER
    description: str = ''

    @property
    def repo(self) -> str:
        return f'{self.owner}/{self.name}'

    @property
    def image(self) -> str:
        return f'ghcr.io/{self.owner.lower()}/{self.name}'

    def check(self) -> None:
        if not NAME_RE.match(self.name):
            raise RepoError('NAME must be a DNS label (lowercase, digits, hyphens, max 40 chars)')
        if self.stack not in STACKS:
            raise RepoError(f'STACK must be one of {", ".join(sorted(STACKS))}')


def image_digest(image: str, tag: str, opener: Opener = _open) -> str:
    """The digest GHCR serves for ``image:tag`` to an anonymous client (as the cluster pulls)."""
    path = image.removeprefix('ghcr.io/')
    try:
        _, body = opener(urllib.request.Request(f'https://ghcr.io/token?scope=repository:{path}:pull'))
        token = json.loads(body)['token']
        headers, _ = opener(urllib.request.Request(
            f'https://ghcr.io/v2/{path}/manifests/{tag}', method='HEAD',
            headers={'Authorization': f'Bearer {token}', 'Accept': MANIFEST_TYPES}))
    except urllib.error.HTTPError as error:
        hint = (' (is the package private? Make it public: the cluster pulls anonymously)'
                if error.code in (401, 403) else '')
        raise RepoError(f'GHCR refused {image}:{tag}: {error.code}{hint}') from None
    except (urllib.error.URLError, KeyError, ValueError) as error:
        raise RepoError(f'could not read {image}:{tag} from GHCR: {error}') from None
    digest = {k.lower(): v for k, v in headers.items()}.get('docker-content-digest', '')
    if not digest.startswith('sha256:'):
        raise RepoError(f'GHCR returned no digest for {image}:{tag}')
    return digest


def wait_for_first_run(runner: Runner, req: Request, *, out: Callable[[str], None],
                       sleep: Callable[[float], None], attempts: int = 120) -> dict:
    """The first completed Container run on main (about 10 minutes at most)."""
    query = ['gh', 'run', 'list', '-R', req.repo, '--workflow', APP_WORKFLOW, '--branch', 'main', '--limit', '1',
             '--json', 'databaseId,number,headSha,status,conclusion,url']
    waiting = ''
    for _ in range(attempts):
        runs = runner.json(query, check=False) or []
        if runs and runs[0]['status'] == 'completed':
            run = runs[0]
            if run['conclusion'] != 'success':
                raise RepoError(f'the first {APP_WORKFLOW} run ended {run["conclusion"]}: {run["url"]}')
            return run
        state = f'run {runs[0]["number"]} {runs[0]["status"]}' if runs else 'waiting for the first run to start'
        if state != waiting:
            out(f'[INFO] {req.repo}: {state}')
            waiting = state
        sleep(5)
    raise RepoError(f'no finished {APP_WORKFLOW} run after {attempts * 5 // 60} minutes; '
                    f'see https://github.com/{req.repo}/actions')


def create(runner: Runner, req: Request, *, out: Callable[[str], None] = print,
           sleep: Callable[[float], None] = time.sleep, opener: Opener = _open) -> str:
    """Create the repository; return the image ``REPO:<run>-<sha>@sha256:…`` of its first run."""
    req.check()
    if runner.run(['gh', 'repo', 'view', req.repo, '--json', 'name'], check=False).returncode == 0:
        raise RepoError(f'{req.repo} already exists; choose another NAME (or use it with make app-new --from-repo)')
    template = STACKS[req.stack]
    if runner.dry_run:
        out(f'Plan (app-repo {req.repo}):')
        out(f'  - render {template} ({req.stack}) with app_name={req.name} using uvx {COPIER}')
        out(f'  - create the public repository {req.repo} and push the first commit over SSH')
        out(f'  - wait for its first {APP_WORKFLOW} run, read the image digest from GHCR, print the app-new line')
        return f'{req.image}:<run>-<sha>@sha256:<digest>'

    scratch = Path(tempfile.mkdtemp(prefix='app-repo-'))
    try:
        work = scratch / req.name
        data = ['--data', f'app_name={req.name}'] + (['--data', f'description={req.description}']
                                                      if req.description else [])
        runner.run(['uvx', COPIER, 'copy', '--defaults', '--quiet', *data,
                    f'https://github.com/{template}.git', work])
        git = ['git', '-C', work]
        runner.run([*git, 'init', '--quiet', '--initial-branch=main'])
        runner.run([*git, 'add', '--all'])
        runner.run([*git, 'commit', '--quiet', '-m', f'Start {req.name} from {template}'])
        runner.run(['gh', 'repo', 'create', req.repo, '--public',
                    '--description', req.description or f'A swhurl app ({req.stack})'], mutating=True)
        runner.run([*git, 'push', '--quiet', f'ssh://git@github.com/{req.repo}.git', 'main'], mutating=True)
        out(f'[OK] Created https://github.com/{req.repo} from {template}')
    finally:
        shutil.rmtree(scratch, ignore_errors=True)

    run = wait_for_first_run(runner, req, out=out, sleep=sleep)
    tag = f'{run["number"]}-{run["headSha"][:7]}'
    image = f'{req.image}:{tag}@{image_digest(req.image, tag, opener)}'
    out(f'[OK] First image: {image}')
    out('Next, add it to staging (a Git edit; commit and push it):')
    out(f'  make app-new NAME={req.name} ARGS="--from-repo {req.repo} --env staging --image {image}"')
    return image


def main(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    argv = argv or []
    if len(argv) != 1:
        print(__doc__.split('\n\n')[1], file=sys.stderr)
        return 2
    req = Request(argv[0], os.environ.get('STACK') or 'typescript', os.environ.get('OWNER') or APP_OWNER,
                  os.environ.get('DESCRIPTION', ''))
    try:
        create(runner or Runner.from_environment(), req)
    except (RepoError, CommandError) as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 1
    return 0
