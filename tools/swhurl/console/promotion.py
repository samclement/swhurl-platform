"""Review and open promotion PRs using GitHub's authoritative tree, not cached cluster rows.

A promotion PR carries only the app's records (promotion.records): production's Release, its offers
documents and, for a first promotion, its Settings and encrypted files. Main renders the rest when the
PR is merged (the merge gate, or the Render workflow after a merge on GitHub)."""
from __future__ import annotations

import contextlib
import difflib
import io
import re
import shutil
import tempfile
from collections.abc import Callable
from pathlib import Path

from swhurl.apps import ops, promotion
from swhurl.apps.errors import EditError
from swhurl.apps.instance import Instance
from swhurl.apps.model import write
from swhurl.apps.model.calls import admits
from swhurl.apps.new import GenerationError
from swhurl.console import changes, merge
from swhurl.console.actions import ActionError
from swhurl.run import CommandError, Runner

IMAGE_LINE = re.compile(r'(?m)^Promote the reviewed staging image `([^`\n]+)` to `[a-z0-9-]+/prod`\.')
"""The PR body's first line, which names the promoted image: an open PR for another image is refused."""
RENDERED = ('apps', 'clusters', 'platform')  # where a promotion's rendered files are
DIFF_LINES = 400  # the review page's rendered diff stops here


def main_tree(runner: Runner, github: changes.GitHub, read: Callable[[Path], object]):
    api = changes.GitHubAPI(runner, github)
    work = Path(tempfile.mkdtemp(prefix='promotion-review-'))
    try:
        head = api.json('GET', '/git/ref/heads/main')['object']['sha']
        return read(changes.download(api, head, work))
    finally:
        api.client.close()
        shutil.rmtree(work, ignore_errors=True)


def settled_status(runner: Runner, github: changes.GitHub | None, status: ops.InstanceStatus | None) -> ops.InstanceStatus | None:
    """``status`` as the gate judges it: when its unit is behind Git's newest revision, GitHub is asked whether
    the commits in between change the instance's own files (``promotion.settled``). One request, only then."""
    def changed(applied: str, newest: str) -> list[str] | None:
        if github is None:
            return None
        try:
            contained, paths = changes.changed_since(runner, github, applied, newest)
        except ActionError:
            return None
        return paths if contained else None
    return promotion.settled(status, changed)


def staging_status(runner: Runner, github: changes.GitHub | None, instance: Instance) -> ops.InstanceStatus | None:
    """Staging read from the cluster, as the gate judges it."""
    return settled_status(runner, github, ops.gather_status(runner, instance))


def admitting(root: Path, app: str) -> list[str]:
    """The production apps a first promotion of ``app`` also changes once main renders it: each app staging
    calls that runs in production and allows it there gains the new caller in its allowed callers and
    network policy."""
    world, _ = write.read(root)
    staging = world[Instance(app, 'staging')]
    return [callee for callee in staging.settings.grants.calls
            if callee != app and (found := world.get(Instance(callee, 'prod'))) and admits(found, app, staging.app.team)]


def contents(root: Path) -> dict[str, bytes]:
    """Every file under the folders a promotion writes, by path from ``root``."""
    return {path.relative_to(root).as_posix(): path.read_bytes()
            for top in RENDERED for path in sorted((root / top).rglob('*')) if path.is_file()}


def rendered_diff(root: Path, app: str, image: str, runner: Runner) -> tuple[str, int, list[str]]:
    """What a later promotion of ``image`` changes in main (``root``, which is changed): ``(unified diff of
    the rendered files, how many, the records it changes)``, the diff stopping after DIFF_LINES lines.
    Records are left out of the diff: the PR shows them."""
    before = contents(root)
    with contextlib.redirect_stdout(io.StringIO()):
        from swhurl.apps import edit
        edit.promote(root, app, expect_image=image, runner=runner)
    after = contents(root)
    changed = sorted(path for path in before.keys() | after.keys() if before.get(path) != after.get(path))
    paths = [path for path in changed if not promotion.records(app, path)]
    lines: list[str] = []
    for path in paths:
        lines += difflib.unified_diff(before.get(path, b'').decode(errors='replace').splitlines(),
                                      after.get(path, b'').decode(errors='replace').splitlines(),
                                      f'a/{path}', f'b/{path}', lineterm='')
    if len(lines) > DIFF_LINES:
        lines = [*lines[:DIFF_LINES], f'… {len(lines) - DIFF_LINES} more lines']
    return '\n'.join(lines), len(paths), [path for path in changed if promotion.records(app, path)]


def review(root: Path, app: str, status: ops.InstanceStatus, runner: Runner | None = None) -> dict:
    world, _ = write.read(root)
    decision = promotion.select(root, world, app)
    source, target = world[Instance(app, 'staging')], world.get(Instance(app, 'prod'))
    problem = promotion.source_problem(status, decision.image)
    if problem:
        raise ActionError(problem)
    exposure = source.settings.grants.exposure
    first, admits = None, []
    if decision.outcome == 'create':
        first = promotion.first_production(root, world, app, preview=True)
        admits = admitting(root, app)
    needs_host = decision.outcome == 'create' and exposure == 'public'
    if target:
        settings = target.settings
        addresses = (settings.choices.host or promotion.contract.default_host(app, 'prod'),) if settings.grants.exposure != 'private' else ()
    elif first and first.host:
        addresses = (first.host,)
    else:
        addresses = ()
    found = {'app': app, 'outcome': decision.outcome, 'image': decision.image, 'previous': decision.previous,
             'rollback': decision.rollback, 'needs_host': needs_host,
             'secrets': first.secret_keys if first else [], 'storage': first.database_size if first and first.database_size else None,
             'admits': admits,
             'address': ', '.join('https://' + address for address in addresses) or
             ('private (no route)' if not needs_host else ''),
             'setup': decision.outcome == 'create', 'diff': '', 'diff_files': 0, 'diff_problem': '', 'held': ''}
    if decision.outcome == 'update':
        before, staged = promotion.release_of(root, app, 'prod'), promotion.release_of(root, app, 'staging')
        try:
            found['diff'], found['diff_files'], records = rendered_diff(root, app, decision.image, runner or Runner())
        except (EditError, CommandError, GenerationError) as error:
            found['diff_problem'] = str(error)
        else:
            # Why a person would merge it (promotion.held), shown before the PR exists; staging runs this digest.
            found['held'] = merge.judge('promote', app, 'staging', records, before,
                                        merge.ACTIONS['promote'].read(root, app, 'staging'), lambda release: promotion.staged(release, [staged]))
    return found


def merge_mode(pr: changes.PullRequest) -> str:
    """How an open console PR gets merged: by the gate (its label), or by a person and why."""
    if changes.AUTO_MERGE_LABEL in pr.labels:
        return 'Auto-merge after checks'
    if pr.refused:
        return f'Manual merge required: auto-merge was refused: {pr.refused}'
    plan = re.search(r'(?m)^\*\*Merge plan:\*\* (.+)$', pr.body)
    return plan.group(1) if plan else 'Manual merge required'


def matching(pr: changes.PullRequest, app: str) -> bool:
    """Whether ``pr`` promotes ``app``'s staging to production (merge.parse)."""
    return merge.parse(pr.branch) == ('promote', app, 'staging')


def promoted_image(body: str) -> str:
    found = IMAGE_LINE.search(body)
    return found[1] if found else ''


def existing(runner: Runner, github: changes.GitHub, app: str, image: str) -> changes.PullRequest | None:
    for pr in changes.console_prs(runner, github):
        if matching(pr, app):
            if promoted_image(pr.body) != image:
                raise ActionError(f'A promotion PR is already open: {pr.url}. Close it explicitly before reviewing a replacement image.')
            return pr
    return None
