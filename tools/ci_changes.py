"""Classify changed paths for the Validate workflow's app-only fast path."""

from __future__ import annotations

import re
import sys

APP_INSTANCE = re.compile(r"apps/[^/]+/.+")
APP_UNIT = re.compile(r"clusters/home/app-[^/]+\.yaml")


def classify(paths: list[str]) -> tuple[bool, bool]:
    """Return (app_instance_only, tooling_tests_needed); empty paths fail closed."""
    if not paths or any(not (APP_INSTANCE.fullmatch(path) or APP_UNIT.fullmatch(path)) for path in paths):
        return False, True
    # The tooling tests use apps/hello as a checked-in source fixture.
    return True, any(path.startswith("apps/hello/") for path in paths)


def main() -> int:
    paths = sys.stdin.buffer.read().decode().split("\0")
    paths = [path for path in paths if path]
    app_only, tests_needed = classify(paths)
    print(f"app_instance_only={str(app_only).lower()}")
    print(f"tooling_tests_needed={str(tests_needed).lower()}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
