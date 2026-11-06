"""The platform checks the console shows: one ``swhurl-v2 verify`` run per page, decided in Go.

The other ``verify-platform`` checks read Secrets, run commands in pods or need the operator's machine, so they stay
``make verify-platform``'s; this module neither imports nor names them.
"""
from __future__ import annotations

import tempfile
from pathlib import Path

from swhurl import platform_checks
from swhurl.console import v2
from swhurl.console.cluster import Checks, ReadError
from swhurl.report import Entry
from swhurl.run import CommandError, Runner


def checks(runner: Runner, *, in_cluster: bool) -> Checks:
    """What ``swhurl-v2 verify`` reported, by section. No complete report is a failed read (the page shows it
    unavailable), never a pass; a failing check is the report's, not an error."""
    with tempfile.TemporaryDirectory() as root:
        try:
            argv = [v2.BINARY, 'verify', '--source', 'console', '--json', *v2.credential(runner, Path(root), in_cluster=in_cluster)]
            result = runner.run(argv, check=False)
        except (CommandError, OSError) as error:
            raise ReadError(f'platform checks unavailable: {error}') from None
    try:
        found = platform_checks.entries(result.stdout)
    except ValueError as error:
        raise ReadError(f'platform checks unavailable ({error}): {v2.refusal(result)}') from None
    sections: dict[str, list[Entry]] = {}
    for entry in found:
        sections.setdefault(entry.section, []).append(entry)
    return Checks(all(entry.level != 'bad' for entry in found), list(sections.items()))
