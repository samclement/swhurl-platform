"""The console's one adapter to ``swhurl-v2``: argv, the binary, and v2's own words.

The console owns no v2 model. It names the records root, the profile and the destination the binary writes, runs the
binary in the pull request's clone or in this pod, and shows what the binary refused verbatim. A change is one
``swhurl-v2 change`` run, which opens its own pull request; the rest of the console's commands read or write one
instance's records.
"""
from __future__ import annotations

import os
import tempfile
from collections.abc import Iterator
from pathlib import Path

from swhurl.console.actions import ActionError
from swhurl.run import CommandError, Result, Runner

BINARY = 'swhurl-v2'
RECORDS = 'v2-records'
PROFILE = 'v2/testdata/profile.json'
CALLS = f'{RECORDS}/calls.json'
EGRESS = 'platform/egress/allocation.json'
TOKEN_ENV = 'CONSOLE_TOKEN'
"""Where ``swhurl-v2 change`` reads its credential: the console's own token, in the child's environment only."""
APP_REPOS_TOKEN_ENV = 'APP_REPOS_TOKEN'
IMAGE_WEBHOOK_TOKEN_ENV = 'IMAGE_WEBHOOK_TOKEN'
"""What ``swhurl-v2 app-repo`` reads: the token that creates repositories and the one that signs the webhook. Both
reach the child's environment only, never its argv."""


def credential(runner: Runner, root: Path, *, in_cluster: bool) -> list[str]:
    """How a live command reads the cluster: the pod's own ServiceAccount, or (``--dev``) the host's current
    kubeconfig written under ``root``, readable by this user only and registered as a secret so it is never shown."""
    if in_cluster:
        return ['--in-cluster']
    document = runner.output(['kubectl', 'config', 'view', '--raw', '--minify', '--flatten', '-o', 'json'], secret_output=True)
    runner.add_secret(document)
    path = root / 'kubeconfig.json'
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(descriptor, 'w') as output:
        output.write(document)
    return ['--kubeconfig', str(path)]


def available(runner: Runner) -> bool:
    """Whether this console's image carries the binary: no binary is a read-only fallback, not a failed job."""
    try:
        runner.run([BINARY, '--help'], check=False)
    except CommandError:
        return False
    return True


def unit(runner: Runner, action: str, unit: str, *, in_cluster: bool) -> Iterator[str]:
    """One unit action, run in this pod: v2 patches the API server and prints the job's lines as it goes. The
    credential is this pod's ServiceAccount, or under ``--dev`` the host's kubeconfig in a folder of the job's own."""
    with tempfile.TemporaryDirectory(prefix='console-unit-') as root:
        argv = [BINARY, 'unit', '--action', action, '--unit', unit,
                *credential(runner, Path(root), in_cluster=in_cluster)]
        yield from runner.stream(argv, mutating=True)


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
