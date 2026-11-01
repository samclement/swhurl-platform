"""How a bot pushes to main: Render, Deploy, the console's merge gate and Validate's console image pin
(docs/architecture.md#how-changes-reach-the-cluster).

``push_main`` starts from the newest main in a fresh worktree, lets ``change`` edit it, checks the result,
commits the whole tree and pushes without force. A push lost to another writer starts again from the
newer main; anything ``change``, the checks or ``ready`` raise ends it with nothing pushed. The
``bot-render`` and ``bot-deploy`` commands are the Render and Deploy workflows' one step each.
"""
from __future__ import annotations

import argparse
import os
import re
import sys
import tempfile
from collections.abc import Callable, Sequence
from dataclasses import dataclass
from pathlib import Path

from swhurl import ROOT
from swhurl.run import CommandError, Runner

CHECKS = ['-j2', '--output-sync=target', 'check-repo', 'check-apps']
CHECKS_V2 = ['check-v2']
"""What a v2 deploy must pass before it is pushed: v2's Go rules and the platform files the records render. v2's
own compiled output is what the deploy itself wrote, so there is no v1 record to compare it with."""
ATTEMPTS = 5  # Render and Deploy; the merge gate makes its own number of attempts


@dataclass(frozen=True)
class Commit:
    """What ``change`` made: the commit message, and parents after main (a merged PR's head)."""
    message: str
    parents: tuple[str, ...] = ()


Change = Callable[[Path, str], Commit | None]  # (worktree, main's commit) -> what to commit, or None: nothing to push


def swhurl(runner: Runner, work: Path, *args: str, check: bool = True):
    """A swhurl command from the worktree's own tooling, run in the worktree."""
    return runner.run([sys.executable, '-m', 'swhurl', *args], cwd=work, check=check,
                      env={'PYTHONPATH': str(work / 'tools'), 'PYTHONDONTWRITEBYTECODE': '1'})


def checked(runner: Runner, work: Path, checks: Sequence[str] = CHECKS) -> None:
    """The checks the tree must pass: check-repo renders the registered units, check-apps compiles v2's every record
    and compares it with the files Git holds, and check-v2 holds v2's Go rules. A failure shows the
    whole output: CI has no other copy of it."""
    result = runner.run(['make', *checks], cwd=work, check=False)
    if result.returncode:
        print(runner.redact(result.stdout + result.stderr), file=sys.stderr, flush=True)
        raise CommandError(result.args, f'make {" ".join(checks)} exited {result.returncode}', result.returncode)


def push_main(runner: Runner, root: Path, change: Change, *, attempts: int = ATTEMPTS,
              ready: Callable[[], None] = lambda: None, git: Sequence[str] = ('git',),
              checks: Sequence[str] = CHECKS) -> str | None:
    """The pushed commit, or None when ``change`` found nothing to push. ``ready`` runs after the checks,
    just before the commit; ``git`` may carry an identity for the commit."""
    for attempt in range(1, attempts + 1):
        runner.run(['git', 'fetch', 'origin', 'main'], cwd=root)
        base = runner.output(['git', 'rev-parse', 'origin/main'], cwd=root).strip()
        with tempfile.TemporaryDirectory(prefix='bot-') as tmp:
            work = Path(tmp) / 'work'
            runner.run(['git', 'worktree', 'add', '--detach', str(work), base], cwd=root)
            try:
                commit = change(work, base)
                if commit is None:
                    return None
                checked(runner, work, checks)
                ready()
                runner.run(['git', 'add', '-A'], cwd=work)
                tree = runner.output(['git', 'write-tree'], cwd=work).strip()
                parents = [arg for parent in (base, *commit.parents) for arg in ('-p', parent)]
                made = runner.output([*git, 'commit-tree', tree, *parents, '-m', commit.message], cwd=work).strip()
                # Not forced: a main that moved since the fetch refuses it, and the change is made again.
                pushed = runner.run(['git', 'push', 'origin', f'{made}:refs/heads/main'], cwd=work, mutating=True, check=False)
            finally:
                runner.run(['git', 'worktree', 'remove', '--force', str(work)], cwd=root, check=False)
        if not pushed.returncode:
            return made
        print(f'main moved before the push (attempt {attempt} of {attempts}); trying again on the newer main', flush=True)
    raise RuntimeError(f'main kept moving: {attempts} pushes lost; the next run tries again')


def changed(runner: Runner, work: Path) -> list[str]:
    """Every path the worktree changed, staged."""
    runner.run(['git', 'add', '-A'], cwd=work)
    return runner.output(['git', 'diff', '--cached', '--name-only'], cwd=work).splitlines()


def apps_in(paths: list[str]) -> str:
    return ', '.join(sorted({path.split('/')[1] for path in paths if re.match(r'apps/[^/]+/', path)}))


def render(runner: Runner, work: Path, _base: str) -> Commit | None:
    """Render main from its records: make app-sync, then the platform's NATS user list, which v2 renders."""
    swhurl(runner, work, 'app-sync')
    runner.output(['make', '-s', 'v2-users'], cwd=work)
    paths = changed(runner, work)
    if not paths:
        return None
    return Commit(f'render: {apps_in(paths) or "shared files"} (render workflow)\n\n' + '\n'.join(paths))


DEPLOYED = re.compile(r'^\[OK\] ([a-z0-9-]+)/staging: image ', re.M)
REPORTED = re.compile(r'^\[OK\] [a-z0-9-]+/staging(: image | needs: ).*$', re.M)


def deploy(runner: Runner, work: Path, refresh: bool, failed: list[bool]) -> Commit | None:
    """Bring staging to each app's newest image (make app-deploy). An app that fails keeps what it had and
    is reported through ``failed``; the others are still committed."""
    result = swhurl(runner, work, 'app-deploy', *(['--refresh'] if refresh else []), check=False)
    log = runner.redact(result.stdout + result.stderr)
    print(log, end='', flush=True)
    failed[:] = [bool(result.returncode)]
    if not changed(runner, work):
        return None
    moved = ', '.join(DEPLOYED.findall(log))
    lines = '\n'.join(match.group(0) for match in REPORTED.finditer(log))
    return Commit(f'deploy: {moved or "what the staging apps need and offer"} (deploy workflow)\n\n{lines}'.rstrip() + '\n')


def deploy_v2(runner: Runner, work: Path, failed: list[bool]) -> Commit | None:
    """Bring every v2-owned staging instance to its newest image (make v2-deploy), then render the platform files the
    records decide — the broker's user list carries what each release publishes — so one commit holds both. An
    instance that refused or failed is held and reported through ``failed``; the others are still committed."""
    result = runner.run(['make', 'v2-deploy'], cwd=work, check=False)
    print(runner.redact(result.stdout + result.stderr), end='', flush=True)
    failed[:] = [bool(result.returncode)]
    for target in ('v2-users', 'v2-reloader'):
        runner.output(['make', '-s', target], cwd=work)
    paths = changed(runner, work)
    if not paths:
        return None
    moved = ', '.join(sorted({path.split('/')[1] for path in paths if path.startswith('v2-records/')}))
    return Commit(f'deploy: {moved or "the v2 staging images"} (v2 deploy workflow)\n\n' + '\n'.join(paths).rstrip() + '\n')


def _publish(runner: Runner, root: Path, change: Change, nothing: str, checks: Sequence[str] = CHECKS) -> int:
    try:
        made = push_main(runner, root, change, checks=checks)
    except (CommandError, RuntimeError) as error:
        print(runner.redact(str(error)), file=sys.stderr)
        return 1
    print(f'pushed {made[:12]} to main' if made else nothing)
    return 0


def main_render(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl bot-render', description='Render the newest main from its records and push it')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    runner = runner or Runner()
    return _publish(runner, args.root, lambda work, base: render(runner, work, base),
                    'main is rendered (another writer rendered it); nothing to do.')


def main_deploy_v2(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl bot-deploy-v2',
                                description='Bring every v2-owned staging instance to its newest image and push it')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    runner = runner or Runner()
    failed = [False]
    code = _publish(runner, args.root, lambda work, _base: deploy_v2(runner, work, failed), 'Nothing changed.',
                    checks=CHECKS_V2)
    return code or int(failed[0])


def main_deploy(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl bot-deploy', description="Bring staging to each app's newest image and push it")
    p.add_argument('--refresh', action='store_true', help='read swhurl.yaml again for images staging already runs')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    runner = runner or Runner()
    runner.add_secret(os.environ.get('GITHUB_TOKEN'))
    failed = [False]
    code = _publish(runner, args.root, lambda work, _base: deploy(runner, work, args.refresh, failed), 'Nothing changed.')
    return code or int(failed[0])
