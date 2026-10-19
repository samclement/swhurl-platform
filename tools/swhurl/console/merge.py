"""The merge gate: merge an eligible console PR onto the newest main, check the result and push it.

A promotion PR carries only its app's records: the gate merges it onto the newest main, applies
promotion's one rule (promotion.held), renders with app-sync, checks and pushes one commit, merging
again when main moved first. A new-app PR is replayed on the newest main (replay_new_app) and compared
with its request. A PR the gate holds loses its auto-merge label and gets a comment saying why; any
other failure keeps the label, so the next run tries again.
"""
from __future__ import annotations

import contextlib
import io
import json
import os
import re
import shutil
import sys
import tempfile
import time
from collections.abc import Callable
from pathlib import Path

from swhurl import ROOT, yamlio
from swhurl.apps import declaration, egress, events, helmrelease, new, promotion
from swhurl.apps.instance import instance_folder, instance_unit_file
from swhurl.apps.model.records import APP_FILE, RELEASE_FILE, SETTINGS_FILE
from swhurl.apps.yaml_file import EditError
from swhurl.run import Runner

VALIDATE_WAIT = 120  # seconds this run waits for Validate on the PR head, asking every VALIDATE_POLL
VALIDATE_POLL = 5
MERGE_CHECKS = ['-j2', '--output-sync=target', 'check-repo', 'check-apps']
ATTEMPTS = 3  # a promotion is merged again on a newer main this many times when its push loses a race
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
    """A person merges this PR: the gate removes its label and comments the reason."""


class Skip(Exception):
    """Not this run's to merge (held, not yet validated, moved): nothing is said on the PR."""


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


def replay_new_app(base: Path, head: Path, work: Path, app: str, body: str, files: list[str],
                   opener: declaration.Opener = declaration._open) -> None:
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
        problem = new.platform_origin_problem(new.parse_args(argv, opener))
        code = 2 if problem else new.main([*argv, '--root', str(work), '--no-policy-check', *extra], opener)
    if code:
        reason = problem or ([line for line in said.getvalue().splitlines() if line.strip()] or ['it failed'])[-1]
        raise Hold(f'the request does not replay on main: {reason}')


def scope(base: Path, head: Path, work: Path, files: list[str], app: str, env: str) -> None:
    """The replay may change only this instance, its unit and registration and the shared files derived
    from every instance: a change to another app is that app's team's to review. The PR's own files must
    be what the replay made; its copies of the shared files are made again (the replay wins)."""
    folder, unit = f'apps/{app}/{env}/', instance_unit_file(Path(), app, env).as_posix()

    def own(path: str) -> bool:
        return path.startswith(folder) or path in (unit, f'apps/{app}/{APP_FILE}')
    changed = changed_files(base, work)
    others = sorted(path for path in changed if not own(path) and path not in (REGISTRY, *DERIVED))
    if others:
        raise Hold('it also changes ' + ', '.join(others[:5]) + ': a person reviews it')
    extra = sorted(path for path in files if not own(path) and path not in (REGISTRY, *DERIVED))
    if extra:
        raise Hold('the PR changes files its request does not: ' + ', '.join(extra[:5]))
    for path in sorted(set(changed) | set(files)):
        if own(path) and not same_file(head / path, work / path, reallocated=path.endswith(('/helmrelease.yaml', f'/{SETTINGS_FILE}'))):
            raise Hold(f'the PR differs from what its request makes on main: {path}')
    made = sorted(path for path in changed if path.endswith('.sops.yaml') and path != f'{folder}{events.SECRET_FILE}')
    if made:
        raise Hold(f'the request makes an encrypted file, which a person reviews: {", ".join(made)}')


def same_file(a: Path, b: Path, *, reallocated: bool = False) -> bool:
    """Both absent, or the same bytes or the same YAML documents. ``reallocated``: a HelmRelease or Settings
    record whose egress port the replay may have chosen again (another instance took the PR's meanwhile)."""
    if not (a.is_file() and b.is_file()):
        return a.is_file() == b.is_file()
    if a.read_bytes() == b.read_bytes():
        return True
    def documents(path: Path) -> list:
        found = list(yamlio.load_all(path.read_text()))
        for doc in found if reallocated else ():
            if isinstance(doc, dict) and doc.get('kind') == 'HelmRelease':
                (doc.get('metadata', {}).get('annotations') or {}).pop('platform.swhurl.com/render', None)
            if isinstance(doc, dict) and isinstance(doc.get('allocations'), dict):  # instance.yaml
                doc['allocations'].pop('egressPort', None)
            elif isinstance(doc, dict):
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
        raise Skip('PR is held, moved, closed, a fork, or outside the supported console branches')
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
                raise Skip('Validate has not passed on the current PR head')
            if waited:
                print(f'Validate passed after a {waited} s wait', flush=True)
            return
        if waited >= VALIDATE_WAIT:
            raise Skip('Validate has not passed on the current PR head (still running)')
        if not waited:
            print(f'Waiting up to {VALIDATE_WAIT} s for Validate on {sha[:7]}', flush=True)
        sleep(VALIDATE_POLL)
        waited += VALIDATE_POLL


def fetch(runner: Runner, root: Path, number: int, sha: str) -> str:
    """Fetch main and the PR head (separately: together, FETCH_HEAD is main's); main's commit."""
    runner.run(['git', 'fetch', 'origin', 'main'], cwd=root)
    runner.run(['git', 'fetch', 'origin', f'pull/{number}/head'], cwd=root)
    base = runner.output(['git', 'rev-parse', 'origin/main'], cwd=root).strip()
    if runner.output(['git', 'rev-parse', 'FETCH_HEAD'], cwd=root).strip() != sha:
        raise Skip('PR head changed during fetch')
    return base


def checked(runner: Runner, work: Path) -> None:
    """check-repo renders the registered units; tooling tests read frozen fixtures, and templates and
    OTel are unchanged by a console PR. A failure is the merge result's, which a person looks at."""
    done = runner.run(['make', *MERGE_CHECKS], cwd=work, check=False)
    if done.returncode:
        said = [line for line in (done.stdout + done.stderr).splitlines() if line.strip()] or ['it failed']
        raise Hold(f'the merged result fails make check-repo check-apps: {runner.redact(said[-1])}')


def commit(runner: Runner, work: Path, base: str, sha: str, number: int, pr: dict) -> str:
    """The tree in ``work`` as a merge of the PR head into ``base``: GitHub shows the PR merged."""
    runner.run(['git', 'add', '-A'], cwd=work)
    tree = runner.output(['git', 'write-tree'], cwd=work).strip()
    return runner.output([*GIT, 'commit-tree', tree, '-p', base, '-p', sha,
                          '-m', f'Merge pull request #{number}: {pr.get("title", "")}'], cwd=work).strip()


def merge(runner: Runner, root: Path, number: int, sha: str, repo: str,
          sleep: Callable[[float], None] = time.sleep, opener: declaration.Opener = declaration._open) -> None:
    pr_path = f'repos/{repo}/pulls/{number}'
    pr = runner.json(['gh', 'api', pr_path])
    action, app = eligible(pr, sha, repo)
    validated(runner, sha, sleep)
    files = runner.output(['gh', 'pr', 'diff', str(number), '--repo', repo, '--name-only']).splitlines()
    if action == 'promote':
        merge_promotion(runner, root, number, sha, repo, pr, app, files)
    else:
        merge_new_app(runner, root, number, sha, repo, pr, app, files, opener)
    runner.run(['git', 'push', 'origin', '--delete', pr['head']['ref']], cwd=root, mutating=True, check=False)


def staged(runner: Runner, work: Path, app: str, digest: str) -> bool:
    """Whether staging ran ``digest``: it is in the history of staging's Release record on main."""
    path = instance_folder(Path(), app, 'staging') / RELEASE_FILE
    found = runner.run(['git', 'log', f'-S{digest}', '--format=%H', 'HEAD', '--', path.as_posix()], cwd=work, check=False)
    return not found.returncode and bool(found.stdout.strip())


def merge_promotion(runner: Runner, root: Path, number: int, sha: str, repo: str, pr: dict, app: str,
                    files: list[str]) -> None:
    """Merge the PR's records onto the newest main, render, check, apply promotion's rule and push; a push
    that loses to another writer is merged again on the newer main (ATTEMPTS times)."""
    if other := sorted(path for path in files if not promotion.records(app, path)):
        raise Hold(f"it changes {', '.join(other[:5])}, which are not {app}'s promotion records: a person reviews it")
    for attempt in range(1, ATTEMPTS + 1):
        base = fetch(runner, root, number, sha)
        with tempfile.TemporaryDirectory(prefix='auto-merge-') as tmp:
            work = Path(tmp) / 'work'
            runner.run(['git', 'worktree', 'add', '--detach', str(work), base], cwd=root)
            try:
                try:
                    before = promotion.release_of(work, app, 'prod')
                except EditError as error:
                    raise Hold(f"main's production release record cannot be read: {error}") from None
                merged = runner.run([*GIT, 'merge', '--no-ff', '--no-commit', sha], cwd=work, check=False)
                if merged.returncode:
                    raise Hold('it conflicts with main: close it and promote again from the console')
                try:
                    after = promotion.release_of(work, app, 'prod')
                except EditError as error:
                    raise Hold(str(error)) from None
                if reason := promotion.held(app, files, before, after, lambda digest, work=work: staged(runner, work, app, digest)):
                    raise Hold(f'it {reason}: a person reviews it')
                synced = runner.run([sys.executable, '-m', 'swhurl', 'app-sync'], cwd=work, check=False,
                                    env={'PYTHONPATH': str(work / 'tools'), 'PYTHONDONTWRITEBYTECODE': '1'})
                if synced.returncode:
                    said = [line for line in (synced.stdout + synced.stderr).splitlines() if line.strip()] or ['it failed']
                    raise Hold(f'main does not render it: {runner.redact(said[-1])}')
                checked(runner, work)
                # Recheck the person's hold and the exact PR head after the checks.
                eligible(runner.json(['gh', 'api', f'repos/{repo}/pulls/{number}']), sha, repo)
                made = commit(runner, work, base, sha, number, pr)
                # Not forced: a main that moved since the fetch refuses it, and the merge is made again.
                pushed = runner.run(['git', 'push', 'origin', f'{made}:refs/heads/main'], cwd=work, mutating=True, check=False)
            finally:
                runner.run(['git', 'worktree', 'remove', '--force', str(work)], cwd=root, check=False)
        if not pushed.returncode:
            return
        print(f'#{number}: main moved before the push (attempt {attempt} of {ATTEMPTS}); merging again', flush=True)
    raise RuntimeError(f'main kept moving: {ATTEMPTS} pushes lost; the next run tries again')


def merge_new_app(runner: Runner, root: Path, number: int, sha: str, repo: str, pr: dict, app: str,
                  files: list[str], opener: declaration.Opener) -> None:
    base_sha = fetch(runner, root, number, sha)
    with tempfile.TemporaryDirectory(prefix='auto-merge-') as tmp:
        base, head, work = Path(tmp) / 'base', Path(tmp) / 'candidate', Path(tmp) / 'work'
        try:
            for path, revision in ((base, base_sha), (head, sha), (work, base_sha)):
                runner.run(['git', 'worktree', 'add', '--detach', str(path), revision], cwd=root)
            replay_new_app(base, head, work, app, pr.get('body') or '', files, opener)
            scope(base, head, work, files, app, 'staging')
            checked(runner, work)
            # Recheck the human's hold and the exact PR head after the potentially long validation.
            eligible(runner.json(['gh', 'api', f'repos/{repo}/pulls/{number}']), sha, repo)
            current = runner.output(['git', 'ls-remote', 'origin', 'refs/heads/main'], cwd=root).split()[0]
            if current != base_sha:
                raise Skip('main moved during validation; the next run replays it on the new base')
            # Fast-forward rejects a concurrent main push (the next run replays again). No force push.
            made = commit(runner, work, base_sha, sha, number, pr)
            runner.run(['git', 'push', 'origin', f'{made}:refs/heads/main'], cwd=work, mutating=True)
        finally:
            for path in (work, head, base):
                runner.run(['git', 'worktree', 'remove', '--force', str(path)], cwd=root, check=False)


def refuse(runner: Runner, repo: str, number: int, reason: str) -> None:
    """Hand the PR to a person: remove its label and leave the reason, one redacted line, where the
    console's Activity reads it."""
    runner.run(['gh', 'api', '--method', 'DELETE', f'repos/{repo}/issues/{number}/labels/auto-merge'],
               mutating=True, check=False)
    said = ' '.join(reason.split())[:REFUSAL_LIMIT]
    runner.run(['gh', 'api', '--method', 'POST', f'repos/{repo}/issues/{number}/comments', '--input', '-'],
               input=json.dumps({'body': f'{REFUSAL}{said}\n\nReview the changes and merge this yourself, or close it '
                                         'and start again from the console.'}), mutating=True, check=False)


def main(argv=None, runner: Runner | None = None) -> int:
    """Merge the eligible console PRs one at a time. A hold or a failure of one PR is reported and the
    queue goes on; the run fails at the end if any PR was held or failed."""
    import argparse
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('number', type=int, nargs='?')
    parser.add_argument('sha', nargs='?')
    args = parser.parse_args(argv)
    if (args.number is None) != (args.sha is None):
        parser.error('provide both PR number and SHA, or neither to process the eligible queue')
    runner = runner or Runner()
    runner.add_secret(os.environ.get('GH_TOKEN'))
    repo = os.environ.get('GITHUB_REPOSITORY', '')
    if not repo:
        print('Not merging console PRs: GITHUB_REPOSITORY is not set')
        return 1
    try:
        queue = [{'number': args.number, 'headRefOid': args.sha, 'headRefName': 'console/'}] if args.number is not None \
            else runner.json(['gh', 'pr', 'list', '--state', 'open', '--label', 'auto-merge',
                              '--json', 'number,headRefOid,headRefName'])
    except Exception as error:  # noqa: BLE001 - reported, and the run fails
        print(f'Not merging console PRs: {runner.redact(str(error))}')
        return 1
    failed = False
    for item in queue:
        if not item['headRefName'].startswith('console/'):
            continue
        number = item['number']
        try:
            merge(runner, ROOT, number, item['headRefOid'], repo)
        except Skip as error:
            print(f'Not merging #{number}: {error}')
        except Hold as error:
            reason = runner.redact(str(error))
            print(f'Not merging #{number}: {reason}')
            refuse(runner, repo, number, reason)
            failed = True
        except Exception as error:  # noqa: BLE001 - one PR's failure keeps its label; the queue goes on
            print(f'Could not merge #{number} ({type(error).__name__}), the next run tries again: {runner.redact(str(error))}')
            failed = True
    return 1 if failed else 0
