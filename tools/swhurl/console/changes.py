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
from dataclasses import dataclass
from dataclasses import field as dataclass_field
from pathlib import Path
from typing import Any

import httpx
import yaml

from swhurl import platform
from swhurl.apps import channels, events, repo
from swhurl.apps.contract import (
    APP_OWNER,
    EXPOSURES,
    STACKS,
    TEAMS_FILE,
)
from swhurl.apps.instance import NAME_RE, Instance
from swhurl.console.actions import ActionError, Job
from swhurl.run import Runner

DEFAULT_REPO = platform.GITHUB_REPO
BASE = 'main'
BRANCH_PREFIX = 'console/'
AUTO_MERGE_LABEL = 'auto-merge'
"""A console PR with this label merges itself once Validate passes (.github/workflows/auto-merge.yml)."""
AUTHOR = ('swhurl console', 'console@users.noreply.github.com')
UNSET = ('', 'REPLACE_ME')

EXPOSURE_LABELS = {
    'authenticated-web': ('Signed in', 'A web address under {domain}, behind Google sign-in: only the accounts on the sign-in list.'),
    'public': ('Public', 'Anyone on the internet, no sign-in. Needs a host outside {domain}.'),
    'private': ('Private', 'No web address, and no other app can connect to it. For workers.'),
}
"""The form's plain-language names for each exposure: (title, description)."""


def platform_teams(runner: Runner, github: GitHub) -> dict[str, str]:
    """Team names and descriptions from the registry on ``main`` (the console image carries no checkout)."""
    api = GitHubAPI(runner, github)
    try:
        listing = api.json('GET', f'/contents/{TEAMS_FILE.as_posix()}?ref={BASE}')
        registered = (yaml.safe_load(base64.b64decode(listing.get('content', ''))) or {}).get('teams') or {}
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
        progress, checks_url = 'Awaiting setup or manual review', p['html_url'] + '/checks'
        if 'promotion-review-required' in labels:
            progress = 'Review required: merge verification failed'
        elif AUTO_MERGE_LABEL in labels and p['head'].get('sha'):
            api = GitHubAPI(runner, github)
            try:
                runs = api.json('GET', f"/actions/runs?head_sha={p['head']['sha']}&event=pull_request&per_page=30")
                checks = [r for r in runs.get('workflow_runs', [])
                          if r.get('name') == 'Validate' and r.get('head_sha') == p['head']['sha']]
                latest = checks[0] if checks else {}
                checks_url = latest.get('html_url', checks_url)
                if latest.get('conclusion') == 'success':
                    progress = 'Checks passed; awaiting merge verification'
                elif latest.get('conclusion') in ('failure', 'cancelled', 'timed_out', 'action_required'):
                    progress = 'Failed validation; unmerged'
                else:
                    progress = 'Checks running' if latest.get('status') == 'in_progress' else 'Checks pending'
                details = api.json('GET', f"/pulls/{p['number']}")
                if details.get('mergeable_state') == 'dirty':
                    progress = 'Merge conflict; review required'
            except ActionError:
                progress = 'Merge requested; check progress on GitHub'
            finally:
                api.client.close()
        out.append(PullRequest(p['number'], p['title'], p['html_url'], p['head']['ref'], p['created_at'][:10],
                               p.get('body') or '', labels, progress, checks_url))
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
            verify: Callable[[], None] | None = None) -> str:
    """Download ``main``, apply ``change(tree)``, commit its files to a new branch and open a PR. Returns the PR's URL.

    With ``auto_merge`` the PR is labelled to merge itself once Validate passes, unless it adds or changes an
    encrypted file: a new Secret's values are ``REPLACE_ME`` until someone sets them on the branch."""
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
        body = body(tree) if callable(body) else body
        auto_merge = auto_merge() if callable(auto_merge) else auto_merge
        after = snapshot(tree)
        changed = sorted(p for p in after.keys() | before.keys() if after.get(p) != before.get(p))
        if not changed:
            raise ActionError('the change left no files changed; nothing to propose')
        job.lines += [f'{"D" if p not in after else "A" if p not in before else "M"} {p}' for p in changed]
        encrypted = [p for p in changed if p.endswith('.sops.yaml')]
        if auto_merge and encrypted:
            auto_merge = False
            if all(Path(p).name == events.SECRET_FILE for p in encrypted):  # generated whole: nothing is left to fill in
                job.lines.append("Not merging automatically: it adds the app's events key, generated and encrypted here with "
                                 'nothing to set; review it, then merge it yourself')
            else:
                job.lines.append('Not merging automatically: set the Secret values on the branch first, then merge it yourself')
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
        note = '\n\nMerges itself when Validate passes (label `auto-merge`; remove it to merge by hand).' if auto_merge else ''
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
        configmap = yaml.safe_load(base64.b64decode(listing.get('content', ''))) or {}
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
    """What a form chose to connect an app to: apps to call, ``<app>.<event>`` to receive and
    events to publish, each a comma-separated field of that name."""
    calls: tuple[str, ...] = ()
    subscribe: tuple[str, ...] = ()
    publish: tuple[str, ...] = ()

    @classmethod
    def from_form(cls, form: Mapping[str, str]) -> Connections:
        chosen = cls(*(tuple(name.strip() for name in form.get(field, '').split(',') if name.strip())
                       for field in ('calls', 'subscribe', 'publish')))
        bad = [name for name in chosen.calls if not NAME_RE.match(name)]
        bad += [name for name in chosen.subscribe if not events.SUBSCRIPTION_RE.match(name)]
        bad += [name for name in chosen.publish if not events.EVENT_RE.match(name)]
        if bad:
            raise ActionError(f'{", ".join(bad)}: a call names an app, a subscription <app>.<event>, a published event a lowercase name')
        return chosen

    @property
    def touches_others(self) -> bool:
        """A call or a subscription also changes the other app: its allowed callers, the NATS users."""
        return bool(self.calls or self.subscribe)

    def check_offered(self, callable_apps: set[str], offered_events: set[str]) -> None:
        """Refuse a choice outside the lists the form offered, before a repository is created for an
        app that could not then be added."""
        missing = [name for name in self.calls if name not in callable_apps]
        missing += [name for name in self.subscribe if name not in offered_events]
        if missing:
            raise ActionError(f'{", ".join(missing)}: not a web app, or an event, that staging has now; reload the form')

    def new_app_flags(self) -> list[str]:
        """``--calls=``, ``--subscribe=`` and ``--publish=`` for app-new."""
        return [f'--{field}={",".join(names)}' for field, names in
                (('calls', self.calls), ('subscribe', self.subscribe), ('publish', self.publish)) if names]

    def adding(self, app: str, env: str) -> tuple[Command, ...]:
        """The app-calls and app-events commands that add these to an instance; one clone runs both,
        so one pull request carries them."""
        commands = []
        if self.calls:
            commands.append(Command('app-calls', (app, env, f'--add={",".join(self.calls)}')))
        added = [f'--add-{field}={",".join(names)}' for field, names in
                 (('subscribe', self.subscribe), ('publish', self.publish)) if names]
        if added:
            commands.append(Command('app-events', (app, env, *added)))
        if not commands:
            raise ActionError('choose an app to call, an event to receive or an event to publish')
        return tuple(commands)


def removing(app: str, env: str, form: Mapping[str, str]) -> tuple[Command, ...]:
    """The command that removes one connection this app declared: the row's ``role``, ``app`` and ``event``."""
    role, other, event = (form.get(key, '').strip() for key in ('role', 'app', 'event'))
    if role == 'calls' and NAME_RE.match(other):
        return (Command('app-calls', (app, env, f'--remove={other}')),)
    if role == 'subscribes' and events.SUBSCRIPTION_RE.match(f'{other}.{event}'):
        return (Command('app-events', (app, env, f'--remove-subscribe={other}.{event}')),)
    if role == 'publishes' and events.EVENT_RE.match(event):
        return (Command('app-events', (app, env, f'--remove-publish={event}')),)
    raise ActionError('no such connection to remove: only the app that declared one can remove it')


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


INSTANCE_CHANGES = {'scale': 'scale', 'expose': 'change access to', 'connect': 'change the connections of',
                    'disconnect': 'change the connections of', 'remove': 'remove'}
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

    @property
    def body(self) -> str:
        return f'{self.preface}Generated by the console with:\n\n    {self.make}'

    @classmethod
    def to_instance(cls, kind: str, instance: Instance, form: Mapping[str, str]) -> Change:
        """Scale, change access to, connect, disconnect or remove ``instance``. Only shapes are
        checked here; whether the change is allowed is the command's own rule, run in the clone."""
        app, env, touches_others = instance.app, instance.env, False
        if kind == 'scale':
            commands: tuple[Command, ...] = (Command('app-scale', (app, env, *scale_flags(form))),)
        elif kind == 'expose':
            commands = (Command('app-expose', (app, env, *expose_flags(form))),)
        elif kind == 'connect':
            chosen = Connections.from_form(form)
            commands, touches_others = chosen.adding(app, env), chosen.touches_others
        elif kind == 'disconnect':
            commands = removing(app, env, form)
        elif kind == 'remove':
            commands = (Command('app-remove', (app, env)),)
        else:
            raise ActionError('no such change')
        return cls(kind, f'{app}/{env}', f'{kind}-{app}-{env}', f'apps: {INSTANCE_CHANGES[kind]} {app}/{env}', commands,
                   ' && '.join(command.as_make() for command in commands), touches_others=touches_others)

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
        return open_pr(runner, github, job, slug=self.slug, title=self.title, body=self.body,
                       change=lambda clone: self.apply(runner, job, clone), auto_merge=self.auto_merge)


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
        """``questions`` are the stack template's own (its copier.yml): each is a ``feature-<name>`` field.
        ``teams`` are the registered ones; the form's team must be among them. A worker gets no exposure
        or host: its swhurl.yaml makes it private. When the lists the form offered are given, a call or
        subscription outside them is refused."""
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
        team = form.get('team', '').strip()
        if not teams:
            raise ActionError(f'no team is registered ({TEAMS_FILE}): add one on the Teams page first; every app belongs to a team')
        if team not in teams:
            raise ActionError(f'choose the team that owns the app: one of {", ".join(teams)} ({TEAMS_FILE})')
        flags = [f'--team={team}']
        exposure, host = form.get('exposure', '').strip(), form.get('host', '').strip()
        if answers.get('kind') == 'worker':
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
                      preface=(f'The console created https://github.com/{made.repo} from [{template}](https://github.com/{template}) '
                               'and waited for its first image.\n\n'))
