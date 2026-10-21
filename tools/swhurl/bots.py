"""How a bot pushes to main: Render, Deploy and the console's merge gate (docs/architecture.md#how-changes-reach-the-cluster).

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


def checked(runner: Runner, work: Path) -> None:
    """check-repo renders the registered units, check-apps compares every instance with its records. A
    failure shows the whole output: CI has no other copy of it."""
    result = runner.run(['make', *CHECKS], cwd=work, check=False)
    if result.returncode:
        print(runner.redact(result.stdout + result.stderr), file=sys.stderr, flush=True)
        raise CommandError(result.args, f'make {" ".join(CHECKS)} exited {result.returncode}', result.returncode)


def push_main(runner: Runner, root: Path, change: Change, *, attempts: int = ATTEMPTS,
              ready: Callable[[], None] = lambda: None, git: Sequence[str] = ('git',)) -> str | None:
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
                checked(runner, work)
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
    """Render main from its records (make app-sync)."""
    swhurl(runner, work, 'app-sync')
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


def _publish(runner: Runner, root: Path, change: Change, nothing: str) -> int:
    try:
        made = push_main(runner, root, change)
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
