"""The merge gate: merge an eligible console PR onto the newest main, check the result and push it.

Promotion and new-app PRs carry only their input records. The gate merges them onto the newest
main, applies the action's rule, renders with app-sync, checks and pushes one commit, merging again
when main moved first. A PR the gate holds loses its auto-merge label and gets a comment saying why;
any other failure keeps the label, so the next run tries again.
"""
from __future__ import annotations

import json
import os
import re
import sys
import tempfile
import time
from collections.abc import Callable
from pathlib import Path

import yaml

from swhurl import ROOT, yamlio
from swhurl.apps import events, promotion
from swhurl.apps.calling import NOTIFY
from swhurl.apps.instance import instance_folder, instance_unit_file
from swhurl.apps.model.records import APP_FILE, RELEASE_FILE, SETTINGS_FILE, App, RecordError, Release, Settings
from swhurl.apps.yaml_file import EditError
from swhurl.run import CommandError, Runner

VALIDATE_WAIT = 120  # seconds this run waits for Validate on the PR head, asking every VALIDATE_POLL
VALIDATE_POLL = 5
MERGE_CHECKS = ['-j2', '--output-sync=target', 'check-repo', 'check-apps']
ATTEMPTS = 3  # a PR is merged again on a newer main this many times when its push loses a race
REFUSAL = 'Auto-merge refused: '  # starts the comment the gate leaves on a PR it holds; the console shows the rest
REFUSAL_LIMIT = 300
BRANCH = re.compile(r'console/(new|promote)-([a-z][a-z0-9-]*)-staging-[0-9a-f]{7,40}')
PUBLIC_KEY = re.compile(r'U[A-Z2-7]{55}')  # an nkeys user public key
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


def new_app_records(app: str, path: str) -> bool:
    """The only inputs a new-app PR may add: its App, staging records, offers and events key."""
    folder = f'apps/{app}/staging/'
    return path == f'apps/{app}/{APP_FILE}' or (path.startswith(folder) and (
        path.removeprefix(folder) in (RELEASE_FILE, SETTINGS_FILE, events.SECRET_FILE)
        or path.startswith(f'{folder}offers/')))


def check_events_key(root: Path, app: str, settings: Settings) -> None:
    """A new encrypted Secret containing only the NATS seed, to the repository's recipients alone.
    The public half is an allocation in instance.yaml; neither half is made again by the gate."""
    key = f'apps/{app}/staging/{events.SECRET_FILE}'
    try:
        doc = yamlio.load((root / key).read_text())
        if not isinstance(doc, dict):
            raise ValueError('not a Secret')
        sops, values = doc.get('sops'), doc.get('stringData')
        wanted = recipients(root, key)
        if (doc.get('apiVersion') != 'v1' or doc.get('kind') != 'Secret' or 'data' in doc
                or doc.get('metadata') != {'name': events.secret_name(app), 'namespace': f'{app}-staging'}
                or not isinstance(values, dict) or set(values) != {events.SEED_VARIABLE}
                or not isinstance(values[events.SEED_VARIABLE], str) or not values[events.SEED_VARIABLE].startswith('ENC[')
                or not isinstance(sops, dict) or not wanted
                or not isinstance(sops.get('age'), list)
                or any(not isinstance(r, dict) or not isinstance(r.get('recipient'), str) for r in sops['age'])
                or {r.get('recipient') for r in sops['age']} != wanted
                or any(sops.get(other) for other in ('kms', 'gcp_kms', 'azure_kv', 'hc_vault', 'pgp'))):
            raise ValueError('not the seed alone')
    except (OSError, ValueError, yaml.YAMLError):
        raise Hold("the events key is not the app's generated, encrypted NATS seed alone") from None
    if not PUBLIC_KEY.fullmatch(settings.allocations.events_key):
        raise Hold('the PR pairs its events key with no NATS public key')


def check_new_app(work: Path, app: str) -> None:
    """Required, strict records; no port chosen on the PR branch and no grants to other apps."""
    if app == NOTIFY:
        raise Hold(f'{NOTIFY} is reserved for the platform notification service')
    folder = instance_folder(work, app, 'staging')
    try:
        App.parse(yamlio.load((folder.parent / APP_FILE).read_text()))
        release = Release.parse(yamlio.load((folder / RELEASE_FILE).read_text()))
        settings = Settings.parse(yamlio.load((folder / SETTINGS_FILE).read_text()))
    except (OSError, RecordError, yaml.YAMLError) as error:
        raise Hold(f'the new app records cannot be read: {error}') from None
    if settings.exceptions:
        raise Hold('it sets policy exceptions: a person reviews it')
    if settings.allocations.egress_port is not None:
        raise Hold('a new-app PR must not preallocate an egress port')
    if settings.calls or settings.subscriptions:
        raise Hold('it calls or subscribes to another app: a person reviews it')
    if events.has_nats(work) or release.publishes or settings.allocations.events_key or (folder / events.SECRET_FILE).exists():
        check_events_key(work, app, settings)


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
    OTel are unchanged by a console PR. A failed command keeps the label so a later run can retry."""
    runner.run(['make', *MERGE_CHECKS], cwd=work)


def commit(runner: Runner, work: Path, base: str, sha: str, number: int, pr: dict) -> str:
    """The tree in ``work`` as a merge of the PR head into ``base``: GitHub shows the PR merged."""
    runner.run(['git', 'add', '-A'], cwd=work)
    tree = runner.output(['git', 'write-tree'], cwd=work).strip()
    return runner.output([*GIT, 'commit-tree', tree, '-p', base, '-p', sha,
                          '-m', f'Merge pull request #{number}: {pr.get("title", "")}'], cwd=work).strip()


def merge(runner: Runner, root: Path, number: int, sha: str, repo: str,
          sleep: Callable[[float], None] = time.sleep) -> None:
    pr_path = f'repos/{repo}/pulls/{number}'
    pr = runner.json(['gh', 'api', pr_path])
    action, app = eligible(pr, sha, repo)
    validated(runner, sha, sleep)
    files = runner.output(['gh', 'pr', 'diff', str(number), '--repo', repo, '--name-only']).splitlines()
    merge_records(runner, root, number, sha, repo, pr, action, app, files)
    runner.run(['git', 'push', 'origin', '--delete', pr['head']['ref']], cwd=root, mutating=True, check=False)


def staged(runner: Runner, work: Path, app: str, release: Release) -> bool:
    """Whether main selected this exact Release for staging, including changes that kept its digest
    (a refreshed declaration). A digest in a comment or a retagged rollback cannot authenticate it.
    Follow only main's first-parent history; failed Git reads propagate instead of becoming a hold."""
    path = (instance_folder(Path(), app, 'staging') / RELEASE_FILE).as_posix()
    revisions = runner.output(['git', 'log', '--first-parent', '--diff-filter=AM',
                               '--format=%H', 'HEAD', '--', path], cwd=work).splitlines()
    for revision in revisions:
        text = runner.output(['git', 'show', f'{revision}:{path}'], cwd=work)
        selected = Release.parse(yamlio.load(text), f'{app}/staging at {revision}')
        if selected == release:
            return True
    return False


def merge_records(runner: Runner, root: Path, number: int, sha: str, repo: str, pr: dict, action: str,
                  app: str, files: list[str]) -> None:
    """Merge the PR's records onto the newest main, apply the action's rule, render, check and push; a push
    that loses to another writer is merged again on the newer main (ATTEMPTS times)."""
    kind = 'promotion' if action == 'promote' else 'new-app'
    owns = promotion.records if action == 'promote' else new_app_records
    if other := sorted(path for path in files if not owns(app, path)):
        raise Hold(f"it changes {', '.join(other[:5])}, which are not {app}'s {kind} records: a person reviews it")
    for attempt in range(1, ATTEMPTS + 1):
        base = fetch(runner, root, number, sha)
        with tempfile.TemporaryDirectory(prefix='auto-merge-') as tmp:
            work = Path(tmp) / 'work'
            runner.run(['git', 'worktree', 'add', '--detach', str(work), base], cwd=root)
            try:
                try:
                    before = promotion.release_of(work, app, 'prod') if action == 'promote' else None
                    if action == 'new' and ((work / 'apps' / app).exists() or any(
                            instance_unit_file(work, app, env).exists() for env in ('staging', 'prod'))):
                        raise Hold('new-app PR overwrites an existing app or instance')
                except EditError as error:
                    raise Hold(f"main's production release record cannot be read: {error}") from None
                merged = runner.run([*GIT, 'merge', '--no-ff', '--no-commit', sha], cwd=work, check=False)
                if merged.returncode:
                    conflicts = runner.output(['git', 'diff', '--name-only', '--diff-filter=U'], cwd=work)
                    if conflicts.strip():
                        raise Hold(f'it conflicts with main: close it and {"promote" if action == "promote" else "create it"} again from the console')
                    raise CommandError(merged.args, runner.redact(merged.stderr or merged.stdout), merged.returncode)
                if action == 'new':
                    check_new_app(work, app)
                else:
                    try:
                        after = promotion.release_of(work, app, 'prod')
                    except EditError as error:
                        raise Hold(str(error)) from None
                    if reason := promotion.held(app, files, before, after, lambda release, work=work: staged(runner, work, app, release)):
                        raise Hold(f'it {reason}: a person reviews it')
                runner.run([sys.executable, '-m', 'swhurl', 'app-sync'], cwd=work,
                           env={'PYTHONPATH': str(work / 'tools'), 'PYTHONDONTWRITEBYTECODE': '1'})
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
