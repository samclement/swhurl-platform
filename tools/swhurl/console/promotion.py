"""How a promotion PR is recognised, and its manual merge presentation."""
from __future__ import annotations

import re

from swhurl.console import github as gh
from swhurl.console import merge


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
