"""Validate the exact merge result and fast-forward main only while its checked base still matches."""
from __future__ import annotations

import json
import os
import re
import shutil
import tempfile
from pathlib import Path

import yaml

from swhurl import ROOT
from swhurl.apps import new, promotion
from swhurl.apps.yaml_file import EditError
from swhurl.run import CommandError, Runner

MARKER = re.compile(r'<!-- swhurl-promotion (\{[^\n]*\}) -->')
BRANCH = re.compile(r'console/(new|promote)-([a-z][a-z0-9-]*)-staging-[0-9a-f]{7,40}')


class Hold(Exception):
    pass


def review_metadata(body: str) -> dict:
    match = MARKER.search(body)
    if not match:
        raise Hold('promotion has no reviewed configuration; create a fresh promotion')
    try:
        return json.loads(match[1])
    except (TypeError, ValueError):
        raise Hold('invalid promotion review metadata') from None


def scope(base: Path, candidate: Path, files: list[str], app: str, env: str) -> None:
    unit = f'clusters/home/app-{app}-{env}.yaml'
    registry = 'clusters/home/kustomization.yaml'
    for path in files:
        if path.endswith('.sops.yaml') or not (path.startswith(f'apps/{app}/{env}/') or path in (unit, registry)):
            raise Hold('diff includes encrypted files, infrastructure or unrelated edits')
        if not (candidate / path).is_file():
            raise Hold('automatic merge does not delete app resources')
    if registry in files:
        old = yaml.safe_load((base / registry).read_text())
        updated = yaml.safe_load((candidate / registry).read_text())
        expected = json.loads(json.dumps(old))
        expected['resources'].append(f'app-{app}-{env}.yaml')
        if updated != expected:
            raise Hold('unit registration changes more than this app')


def check_promotion(base: Path, candidate: Path, app: str, body: str, files: list[str], runner: Runner) -> None:
    from swhurl.console.promotion import inputs_hash

    meta = review_metadata(body)
    if meta.get('app') != app or meta.get('setup'):
        raise Hold('promotion needs setup or a fresh review')
    if promotion.fingerprint(base, app, 'prod') != meta.get('target'):
        raise Hold('production changed after review; review a fresh promotion')
    if inputs_hash(base) != meta.get('inputs'):
        raise Hold('promotion tooling, policy, chart or platform settings changed; review again')
    source, target = promotion.read(base, app)
    actual = promotion.YamlFile(candidate / 'apps' / app / 'prod/helmrelease.yaml').data
    if promotion.ops.image_reference(promotion.image_of(actual)) != meta.get('image'):
        raise Hold('PR no longer pins the reviewed image')
    if target is not None:
        expected = json.loads(json.dumps(target))
        image = promotion.image_of(expected)
        reviewed = new.parse_image(meta['image'])
        if image.get('repository') != reviewed['repository']:
            raise Hold('production image repository changed')
        for key in ('tag', 'digest'):
            if key in reviewed:
                image[key] = reviewed[key]
            else:
                image.pop(key, None)
        if actual != expected or files != [f'apps/{app}/prod/helmrelease.yaml']:
            raise Hold('later promotion changes more than the image pin')
        return
    if promotion.fingerprint(base, app, 'staging', ignore_image=True) != meta.get('source'):
        raise Hold('first-production staging configuration changed; regenerate and review')
    with tempfile.TemporaryDirectory(prefix='merge-promotion-') as tmp:
        work = Path(tmp)
        for rel in ('apps/' + app + '/staging', 'clusters/home/kustomization.yaml',
                    'platform/reloader/helmrelease.yaml', '.sops.yaml'):
            src, dst = base / rel, work / rel
            if src.exists():
                dst.parent.mkdir(parents=True, exist_ok=True)
                (shutil.copytree if src.is_dir() else shutil.copyfile)(src, dst)
        source = json.loads(json.dumps(source))
        source['spec']['values']['controllers']['main']['containers']['main']['image'] = new.parse_image(meta['image'])
        promotion.create(work, app, source, runner=runner)
        expected_files = sorted([p.relative_to(work).as_posix() for p in (work / 'apps' / app / 'prod').iterdir()]
                                + [f'clusters/home/app-{app}-prod.yaml', 'clusters/home/kustomization.yaml'])
        if sorted(files) != expected_files:
            raise Hold('first promotion has unexpected files')
        for rel in expected_files:
            if yaml.safe_load((candidate / rel).read_text()) != yaml.safe_load((work / rel).read_text()):
                raise Hold(f'first production differs from reviewed staging conversion: {rel}')


def eligible(pr: dict, sha: str, repo: str) -> tuple[str, str]:
    match = BRANCH.fullmatch(pr.get('head', {}).get('ref', ''))
    if (pr.get('state') != 'open' or not match or pr.get('base', {}).get('ref') != 'main'
            or pr.get('head', {}).get('sha') != sha
            or pr.get('head', {}).get('repo', {}).get('full_name') != repo
            or 'auto-merge' not in {label['name'] for label in pr.get('labels', [])} or pr.get('draft')):
        raise Hold('PR is held, moved, closed, a fork, or outside the supported console branches')
    return match[1], match[2]


def merge(runner: Runner, root: Path, number: int, sha: str, repo: str) -> None:
    pr_path = f'repos/{repo}/pulls/{number}'
    pr = runner.json(['gh', 'api', pr_path])
    action, app = eligible(pr, sha, repo)
    runs = runner.json(['gh', 'run', 'list', '--workflow', 'validate.yml', '--commit', sha,
                        '--event', 'pull_request', '--limit', '1', '--json', 'databaseId,status,conclusion'])
    if not runs or runs[0].get('status') != 'completed' or runs[0].get('conclusion') != 'success':
        raise Hold('Validate has not passed on the current PR head')
    files = runner.output(['gh', 'pr', 'diff', str(number), '--repo', repo, '--name-only']).splitlines()
    runner.run(['git', 'fetch', 'origin', 'main', f'pull/{number}/head'], cwd=root)
    base_sha = runner.output(['git', 'rev-parse', 'origin/main'], cwd=root).strip()
    if runner.output(['git', 'rev-parse', 'FETCH_HEAD'], cwd=root).strip() != sha:
        raise Hold('PR head changed during fetch')
    with tempfile.TemporaryDirectory(prefix='auto-merge-') as tmp:
        base, candidate = Path(tmp) / 'base', Path(tmp) / 'candidate'
        try:
            runner.run(['git', 'worktree', 'add', '--detach', str(base), base_sha], cwd=root)
            runner.run(['git', 'worktree', 'add', '--detach', str(candidate), base_sha], cwd=root)
            runner.run(['git', '-c', 'user.name=swhurl auto-merge', '-c', 'user.email=console@users.noreply.github.com',
                        'merge', '--no-ff', '--no-edit', sha], cwd=candidate)
            scope(base, candidate, files, app, 'prod' if action == 'promote' else 'staging')
            if action == 'promote':
                check_promotion(base, candidate, app, pr.get('body') or '', files, runner)
            elif (base / 'apps' / app / 'staging').exists():
                raise Hold('new-app PR overwrites an existing staging instance')
            runner.run(['make', 'check'], cwd=candidate)
            # Recheck the human's hold and the exact PR head after the potentially long validation.
            eligible(runner.json(['gh', 'api', pr_path]), sha, repo)
            current = runner.output(['git', 'ls-remote', 'origin', 'refs/heads/main'], cwd=root).split()[0]
            if current != base_sha:
                raise Hold('main moved during validation; retry against the new base')
            # Fast-forward rejects a concurrent main push. No force push, and no unvalidated merge result.
            runner.run(['git', 'push', 'origin', 'HEAD:refs/heads/main'], cwd=candidate, mutating=True)
            runner.run(['git', 'push', 'origin', '--delete', pr['head']['ref']], cwd=root, mutating=True, check=False)
        finally:
            for path in (candidate, base):
                runner.run(['git', 'worktree', 'remove', '--force', str(path)], cwd=root, check=False)


def main(argv=None, runner: Runner | None = None) -> int:
    import argparse
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('number', type=int, nargs='?')
    parser.add_argument('sha', nargs='?')
    args = parser.parse_args(argv)
    if (args.number is None) != (args.sha is None):
        parser.error('provide both PR number and SHA, or neither to process the eligible queue')
    runner = runner or Runner()
    runner.add_secret(os.environ.get('GH_TOKEN'))
    number = None
    try:
        repo = os.environ['GITHUB_REPOSITORY']
        queue = [{'number': args.number, 'headRefOid': args.sha}] if args.number is not None else runner.json(
            ['gh', 'pr', 'list', '--state', 'open', '--label', 'auto-merge',
             '--json', 'number,headRefOid,headRefName'])
        for item in queue:
            if args.number is None and not item['headRefName'].startswith('console/'):
                continue
            number = item['number']
            try:
                merge(runner, ROOT, number, item['headRefOid'], repo)
            except Hold as error:
                if 'Validate has not passed' in str(error) or 'PR is held' in str(error):
                    print(f'Not merging #{number}: {error}')
                    continue
                raise
    except (Hold, EditError, CommandError, KeyError, ValueError, TypeError, IndexError) as error:
        reason = runner.redact(str(error))
        print(f'Not merging {"#" + str(number) if number is not None else "console PRs"}: {reason}')
        transient = 'main moved during validation' in reason or (isinstance(error, CommandError)
                                                                 and error.args_run[:2] == ('git', 'push'))
        repo = os.environ.get('GITHUB_REPOSITORY')
        if repo and number is not None and not transient:
            # Persist the failed merge gate across console restarts, without changing reviewed PR contents.
            runner.run(['gh', 'api', '--method', 'DELETE', f'repos/{repo}/issues/{number}/labels/auto-merge'],
                       mutating=True, check=False)
            runner.run(['gh', 'api', '--method', 'POST', f'repos/{repo}/issues/{number}/labels', '--input', '-'],
                       input=json.dumps({'labels': ['promotion-review-required']}), mutating=True, check=False)
        return 1
    return 0
