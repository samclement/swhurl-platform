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

from swhurl.apps import calling, helmrelease, ops, promotion
from swhurl.apps.instance import Instance, instance_folder
from swhurl.apps.model import sync as model_sync
from swhurl.apps.new import GenerationError
from swhurl.apps.yaml_file import EditError
from swhurl.console import changes
from swhurl.console.actions import ActionError, Job
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


def admitting(root: Path, app: str, source: dict) -> list[str]:
    """The production apps a first promotion of ``app`` also changes once main renders it: each app staging
    calls that already runs in production gains the new caller in its allowed callers and network policy."""
    return [callee for callee in calling.declared_calls(helmrelease.variables(source))
            if callee != app and instance_folder(root, callee, 'prod').is_dir()]


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
    source, target = promotion.read(root, app)
    decision = promotion.decide(source, target)
    problem = promotion.source_problem(status, decision.image)
    if problem:
        raise ActionError(problem)
    namespace = promotion.read_yaml(instance_folder(root, app, 'staging') / 'namespace.yaml')
    exposure = namespace.get('metadata', {}).get('labels', {}).get(promotion.contract.EXPOSURE)
    if exposure not in promotion.contract.EXPOSURES:
        raise EditError('staging needs a supported exposure label')
    first, admits = None, []
    if decision.outcome == 'create':
        first = promotion.first_production(root, model_sync.read(root)[0], app, preview=True)
        admits = admitting(root, app, source)
    needs_host = decision.outcome == 'create' and exposure == 'public'
    if target:
        addresses = helmrelease.hosts(target)
    elif first and first.host:
        addresses = (first.host,)
    else:
        addresses = ()
    database = first.entry.settings.allocations.database if first else None
    found = {'app': app, 'outcome': decision.outcome, 'image': decision.image, 'previous': decision.previous,
             'rollback': decision.rollback, 'needs_host': needs_host,
             'secrets': first.secret_keys if first else [], 'storage': database.size if database else None,
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
            found['held'] = promotion.held(app, records, before, promotion.release_of(root, app, 'prod'),
                                           lambda release: release == staged)
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
    return re.fullmatch(rf'console/promote-{re.escape(app)}-staging-[0-9a-f]{{7,40}}', pr.branch) is not None


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


def open_pr(runner: Runner, github: changes.GitHub, job: Job, app: str, form: dict[str, str],
            staging: ops.InstanceStatus | None = None) -> str:
    """``staging``: the status the job has just judged. When its unit is behind main by commits that leave it
    alone, the tool is told which two commits were compared: it reads staging again, in a tree with no history."""
    image = form['image']
    previous = existing(runner, github, app, image)
    if previous:
        job.lines.append('Reusing the existing promotion pull request')
        return previous.url
    reasons, admits, first = [], [], []

    def change(root: Path):
        source, target = promotion.read(root, app)
        if target is None:
            first.append(True)
            admits.extend(admitting(root, app, source))
        before_files = {path: data for path, data in contents(root).items() if promotion.records(app, path)}
        before = promotion.release_of(root, app, 'prod')
        argv = [app, f'--expect-image={image}']
        if staging and staging.unaffected:
            argv.append(f'--source-unchanged={promotion.sha(staging.applied_revision)}..{promotion.sha(staging.desired_revision)}')
        if form.get('host'):
            argv.append(f'--host={form["host"]}')
        changes.run_tool(runner, job, root, 'app-promote', argv)
        after_files = {path: data for path, data in contents(root).items() if promotion.records(app, path)}
        changed = [path for path in before_files.keys() | after_files.keys() if before_files.get(path) != after_files.get(path)]
        staged = promotion.release_of(root, app, 'staging')
        # Staging runs the reviewed digest now (source_problem), and its record says so on main.
        reason = promotion.held(app, changed, before, promotion.release_of(root, app, 'prod'),
                                lambda release: release == staged)
        if reason:
            reasons.append(reason)
        if admits:
            reasons.append(f'also changes the allowed callers and network policy for {", ".join(admits)} once main renders it')

    def body(_root: Path) -> str:
        instructions = ''
        if first:
            instructions += ('\n\nBefore merging, review the production hostname, and if Secret placeholders were created, set each value on this branch '
                             f'with `make app-secret APP={app} ENV=prod NAME=<name>` (in your own terminal). Main renders production when this is merged.')
        if admits:
            instructions += (f"\n\nThis also lets `{app}` call {', '.join(f'`{callee}/prod`' for callee in admits)}: "
                             "each gains it in `SWHURL_ALLOWED_CALLERS` and its network policy when main renders it.")
        return f'Promote the reviewed staging image `{image}` to `{app}/prod`.' + instructions

    def verify():
        problem = promotion.source_problem(staging_status(runner, github, Instance(app, 'staging')), image)
        if problem:
            raise ActionError(problem)

    return changes.open_pr(runner, github, job, slug=f'promote-{app}-staging',
                           title=f'apps: promote {app}/staging to {app}/prod', body=body, change=change,
                           auto_merge=lambda: form.get('hold') != 'on' and not reasons,
                           manual_merge_reason=lambda: ' and '.join(reasons), verify=verify,
                           keep=lambda path: promotion.records(app, path))
