"""The console's GitHub client and what it reads there: open ``console/*`` pull requests, how their checks
and merges went, main's commit and the team and channel registries on ``main``.

Everything goes through GitHub's REST API with ``httpx`` (no ``git`` push, so no credential on disk). The
token (a fine-grained GitHub token in the ``console-github`` Secret) travels only in the ``Authorization``
header of those requests, never in a command line, and is redacted from every message.
"""
from __future__ import annotations

import base64
import json
import os
from collections.abc import Mapping
from dataclasses import dataclass
from dataclasses import field as dataclass_field
from typing import Any

import httpx
import yaml

from swhurl import platform, yamlio
from swhurl.apps import channels
from swhurl.apps.contract import TEAMS_FILE
from swhurl.console import merge
from swhurl.console.actions import ActionError
from swhurl.console.merge import REFUSAL, REFUSAL_LIMIT
from swhurl.run import Runner

DEFAULT_REPO = platform.GITHUB_REPO
BASE = 'main'
BRANCH_PREFIX = 'console/'
AUTO_MERGE_LABEL = 'auto-merge'
GATE_AUTHOR = 'github-actions[bot]'  # the Auto-merge workflow comments with GITHUB_TOKEN
"""A console PR with this label merges itself once Validate passes (.github/workflows/auto-merge.yml)."""
UNSET = ('', 'REPLACE_ME')


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

    def call(self, method: str, path: str, body: dict[str, Any] | None = None, *, missing_ok: bool = False,
             headers: Mapping[str, str] | None = None) -> httpx.Response:
        try:
            reply = self.client.request(method, path, json=body, headers=headers)
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
