"""``make help``: list Makefile targets from their ``## description`` comments.

The Makefile is the only place a target's help line is written; this prints it,
and a test checks every documented target also appears in docs/commands.md.
"""
from __future__ import annotations

import re
from pathlib import Path

from swhurl import ROOT

TARGET = re.compile(r'^(?P<targets>[a-z][a-z0-9-]*(?: [a-z][a-z0-9-]*)*):[^=]*?## (?P<help>.+)$')


def targets(makefile: str) -> list[tuple[str, str]]:
    """``[(targets, help)]`` in file order, for lines like ``a b: deps ## help``."""
    return [(m['targets'], m['help'].strip()) for m in map(TARGET.match, makefile.splitlines()) if m]


def main(argv: list[str] | None = None, root: Path = ROOT) -> int:
    entries = targets((root / 'Makefile').read_text())
    width = 26
    print('Targets (DRY_RUN=true plans where supported; see docs/commands.md):')
    for name, text in entries:
        if len(name) > width:
            print(f'  {name}\n  {"":<{width}}  {text}')
        else:
            print(f'  {name:<{width}}  {text}')
    return 0
