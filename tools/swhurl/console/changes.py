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
import shutil
import sys
import tarfile
import tempfile
from collections.abc import Iterator, Mapping
from dataclasses import dataclass, replace
from dataclasses import field as dataclass_field
from pathlib import Path
from typing import Any

import httpx
import yaml

from swhurl import platform, yamlio
from swhurl.apps import calling, channels, egress, events, helmrelease, promotion, repo
from swhurl.apps.contract import (
    APP_OWNER,
    APP_TYPES,
    EXPOSURES,
    STACKS,
    TEAMS_FILE,
    type_answers,
)
from swhurl.apps.instance import NAME_RE, Instance
from swhurl.apps.model import render, store
from swhurl.apps.model import sync as model_sync
from swhurl.apps.model.draft import Platform
from swhurl.apps.model.records import SETTINGS_FILE, Release, Settings
from swhurl.apps.model.render import ANNOTATION
from swhurl.console import merge
from swhurl.console.actions import ActionError, Job
from swhurl.console.merge import ACTIONS, REFUSAL, REFUSAL_LIMIT
from swhurl.console.stages import Target
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

    def call(self, method: str, path: str, body: dict[str, Any] | None = None, *, missing_ok: bool = False) -> httpx.Response:
        try:
            reply = self.client.request(method, path, json=body)
        except httpx.HTTPError as error:
            raise ActionError(f'GitHub {method} {path} failed: {self.runner.redact(str(error))}') from None
        if reply.status_code == 404 and missing_ok:
            return reply
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
    checks_url: str = ''
    checks: str = ''  # Validate on its head: passed, failed, running, pending, conflict, or '' (not read)
    refused: str = ''  # why the merge gate held it (its comment), when it did and the label is off


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


def pull_checks(runner: Runner, github: GitHub, pull: dict, labels: tuple[str, ...]) -> tuple[str, str, str]:
    """Facts about an open PR: its best checks link, how Validate went on its head (``PullRequest.checks``,
    read whoever merges it) and, without the auto-merge label, why the merge gate held it (``PullRequest.refused``)."""
    checks_url, checks = pull['html_url'] + '/checks', ''
    if not pull['head'].get('sha'):
        return checks_url, checks, ''
    api = GitHubAPI(runner, github)
    try:
        runs = api.json('GET', f"/actions/runs?head_sha={pull['head']['sha']}&event=pull_request&per_page=30")
        latest = next((r for r in runs.get('workflow_runs', [])
                       if r.get('name') == 'Validate' and r.get('head_sha') == pull['head']['sha']), {})
        checks_url = latest.get('html_url', checks_url)
        checks = ('passed' if latest.get('conclusion') == 'success'
                  else 'failed' if latest.get('conclusion') in ('failure', 'cancelled', 'timed_out', 'action_required')
                  else 'running' if latest.get('status') == 'in_progress' else 'pending')
        if api.json('GET', f"/pulls/{pull['number']}").get('mergeable_state') == 'dirty':
            checks = 'conflict'
    except ActionError:
        checks = ''
    finally:
        api.client.close()
    gated = AUTO_MERGE_LABEL not in labels and merge.gated(pull['head']['ref'])
    return checks_url, checks, refusal(runner, github, pull['number']) if gated else ''


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
    """Immutable Git evidence, not a verdict. Re-render the complete public inputs to identify the
    intended output, including Settings and other instances' relationships. Encrypted files contribute
    only their presence, never their contents. No records means absent even beside stale output."""
    api = GitHubAPI(runner, github)
    try:
        content = api.call('GET', f'/tarball/{revision}').content
        with tempfile.TemporaryDirectory(prefix='console-targets-') as tmp:
            root = Path(tmp)
            public = {calling.SIGNING_KEYS_FILE, events.CONTROLLER_KEY_FILE, events.CONSOLE_KEY_FILE}
            proxy = False
            with tarfile.open(fileobj=io.BytesIO(content), mode='r:gz') as archive:
                for member in archive.getmembers():
                    path = Path(*Path(member.name).parts[1:])
                    if path.is_absolute() or '..' in path.parts:
                        raise ValueError('invalid archive path')
                    proxy |= path == egress.PROXY_FILE or egress.PROXY_FILE.parent in path.parents
                    if not member.isfile():
                        continue
                    instance = len(path.parts) == 4 and path.parts[0] == 'apps'
                    encrypted = instance and path.name in ('events.sops.yaml', 'secret.sops.yaml')
                    if (path in public or encrypted or
                            (len(path.parts) == 3 and path.parts[0] == 'apps' and path.name == 'app.yaml') or
                            (instance and path.name in ('instance.yaml', 'release.yaml', 'helmrelease.yaml'))):
                        target = root / path
                        target.parent.mkdir(parents=True, exist_ok=True)
                        target.write_bytes(b'' if encrypted else archive.extractfile(member).read())
            world, problems = store.world(root)
            if problems:
                raise ValueError('; '.join(problems))
            def text(path: Path) -> str:
                return (root / path).read_text().strip() if (root / path).is_file() else ''
            keys = text(calling.SIGNING_KEYS_FILE)
            # Do not use store.platform here: its scratch-root signing-key fallback is not a Git fact.
            platform_inputs = Platform(json.dumps(json.loads(keys), sort_keys=True, separators=(',', ':')) if keys else '',
                                       proxy, text(events.CONTROLLER_KEY_FILE), text(events.CONSOLE_KEY_FILE))
            def digest(*parts: bytes) -> str:
                return hashlib.sha256(b'\0'.join(parts)).hexdigest()[:16]
            found = {}
            for each, entry in world.items():
                folder = each.folder(root)
                app, record = (folder.parent / 'app.yaml').read_bytes(), (folder / 'release.yaml').read_bytes()
                # Allocation completes creation; it is not a new operator choice. Use the actual
                # allocation for rendering, ignoring only that port for creation's input identity.
                inputs = replace(entry.settings, allocations=replace(entry.settings.allocations, egress_port=None))
                mine = yamlio.dump(inputs.document()).encode()
                path = folder / 'helmrelease.yaml'
                release = yamlio.load(path.read_text()) if path.is_file() else None
                if path.is_file() and not isinstance(release, dict):
                    raise ValueError(f'{each}: malformed HelmRelease')
                expected = ''
                if not proxy or entry.settings.allocations.egress_port is not None:
                    output = render.instance(each, world, platform_inputs)[path.relative_to(root)][0]
                    expected = output['metadata']['annotations'][ANNOTATION]
                found[str(each)] = Target(
                    digest(app, record, mine), digest(app, mine), entry.release.image.digest,
                    (release.get('metadata', {}).get('annotations') or {}).get(ANNOTATION, '') if release else None,
                    helmrelease.image(release).get('digest', '') if release else '', expected)
            return found
    except (tarfile.TarError, OSError, ValueError, AttributeError, TypeError, KeyError, yaml.YAMLError) as error:
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
        out.append(PullRequest(p['number'], p['title'], p['html_url'], p['head']['ref'], p['created_at'][:10],
                               p.get('body') or '', labels, *pull_checks(runner, github, p, labels)))
    return out



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


def staging_history(api: GitHubAPI, revision: str, app: str, current: Release | None) -> Iterator[Release]:
    """Staging snapshots on main's first-parent chain, starting with the downloaded main.
    Follow explicit parents, never GitHub's path-filtered commit list (which also visits side branches).
    A missing record is skipped; all other failed reads or malformed Releases are failures.
    """
    if current is not None:
        yield current
    path = f'apps/{app}/staging/release.yaml'
    while parents := api.json('GET', f'/git/commits/{revision}')['parents']:
        revision = parents[0]['sha']
        reply = api.call('GET', f'/contents/{path}?ref={revision}', missing_ok=True)
        if reply.status_code == 404:
            continue
        yield Release.parse(yamlio.load(base64.b64decode(reply.json()['content']).decode()), f'{app}/staging at {revision}')


def publish(api: GitHubAPI, job: Job, tree: Path, before: dict, after: dict, changed: list[str],
            head: str, branch: str, title: str, body: str, auto_merge: bool, plan: str) -> str:
    """Publish a prepared, judged proposal. No policy or preparation callbacks cross this boundary."""
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


def run_tool(runner: Runner, job: Job, clone: Path, command: str, argv: list[str], input: str | None = None) -> None:
    """The clone's own tooling (``app-new``, ``app-promote``, ...): its rules, policy check and SOPS recipients.
    ``input`` is its standard input: the one way a secret reaches it (never an argument, never in the job's lines).
    It writes no bytecode: every file it leaves in the clone becomes part of the commit."""
    result = runner.run([sys.executable, '-m', 'swhurl', command, *argv], cwd=clone, check=False, input=input,
                        env={'PYTHONPATH': str(clone / 'tools'), 'PYTHONDONTWRITEBYTECODE': '1'})
    job.lines += (result.stdout + result.stderr).splitlines()
    if result.returncode:
        raise ActionError(f'{command} refused this change (see above); nothing was written to GitHub')


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


def open_action(runner: Runner, github: GitHub, job: Job, action: str, name: str, env: str,
                commands: tuple[merge.Command, ...], *, preface: str = '', auto_merge: bool = True) -> str:
    """Prepare and judge sequentially, then publish only owned inputs. Failed preparation or reads
    make no GitHub writes; promotion readiness is checked once, last, before the first write."""
    api = GitHubAPI(runner, github)
    workdir = Path(tempfile.mkdtemp(prefix='console-'))
    try:
        head = api.json('GET', f'/git/ref/heads/{BASE}')['object']['sha']
        try:
            branch = merge.branch(action, name, env, head[:7])
        except ValueError as error:
            raise ActionError(str(error)) from None
        entry = ACTIONS[action]
        job.lines.append(f'Downloading {github.repo} {BASE} ({head[:7]})')
        tree = download(api, head, workdir)
        before = snapshot(tree)
        current = promotion.release_of(tree, name, 'staging') if action == 'promote' else None
        previous = entry.read(tree, name, env)
        first, admits = False, []
        if action == 'promote':
            from swhurl.console import promotion as review
            world, _ = model_sync.read(tree)
            first = promotion.select(tree, world, name).outcome == 'create'
            if first:
                admits = review.admitting(tree, name)
        for command in commands:
            run_tool(runner, job, tree, command.name, list(command.argv), input=command.input)
        if action == 'new':
            # app-new renders a port for CLI use; the gate allocates it afresh on newest main.
            path = tree / 'apps' / name / 'staging' / SETTINGS_FILE
            settings = Settings.parse(yamlio.load(path.read_text()))
            settings = replace(settings, allocations=replace(settings.allocations, egress_port=None))
            path.write_text(store.text(settings.document()))
        proposed = entry.read(tree, name, env)
        after = snapshot(tree)
        statuses = {p: 'D' if p not in after else 'A' if p not in before else 'M'
                    for p in after.keys() | before.keys() if after.get(p) != before.get(p)}
        changed = sorted(p for p, status in statuses.items() if status in entry.keeps and entry.owns(name, env, p))
        if not changed:
            raise ActionError('the change left no files changed; nothing to propose')
        reason = merge.judge(action, name, env, changed, previous, proposed,
                             lambda release: promotion.staged(release, staging_history(api, head, name, current)))
        encrypted = merge.encrypted_reason(action, name, env, {p: statuses[p] for p in changed}, tree)
        auto_merge = auto_merge and not (reason or encrypted)
        plan = ''
        for path in changed:
            job.lines.append(f'{statuses[path]} {path}')
        if not auto_merge:
            affected = (f'also changes the allowed callers and network policy for {", ".join(admits)} once main renders it'
                        if admits else '')
            why = ' and '.join(dict.fromkeys(filter(None, (reason, encrypted, affected)))) or 'needs manual review'
            plan = f'Manual merge required: this pull request {why}. Review the changes, then merge it yourself.'
            job.lines.append(f'Merge plan: {plan}')
        body = preface + 'Generated by the console with:\n\n    ' + ' && '.join(c.as_make() for c in commands)
        if action == 'promote':
            image = next(arg.split('=', 1)[1] for arg in commands[0].argv if arg.startswith('--expect-image='))
            body = f'Promote the reviewed staging image `{image}` to `{name}/prod`.'
            if first:
                body += ('\n\nBefore merging, review the production hostname, and if Secret placeholders were created, set each value on this branch '
                         f'with `make app-secret APP={name} ENV=prod NAME=<name>` (in your own terminal). Main renders production when this is merged.')
            if admits:
                body += (f"\n\nThis also lets `{name}` call {', '.join(f'`{callee}/prod`' for callee in admits)}: "
                         "each gains it in `SWHURL_ALLOWED_CALLERS` and its network policy when main renders it.")
            problem = promotion.source_problem(review.staging_status(runner, github, Instance(name, 'staging')), image)
            if problem:
                raise ActionError(problem)
        if runner.dry_run:
            job.lines.append(f'Dry run: would commit {len(changed)} file(s) to {branch}; no PR opened')
            return ''
        return publish(api, job, tree, before, after, changed, head, branch,
                       entry.title.format(name=name, env=env), body, auto_merge, plan)
    finally:
        api.client.close()
        shutil.rmtree(workdir, ignore_errors=True)


def open_new_app(runner: Runner, github: GitHub, job: Job, chosen: NewApp, image: str) -> str:
    made, template = chosen.request, STACKS[chosen.stack]
    commands = ACTIONS['new'].commands(chosen.name, 'staging', {'repo': made.repo, 'image': image, 'flags': chosen.flags})
    return open_action(runner, github, job, 'new', chosen.name, 'staging', commands,
                       preface=(f'The console created https://github.com/{made.repo} from [{template}](https://github.com/{template}) '
                                'and waited for its first image.\n\n'))


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
            if merge.checked_host(host):
                flags.append(f'--host={host}')
        chosen = merge.Connections.from_form(form)
        if callable_apps is not None and offered_events is not None:
            chosen.check_offered(callable_apps, offered_events)
        return cls(name, stack, description, answers, (*flags, *chosen.new_app_flags()))

    @classmethod
    def from_record(cls, record: Mapping[str, Any]) -> NewApp:
        """The choices a job kept; ``ValueError``, ``KeyError`` or ``TypeError`` if they cannot be used."""
        name, stack, flags = str(record['name']), str(record['stack']), tuple(str(flag) for flag in record['extra'])
        if not NAME_RE.match(name) or stack not in STACKS or not all(flag.startswith('--') for flag in flags):
            raise ValueError(name)
        if any(flag.startswith('--publish=') for flag in flags):
            raise ActionError('publishing choices are no longer supported: edit asyncapi.yaml named by '
                              'provides.eventsFile, then start a new job')
        return cls(name, stack, str(record.get('description') or ''),
                   {str(k): str(v) for k, v in (record.get('answers') or {}).items()}, flags)

    @property
    def record(self) -> dict[str, Any]:
        return {'kind': NEW_APP, 'name': self.name, 'stack': self.stack, 'description': self.description,
                'answers': dict(self.answers), 'extra': list(self.flags)}

    @property
    def target(self) -> str:
        return merge.target('new', self.name, 'staging')

    @property
    def request(self) -> repo.Request:
        return repo.Request(self.name, self.stack, APP_OWNER, self.description, dict(self.answers))
