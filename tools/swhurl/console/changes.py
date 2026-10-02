"""Changes through GitHub: download ``main``, run its own tooling, commit to a ``console/*`` branch, open a PR.

Everything goes through GitHub's REST API with ``httpx`` (the image has no
``git`` or ``curl``): ``main``'s tarball is unpacked into a work tree, the
tree's own tooling (not this image's) makes and checks the change, so it
follows the rules CI will apply even when the console is older than ``main``,
and the files it added, changed or deleted become one commit (blobs, a tree,
a commit and a ref) on a new ``console/*`` branch, then a PR. The token (a
fine-grained GitHub token in the ``console-github`` Secret) travels only in the
``Authorization`` header of those requests, never in a command line, and is
redacted from every message. The console never writes any ref but a new
``console/*`` branch.
"""
from __future__ import annotations

import base64
import hashlib
import io
import os
import re
import shutil
import sys
import tarfile
import tempfile
from collections.abc import Callable, Mapping
from dataclasses import dataclass
from dataclasses import field as dataclass_field
from pathlib import Path
from typing import Any

import httpx

from swhurl import platform
from swhurl.apps import new, repo
from swhurl.apps.contract import (
    DATABASES,
    ENVIRONMENTS,
    EXPOSURES,
    OTLP_ENDPOINT,
    OTLP_HOST_IP,
    OTLP_PROTOCOL,
    PRESETS,
    STACKS,
)
from swhurl.apps.new import NAME_RE
from swhurl.console.actions import ActionError, Job
from swhurl.run import Runner

DEFAULT_REPO = platform.GITHUB_REPO
BASE = 'main'
BRANCH_PREFIX = 'console/'
AUTHOR = ('swhurl console', 'console@users.noreply.github.com')
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
    ('secret_keys', 'secret-keys', 'Secret environment variables'),
    ('database', 'database', 'Database'),
    ('otlp', 'otlp', 'Sends OpenTelemetry'),
    ('issuer', 'issuer', 'Certificate issuer'),
)
CHOICES = {'env': ENVIRONMENTS, 'kind': ('web', 'worker'), 'exposure': EXPOSURES, 'database': DATABASES,
           'issuer': ('letsencrypt-prod', 'letsencrypt-staging', 'selfsigned')}
CHECKBOXES = {'otlp'}
"""On/off fields: always sent explicitly (``--<flag>`` or ``--no-<flag>``), so a preset's default can be turned off."""
EXPOSURE_LABELS = {
    'authenticated-web': ('Signed in', 'A web address under {domain}, behind Google sign-in: only the accounts on the sign-in list.'),
    'public': ('Public', 'Anyone on the internet, no sign-in. Needs a host outside {domain}.'),
    'private': ('Private', 'No web address; reachable only inside the cluster. For workers and internal services.'),
}
"""The form's plain-language names for each exposure: (title, description)."""
ADVANCED_GROUPS = (('Runtime', ('kind', 'port', 'health_path', 'uid')),
                   ('Resources', ('cpu', 'memory', 'memory_limit', 'persistence')),
                   ('Telemetry and TLS', ('otlp', 'issuer')))
"""The New app form's Advanced section, in order. Name, environment, image, secret keys, database, exposure and
host are up front; every other NEW_APP_FIELDS field is in one group (a test checks)."""
NEW_REPO = 'new-repo'
PRESET_LABELS = {NEW_REPO: 'New app and repository', 'swhurl-web': 'Web app from the swhurl template',
                 'swhurl-worker': 'Worker from the swhurl template', '': 'Other image'}
"""The New app page's tabs: a new repository from a stack (console/repos.py), or an image that already exists."""


def new_repo_args(form: Mapping[str, str], questions: list[repo.Question]) -> tuple[str, str, str, dict[str, str], list[str]]:
    """``(name, stack, description, template answers, extra app-new argv)`` from the New app and repository form.

    ``questions`` are the stack template's own (its copier.yml): each is a ``feature-<name>`` field.
    A worker gets no exposure or host: its swhurl.yaml makes it private."""
    name, stack = form.get('name', '').strip(), form.get('stack', '').strip()
    description = ' '.join(form.get('description', '').split())[:200]
    if not NAME_RE.match(name):
        raise ActionError('name must be a DNS label: lowercase letters, digits and hyphens, at most 40 characters')
    if stack not in STACKS:
        raise ActionError(f'stack must be one of {", ".join(sorted(STACKS))}')
    answers = {}
    for question in questions:
        value = form.get(f'feature-{question.name}', '').strip() or question.default
        if value not in question.choices:
            raise ActionError(f'{question.name} must be one of {", ".join(question.choices)}')
        answers[question.name] = value
    if answers.get('kind') == 'worker':
        if form.get('host', '').strip():
            raise ActionError('a worker has no web address; leave Host empty')
        return name, stack, description, answers, []
    extra = []
    exposure, host = form.get('exposure', '').strip(), form.get('host', '').strip()
    if exposure:
        if exposure not in EXPOSURES:
            raise ActionError(f'exposure must be one of {", ".join(EXPOSURES)}')
        extra.append(f'--exposure={exposure}')
    if host:
        if not re.fullmatch(r'[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+', host):
            raise ActionError(f'host {host!r} is not a DNS name')
        extra.append(f'--host={host}')
    return name, stack, description, answers, extra


def new_repo_body(repo_url: str, template: str, argv: list[str]) -> str:
    name = argv[0]
    return (f'The console created {repo_url} from [{template}](https://github.com/{template}) and waited for its first '
            f'image.\n\n' + new_app_body(name, 'staging', argv))
OTLP_HINT = (f'Tick if the app has an OpenTelemetry SDK. Sets OTEL_EXPORTER_OTLP_ENDPOINT={OTLP_ENDPOINT} '
             f'({OTLP_HOST_IP} is the node IP), OTEL_EXPORTER_OTLP_PROTOCOL={OTLP_PROTOCOL} and OTEL_SERVICE_NAME=<name>; '
             'no key needed. Logs on stdout reach ClickStack either way.')


def new_app_defaults(preset: str = '') -> dict[str, str]:
    """What app-new uses when a field is left empty, read from its own parser (this image's copy)."""
    parser = new.parser(preset or None)
    return {field: str(parser.get_default(field)) for field, _, _ in NEW_APP_FIELDS
            if parser.get_default(field) is not None and field not in CHECKBOXES}


def new_app_checked(preset: str = '') -> dict[str, bool]:
    """Whether each checkbox starts ticked (a preset can turn one on)."""
    parser = new.parser(preset or None)
    return {field: bool(parser.get_default(field)) for field in CHECKBOXES}


@dataclass(frozen=True)
class GitHub:
    repo: str
    token: str
    api: str = 'https://api.github.com'
    transport: httpx.BaseTransport | None = dataclass_field(default=None, compare=False, repr=False)  # tests


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
    preset = form.get('preset', '').strip()
    if preset and preset not in PRESETS:
        raise ActionError(f'preset must be one of {", ".join(sorted(PRESETS))}')
    argv = [name, f'--env={env}'] + ([f'--preset={preset}'] if preset else [])
    for field, flag, label in NEW_APP_FIELDS:
        value = form.get(field, '').strip()
        if field in CHECKBOXES:
            if value not in ('', 'on'):
                raise ActionError(f'{label} is a checkbox')
            argv.append(f'--{flag}' if value else f'--no-{flag}')
            continue
        if not value:
            continue
        if '\n' in value or '\r' in value:
            raise ActionError(f'{label} must be one line')
        if field in CHOICES and value not in CHOICES[field]:
            raise ActionError(f'{label} must be one of {", ".join(CHOICES[field])}')
        argv.append(f'--{flag}={value}')
    return name, env, argv


def new_app_body(name: str, env: str, argv: list[str]) -> str:
    """The new-app PR's description: how it was made and, with secret keys, how to set their values."""
    body = f'Generated by the console with:\n\n    make app-new NAME={name} ARGS="{" ".join(argv[1:])}"'
    keys = next((a.split('=', 1)[1] for a in argv if a.startswith('--secret-keys=')), '')
    if keys:
        body += (f'\n\n**Before merging, set the secret values** ({keys.replace(",", ", ")}). Each is `REPLACE_ME` in '
                 f'an encrypted file. On this branch:\n\n    sops apps/{name}/{env}/secret.sops.yaml\n\n'
                 'Replace each `REPLACE_ME` with the real value, save, then commit and push. '
                 'The values stay encrypted in Git; the app receives them as environment variables.')
    return body


def branch_name(slug: str, revision: str) -> str:
    branch = f'{BRANCH_PREFIX}{slug}-{revision}'
    if not re.fullmatch(r'console/[a-z0-9][a-z0-9-]*-[0-9a-f]{7,40}', branch):
        raise ActionError(f'refusing to push {branch!r}: the console pushes only console/<name>-<commit> branches')
    return branch


class GitHubAPI:
    """The few REST calls a PR needs; errors are ActionErrors with the token redacted."""

    def __init__(self, runner: Runner, github: GitHub):
        self.runner = runner
        self.client = httpx.Client(
            base_url=f'{github.api}/repos/{github.repo}', transport=github.transport, timeout=60,
            follow_redirects=True,  # the tarball redirects to codeload; httpx drops Authorization across hosts
            headers={'Authorization': f'Bearer {github.token}', 'Accept': 'application/vnd.github+json',
                     'X-GitHub-Api-Version': '2022-11-28', 'User-Agent': 'swhurl-console'})

    def call(self, method: str, path: str, body: dict[str, Any] | None = None) -> httpx.Response:
        try:
            reply = self.client.request(method, path, json=body)
        except httpx.HTTPError as error:
            raise ActionError(f'GitHub {method} {path} failed: {self.runner.redact(str(error))}') from None
        if reply.status_code >= 300:
            try:
                message = reply.json().get('message', '')
            except ValueError:
                message = reply.text[:200]
            raise ActionError(f'GitHub {method} {path} returned {reply.status_code}: {self.runner.redact(message)}')
        return reply

    def json(self, method: str, path: str, body: dict[str, Any] | None = None) -> Any:
        return self.call(method, path, body).json()


@dataclass(frozen=True)
class PullRequest:
    number: int
    title: str
    url: str
    branch: str
    opened: str  # YYYY-MM-DD


def console_prs(runner: Runner, github: GitHub) -> list[PullRequest]:
    """Open pull requests the console made (``console/*`` branches), newest first."""
    runner.add_secret(github.token)
    api = GitHubAPI(runner, github)
    try:
        pulls = api.json('GET', '/pulls?state=open&sort=created&direction=desc&per_page=50')
    finally:
        api.client.close()
    return [PullRequest(p['number'], p['title'], p['html_url'], p['head']['ref'], p['created_at'][:10])
            for p in pulls if p['head']['ref'].startswith(BRANCH_PREFIX)]


def snapshot(root: Path) -> dict[str, tuple[str, str]]:
    """``{path: (mode, sha256)}`` for every file under ``root``."""
    files = {}
    for path in sorted(root.rglob('*')):
        if path.is_file():
            mode = '100755' if os.access(path, os.X_OK) else '100644'
            files[path.relative_to(root).as_posix()] = (mode, hashlib.sha256(path.read_bytes()).hexdigest())
    return files


def download(api: GitHubAPI, revision: str, workdir: Path) -> Path:
    """Unpack the repository at ``revision`` into ``workdir/repo``."""
    with tarfile.open(fileobj=io.BytesIO(api.call('GET', f'/tarball/{revision}').content), mode='r:gz') as tar:
        tar.extractall(workdir / 'src', filter='data')
    (top,) = (workdir / 'src').iterdir()  # GitHub wraps the tree in <owner>-<repo>-<sha>/
    return top.rename(workdir / 'repo')


def open_pr(runner: Runner, github: GitHub, job: Job, *, slug: str, title: str, body: str,
            change: Callable[[Path], None]) -> str:
    """Download ``main``, apply ``change(tree)``, commit its files to a new branch and open a PR. Returns the PR's URL."""
    runner.add_secret(github.token)  # whoever built ``github``, the token is redacted from every message
    api = GitHubAPI(runner, github)
    workdir = Path(tempfile.mkdtemp(prefix='console-'))
    try:
        head = api.json('GET', f'/git/ref/heads/{BASE}')['object']['sha']
        branch = branch_name(slug, head[:7])
        job.lines.append(f'Downloading {github.repo} {BASE} ({head[:7]})')
        tree = download(api, head, workdir)
        before = snapshot(tree)
        change(tree)
        after = snapshot(tree)
        changed = sorted(p for p in after.keys() | before.keys() if after.get(p) != before.get(p))
        if not changed:
            raise ActionError('the change left no files changed; nothing to propose')
        job.lines += [f'{"D" if p not in after else "A" if p not in before else "M"} {p}' for p in changed]
        if runner.dry_run:
            job.lines.append(f'Dry run: would commit {len(changed)} file(s) to {branch}; no PR opened')
            return ''
        entries = []
        for path in changed:
            if path not in after:
                entries.append({'path': path, 'mode': before[path][0], 'type': 'blob', 'sha': None})
                continue
            content = base64.b64encode((tree / path).read_bytes()).decode()
            blob = api.json('POST', '/git/blobs', {'content': content, 'encoding': 'base64'})['sha']
            entries.append({'path': path, 'mode': after[path][0], 'type': 'blob', 'sha': blob})
        base_tree = api.json('GET', f'/git/commits/{head}')['tree']['sha']
        new_tree = api.json('POST', '/git/trees', {'base_tree': base_tree, 'tree': entries})['sha']
        message = f'[console] {title}\n\n{body}\n\nRequested-by: {job.identity}\n'
        commit = api.json('POST', '/git/commits', {'message': message, 'tree': new_tree, 'parents': [head],
                                                   'author': {'name': AUTHOR[0], 'email': AUTHOR[1]}})['sha']
        job.lines.append(f'Committed {commit[:7]} [console] {title}')
        job.lines.append(f'Creating branch {branch}')
        api.call('POST', '/git/refs', {'ref': f'refs/heads/{branch}', 'sha': commit})
        url = api.json('POST', '/pulls', {'title': f'[console] {title}', 'head': branch, 'base': BASE,
                                          'body': f'{body}\n\nRequested-by: {job.identity}'})['html_url']
        job.lines.append(f'Opened {url}')
        return url
    finally:
        api.client.close()
        shutil.rmtree(workdir, ignore_errors=True)


def run_tool(runner: Runner, job: Job, clone: Path, command: str, argv: list[str]) -> None:
    """The clone's own tooling (``app-new``, ``app-promote``, ...): its rules, policy check and SOPS recipients."""
    result = runner.run([sys.executable, '-m', 'swhurl', command, *argv], cwd=clone, check=False,
                        env={'PYTHONPATH': str(clone / 'tools')})
    job.lines += (result.stdout + result.stderr).splitlines()
    if result.returncode:
        raise ActionError(f'{command} refused this change (see above); nothing was written to GitHub')


def run_app_new(runner: Runner, job: Job, clone: Path, argv: list[str]) -> None:
    run_tool(runner, job, clone, 'app-new', argv)


SCALE_FIELDS = (('replicas', 'replicas', 'Replicas'), ('cpu', 'cpu', 'CPU request'),
                ('memory', 'memory', 'Memory request'), ('memory_limit', 'memory-limit', 'Memory limit'))


def expose_args(form: Mapping[str, str]) -> list[str]:
    """``--exposure=`` and an optional ``--host=`` for app-expose."""
    exposure, host = form.get('exposure', '').strip(), form.get('host', '').strip()
    if exposure not in EXPOSURES:
        raise ActionError(f'exposure must be one of {", ".join(EXPOSURES)}')
    if host and not re.fullmatch(r'[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+', host):
        raise ActionError(f'host {host!r} is not a DNS name')
    return [f'--exposure={exposure}'] + ([f'--host={host}'] if host else [])


def scale_args(form: Mapping[str, str]) -> list[str]:
    """``--flag=value`` for each filled-in scale field; at least one is needed."""
    argv = []
    for field, flag, label in SCALE_FIELDS:
        value = form.get(field, '').strip()
        if value:
            if not re.fullmatch(r'[0-9A-Za-z.]{1,12}', value):
                raise ActionError(f'{label} {value!r} is not a number or quantity')
            argv.append(f'--{flag}={value}')
    if not argv:
        raise ActionError('change at least one of replicas, CPU, memory or memory limit')
    return argv
