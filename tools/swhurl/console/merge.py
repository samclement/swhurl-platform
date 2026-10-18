"""Validate the exact merge result and fast-forward main only while its checked base still matches."""
from __future__ import annotations

import contextlib
import io
import json
import os
import re
import shutil
import tempfile
import time
from collections.abc import Callable
from pathlib import Path

from swhurl import ROOT, yamlio
from swhurl.apps import egress, events, helmrelease, new, promotion
from swhurl.apps.instance import Instance, instance_folder, instance_unit_file
from swhurl.apps.model import sync as model_sync
from swhurl.apps.yaml_file import EditError, YamlFile
from swhurl.run import CommandError, Runner

MARKER = re.compile(r'<!-- swhurl-promotion (\{[^\n]*\}) -->')
VALIDATE_WAIT = 120  # seconds this run waits for Validate on the PR head, asking every VALIDATE_POLL
VALIDATE_POLL = 5
MERGE_CHECKS = ['-j2', '--output-sync=target', 'check-repo', 'check-apps']
REFUSAL = 'Auto-merge refused: '  # starts the comment the gate leaves on a PR it holds; the console shows the rest
REFUSAL_LIMIT = 300
BRANCH = re.compile(r'console/(new|promote)-([a-z][a-z0-9-]*)-staging-[0-9a-f]{7,40}')
# A new app's files outside its folder, generated from every instance's declaration (make app-sync): its
# NATS user, its egress port, Reloader watching its namespace. The gate regenerates them on the merge result.
DERIVED = ('platform/nats/users.yaml', 'platform/egress/proxy.yaml', 'platform/reloader/helmrelease.yaml')
REGISTRY = 'clusters/home/kustomization.yaml'
PROXY_VARIABLES = (egress.HTTP_VARIABLE, egress.HTTPS_VARIABLE, egress.JAVA_VARIABLE, egress.NODE_VARIABLE,
                   egress.NO_PROXY_VARIABLE)
GIT = ['git', '-c', 'user.name=swhurl auto-merge', '-c', 'user.email=console@users.noreply.github.com']


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


def scope(base: Path, candidate: Path, files: list[str], app: str, env: str, new_app: bool = False) -> None:
    unit = instance_unit_file(Path(), app, env).as_posix()
    registry = 'clusters/home/kustomization.yaml'
    events_key = f'apps/{app}/{env}/{events.SECRET_FILE}'
    for path in files:
        generated = new_app and (path == events_key or path in DERIVED)  # checked by check_new_app
        if not generated and (path.endswith('.sops.yaml')
                              or not (path.startswith(f'apps/{app}/{env}/') or path in (unit, registry))):
            raise Hold('diff includes encrypted files, infrastructure or unrelated edits')
        if not (candidate / path).is_file():
            raise Hold('automatic merge does not delete app resources')
    if registry in files:
        old = yamlio.load((base / registry).read_text())
        updated = yamlio.load((candidate / registry).read_text())
        expected = json.loads(json.dumps(old))
        expected['resources'].append(instance_unit_file(Path(), app, env).name)
        if updated != expected:
            raise Hold('unit registration changes more than this app')


def recipients(root: Path, path: str) -> set[str]:
    """The age recipients .sops.yaml's first rule matching ``path`` encrypts to."""
    config = root / '.sops.yaml'
    for rule in (yamlio.load(config.read_text()) if config.is_file() else {}).get('creation_rules') or []:
        if re.search(rule.get('path_regex', ''), path):
            return {r.strip() for r in str(rule.get('age', '')).split(',') if r.strip()}
    return set()


def release_taken_port(root: Path, app: str) -> None:
    """Drop the new app's egress port when another instance already has it, so make app-sync gives it a
    free one: two new apps opened from the same main are each given the first free port."""
    ports = egress.ports(root)
    own = ports.pop(Instance(app, 'staging'), None)
    if own is None or own not in ports.values():
        return
    document = YamlFile(instance_folder(root, app, 'staging') / 'helmrelease.yaml')
    variables = helmrelease.container(document.data).get('env') or {}
    for key in PROXY_VARIABLES:
        variables.pop(key, None)
    document.save()


def merge_new_app(runner: Runner, base: Path, candidate: Path, sha: str, app: str, files: list[str]) -> None:
    """A new app's merge result on the newest main, whatever main gained since its PR was opened. Every
    new app adds a line to the unit registry and changes the shared files make app-sync derives, so two
    opened from the same main conflict there and may hold the same egress port: those files are taken
    from main and made again here (check_new_app then holds anything else). A conflict anywhere else holds
    the PR."""
    runner.run([*GIT, 'merge', '--no-ff', '--no-commit', sha], cwd=candidate, check=False)
    shared = [REGISTRY, *DERIVED]
    conflicts = [path for path in runner.output(['git', 'diff', '--name-only', '--diff-filter=U'], cwd=candidate).splitlines()
                 if path and path not in shared]
    if conflicts:
        raise Hold('new-app PR conflicts with main outside the files every new app changes: ' + ', '.join(conflicts))
    present = [path for path in shared if (base / path).is_file()]
    if present:
        runner.run(['git', 'checkout', 'HEAD', '--', *present], cwd=candidate)
    new.register(candidate, instance_unit_file(Path(), app, 'staging').name)
    scope(base, candidate, files, app, 'staging', new_app=True)  # before anything runs on the result
    release_taken_port(candidate, app)
    runner.run(['make', 'app-sync'], cwd=candidate)
    runner.run(['git', 'add', '--', *[path for path in shared if (candidate / path).is_file()],
                f'apps/{app}/staging'], cwd=candidate)
    runner.run([*GIT, 'commit', '--no-edit'], cwd=candidate)


def check_new_app(base: Path, candidate: Path, files: list[str], app: str, runner: Runner) -> None:
    """What a new app adds beyond its own files is only what the platform generates for it.

    Its events key: a new Secret holding its NATS seed and nothing else, encrypted to this repository's
    recipients alone (the value cannot be read here, nor by a reviewer). The shared files: exactly what
    make app-sync derives from every instance's declaration, so no permission is granted by hand."""
    key = f'apps/{app}/staging/{events.SECRET_FILE}'
    if key in files:
        if (base / key).exists():
            raise Hold('new-app PR replaces an existing events key')
        doc = yamlio.load((candidate / key).read_text()) or {}
        sops = doc.get('sops') or {}
        values = doc.get('stringData') or {}
        if (doc.get('kind') != 'Secret' or 'data' in doc
                or doc.get('metadata') != {'name': events.secret_name(app), 'namespace': f'{app}-staging'}
                or set(values) != {events.SEED_VARIABLE} or not str(values[events.SEED_VARIABLE]).startswith('ENC[')
                or {r.get('recipient') for r in sops.get('age') or []} != recipients(base, key)
                or any(sops.get(other) for other in ('kms', 'gcp_kms', 'azure_kv', 'hc_vault', 'pgp'))):
            raise Hold("the events key is not the app's generated, encrypted NATS seed alone")
    if any(path in DERIVED for path in files):
        runner.run(['make', 'app-sync'], cwd=candidate)
        changed = [line[3:] for line in runner.output(['git', 'status', '--porcelain'], cwd=candidate).splitlines()
                   if line.strip()]  # 'XY path': the status columns may start with a space
        if changed:
            raise Hold('shared files differ from what the instances declare (make app-sync): ' + ', '.join(changed))


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
    actual = promotion.YamlFile(instance_folder(candidate, app, 'prod') / 'helmrelease.yaml').data
    if promotion.ops.image_reference(promotion.image_of(actual)) != meta.get('image'):
        raise Hold('PR no longer pins the reviewed image')
    if target is None:
        # Production's own credentials (a NATS key, Secret placeholders) cannot be replayed here.
        raise Hold('a first promotion creates production: a person reviews and merges it')
    if promotion.fingerprint(base, app, 'staging', ignore_image=True) != meta.get('source'):
        raise Hold('staging configuration changed after review; review a fresh promotion')
    with tempfile.TemporaryDirectory(prefix='merge-promotion-') as tmp:
        work = Path(tmp) / 'tree'
        shutil.copytree(base, work, ignore=shutil.ignore_patterns('.git'))
        # The promotion replayed on main: the PR must be exactly what it produces. Staging may have a newer
        # build by now; the reviewed image is the one promoted.
        from swhurl.apps import edit
        staging = YamlFile(instance_folder(work, app, 'staging') / 'helmrelease.yaml')
        promotion.image_of(staging.data).update(new.parse_image(meta['image']))
        staging.save()
        with contextlib.redirect_stdout(io.StringIO()):
            edit.promote(work, app, expect_image=meta['image'], runner=runner)
        shutil.copyfile(instance_folder(base, app, 'staging') / 'helmrelease.yaml', staging.path)
        world, _ = model_sync.read(work)
        if world[Instance(app, 'prod')].release.publishes != model_sync.read(base)[0][Instance(app, 'prod')].release.publishes:
            raise Hold('the promotion changes what the app publishes, which changes NATS: a person reviews it')
        produced = sorted(path for path in changed_files(base, work))
        if sorted(files) != produced:
            raise Hold('later promotion changes more than the image and what it brings: '
                       + ', '.join(sorted(set(files) ^ set(produced))))
        for rel in produced:
            if not same_file(candidate / rel, work / rel):
                raise Hold(f'the PR differs from the promotion replayed on main: {rel}')


def same_file(a: Path, b: Path) -> bool:
    """Both absent, or the same bytes or the same YAML documents."""
    if not (a.is_file() and b.is_file()):
        return a.is_file() == b.is_file()
    return a.read_bytes() == b.read_bytes() or yamlio.load_all(a.read_text()) == yamlio.load_all(b.read_text())


def changed_files(base: Path, work: Path) -> list[str]:
    """Files that differ between two trees, by path from their root (no .git)."""
    def files(root: Path) -> dict[str, bytes]:
        return {path.relative_to(root).as_posix(): path.read_bytes() for path in root.rglob('*')
                if path.is_file() and '.git' not in path.relative_to(root).parts}
    before, after = files(base), files(work)
    return [path for path in before.keys() | after.keys() if before.get(path) != after.get(path)]


def eligible(pr: dict, sha: str, repo: str) -> tuple[str, str]:
    match = BRANCH.fullmatch(pr.get('head', {}).get('ref', ''))
    if (pr.get('state') != 'open' or not match or pr.get('base', {}).get('ref') != 'main'
            or pr.get('head', {}).get('sha') != sha
            or pr.get('head', {}).get('repo', {}).get('full_name') != repo
            or 'auto-merge' not in {label['name'] for label in pr.get('labels', [])} or pr.get('draft')):
        raise Hold('PR is held, moved, closed, a fork, or outside the supported console branches')
    return match[1], match[2]


def validated(runner: Runner, sha: str, sleep: Callable[[float], None]) -> None:
    """Wait for Validate on the PR head. The label starts this run beside Validate, and leaving while it
    is still running would cost the next run a second runner start before the merge."""
    waited = 0
    while True:
        runs = runner.json(['gh', 'run', 'list', '--workflow', 'validate.yml', '--commit', sha,
                            '--event', 'pull_request', '--limit', '1', '--json', 'databaseId,status,conclusion'])
        if runs and runs[0].get('status') == 'completed':
            if runs[0].get('conclusion') != 'success':
                raise Hold('Validate has not passed on the current PR head')
            if waited:
                print(f'Validate passed after a {waited} s wait', flush=True)
            return
        if waited >= VALIDATE_WAIT:
            raise Hold('Validate has not passed on the current PR head (still running)')
        if not waited:
            print(f'Waiting up to {VALIDATE_WAIT} s for Validate on {sha[:7]}', flush=True)
        sleep(VALIDATE_POLL)
        waited += VALIDATE_POLL


def merge(runner: Runner, root: Path, number: int, sha: str, repo: str,
          sleep: Callable[[float], None] = time.sleep) -> None:
    pr_path = f'repos/{repo}/pulls/{number}'
    pr = runner.json(['gh', 'api', pr_path])
    action, app = eligible(pr, sha, repo)
    validated(runner, sha, sleep)
    files = runner.output(['gh', 'pr', 'diff', str(number), '--repo', repo, '--name-only']).splitlines()
    runner.run(['git', 'fetch', 'origin', 'main'], cwd=root)
    runner.run(['git', 'fetch', 'origin', f'pull/{number}/head'], cwd=root)
    base_sha = runner.output(['git', 'rev-parse', 'origin/main'], cwd=root).strip()
    if runner.output(['git', 'rev-parse', 'FETCH_HEAD'], cwd=root).strip() != sha:
        raise Hold('PR head changed during fetch')
    with tempfile.TemporaryDirectory(prefix='auto-merge-') as tmp:
        base, candidate = Path(tmp) / 'base', Path(tmp) / 'candidate'
        try:
            runner.run(['git', 'worktree', 'add', '--detach', str(base), base_sha], cwd=root)
            runner.run(['git', 'worktree', 'add', '--detach', str(candidate), base_sha], cwd=root)
            if action == 'new':
                merge_new_app(runner, base, candidate, sha, app, files)
            else:
                runner.run([*GIT, 'merge', '--no-ff', '--no-edit', sha], cwd=candidate)
            scope(base, candidate, files, app, 'prod' if action == 'promote' else 'staging', new_app=action == 'new')
            if action == 'promote':
                check_promotion(base, candidate, app, pr.get('body') or '', files, runner)
            elif instance_folder(base, app, 'staging').exists():
                raise Hold('new-app PR overwrites an existing staging instance')
            else:
                check_new_app(base, candidate, files, app, runner)
            # scope holds both actions to app files, their unit and its registration (and a new app's
            # generated key and derived shared files, checked above). check-repo renders the registered
            # unit; tooling tests read frozen fixtures, and templates/OTel are unchanged.
            runner.run(['make', *MERGE_CHECKS], cwd=candidate)
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
            # The reason, where the console's Activity reads it: one line, already redacted.
            said = ' '.join(reason.split())[:REFUSAL_LIMIT]
            runner.run(['gh', 'api', '--method', 'POST', f'repos/{repo}/issues/{number}/comments', '--input', '-'],
                       input=json.dumps({'body': f'{REFUSAL}{said}\n\nReview the changes and merge this yourself, or close it '
                                                 'and review a fresh promotion.'}), mutating=True, check=False)
        return 1
    return 0
