"""Classify changed paths for the Validate workflow: the app-only fast path and records-only changes."""

from __future__ import annotations

import re
import sys

APP_INSTANCE = re.compile(r"apps/[^/]+/.+")
APP_UNIT = re.compile(r"clusters/home/app-[^/]+\.yaml")
# Where a new app or a first promotion registers its unit. Only check-repo reads it, and that always runs.
REGISTRY = "clusters/home/kustomization.yaml"
# An app's input records (docs/plan.md section 24): everything else under apps/ is rendered from them.
RECORD = re.compile(r"apps/[^/]+/app\.yaml"
                    r"|apps/[^/]+/(staging|prod)/(release\.yaml|instance\.yaml|[^/]+\.sops\.yaml|offers/.+)")

SHARED_INPUTS = frozenset({'apps/teams.yaml', 'platform/notify/kustomization.yaml',
                           'clusters/home/platform-notify.yaml'})
CHANNEL = re.compile(r'platform/notify/channel-[a-z]([a-z0-9-]{0,38}[a-z0-9])?\.sops\.yaml')


def classify(paths: list[str]) -> tuple[bool, bool]:
    """Return (app_instance_only, tooling_tests_needed); empty paths fail closed."""
    if not paths or any(not (APP_INSTANCE.fullmatch(path) or APP_UNIT.fullmatch(path) or path == REGISTRY)
                        for path in paths):
        return False, True
    # The tooling tests' sample instance is tests/fixtures/instance, which is not an app path.
    return True, False


def records_only(paths: list[str]) -> bool:
    """Whether every changed path is an input record, so the checks render the records first; empty is False."""
    return bool(paths) and all(RECORD.fullmatch(path) or path in SHARED_INPUTS or CHANNEL.fullmatch(path) for path in paths)


def main() -> int:
    paths = sys.stdin.buffer.read().decode().split("\0")
    paths = [path for path in paths if path]
    app_only, tests_needed = classify(paths)
    print(f"app_instance_only={str(app_only).lower()}")
    print(f"tooling_tests_needed={str(tests_needed).lower()}")
    print(f"records_only={str(records_only(paths)).lower()}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
