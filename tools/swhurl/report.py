"""Operator-facing output: ``[OK]``/``[BAD]``/``[WARN]`` lines and an exit code.

Every command reports through a :class:`Report` so wording and exit codes stay
consistent, and tests can read what was reported without parsing stdout.
"""
from __future__ import annotations

import sys
from collections.abc import Callable
from typing import TextIO


class Report:
    def __init__(self, out: TextIO | None = None, redact: Callable[[str], str] | None = None):
        """``redact`` (usually ``runner.redact``) is applied to every line before it is printed."""
        self.out = out or sys.stdout
        self.redact = redact or (lambda text: text)
        self.failures = 0
        self.warnings = 0
        self.lines: list[str] = []

    def _emit(self, line: str) -> None:
        line = self.redact(line)
        self.lines.append(line)
        print(line, file=self.out)

    def section(self, title: str) -> None:
        self._emit(f'\n== {title} ==')

    def ok(self, message: str) -> None:
        self._emit(f'[OK] {message}')

    def bad(self, message: str) -> None:
        self.failures += 1
        self._emit(f'[BAD] {message}')

    def warn(self, message: str) -> None:
        self.warnings += 1
        self._emit(f'[WARN] {message}')

    def info(self, message: str) -> None:
        self._emit(f'[INFO] {message}')

    def detail(self, message: str) -> None:
        """An indented follow-up line under the previous result (for example a fix hint)."""
        self._emit(f'       {message}')

    def line(self, message: str = '') -> None:
        self._emit(message)

    @property
    def passed(self) -> bool:
        return self.failures == 0

    def exit_code(self) -> int:
        return 0 if self.passed else 1
