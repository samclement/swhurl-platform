"""Changes through GitHub: download ``main``, run its own tooling, commit to a ``console/*`` branch, open a PR.

Everything goes through GitHub's REST API with ``httpx`` (no ``git`` push,
so no credential on disk): ``main``'s tarball is unpacked into a work tree, the
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
import json
import os
import re
import shutil
import sys
import tarfile
import tempfile
from collections.abc import Callable, Mapping
from dataclasses import dataclass, replace
from dataclasses import field as dataclass_field
from pathlib import Path
from typing import Any

import httpx
import yaml

from swhurl import platform, yamlio
from swhurl.apps import channels, egress, events, helmrelease, repo
from swhurl.apps.contract import (
    APP_OWNER,
    APP_TYPES,
    EXPOSURES,
    STACKS,
    TEAMS_FILE,
    schedule_problem,
    type_answers,
)
from swhurl.apps.instance import NAME_RE, Instance
from swhurl.apps.model import store
from swhurl.apps.model.records import ALLOW_RE, SETTINGS_FILE, Settings
from swhurl.apps.model.render import ANNOTATION
from swhurl.console.actions import ActionError, Job
from swhurl.console.merge import ACTIONS, INSTANCE_KINDS, REFUSAL, REFUSAL_LIMIT, new_app_records, settings_held
from swhurl.console.stages import ABSENT, UNRENDERED, Target
from swhurl.run import Runner

DEFAULT_REPO = platform.GITHUB_REPO
BASE = 'main'
BRANCH_PREFIX = 'console/'
AUTO_MERGE_LABEL = 'auto-merge'
GATE_AUTHOR = 'github-actions[bot]'  # the Auto-merge workflow comments with GITHUB_TOKEN
"""A console PR with this label merges itself once Validate passes (.github/workflows/auto-merge.yml)."""
AUTHOR = ('swhurl console', 'console@users.noreply.github.com')
UNSET = ('', 'REPLACE_ME')

EXPOSURE_LABELS = {
    'authenticated-web': ('Signed in', 'A web address under {domain}, behind Google sign-in: only the accounts on the sign-in list.'),
    'public': ('Public', 'Anyone on the internet, no sign-in. Needs a host outside {domain}.'),
    'private': ('Private', 'No web address: only the apps granted a call to it can reach it. For APIs, workers and jobs.'),
}
"""The form's plain-language names for each exposure: (title, description)."""


def platform_teams(runner: Runner, github: GitHub) -> dict[str, str]:
    """Team names and descriptions from the registry on ``main`` (the console image carries no checkout)."""
    api = GitHubAPI(runner, github)
    try:
        listing = api.json('GET', f'/contents/{TEAMS_FILE.as_posix()}?ref={BASE}')
        registered = (yamlio.load(base64.b64decode(listing.get('content', ''))) or {}).get('teams') or {}
    except (ValueError, AttributeError, yaml.YAMLError) as error:
        raise ActionError(f'cannot read {TEAMS_FILE}: {error}') from None
    finally:
        api.client.close()
    return {str(name): (entry or {}).get('description', '') for name, entry in registered.items()}


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


def branch_name(slug: str, revision: str) -> str:
    branch = f'{BRANCH_PREFIX}{slug}-{revision}'
    if not re.fullmatch(r'console/[a-z0-9][a-z0-9-]*-[0-9a-f]{7,40}', branch):
        raise ActionError(f'refusing to push {branch!r}: the console pushes only console/<name>-<commit> branches')
    return branch


class GitHubAPI:
    """The few REST calls a PR needs; errors are ActionErrors with the token redacted."""

    def __init__(self, runner: Runner, github: GitHub):
        self.runner = runner
        runner.add_secret(github.token)
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
    body: str = ''
    labels: tuple[str, ...] = ()
    progress: str = 'Awaiting review'
    checks_url: str = ''
    checks: str = ''  # Validate on its head: passed, failed, running, pending, conflict, or '' (not read)
    refused: str = ''  # why the merge gate held it (its comment), when it did and the label is off


GATE_BRANCHES = tuple(f'{BRANCH_PREFIX}{action}-' for action in ACTIONS)
"""The console branches the merge gate takes; only these can carry its refusal."""


def refusal(runner: Runner, github: GitHub, number: int) -> str:
    """Why the merge gate held a PR: the newest reason it left as a comment, or '' (none, or unreadable).
    Anyone can comment on a public repository, so only the workflow's own comments count."""
    api = GitHubAPI(runner, github)
    try:
        comments = api.json('GET', f'/issues/{number}/comments?per_page=100')
    except ActionError:
        return ''
    finally:
        api.client.close()
    for comment in reversed(comments):
        first = (comment.get('body') or '').split('\n', 1)[0]
        if comment.get('user', {}).get('login') == GATE_AUTHOR and first.startswith(REFUSAL):
            return first.removeprefix(REFUSAL)[:REFUSAL_LIMIT]
    return ''


def pull_progress(runner: Runner, github: GitHub, pull: dict, labels: tuple[str, ...]) -> tuple[str, str, str, str]:
    """Describe review or validation progress, its best available checks link, how Validate went on its
    head (``PullRequest.checks``, read whoever merges it) and, without the auto-merge label, why the
    merge gate held it (``PullRequest.refused``)."""
    checks_url = pull['html_url'] + '/checks'
    if not pull['head'].get('sha'):
        return 'Awaiting setup or manual review', checks_url, '', ''
    api = GitHubAPI(runner, github)
    try:
        runs = api.json('GET', f"/actions/runs?head_sha={pull['head']['sha']}&event=pull_request&per_page=30")
        found = [r for r in runs.get('workflow_runs', [])
                 if r.get('name') == 'Validate' and r.get('head_sha') == pull['head']['sha']]
        latest = found[0] if found else {}
        checks_url = latest.get('html_url', checks_url)
        if latest.get('conclusion') == 'success':
            checks, progress = 'passed', 'Checks passed; awaiting merge verification'
        elif latest.get('conclusion') in ('failure', 'cancelled', 'timed_out', 'action_required'):
            checks, progress = 'failed', 'Failed validation; unmerged'
        elif latest.get('status') == 'in_progress':
            checks, progress = 'running', 'Checks running'
        else:
            checks, progress = 'pending', 'Checks pending'
        details = api.json('GET', f"/pulls/{pull['number']}")
        if details.get('mergeable_state') == 'dirty':
            checks, progress = 'conflict', 'Merge conflict; review required'
    except ActionError:
        checks, progress = '', 'Merge requested; check progress on GitHub'
    finally:
        api.client.close()
    if AUTO_MERGE_LABEL not in labels:
        refused = refusal(runner, github, pull['number']) if pull['head']['ref'].startswith(GATE_BRANCHES) else ''
        progress = f'Auto-merge refused: {refused}' if refused else 'Awaiting setup or manual review'
        return progress, pull['html_url'] + '/checks', checks, refused
    return progress, checks_url, checks, ''


def pull_state(runner: Runner, github: GitHub, number: int) -> tuple[str, str, str]:
    """How a pull request that is no longer listed as open ended: ``('merged' | 'closed' | 'open', when, commit)``,
    the commit being the one that merged it into main."""
    api = GitHubAPI(runner, github)
    try:
        pull = api.json('GET', f'/pulls/{number}')
    finally:
        api.client.close()
    if pull.get('merged_at'):
        return 'merged', pull['merged_at'], pull.get('merge_commit_sha') or ''
    return ('closed', pull.get('closed_at') or '', '') if pull.get('state') == 'closed' else ('open', '', '')


def main_commit(runner: Runner, github: GitHub) -> str:
    api = GitHubAPI(runner, github)
    try:
        return api.json('GET', '/git/ref/heads/main')['object']['sha']
    finally:
        api.client.close()


def render_targets(runner: Runner, github: GitHub, revision: str) -> dict[str, Target]:
    """Immutable Git facts from a complete tree. Missing paths mean absent only after a successful read.
    Read only HelmReleases and the platform's settings files; encrypted files and credentials never enter the result."""
    api = GitHubAPI(runner, github)
    try:
        content = api.call('GET', f'/tarball/{revision}').content
        with tarfile.open(fileobj=io.BytesIO(content), mode='r:gz') as archive:
            releases, settings, records, apps = {}, {}, {}, {}
            for member in archive.getmembers():
                parts = Path(member.name).parts
                if not member.isfile() or len(parts) < 4 or parts[1] != 'apps':
                    continue
                if len(parts) == 4 and parts[3] == 'app.yaml':
                    apps[parts[2]] = archive.extractfile(member).read()
                elif len(parts) == 5 and parts[4] == 'helmrelease.yaml':
                    releases[f'{parts[2]}/{parts[3]}'] = yamlio.load(archive.extractfile(member).read().decode())
                elif len(parts) == 5 and parts[4] == 'instance.yaml':
                    settings[f'{parts[2]}/{parts[3]}'] = archive.extractfile(member).read()
                elif len(parts) == 5 and parts[4] == 'release.yaml':
                    records[f'{parts[2]}/{parts[3]}'] = archive.extractfile(member).read()

            def digest(*parts: bytes) -> str:
                return hashlib.sha256(b'\0'.join(parts)).hexdigest()[:16]
            found = {}
            for name in releases.keys() | records.keys():
                app, mine, record = apps.get(name.split('/')[0], b''), settings.get(name), records.get(name)
                if mine is not None:
                    # A system allocation completes creation after a records-only merge; it is not
                    # a new operator choice. Ignore only that port when identifying the inputs.
                    inputs = Settings.parse(yamlio.load(mine.decode()))
                    inputs = replace(inputs, allocations=replace(inputs.allocations, egress_port=None))
                    mine = yamlio.dump(inputs.document()).encode()
                release = releases.get(name)
                if release is None and (mine is None or record is None):
                    continue  # neither rendered nor recorded: not an instance
                rendered_digest = helmrelease.image(release).get('digest', '') if release else ''
                requested_digest = str(((yamlio.load(record.decode()) or {}).get('image') or {}).get('digest', '')) \
                    if record is not None else rendered_digest
                # A held later promotion merges new records alongside the old HelmRelease. Its
                # healthy old pod is not evidence that the promoted image has been rendered or run.
                pending = release is None or requested_digest != rendered_digest
                found[name] = Target(
                    UNRENDERED if pending else (release.get('metadata', {}).get('annotations') or {}).get(ANNOTATION, ''),
                    requested_digest,
                    digest(app, mine) if mine is not None else '',
                    digest(app, record, mine) if mine is not None and record is not None else '')
            return found
    except (tarfile.TarError, ValueError, AttributeError, yaml.YAMLError) as error:
        raise ActionError(f'cannot read render targets: {error}') from None
    finally:
        api.client.close()


COMPARED_FILES = 300
"""GitHub lists this many changed files in one answer; a comparison that fills it may have more."""


def changed_since(runner: Runner, github: GitHub, base: str, head: str) -> tuple[bool, list[str] | None]:
    """Whether commit ``head`` contains ``base`` and, if so, the paths changed after it (None when there
    may be more than GitHub lists). Two commits always compare the same: the answer can be kept."""
    api = GitHubAPI(runner, github)
    try:
        compared = api.json('GET', f'/compare/{base}...{head}')
    finally:
        api.client.close()
    if compared.get('status') not in ('ahead', 'identical'):
        return False, None
    files = compared.get('files') or []
    if len(files) >= COMPARED_FILES:
        return True, None
    return True, [name for f in files for name in (f.get('filename'), f.get('previous_filename')) if name]


def console_prs(runner: Runner, github: GitHub) -> list[PullRequest]:
    """Open pull requests the console made (``console/*`` branches), newest first."""
    runner.add_secret(github.token)
    api = GitHubAPI(runner, github)
    try:
        pulls = api.json('GET', '/pulls?state=open&sort=created&direction=desc&per_page=50')
    finally:
        api.client.close()
    out = []
    for p in pulls:
        if not p['head']['ref'].startswith(BRANCH_PREFIX):
            continue
        labels = tuple(label['name'] for label in p.get('labels', []))
        progress, checks_url, checks, refused = pull_progress(runner, github, p, labels)
        out.append(PullRequest(p['number'], p['title'], p['html_url'], p['head']['ref'], p['created_at'][:10],
                               p.get('body') or '', labels, progress, checks_url, checks, refused))
    return out



def creates_instance(pr: PullRequest, app: str, env: str) -> bool:
    """Whether ``pr`` is a console New app PR for ``<app>/<env>`` (branch ``console/new-<app>-<env>-<sha7>``)."""
    rest = pr.branch.removeprefix(f'{BRANCH_PREFIX}new-{app}-{env}-')
    return rest != pr.branch and re.fullmatch('[0-9a-f]{7}', rest) is not None


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


def open_pr(runner: Runner, github: GitHub, job: Job, *, slug: str, title: str, body: str | Callable[[Path], str],
            change: Callable[[Path], None], auto_merge: bool | Callable[[], bool] = False,
            manual_merge_reason: str | Callable[[], str] = '', verify: Callable[[], None] | None = None,
            keep: Callable[[str], bool] | None = None) -> str:
    """Download ``main``, apply ``change(tree)``, commit its files to a new branch and open a PR. Returns the PR's URL.
    ``keep``: which of the changed paths the PR carries (new-app or promotion records); the rest are left out.

    With ``auto_merge`` the PR is labelled to merge itself once Validate passes, unless it adds or changes an
    encrypted file: a new Secret's values are ``REPLACE_ME`` until someone sets them on the branch. A new
    app's generated events key is the exception: it is complete when written."""
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
        parts = job.unit.split('/')
        if len(parts) == 2 and all(NAME_RE.fullmatch(part) for part in parts):
            path = tree / 'apps' / parts[0] / parts[1] / 'helmrelease.yaml'
            job.target_hash = ((yamlio.load(path.read_text()).get('metadata', {}).get('annotations') or {}).get(ANNOTATION, '')
                               if path.is_file() else ABSENT)
        body = body(tree) if callable(body) else body
        auto_merge = auto_merge() if callable(auto_merge) else auto_merge
        manual_merge_reason = manual_merge_reason() if callable(manual_merge_reason) else manual_merge_reason
        after = snapshot(tree)
        changed = sorted(p for p in after.keys() | before.keys() if after.get(p) != before.get(p) and (keep is None or keep(p)))
        if not changed:
            raise ActionError('the change left no files changed; nothing to propose')
        for path in changed:
            if path not in after:
                status = 'D'
            elif path not in before:
                status = 'A'
            else:
                status = 'M'
            job.lines.append(f'{status} {path}')
        encrypted = [p for p in changed if p in after and p.endswith('.sops.yaml')]
        generated_key = bool(encrypted) and all(Path(p).name == events.SECRET_FILE for p in encrypted)
        if generated_key and slug.startswith('new-') and all(p not in before for p in encrypted):
            # A new app's events key is generated whole (nothing is left to fill in, and no one can read it
            # in review): it does not hold the merge. The gate checks it is that key alone (merge.check_new_app).
            encrypted_reason = ''
        elif encrypted:
            auto_merge = False
            if generated_key:
                encrypted_reason = "adds the app's generated encrypted events key"
            else:
                encrypted_reason = 'needs Secret values to be set on the pull request branch'
        else:
            encrypted_reason = ''
        manual_reasons = []
        if manual_merge_reason:
            manual_reasons.append(manual_merge_reason)
        if encrypted_reason:
            manual_reasons.append(encrypted_reason)
        if not auto_merge and not manual_reasons:
            manual_reasons.append('needs manual review')
        if not auto_merge:
            reason = ' and '.join(manual_reasons)
            plan = f'Manual merge required: this pull request {reason}. Review the changes, then merge it yourself.'
            job.lines.append(f'Merge plan: {plan}')
        if verify:
            verify()
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
        note = ('\n\nMerges itself when Validate and the trusted merge checks pass (label `auto-merge`; '
                'remove it to merge by hand).') if auto_merge else f'\n\n**Merge plan:** {plan}'
        pr = api.json('POST', '/pulls', {'title': f'[console] {title}', 'head': branch, 'base': BASE,
                                         'body': f'{body}{note}\n\nRequested-by: {job.identity}'})
        url = pr['html_url']
        job.lines.append(f'Opened {url}')
        if auto_merge:
            try:
                api.call('POST', f"/issues/{pr['number']}/labels", {'labels': [AUTO_MERGE_LABEL]})
                job.lines.append('Labelled auto-merge: it merges itself when Validate passes')
            except ActionError as error:  # the PR stands; it just waits for a person
                job.lines.append(f'Could not label it auto-merge ({error}); merge it yourself')
        return url
    finally:
        api.client.close()
        shutil.rmtree(workdir, ignore_errors=True)


def run_tool(runner: Runner, job: Job, clone: Path, command: str, argv: list[str], input: str | None = None) -> None:
    """The clone's own tooling (``app-new``, ``app-promote``, ...): its rules, policy check and SOPS recipients.
    ``input`` is its standard input: the one way a secret reaches it (never an argument, never in the job's lines).
    It writes no bytecode: every file it leaves in the clone becomes part of the commit."""
    result = runner.run([sys.executable, '-m', 'swhurl', command, *argv], cwd=clone, check=False, input=input,
                        env={'PYTHONPATH': str(clone / 'tools'), 'PYTHONDONTWRITEBYTECODE': '1'})
    job.lines += (result.stdout + result.stderr).splitlines()
    if result.returncode:
        raise ActionError(f'{command} refused this change (see above); nothing was written to GitHub')


SCALE_FIELDS = (('replicas', 'replicas', 'Replicas'), ('cpu', 'cpu', 'CPU request'),
                ('memory', 'memory', 'Memory request'), ('memory_limit', 'memory-limit', 'Memory limit'))
HOST_RE = re.compile(r'[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+')
TEAM_NAME = re.compile(r'[a-z]([a-z0-9-]{0,38}[a-z0-9])?')


def platform_channels(runner: Runner, github: GitHub) -> tuple[dict[str, str], dict[str, list[str]]]:
    """``(each channel's transport, each team's channels)`` from the generated routes on ``main``: names
    only. The console never reads a channel's address (docs/services.md#notification-channels)."""
    api = GitHubAPI(runner, github)
    try:
        listing = api.json('GET', f'/contents/{channels.ROUTES_FILE.as_posix()}?ref={BASE}')
        configmap = yamlio.load(base64.b64decode(listing.get('content', ''))) or {}
        routes = json.loads(configmap['data'][channels.ROUTES_KEY])
        return ({str(name): str(transport) for name, transport in routes.get('channels', {}).items()},
                {str(team): [str(name) for name in names] for team, names in routes.get('teams', {}).items()})
    except (ValueError, AttributeError, KeyError, TypeError, yaml.YAMLError) as error:
        raise ActionError(f'cannot read {channels.ROUTES_FILE}: {type(error).__name__}') from None
    finally:
        api.client.close()


def checked_host(host: str) -> str:
    """``host`` if it is empty or a DNS name."""
    if host and not HOST_RE.fullmatch(host):
        raise ActionError(f'host {host!r} is not a DNS name')
    return host


def scale_flags(form: Mapping[str, str]) -> list[str]:
    """``--flag=value`` for each filled-in scale field; at least one is needed."""
    flags = []
    for field, flag, label in SCALE_FIELDS:
        value = form.get(field, '').strip()
        if value:
            if not re.fullmatch(r'[0-9A-Za-z.]{1,12}', value):
                raise ActionError(f'{label} {value!r} is not a number or quantity')
            flags.append(f'--{flag}={value}')
    if not flags:
        raise ActionError('change at least one of replicas, CPU, memory or memory limit')
    return flags


def expose_flags(form: Mapping[str, str]) -> list[str]:
    """``--exposure=`` and an optional ``--host=`` for app-expose."""
    exposure, host = form.get('exposure', '').strip(), checked_host(form.get('host', '').strip())
    if exposure not in EXPOSURES:
        raise ActionError(f'exposure must be one of {", ".join(EXPOSURES)}')
    return [f'--exposure={exposure}'] + ([f'--host={host}'] if host else [])


@dataclass(frozen=True)
class Connections:
    """Platform grants: apps to call and ``<app>.<event>`` to receive.
    Publications belong to the image's AsyncAPI document, not a connection form."""
    calls: tuple[str, ...] = ()
    subscribe: tuple[str, ...] = ()

    @classmethod
    def from_flags(cls, flags: tuple[str, ...]) -> Connections:
        """Recover call and subscription choices from the app job's saved CLI flags."""
        values = {}
        for flag in flags:
            field, separator, value = flag.removeprefix('--').partition('=')
            if separator and field == 'publish':
                raise ActionError('publishing choices are no longer supported: edit asyncapi.yaml named by '
                                  'provides.eventsFile, then start a new job')
            if separator and field in ('calls', 'subscribe'):
                values[field] = tuple(value.split(','))
        return cls(values.get('calls', ()), values.get('subscribe', ()))

    @classmethod
    def from_form(cls, form: Mapping[str, str]) -> Connections:
        if 'publish' in form:
            raise ActionError('publishing choices are no longer supported: edit asyncapi.yaml named by provides.eventsFile')
        chosen = cls(*(tuple(name.strip() for name in form.get(field, '').split(',') if name.strip())
                       for field in ('calls', 'subscribe')))
        bad = [name for name in chosen.calls if not NAME_RE.match(name)]
        bad += [name for name in chosen.subscribe if not events.SUBSCRIPTION_RE.match(name)]
        if bad:
            raise ActionError(f'{", ".join(bad)}: a call names an app, a subscription <app>.<event>')
        return chosen

    @property
    def touches_others(self) -> bool:
        """A call or a subscription also changes the other app: its allowed callers, the NATS users."""
        return bool(self.calls or self.subscribe)

    @property
    def manual_merge_reason(self) -> str:
        """Name the existing app settings changed by calls or subscriptions."""
        affected = set(self.calls) | {event.split('.', 1)[0] for event in self.subscribe}
        return f'also changes existing app settings for {", ".join(sorted(affected))}' if affected else ''

    def check_offered(self, callable_apps: set[str], offered_events: set[str]) -> None:
        """Refuse a choice outside the lists the form offered, before a repository is created for an
        app that could not then be added."""
        missing = [name for name in self.calls if name not in callable_apps]
        missing += [name for name in self.subscribe if name not in offered_events]
        if missing:
            raise ActionError(f'{", ".join(missing)}: not a web app, or an event, that staging has now; reload the form')

    def new_app_flags(self) -> list[str]:
        """``--calls=`` and ``--subscribe=`` for app-new."""
        return [f'--{field}={",".join(names)}' for field, names in
                (('calls', self.calls), ('subscribe', self.subscribe)) if names]

    def adding(self, app: str, env: str) -> tuple[Command, ...]:
        """The app-calls and app-events commands that add these to an instance; one clone runs both,
        so one pull request carries them."""
        commands = []
        if self.calls:
            commands.append(Command('app-calls', (app, env, f'--add={",".join(self.calls)}')))
        if self.subscribe:
            commands.append(Command('app-events', (app, env, f'--add-subscribe={",".join(self.subscribe)}')))
        if not commands:
            raise ActionError('choose an app to call or an event to receive')
        return tuple(commands)


def removing(app: str, env: str, form: Mapping[str, str]) -> tuple[Command, ...]:
    """The command that removes one connection this app declared: the row's ``role``, ``app`` and ``event``."""
    role, other, event = (form.get(key, '').strip() for key in ('role', 'app', 'event'))
    if role == 'calls' and NAME_RE.match(other):
        return (Command('app-calls', (app, env, f'--remove={other}')),)
    if role == 'subscribes' and events.SUBSCRIPTION_RE.match(f'{other}.{event}'):
        return (Command('app-events', (app, env, f'--remove-subscribe={other}.{event}')),)
    if role == 'publishes':
        raise ActionError('to stop publishing, edit asyncapi.yaml named by provides.eventsFile; '
                          'publications cannot be removed on the platform')
    if role == 'reaches' and not egress.grant_problem(event):
        return (Command('app-egress', (app, env, f'--remove={event}')),)
    raise ActionError('no such connection to remove: only the app that declared one can remove it')


def allow_flags(form: Mapping[str, str]) -> list[str]:
    """``--add=`` and ``--remove=`` for app-allows, from the form's comma-separated ``add`` and ``remove``."""
    chosen = {field: [name.strip() for name in form.get(field, '').split(',') if name.strip()] for field in ('add', 'remove')}
    if bad := [name for names in chosen.values() for name in names if not ALLOW_RE.match(name)]:
        raise ActionError(f'{", ".join(bad)}: an allowed caller is an app name or team:<team>')
    flags = [f'--{field}={",".join(names)}' for field, names in chosen.items() if names]
    if not flags:
        raise ActionError('name an app or team:<team> to allow or stop allowing')
    return flags


def egress_hosts(form: Mapping[str, str]) -> list[str]:
    """The hosts a form grants (``hosts``, comma-separated), each as app-egress keeps it."""
    hosts = [egress.grant(name) for name in form.get('hosts', '').split(',') if name.strip()]
    if not hosts:
        raise ActionError('name a host, such as api.stripe.com')
    problems = [problem for name in hosts if (problem := egress.grant_problem(name))]
    if problems:
        raise ActionError('; '.join(problems))
    return hosts


@dataclass(frozen=True)
class Command:
    """One command of the clone's own tooling. ``input`` is its standard input: the one way a secret
    reaches it (never an argument, never in a message)."""
    name: str
    argv: tuple[str, ...]
    input: str | None = dataclass_field(default=None, repr=False)

    def as_make(self) -> str:
        """An app-* command on an instance as the operator would type it."""
        flags = f' ARGS="{" ".join(self.argv[2:])}"' if self.argv[2:] else ''
        return f'make {self.name} APP={self.argv[0]} ENV={self.argv[1]}{flags}'


INSTANCE_CHANGES = {'scale': 'scale', 'schedule': 'change when it runs:', 'expose': 'change access to', 'connect': 'change the connections of',
                    'disconnect': 'change the connections of', 'egress': 'change the hosts outside the cluster of',
                    'allow': 'change who may call', 'remove': 'remove'}
"""Each change the app page can propose for an instance, and its pull request's verb. A promotion
is not among them: its pull request is built from a review (console/promotion.py)."""


@dataclass(frozen=True)
class Change:
    """One change the console proposes as a pull request: the commands a clone of ``main`` runs,
    the pull request's wording, the job it runs as and how it is merged. Built from a form by
    ``to_instance``, ``to_team``, ``to_channel`` or ``NewApp.change``; a route handler only submits it."""
    kind: str  # the job's name
    target: str  # the job's unit: ``<app>/<env>`` or ``team:<name>``
    slug: str  # the branch is console/<slug>-<commit>
    title: str
    commands: tuple[Command, ...]
    make: str  # the same change as the operator would type it
    auto_merge: bool = False
    touches_others: bool = False  # it also changes another app, whose team reviews it
    preface: str = ''
    manual_merge_reason: str = ''

    @property
    def body(self) -> str:
        return f'{self.preface}Generated by the console with:\n\n    {self.make}'

    @classmethod
    def to_instance(cls, kind: str, instance: Instance, form: Mapping[str, str]) -> Change:
        """Scale, change access to, connect, disconnect or remove ``instance``. Only shapes are
        checked here; whether the change is allowed is the command's own rule, run in the clone."""
        app, env = instance.app, instance.env
        if kind == 'scale':
            commands: tuple[Command, ...] = (Command('app-scale', (app, env, *scale_flags(form))),)
        elif kind == 'schedule':
            when = ' '.join(form.get('schedule', '').split())
            if problem := schedule_problem(when):
                raise ActionError(problem)
            commands = (Command('app-schedule', (app, env, f'--schedule={when}')),)
        elif kind == 'expose':
            commands = (Command('app-expose', (app, env, *expose_flags(form))),)
        elif kind == 'connect':  # only this instance's Settings: the other app's side is rendered on main
            commands = Connections.from_form(form).adding(app, env)
        elif kind == 'disconnect':
            commands = removing(app, env, form)
        elif kind == 'egress':
            commands = (Command('app-egress', (app, env, f'--add={",".join(egress_hosts(form))}')),)
        elif kind == 'allow':
            commands = (Command('app-allows', (app, env, *allow_flags(form))),)
        elif kind == 'remove':
            commands = (Command('app-remove', (app, env)),)
        else:
            raise ActionError('no such change')
        return cls(kind, f'{app}/{env}', f'{kind}-{app}-{env}', f'apps: {INSTANCE_CHANGES[kind]} {app}/{env}', commands,
                   ' && '.join(command.as_make() for command in commands))

    @classmethod
    def to_team(cls, action: str, form: Mapping[str, str]) -> Change:
        """The Teams page's add, edit or remove.

        Only shapes are checked here (a name is a name, a description one short line); whether the team
        exists, still owns apps or is the default is the command's own rule, run in a clone of main."""
        name = form.get('name', '').strip()
        description = ' '.join(form.get('description', '').split())
        rename, move = form.get('rename', '').strip(), form.get('move_apps_to', '').strip()
        for label, value in (('team name', name), ('new name', rename), ('team to move apps to', move)):
            if (value or label == 'team name') and not TEAM_NAME.fullmatch(value):
                raise ActionError(f'{label}: lowercase letters, digits and hyphens, at most 40 characters')
        if len(description) > 200:
            raise ActionError('a description is one line of at most 200 characters')
        if action == 'add':
            flags, title = [f'--description={description}'], f'teams: add {name}'
            make = f"make team-add NAME={name} DESCRIPTION='{description}'"
        elif action == 'edit':
            flags = [f'--description={description}'] + ([f'--rename={rename}'] if rename and rename != name else [])
            title, make = f'teams: change {name}', f'make team-edit NAME={name} ARGS="{" ".join(flags)}"'
        elif action == 'remove':
            flags, title = ([f'--move-apps-to={move}'] if move else []), f'teams: remove {name}'
            make = f'make team-remove NAME={name}' + (f' ARGS="--move-apps-to {move}"' if move else '')
        else:
            raise ActionError('no such team change')
        return cls(f'team {action}', f'team:{name}', f'team-{action}-{name}', title,
                   (Command(f'team-{action}', (name, *flags)),), make)

    @classmethod
    def to_channel(cls, action: str, form: Mapping[str, str]) -> Change:
        """Give a team its notification channel (or a new address for it), or remove it.

        The address is the credential: it is only the command's standard input, and no wording built
        here repeats it."""
        name = form.get('name', '').strip()
        if not TEAM_NAME.fullmatch(name):
            raise ActionError('team name: lowercase letters, digits and hyphens, at most 40 characters')
        if action == 'remove':
            command, title = Command('team-channel', (name, '--remove')), f'teams: remove the notification channel of {name}'
            make = f'make team-channel NAME={name} ARGS=--remove'
        elif action == 'set':
            transport, address = form.get('transport', 'ntfy').strip(), form.get('address', '').strip()
            problem = channels.address_problem(transport, address)
            if problem:
                raise ActionError(f'channel address: {problem}')
            command = Command('team-channel', (name, f'--transport={transport}'), input=address + '\n')
            title = f'teams: set the notification channel of {name}'
            make = f'make team-channel NAME={name}    # the address was entered on the console and is only in the encrypted file'
        else:
            raise ActionError('no such channel change')
        return cls(f'channel {action}', f'team:{name}', f'team-channel-{action}-{name}', title, (command,), make)

    def apply(self, runner: Runner, job: Job, clone: Path) -> None:
        for command in self.commands:
            run_tool(runner, job, clone, command.name, list(command.argv), input=command.input)

    def open(self, runner: Runner, github: GitHub, job: Job) -> str:
        """Open the pull request; returns its URL."""
        app = self.commands[0].argv[0]
        new_app = self.commands[0].name == 'app-new'
        gated = self.kind in INSTANCE_KINDS  # its PR carries the instance's Settings alone; the gate's rule decides
        env = self.commands[0].argv[1] if gated else ''
        seen: dict[str, Settings | None] = {}

        def settings_in(clone: Path) -> Settings | None:
            path = clone / 'apps' / app / env / SETTINGS_FILE
            return Settings.parse(yamlio.load(path.read_text())) if path.is_file() else None

        def held() -> str:
            return settings_held(env, seen['before'], seen['after']) if gated else ''

        def change(clone: Path) -> None:
            if gated:
                seen['before'] = settings_in(clone)
            self.apply(runner, job, clone)
            if gated:
                seen['after'] = settings_in(clone)
            if new_app:
                path = clone / 'apps' / app / 'staging' / SETTINGS_FILE
                settings = Settings.parse(yamlio.load(path.read_text()))
                settings = replace(settings, allocations=replace(settings.allocations, egress_port=None))
                path.write_text(store.text(settings.document()))

        if new_app:
            keep = lambda path: new_app_records(app, path)  # noqa: E731
        elif gated:
            keep = lambda path: ACTIONS[self.kind].owns(app, env, path)  # noqa: E731
        else:
            keep = None
        return open_pr(runner, github, job, slug=self.slug, title=self.title, body=self.body, change=change,
                       auto_merge=(lambda: not held()) if gated else self.auto_merge,
                       manual_merge_reason=held if gated else self.manual_merge_reason, keep=keep)


NEW_APP = 'new-app'  # a job's resume record: the New app form's choices


@dataclass(frozen=True)
class NewApp:
    """The New app form's choices: the repository to create and the app-new flags for its staging
    instance. ``record`` is what a job keeps to finish the work later, and ``from_record`` reads it back."""
    name: str
    stack: str
    description: str
    answers: Mapping[str, str]
    flags: tuple[str, ...]
    touches_others: bool = False

    @classmethod
    def from_form(cls, form: Mapping[str, str], questions: list[repo.Question], teams: Mapping[str, str] | None,
                  callable_apps: set[str] | None = None, offered_events: set[str] | None = None) -> NewApp:
        """The form's ``type`` sets every template answer (contract.APP_TYPES) and the exposure staging
        starts with, unless the form chose one; ``questions`` are the stack template's own (its
        copier.yml), and a type answer the template no longer offers is refused. ``teams`` are the
        registered ones; the form's team must be among them. A worker gets no exposure or host: its
        swhurl.yaml makes it private. When the lists the form offered are given, a call or subscription
        outside them is refused."""
        name, stack = form.get('name', '').strip(), form.get('stack', '').strip()
        description = ' '.join(form.get('description', '').split())[:200]
        if not NAME_RE.match(name):
            raise ActionError('name must be a DNS label: lowercase letters, digits and hyphens, at most 40 characters')
        if stack not in STACKS:
            raise ActionError(f'stack must be one of {", ".join(sorted(STACKS))}')
        app_type = form.get('type', '').strip()
        try:
            answers = type_answers(app_type, stack)
        except ValueError as error:
            raise ActionError(str(error)) from None
        for question in questions:
            if question.name in answers and answers[question.name] not in question.choices:
                raise ActionError(f'the {stack} template no longer offers {question.name}={answers[question.name]} '
                                  f'(it offers {", ".join(question.choices)}): the {APP_TYPES[app_type].title} type needs updating')
        team = form.get('team', '').strip()
        if not teams:
            raise ActionError(f'no team is registered ({TEAMS_FILE}): add one on the Teams page first; every app belongs to a team')
        if team not in teams:
            raise ActionError(f'choose the team that owns the app: one of {", ".join(teams)} ({TEAMS_FILE})')
        flags = [f'--team={team}']
        exposure, host = form.get('exposure', '').strip() or APP_TYPES[app_type].exposure, form.get('host', '').strip()
        if answers.get('kind') != 'web':
            if host:
                raise ActionError('a worker has no web address; leave Host empty')
        else:
            if exposure:
                if exposure not in EXPOSURES:
                    raise ActionError(f'exposure must be one of {", ".join(EXPOSURES)}')
                flags.append(f'--exposure={exposure}')
            if checked_host(host):
                flags.append(f'--host={host}')
        chosen = Connections.from_form(form)
        if callable_apps is not None and offered_events is not None:
            chosen.check_offered(callable_apps, offered_events)
        return cls(name, stack, description, answers, (*flags, *chosen.new_app_flags()), chosen.touches_others)

    @classmethod
    def from_record(cls, record: Mapping[str, Any]) -> NewApp:
        """The choices a job kept; ``ValueError``, ``KeyError`` or ``TypeError`` if they cannot be used."""
        name, stack, flags = str(record['name']), str(record['stack']), tuple(str(flag) for flag in record['extra'])
        if not NAME_RE.match(name) or stack not in STACKS or not all(flag.startswith('--') for flag in flags):
            raise ValueError(name)
        Connections.from_flags(flags)  # refuse obsolete publishing jobs before creating anything
        return cls(name, stack, str(record.get('description') or ''),
                   {str(k): str(v) for k, v in (record.get('answers') or {}).items()}, flags, bool(record.get('touches_others')))

    @property
    def record(self) -> dict[str, Any]:
        return {'kind': NEW_APP, 'name': self.name, 'stack': self.stack, 'description': self.description,
                'answers': dict(self.answers), 'extra': list(self.flags), 'touches_others': self.touches_others}

    @property
    def target(self) -> str:
        return f'{self.name}/staging'

    @property
    def request(self) -> repo.Request:
        return repo.Request(self.name, self.stack, APP_OWNER, self.description, dict(self.answers))

    def change(self, image: str) -> Change:
        """The pull request that adds the staging instance, once the repository has published ``image``.
        It merges itself unless it changes another app: that pull request is the other team's consent."""
        made = self.request
        argv = (self.name, f'--from-repo={made.repo}', '--env=staging', f'--image={image}', *self.flags)
        template = STACKS[self.stack]
        return Change('new app and repository', self.target, f'new-{self.name}-staging', f'apps: add {self.target}',
                      (Command('app-new', argv),), f'make app-new NAME={self.name} ARGS="{" ".join(argv[1:])}"',
                      auto_merge=not self.touches_others, touches_others=self.touches_others,
                      manual_merge_reason=Connections.from_flags(self.flags).manual_merge_reason,
                      preface=(f'The console created https://github.com/{made.repo} from [{template}](https://github.com/{template}) '
                               'and waited for its first image.\n\n'))
