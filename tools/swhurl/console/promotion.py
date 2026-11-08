"""How a promotion pull request is recognised among a console instance's pull requests."""
from __future__ import annotations

from swhurl.console import changes
from swhurl.console import github as gh


def matching(pr: gh.PullRequest, app: str) -> bool:
    """Whether ``pr`` promotes ``app``'s staging to production."""
    return changes.parse(pr.branch) == ('v2-promote', app, 'staging')
