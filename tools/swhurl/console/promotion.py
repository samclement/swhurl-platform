"""Review and recover promotion PRs using GitHub's authoritative tree, not cached cluster rows."""
from __future__ import annotations

import hashlib
import json
import re
import shutil
import tempfile
from collections.abc import Callable
from pathlib import Path

from swhurl import platform
from swhurl.apps import calling, helmrelease, ops, promotion
from swhurl.apps.instance import Instance, instance_folder
from swhurl.apps.model import sync as model_sync
from swhurl.apps.yaml_file import EditError
from swhurl.console import changes
from swhurl.console.actions import ActionError, Job
from swhurl.run import Runner

MARKER = re.compile(r'<!-- swhurl-promotion (\{[^\n]*\}) -->')


def inputs_hash(root: Path) -> str:
    files = [*sorted((root / 'tools/swhurl/apps').glob('*.py')),
             root / platform.SETTINGS, root / platform.HELM_REPOSITORIES, root / '.sops.yaml',
             root / 'tools/swhurl/console/promotion.py', root / 'tools/swhurl/console/merge.py',
             root / '.github/workflows/auto-merge.yml']
    return hashlib.sha256(json.dumps({p.relative_to(root).as_posix(): hashlib.sha256(p.read_bytes()).hexdigest()
                                     for p in files if p.is_file()}, sort_keys=True).encode()).hexdigest()


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
    """The production apps a first promotion of ``app`` also edits: each app staging calls that already
    runs in production gains the new caller in its allowed callers and network policy. The merge gate
    holds a promotion to its own app, so such a pull request is merged by a person."""
    return [callee for callee in calling.declared_calls(helmrelease.variables(source))
            if callee != app and instance_folder(root, callee, 'prod').is_dir()]


def review(root: Path, app: str, status: ops.InstanceStatus) -> dict:
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
    return {'app': app, 'outcome': decision.outcome, 'image': decision.image, 'previous': decision.previous,
            'rollback': decision.rollback,
            'configuration': promotion.fingerprint(root, app, 'staging'),
            'target': promotion.fingerprint(root, app, 'prod'), 'needs_host': needs_host,
            'secrets': first.secret_keys if first else [], 'storage': database.size if database else None,
            'admits': admits,
            'address': ', '.join('https://' + address for address in addresses) or
            ('private (no route)' if not needs_host else ''),
            'setup': decision.outcome == 'create'}  # a person merges every first promotion (merge.check_promotion)


def metadata(root: Path, app: str, image: str, setup: bool) -> dict:
    return {'app': app, 'image': image, 'setup': setup,
            'source': promotion.fingerprint(root, app, 'staging', ignore_image=True),
            'target': promotion.fingerprint(root, app, 'prod'), 'inputs': inputs_hash(root)}


def body_metadata(body: str) -> dict:
    match = MARKER.search(body)
    try:
        return json.loads(match[1]) if match else {}
    except ValueError:
        return {}


def merge_mode(pr: changes.PullRequest) -> tuple[str, str]:
    """How an open console PR gets merged, and the one control that applies to it now:
    ``(words, 'hold' | 'resume' | '')``. Only a promotion can be held or handed back to auto-merge."""
    if 'promotion-review-required' in pr.labels:
        return 'Manual merge required: auto-merge was refused', ''
    promote = pr.branch.startswith('console/promote-')
    if changes.AUTO_MERGE_LABEL in pr.labels:
        return 'Auto-merge after checks', 'hold' if promote else ''
    meta = body_metadata(pr.body) if promote else {}
    if meta.get('rollback'):
        return 'Manual merge required: a rollback needs your review', ''
    if meta and not meta.get('setup'):
        return 'Manual merge required: held by you', 'resume'
    if promote and meta.get('setup'):
        return 'Manual merge required: setup needs your review', ''
    plan = re.search(r'(?m)^\*\*Merge plan:\*\* (.+)$', pr.body)
    if plan:
        return plan.group(1), ''
    return 'Manual merge required', ''


def matching(pr: changes.PullRequest, app: str) -> bool:
    return re.fullmatch(rf'console/promote-{re.escape(app)}-staging-[0-9a-f]{{7,40}}', pr.branch) is not None


def existing(runner: Runner, github: changes.GitHub, app: str, image: str) -> changes.PullRequest | None:
    for pr in changes.console_prs(runner, github):
        if matching(pr, app):
            if body_metadata(pr.body).get('image') != image:
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
    captured, admits, own_setup, rollback = {}, [], [], []

    def change(root: Path):
        source, target = promotion.read(root, app)
        if target is not None and promotion.decide(source, target).rollback:
            rollback.append(True)  # the merge gate holds a rollback too (merge.replay_promotion)
        if promotion.fingerprint(root, app, 'prod') != form['target']:
            raise ActionError('production changed since review; refresh and review it again')
        if promotion.fingerprint(root, app, 'staging') != form['configuration']:
            raise ActionError('staging configuration changed since review; refresh and review it again')
        first = promotion.first_production(root, model_sync.read(root)[0], app, host=form.get('host') or None) if target is None else None
        admits.extend(admitting(root, app, source) if target is None else [])
        if first:  # a person merges every first promotion (merge.check_promotion)
            own_setup.append(True)
        setup = bool(own_setup or admits)
        captured.update(metadata(root, app, image, setup), rollback=bool(rollback))
        if admits:
            job.lines.append(f"Not merging automatically: it also lets {app} call {', '.join(admits)} in production, "
                             "changing those apps' allowed callers and network policy; review them, then merge it yourself")
        argv = [app, f'--expect-image={image}', f'--expect-config={form["configuration"]}']
        if staging and staging.unaffected:
            argv.append(f'--source-unchanged={promotion.sha(staging.applied_revision)}..{promotion.sha(staging.desired_revision)}')
        if form.get('host'):
            argv.append(f'--host={form["host"]}')
        changes.run_tool(runner, job, root, 'app-promote', argv)

    def body(_root: Path) -> str:
        instructions = ''
        if own_setup:
            instructions += ('\n\nBefore merging, review the production hostname, and if Secret placeholders were created, set each value on this branch '
                             f'with `make app-secret APP={app} ENV=prod NAME=<name>` (in your own terminal) and review Reloader registration.')
        if admits:
            instructions += (f"\n\nThis also lets `{app}` call {', '.join(f'`{callee}/prod`' for callee in admits)}: "
                             "each gains it in `SWHURL_ALLOWED_CALLERS` and its network policy. The merge gate holds a "
                             'promotion to its own app, so review those changes and merge this yourself.')
        return (f'Promote the reviewed staging image `{image}` to `{app}/prod`.' + instructions
                + '\n\n<!-- swhurl-promotion ' + json.dumps(captured, sort_keys=True) + ' -->')

    def verify():
        problem = promotion.source_problem(staging_status(runner, github, Instance(app, 'staging')), image)
        if problem:
            raise ActionError(problem)

        def current(root):
            if (promotion.fingerprint(root, app, 'staging') != form['configuration']
                    or promotion.fingerprint(root, app, 'prod') != form['target']
                    or inputs_hash(root) != captured['inputs']):
                raise ActionError('configuration changed before PR creation; refresh and review it again')
        main_tree(runner, github, current)

    return changes.open_pr(runner, github, job, slug=f'promote-{app}-staging',
                           title=f'apps: promote {app}/staging to {app}/prod', body=body, change=change,
                           auto_merge=lambda: form.get('hold') != 'on' and not captured.get('setup') and not rollback,
                           manual_merge_reason=lambda: (f'also changes the allowed callers and network policy for {", ".join(admits)}'
                                                        if admits else 'needs production setup review' if own_setup
                                                        else 'promotes an older image than production runs (a rollback)' if rollback else ''),
                           verify=verify)


def control(runner: Runner, github: changes.GitHub, number: int, action: str) -> str:
    api = changes.GitHubAPI(runner, github)
    try:
        pr = api.json('GET', f'/pulls/{number}')
        if (pr.get('state') != 'open' or not pr.get('head', {}).get('ref', '').startswith('console/promote-')
                or pr.get('head', {}).get('repo', {}).get('full_name') != github.repo or pr.get('base', {}).get('ref') != 'main'):
            raise ActionError('only an open promotion PR from this repository can be held or resumed')
        if action == 'hold':
            labels = {label['name'] for label in pr.get('labels', [])}
            if changes.AUTO_MERGE_LABEL in labels:
                api.call('DELETE', f'/issues/{number}/labels/{changes.AUTO_MERGE_LABEL}')
        elif action == 'resume':
            meta = body_metadata(pr.get('body') or '')
            if 'promotion-review-required' in {label['name'] for label in pr.get('labels', [])}:
                raise ActionError('merge verification failed; close this PR and review a fresh promotion')
            if meta.get('setup'):
                raise ActionError('this promotion needs setup and manual merging; it cannot auto-merge')
            if meta.get('rollback'):
                raise ActionError('this promotion is a rollback: a person reviews and merges it')
            if not meta:
                raise ActionError('this PR has no reviewed configuration; create a fresh promotion')
            api.call('POST', f'/issues/{number}/labels', {'labels': [changes.AUTO_MERGE_LABEL]})
        else:
            raise ActionError('choose hold or resume')
        return pr.get('html_url', f'https://github.com/{github.repo}/pull/{number}')
    finally:
        api.client.close()
