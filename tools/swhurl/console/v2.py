"""The console's one adapter to ``swhurl-v2``: argv, the binary, and v2's own words.

The console owns no v2 model. It names the records root, the profile and the destination the binary
writes, runs the binary in the pull request's clone, and shows what the binary refused verbatim. A v2
change is the action's command (``edit``, ``remove`` or ``promote``) followed by ``render``, so the PR
carries the records and everything v2 derives from them.
"""
from __future__ import annotations

from swhurl.console.actions import ActionError
from swhurl.run import CommandError, Result, Runner

BINARY = 'swhurl-v2'
RECORDS = 'v2-records'
PROFILE = 'v2/testdata/profile.json'
CALLS = f'{RECORDS}/calls.json'
EGRESS = 'platform/egress/allocation.json'
OUT = '.'
BASE = '@base'
"""The commit the pull request is built on: the console fills it in after downloading main."""


def edit_argv(app: str, env: str, setting: str) -> list[str]:
    """One named change to one instance's record. ``env`` is empty for the app-level team change."""
    return ['edit', '--records', RECORDS, '--app', app, *([f'--env={env}'] if env else []),
            '--set', setting, '--profile', PROFILE, '--out', OUT, '--calls', CALLS, '--egress', EGRESS]


def remove_argv(app: str, env: str) -> list[str]:
    return ['remove', '--records', RECORDS, '--app', app, f'--env={env}',
            '--profile', PROFILE, '--calls', CALLS, '--out', OUT]


def promote_argv(app: str, env: str = 'prod') -> list[str]:
    return ['promote', '--records', RECORDS, '--app', app, f'--env={env}',
            '--profile', PROFILE, '--out', OUT, '--calls', CALLS, '--egress', EGRESS]


def render_argv(revision: str = BASE) -> list[str]:
    return ['render', '--records', RECORDS, '--profile', PROFILE, '--out', OUT, '--revision', revision]


def resolve(argv: list[str], *, revision: str, expect: str = '') -> list[str]:
    """The command line with the base commit filled in, and a promotion's reviewed revision appended."""
    resolved = [revision if arg == BASE else arg for arg in argv]
    if expect:
        resolved += ['--expect', expect]
    return resolved


def available(runner: Runner) -> bool:
    """Whether this console's image carries the binary: no binary is a read-only fallback, not a failed job."""
    try:
        runner.run([BINARY, '--help'], check=False)
    except CommandError:
        return False
    return True


def run(runner: Runner, clone, argv: list[str]) -> Result:
    """Run one v2 command in the clone through the Runner. A nonzero exit is v2's own refusal."""
    result = runner.run([BINARY, *argv], cwd=clone, check=False)
    if result.returncode:
        raise ActionError(refusal(result))
    return result


def refusal(result: Result) -> str:
    """v2's diagnostic as it wrote it: the reason it refused, never the console's paraphrase."""
    text = (result.stderr or result.stdout).strip()
    return text or f'{BINARY} {result.args[1] if len(result.args) > 1 else ""} exited {result.returncode}'.strip()


def instance_revision(runner: Runner, clone, app: str, env: str = 'prod') -> str:
    """The instance revision a promotion reviewed, computed by v2 itself from the record in the clone."""
    path = f'{RECORDS}/{app}/{env}/instance.json'
    result = runner.run([BINARY, 'instance-revision', path], cwd=clone, check=False)
    if result.returncode:
        raise ActionError(refusal(result))
    return result.stdout.strip()
