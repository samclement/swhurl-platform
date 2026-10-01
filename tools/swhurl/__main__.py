"""``python3 -m swhurl <command> [args]``: one entry point for operator commands.

Each command is a function ``(argv) -> int`` in a module. Modules are
imported only when their command runs, so one broken command cannot stop the
others. The Makefile calls these; operators normally use ``make``.
"""
from __future__ import annotations

import importlib
import sys

COMMANDS = {
    # command: (module, function, summary)
    'make-help': ('swhurl.makehelp', 'main', 'List Makefile targets from their ## comments'),
    'check-repo': ('swhurl.validate', 'main', 'Render active Flux paths, check schemas, SOPS, shell and doc links'),
    'check-config': ('swhurl.verify', 'verify_config', 'Check required Secret files and settings exist (no cluster)'),
    'verify-platform': ('swhurl.verify', 'main', 'Check live platform state: Flux, keys, redirect, retention'),
    'flux-install': ('swhurl.flux', 'install_main', 'Install or upgrade the Flux controllers with the settings in Git'),
    'flux-wait': ('swhurl.flux', 'main', 'Wait for every Flux unit to apply the fetched Git revision (read-only)'),
    'platform-certs': ('swhurl.settings', 'platform_certs', 'Set CERT_ISSUER in platform-settings (Git edit only)'),
    'check-secrets': ('swhurl.secrets_check', 'main', 'Decrypt tracked Secrets in memory and flag problems'),
    'app': ('swhurl.apps.ops', 'main', 'Operate one app instance: status, logs, reconcile, check'),
    'app-new': ('swhurl.apps.new', 'main', 'Generate an app instance'),
    'app-promote': ('swhurl.apps.edit', 'main_promote', 'Copy an app image (tag and digest) between environments (Git edit)'),
    'app-scale': ('swhurl.apps.edit', 'main_scale', 'Change replicas or resources of an app instance (Git edit)'),
    'app-expose': ('swhurl.apps.edit', 'main_expose', 'Change who can reach an app instance: private, signed-in or public (Git edit)'),
    'app-remove': ('swhurl.apps.edit', 'main_remove', 'Delete an app instance from Git; Flux uninstalls it on push'),
    'check-otel': ('swhurl.otel', 'main', 'Validate each OTel collector config with the collector version it will run'),
    'check-apps': ('swhurl.apps.policy', 'main', 'Render app instances and check the app contract'),
    'lifecycle': ('swhurl.lifecycle', 'main', 'suspend | resume | destroy-data a Flux unit, release or volume'),
    'live-test-lifecycle': ('swhurl.livetests.lifecycle', 'main', 'Live: prove suspend/uninstall/destroy-data/Orphan on a throwaway app'),
    'live-test-reloader': ('swhurl.livetests.reloader', 'main', 'Live: prove Reloader restarts only opted-in workloads in watched namespaces'),
    'live-test-app-template': ('swhurl.livetests.app_template', 'main', 'Live: deploy the generated app fixtures through Flux, check, remove'),
    'clickstack-bootstrap': ('swhurl.clickstack', 'main', 'Live: register the ClickStack admin and set the team ingestion key from SOPS'),
    'backup-mongodb': ('swhurl.recovery', 'backup_mongodb', 'Encrypted ClickStack MongoDB backup to BACKUP_DIR, then prune'),
    'backup-sqlite': ('swhurl.sqlite_backup', 'backup_sqlite', 'Encrypted backup of every app SQLite database, then prune and copy to S3'),
    'live-test-restore-mongodb': ('swhurl.recovery', 'restore_test_mongodb', 'Restore the latest backup into a throwaway namespace and check it'),
    'console': ('swhurl.console.server', 'main', 'Serve the web console (--dev: loopback, fixed identity; needs uv run)'),
    'console-image': ('swhurl.images', 'main', 'Pin the console HelmRelease to the image published for this checkout (Git edit)'),
    'prune-backups': ('swhurl.retention', 'main', 'Prune ClickStack MongoDB backups to the retention set'),
}


def usage() -> str:
    width = max(map(len, COMMANDS))
    lines = ['usage: python3 -m swhurl <command> [args]', '', 'commands:']
    lines += [f'  {name:<{width}}  {summary}' for name, (_, _, summary) in COMMANDS.items()]
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
    module_name, function, _ = COMMANDS[command]
    return getattr(importlib.import_module(module_name), function)(rest)


if __name__ == '__main__':
    sys.exit(main())
