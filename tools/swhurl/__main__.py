"""``python3 -m swhurl <command> [args]``: one entry point for operator commands.

The commands are stated once, in :mod:`swhurl.commands`. The Makefile calls
these; operators normally use ``make``.
"""
from __future__ import annotations

import sys

from swhurl import commands


def main(argv: list[str] | None = None) -> int:
    argv = sys.argv[1:] if argv is None else argv
    if not argv or argv[0] in ('-h', '--help'):
        print(commands.usage())
        return 0 if argv else 2
    command = commands.COMMANDS.get(argv[0])
    if command is None or not command.run:
        print(f'unknown command: {argv[0]}\n\n{commands.usage()}', file=sys.stderr)
        return 2
    return commands.call(command, argv[1:])


if __name__ == '__main__':
    sys.exit(main())
