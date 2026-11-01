"""How a promotion PR is recognised, and whether an instance is settled after other commits."""
from __future__ import annotations

import re
import shutil
import tempfile
from collections.abc import Callable
from pathlib import Path

from swhurl.apps import ops, promotion
from swhurl.console import github as gh
from swhurl.console import merge
from swhurl.console.actions import ActionError
from swhurl.console.proposal import download
from swhurl.run import Runner


def main_tree(runner: Runner, github: gh.GitHub, read: Callable[[Path], object]):
    api = gh.GitHubAPI(runner, github)
    work = Path(tempfile.mkdtemp(prefix='promotion-review-'))
    try:
        head = api.json('GET', '/git/ref/heads/main')['object']['sha']
        return read(download(api, head, work))
    finally:
        api.client.close()
        shutil.rmtree(work, ignore_errors=True)


def settled_status(runner: Runner, github: gh.GitHub | None, status: ops.InstanceStatus | None) -> ops.InstanceStatus | None:
    """``status`` as the gate judges it: when its unit is behind Git's newest revision, GitHub is asked whether
    the commits in between change the instance's own files (``promotion.settled``). One request, only then."""
    def changed(applied: str, newest: str) -> list[str] | None:
        if github is None:
            return None
        try:
            contained, paths = gh.changed_since(runner, github, applied, newest)
        except ActionError:
            return None
        return paths if contained else None
    return promotion.settled(status, changed)


def merge_mode(pr: gh.PullRequest) -> str:
    """How an open console PR gets merged: by the gate (its label), or by a person and why."""
    if gh.AUTO_MERGE_LABEL in pr.labels:
        return 'Auto-merge after checks'
    if pr.refused:
        return f'Manual merge required: auto-merge was refused: {pr.refused}'
    plan = re.search(r'(?m)^\*\*Merge plan:\*\* (.+)$', pr.body)
    return plan.group(1) if plan else 'Manual merge required'


def matching(pr: gh.PullRequest, app: str) -> bool:
    """Whether ``pr`` promotes ``app``'s staging to production, in either writer."""
    return merge.parse(pr.branch) in (('promote', app, 'staging'), ('v2-promote', app, 'staging'))
