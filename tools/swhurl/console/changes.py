"""Changes through Git: clone ``main``, run the clone's own tooling, push a ``console/*`` branch, open a PR.

The clone's tooling, not this image's, generates and checks the change, so it
follows the rules CI will apply even when the console is older than ``main``.
The repository is public, so cloning needs no token. The token (a fine-grained
GitHub token in the ``console-github`` Secret) is used only to push the branch
and open the PR, and never appears in a command line: ``git`` reads it through
a credential helper from its environment, and ``curl`` reads the API header
from stdin. The console never pushes anything but ``console/*`` branches.
"""
from __future__ import annotations

import json
import os
import re
import shutil
import sys
import tempfile
from collections.abc import Callable, Mapping
from dataclasses import dataclass
from pathlib import Path

from swhurl import platform
from swhurl.apps.contract import ENVIRONMENTS, EXPOSURES
from swhurl.apps.new import NAME_RE
from swhurl.console.actions import ActionError, Job
from swhurl.run import Runner

DEFAULT_REPO = platform.GITHUB_REPO
BASE = 'main'
BRANCH_PREFIX = 'console/'
AUTHOR = ('swhurl console', 'console@users.noreply.github.com')
# git asks the helper for credentials; it answers from $GITHUB_TOKEN in its own environment.
CREDENTIAL_HELPER = '!f() { test "$1" = get && printf "username=x-access-token\\npassword=%s\\n" "$GITHUB_TOKEN"; }; f'
UNSET = ('', 'REPLACE_ME')

# The app-new options the form offers, in form order: (field, flag, label).
NEW_APP_FIELDS = (
    ('kind', 'kind', 'Kind'),
    ('exposure', 'exposure', 'Exposure'),
    ('image', 'image', 'Image'),
    ('host', 'host', 'Host'),
    ('health_path', 'health-path', 'Health path'),
    ('port', 'port', 'Port'),
    ('uid', 'uid', 'UID'),
    ('cpu', 'cpu', 'CPU request'),
    ('memory', 'memory', 'Memory request'),
    ('memory_limit', 'memory-limit', 'Memory limit'),
    ('persistence', 'persistence', 'Persistent volume size'),
    ('secret_keys', 'secret-keys', 'Secret keys'),
    ('issuer', 'issuer', 'Certificate issuer'),
)
CHOICES = {'env': ENVIRONMENTS, 'kind': ('web', 'worker'), 'exposure': EXPOSURES,
           'issuer': ('letsencrypt-prod', 'letsencrypt-staging', 'selfsigned')}


@dataclass(frozen=True)
class GitHub:
    repo: str
    token: str
    api: str = 'https://api.github.com'


def github_from_env(runner: Runner, env: Mapping[str, str] | None = None) -> GitHub | None:
    """The token and repository from the environment, or None if no real token is set."""
    env = os.environ if env is None else env
    token = env.get(platform.CONSOLE_TOKEN_KEY, '').strip()
    if token in UNSET:
        return None
    runner.add_secret(token)
    return GitHub(env.get('CONSOLE_REPO') or DEFAULT_REPO, token)


def new_app_args(form: Mapping[str, str]) -> tuple[str, str, list[str]]:
    """``(name, env, app-new argv)`` from the form. Values go as ``--flag=value`` so none can become an option."""
    name, env = form.get('name', '').strip(), form.get('env', '').strip()
    if not NAME_RE.match(name):
        raise ActionError('name must be a DNS label: lowercase letters, digits and hyphens, at most 40 characters')
    if env not in ENVIRONMENTS:
        raise ActionError(f'env must be one of {", ".join(ENVIRONMENTS)}')
    argv = [name, f'--env={env}']
    for field, flag, label in NEW_APP_FIELDS:
        value = form.get(field, '').strip()
        if not value:
            continue
        if '\n' in value or '\r' in value:
            raise ActionError(f'{label} must be one line')
        if field in CHOICES and value not in CHOICES[field]:
            raise ActionError(f'{label} must be one of {", ".join(CHOICES[field])}')
        argv.append(f'--{flag}={value}')
    return name, env, argv


def branch_name(slug: str, revision: str) -> str:
    branch = f'{BRANCH_PREFIX}{slug}-{revision}'
    if not re.fullmatch(r'console/[a-z0-9][a-z0-9-]*-[0-9a-f]{7,40}', branch):
        raise ActionError(f'refusing to push {branch!r}: the console pushes only console/<name>-<commit> branches')
    return branch


def open_pr(runner: Runner, github: GitHub, job: Job, *, slug: str, title: str, body: str,
            change: Callable[[Path], None]) -> str:
    """Clone, apply ``change(clone)``, commit, push and open a PR. Returns the PR's URL."""
    runner.add_secret(github.token)  # whoever built ``github``, the token is redacted from every message
    workdir = Path(tempfile.mkdtemp(prefix='console-'))
    try:
        clone = workdir / 'repo'
        job.lines.append(f'Cloning {github.repo} ({BASE})')
        runner.run(['git', 'clone', '--quiet', '--depth', '1', '--branch', BASE,
                    f'https://github.com/{github.repo}.git', str(clone)])
        revision = runner.output(['git', '-C', str(clone), 'rev-parse', '--short=7', 'HEAD']).strip()
        branch = branch_name(slug, revision)
        change(clone)
        if not runner.output(['git', '-C', str(clone), 'status', '--porcelain']).strip():
            raise ActionError('the change left no files changed; nothing to propose')
        runner.run(['git', '-C', str(clone), 'add', '--all'])
        message = f'[console] {title}\n\n{body}\n\nRequested-by: {job.identity}\n'
        runner.run(['git', '-C', str(clone), '-c', f'user.name={AUTHOR[0]}', '-c', f'user.email={AUTHOR[1]}',
                    'commit', '--quiet', '--file=-'], input=message)
        job.lines += runner.output(['git', '-C', str(clone), 'show', '--stat', '--format=%h %s', 'HEAD']).splitlines()
        job.lines.append(f'Pushing {branch}')
        runner.run(['git', '-C', str(clone), '-c', 'credential.helper=', '-c', f'credential.helper={CREDENTIAL_HELPER}',
                    'push', '--quiet', 'origin', f'HEAD:refs/heads/{branch}'],
                   env={'GITHUB_TOKEN': github.token, 'GIT_TERMINAL_PROMPT': '0'}, mutating=True)
        url = create_pull_request(runner, github, branch=branch, title=f'[console] {title}',
                                  body=f'{body}\n\nRequested-by: {job.identity}')
        job.lines.append(f'Opened {url}' if url else 'Dry run: no PR opened')
        return url
    finally:
        shutil.rmtree(workdir, ignore_errors=True)


def create_pull_request(runner: Runner, github: GitHub, *, branch: str, title: str, body: str) -> str:
    payload = json.dumps({'title': title, 'head': branch, 'base': BASE, 'body': body})
    out = runner.output(['curl', '--silent', '--show-error', '--fail-with-body', '--config', '-',
                         '--request', 'POST', '--header', 'Accept: application/vnd.github+json',
                         '--header', 'Content-Type: application/json', '--data-binary', payload,
                         f'{github.api}/repos/{github.repo}/pulls'],
                        input=f'header = "Authorization: Bearer {github.token}"\n', mutating=True)
    return json.loads(out)['html_url'] if out.strip() else ''


def run_app_new(runner: Runner, job: Job, clone: Path, argv: list[str]) -> None:
    """The clone's own ``app-new``: its rules, its policy check, its SOPS recipients."""
    result = runner.run([sys.executable, '-m', 'swhurl', 'app-new', *argv], cwd=clone, check=False,
                        env={'PYTHONPATH': str(clone / 'tools')})
    job.lines += (result.stdout + result.stderr).splitlines()
    if result.returncode:
        raise ActionError('app-new refused this instance (see above); nothing was pushed')
