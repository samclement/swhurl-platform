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
from swhurl.apps import ops, promotion
from swhurl.apps.yaml_file import EditError
from swhurl.console import changes, cluster
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


def review(root: Path, app: str, status: ops.InstanceStatus) -> dict:
    source, target = promotion.read(root, app)
    decision = promotion.decide(source, target)
    problem = promotion.source_problem(status, decision.image, status.applied_revision)
    if problem:
        raise ActionError(problem)
    namespace = promotion.YamlFile(root / 'apps' / app / 'staging/namespace.yaml').data
    exposure = namespace.get('metadata', {}).get('labels', {}).get(promotion.contract.EXPOSURE)
    if exposure not in promotion.contract.EXPOSURES:
        raise EditError('staging needs a supported exposure label')
    args = None
    if decision.outcome == 'create':
        args = promotion.first_args(root, app, source, preview=True)
    needs_host = decision.outcome == 'create' and exposure == 'public'
    addresses = cluster.release_hosts(target) if target else ((args.host,) if args and args.host else ())
    return {'app': app, 'outcome': decision.outcome, 'image': decision.image, 'previous': decision.previous,
            'rollback': decision.rollback, 'revision': status.applied_revision,
            'configuration': promotion.fingerprint(root, app, 'staging'),
            'target': promotion.fingerprint(root, app, 'prod'), 'needs_host': needs_host,
            'secrets': args.secret_keys if args else [], 'storage': args.persistence if args else None,
            'address': ', '.join('https://' + address for address in addresses) or
            ('private (no route)' if not needs_host else ''),
            'setup': bool(needs_host or args and args.secret_keys)}


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


def matching(pr: changes.PullRequest, app: str) -> bool:
    return re.fullmatch(rf'console/promote-{re.escape(app)}-staging-[0-9a-f]{{7,40}}', pr.branch) is not None


def existing(runner: Runner, github: changes.GitHub, app: str, image: str) -> changes.PullRequest | None:
    for pr in changes.console_prs(runner, github):
        if matching(pr, app):
            if body_metadata(pr.body).get('image') != image:
                raise ActionError(f'A promotion PR is already open: {pr.url}. Close it explicitly before reviewing a replacement image.')
            return pr
    return None


def open_pr(runner: Runner, github: changes.GitHub, job: Job, app: str, form: dict[str, str]) -> str:
    image = form['image']
    previous = existing(runner, github, app, image)
    if previous:
        job.lines.append('Reusing the existing promotion pull request')
        return previous.url
    captured = {}

    def change(root: Path):
        source, target = promotion.read(root, app)
        if promotion.fingerprint(root, app, 'prod') != form['target']:
            raise ActionError('production changed since review; refresh and review it again')
        if promotion.fingerprint(root, app, 'staging') != form['configuration']:
            raise ActionError('staging configuration changed since review; refresh and review it again')
        args = promotion.first_args(root, app, source, host=form.get('host') or None) if target is None else None
        setup = bool(args and (args.secret_keys or args.exposure == 'public'))
        captured.update(metadata(root, app, image, setup))
        argv = [app, f'--expect-image={image}', f'--expect-config={form["configuration"]}']
        if form.get('host'):
            argv.append(f'--host={form["host"]}')
        changes.run_tool(runner, job, root, 'app-promote', argv)

    def body(_root: Path) -> str:
        instructions = ('\n\nBefore merging, review the production hostname, and if Secret stubs were created, set their values with '
                        f'`sops apps/{app}/prod/secret.sops.yaml` and review Reloader registration.'
                        if captured.get('setup') else '')
        return (f'Promote the reviewed staging image `{image}` to `{app}/prod`.' + instructions
                + '\n\n<!-- swhurl-promotion ' + json.dumps(captured, sort_keys=True) + ' -->')

    def verify():
        problem = promotion.source_problem(ops.gather_status(runner, ops.Instance(app, 'staging')),
                                           image, form['revision'])
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
                           auto_merge=lambda: form.get('hold') != 'on' and not captured.get('setup'), verify=verify)


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
            if not meta:
                raise ActionError('this PR has no reviewed configuration; create a fresh promotion')
            api.call('POST', f'/issues/{number}/labels', {'labels': [changes.AUTO_MERGE_LABEL]})
        else:
            raise ActionError('choose hold or resume')
        return pr.get('html_url', f'https://github.com/{github.repo}/pull/{number}')
    finally:
        api.client.close()
