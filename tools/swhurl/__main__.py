"""``python3 -m swhurl <command> [args]``: one entry point for operator commands.

Each command lives in its own module with a ``main(argv) -> int``. Modules are
imported only when their command runs, so one broken command cannot stop the
others. The Makefile calls these; operators normally use ``make``.
"""
from __future__ import annotations

import importlib
import sys

COMMANDS = {
    'validate-repo': ('swhurl.validate', 'Render active Flux paths, check schemas, SOPS, shell and doc links'),
    'secrets-check': ('swhurl.secrets_check', 'Decrypt tracked Secrets in memory and flag problems'),
    'app-new': ('swhurl.app_new', 'Generate an app instance'),
    'app-policy': ('swhurl.app_policy', 'Render app instances and check the app contract'),
    'prune-backups': ('swhurl.backups', 'Prune ClickStack MongoDB backups to the retention set'),
}


def usage() -> str:
    width = max(map(len, COMMANDS))
    lines = ['usage: python3 -m swhurl <command> [args]', '', 'commands:']
    lines += [f'  {name:<{width}}  {summary}' for name, (_, summary) in COMMANDS.items()]
    return '\n'.join(lines)


def main(argv: list[str] | None = None) -> int:
    argv = sys.argv[1:] if argv is None else argv
    if not argv or argv[0] in ('-h', '--help', 'help'):
        print(usage())
        return 0 if argv else 2
    command, rest = argv[0], argv[1:]
    if command not in COMMANDS:
        print(f'unknown command: {command}\n\n{usage()}', file=sys.stderr)
        return 2
    module = importlib.import_module(COMMANDS[command][0])
    return module.main(rest)


if __name__ == '__main__':
    sys.exit(main())
