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
REQUEST = re.compile(r'<!-- swhurl-request (\{[^\n]*\}) -->')
"""What a console PR asks for, in its body: the commands its files came from (no standard input), which
this gate replays on the newest main."""
GATE_FLAGS = ('--root', '--manifest', '--no-register', '--no-policy-check', '--policy-check', '--events-key')
"""app-new options a request may not set: where it writes, what it checks and the prepared key are the gate's."""
PUBLIC_KEY = re.compile(r'U[A-Z2-7]{55}')  # an nkeys user public key
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


def recipients(root: Path, path: str) -> set[str]:
    """The age recipients .sops.yaml's first rule matching ``path`` encrypts to."""
    config = root / '.sops.yaml'
    for rule in (yamlio.load(config.read_text()) if config.is_file() else {}).get('creation_rules') or []:
        if re.search(rule.get('path_regex', ''), path):
            return {r.strip() for r in str(rule.get('age', '')).split(',') if r.strip()}
    return set()


def request_marker(commands) -> str:
    """The marker a console PR's body carries for its commands (each with ``name`` and ``argv``)."""
    return '<!-- swhurl-request ' + json.dumps({'commands': [[c.name, *c.argv] for c in commands]}) + ' -->'


def request(body: str) -> list[list[str]]:
    """The commands a console PR says its files came from (changes.request_marker)."""
    match = REQUEST.search(body)
    try:
        commands = json.loads(match[1])['commands'] if match else None
    except (TypeError, ValueError, KeyError):
        commands = None
    if not (isinstance(commands, list) and commands
            and all(isinstance(c, list) and c and all(isinstance(part, str) for part in c) for c in commands)):
        raise Hold('the PR does not say what it was made with (no request); open a fresh one from the console')
    return commands


def check_events_key(base: Path, head: Path, app: str) -> str:
    """A new app's events key, the one input the gate cannot make again: a new Secret holding its NATS seed
    and nothing else, encrypted to this repository's recipients alone (no one, gate or reviewer, can read
    it). Returns the public key the PR pairs it with."""
    key = f'apps/{app}/staging/{events.SECRET_FILE}'
    if (base / key).exists():
        raise Hold('new-app PR replaces an existing events key')
    doc = yamlio.load((head / key).read_text()) or {}
    sops = doc.get('sops') or {}
    values = doc.get('stringData') or {}
    if (doc.get('kind') != 'Secret' or 'data' in doc
            or doc.get('metadata') != {'name': events.secret_name(app), 'namespace': f'{app}-staging'}
            or set(values) != {events.SEED_VARIABLE} or not str(values[events.SEED_VARIABLE]).startswith('ENC[')
            or {r.get('recipient') for r in sops.get('age') or []} != recipients(base, key)
            or any(sops.get(other) for other in ('kms', 'gcp_kms', 'azure_kv', 'hc_vault', 'pgp'))):
        raise Hold("the events key is not the app's generated, encrypted NATS seed alone")
    release = yamlio.load((head / f'apps/{app}/staging/helmrelease.yaml').read_text())
    public = str(helmrelease.variables(release).get(events.NKEY_VARIABLE, ''))
    if not PUBLIC_KEY.fullmatch(public):
        raise Hold("the PR pairs its events key with no NATS public key")
    return public


def replay_new_app(base: Path, head: Path, work: Path, app: str, body: str, files: list[str]) -> None:
    """The new app's request, run with main's tooling on the newest main (``work``): its egress port, unit
    registration and the shared files are made there, whatever main gained since the PR was opened. Its
    events key is the PR's own (check_events_key)."""
    commands = request(body)
    if len(commands) != 1 or commands[0][:2] != ['app-new', app]:
        raise Hold('a new-app PR is one make app-new of its own app')
    argv = commands[0][1:]
    if any(arg.split('=')[0] in GATE_FLAGS for arg in argv):
        raise Hold('the request sets options only the gate may set')
    if instance_folder(base, app, 'staging').exists():
        raise Hold('new-app PR overwrites an existing staging instance')
    extra = []
    key = f'apps/{app}/staging/{events.SECRET_FILE}'
    if key in files:
        extra = ['--events-key', check_events_key(base, head, app)]
        (work / key).parent.mkdir(parents=True)
        shutil.copyfile(head / key, work / key)
    said = io.StringIO()
    with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(said):
        problem = new.platform_origin_problem(new.parse_args(argv))
        code = 2 if problem else new.main([*argv, '--root', str(work), '--no-policy-check', *extra])
    if code:
        reason = problem or ([line for line in said.getvalue().splitlines() if line.strip()] or ['it failed'])[-1]
        raise Hold(f'the request does not replay on main: {reason}')


def replay_promotion(base: Path, head: Path, work: Path, app: str, body: str, runner: Runner) -> None:
    """A later promotion of the reviewed image, run with main's tooling on the newest main (``work``)."""
    from swhurl.apps import edit
    from swhurl.console.promotion import inputs_hash

    meta = review_metadata(body)
    if meta.get('app') != app or meta.get('setup'):
        raise Hold('promotion needs setup or a fresh review')
    if promotion.fingerprint(base, app, 'prod') != meta.get('target'):
        raise Hold('production changed after review; review a fresh promotion')
    if inputs_hash(base) != meta.get('inputs'):
        raise Hold('promotion tooling, policy, chart or platform settings changed; review again')
    _, target = promotion.read(base, app)
    if target is None:
        # Production's own credentials (a NATS key, Secret placeholders) cannot be replayed here.
        raise Hold('a first promotion creates production: a person reviews and merges it')
    if promotion.fingerprint(base, app, 'staging', ignore_image=True) != meta.get('source'):
        raise Hold('staging configuration changed after review; review a fresh promotion')
    # Staging may have a newer build by now; the reviewed image is the one promoted.
    staging = YamlFile(instance_folder(work, app, 'staging') / 'helmrelease.yaml')
    promotion.image_of(staging.data).update(new.parse_image(meta['image']))
    staging.save()
    with contextlib.redirect_stdout(io.StringIO()):
        edit.promote(work, app, expect_image=meta['image'], runner=runner)
    shutil.copyfile(instance_folder(base, app, 'staging') / 'helmrelease.yaml', staging.path)
    if model_sync.read(work)[0][Instance(app, 'prod')].release.publishes != model_sync.read(base)[0][Instance(app, 'prod')].release.publishes:
        raise Hold('the promotion changes what the app publishes, which changes NATS: a person reviews it')


def scope(base: Path, head: Path, work: Path, files: list[str], app: str, env: str) -> None:
    """The replay may change only this instance, its unit and registration and the shared files derived
    from every instance: a change to another app is that app's team's to review. The PR's own files must
    be what the replay made; its copies of the shared files are made again (the replay wins)."""
    folder, unit = f'apps/{app}/{env}/', instance_unit_file(Path(), app, env).as_posix()

    def own(path: str) -> bool:
        return path.startswith(folder) or path == unit
    changed = changed_files(base, work)
    others = sorted(path for path in changed if not own(path) and path not in (REGISTRY, *DERIVED))
    if others:
        raise Hold('it also changes ' + ', '.join(others[:5]) + ': a person reviews it')
    extra = sorted(path for path in files if not own(path) and path not in (REGISTRY, *DERIVED))
    if extra:
        raise Hold('the PR changes files its request does not: ' + ', '.join(extra[:5]))
    for path in sorted(set(changed) | set(files)):
        if own(path) and not same_file(head / path, work / path, reallocated=path.endswith('/helmrelease.yaml')):
            raise Hold(f'the PR differs from what its request makes on main: {path}')
    made = sorted(path for path in changed if path.endswith('.sops.yaml') and path != f'{folder}{events.SECRET_FILE}')
    if made:
        raise Hold(f'the request makes an encrypted file, which a person reviews: {", ".join(made)}')


def same_file(a: Path, b: Path, *, reallocated: bool = False) -> bool:
    """Both absent, or the same bytes or the same YAML documents. ``reallocated``: a HelmRelease whose egress
    port the replay may have chosen again (another instance took the PR's while it was open)."""
    if not (a.is_file() and b.is_file()):
        return a.is_file() == b.is_file()
    if a.read_bytes() == b.read_bytes():
        return True
    def documents(path: Path) -> list:
        found = list(yamlio.load_all(path.read_text()))
        for doc in found if reallocated else ():
            for key in PROXY_VARIABLES:
                (helmrelease.container(doc).get('env') or {}).pop(key, None)
        return found
    return documents(a) == documents(b)


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
        base, head, work = Path(tmp) / 'base', Path(tmp) / 'candidate', Path(tmp) / 'work'
        try:
            for path, revision in ((base, base_sha), (head, sha), (work, base_sha)):
                runner.run(['git', 'worktree', 'add', '--detach', str(path), revision], cwd=root)
            body = pr.get('body') or ''
            if action == 'new':
                replay_new_app(base, head, work, app, body, files)
            else:
                replay_promotion(base, head, work, app, body, runner)
            scope(base, head, work, files, app, 'staging' if action == 'new' else 'prod')
            # check-repo renders the registered units; tooling tests read frozen fixtures, and templates and
            # OTel are unchanged.
            runner.run(['make', *MERGE_CHECKS], cwd=work)
            # Recheck the human's hold and the exact PR head after the potentially long validation.
            eligible(runner.json(['gh', 'api', pr_path]), sha, repo)
            current = runner.output(['git', 'ls-remote', 'origin', 'refs/heads/main'], cwd=root).split()[0]
            if current != base_sha:
                raise Hold('main moved during validation; retry against the new base')
            # The replayed tree, as a merge of the PR head: GitHub shows the PR merged. Fast-forward rejects
            # a concurrent main push (the next run replays again). No force push, no unvalidated tree.
            runner.run(['git', 'add', '-A'], cwd=work)
            tree = runner.output(['git', 'write-tree'], cwd=work).strip()
            commit = runner.output([*GIT, 'commit-tree', tree, '-p', base_sha, '-p', sha,
                                    '-m', f'Merge pull request #{number}: {pr.get("title", "")}'], cwd=work).strip()
            runner.run(['git', 'push', 'origin', f'{commit}:refs/heads/main'], cwd=work, mutating=True)
            runner.run(['git', 'push', 'origin', '--delete', pr['head']['ref']], cwd=root, mutating=True, check=False)
        finally:
            for path in (work, head, base):
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
