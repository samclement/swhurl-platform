"""The platform checks that only read the cluster, as ``swhurl-v2 verify --json`` reports them (v2-platform-checks).

Go decides them; ``verify-platform`` and the console only show them. Standard library only, so the console reads
the report without importing ``verify`` and the modules its other checks need.
"""
from __future__ import annotations

import json

from swhurl.report import Entry

KIND, VERSION = 'v2-platform-checks', 1
LEVELS = ('ok', 'warn', 'bad')


def entries(stdout: str) -> list[Entry]:
    """Every entry in the order the checks ran, with its section. Anything but a complete version-1 document is a
    ValueError: a check that did not report is never shown as one that passed."""
    try:
        document = json.loads(stdout)
    except json.JSONDecodeError as error:
        raise ValueError(f'not a {KIND} document') from error
    if not isinstance(document, dict) or document.get('kind') != KIND or document.get('version') != VERSION:
        raise ValueError(f'not a {KIND} version {VERSION} document')
    checks = document.get('checks')
    if not isinstance(checks, list) or not checks:
        raise ValueError(f'{KIND}: no checks')
    found = []
    for check in checks:
        section = check.get('section') if isinstance(check, dict) else None
        listed = check.get('entries') if isinstance(check, dict) else None
        if not isinstance(section, str) or not section or not isinstance(listed, list) or not listed:
            raise ValueError(f'{KIND}: a check without a section or entries')
        for entry in listed:
            level, message = (entry.get('level'), entry.get('message')) if isinstance(entry, dict) else (None, None)
            if level not in LEVELS or not isinstance(message, str):
                raise ValueError(f'{KIND}: an entry without a level or message')
            found.append(Entry(section, level, message))
    return found
