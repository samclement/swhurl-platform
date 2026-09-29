"""Operator-facing output: ``[OK]``/``[BAD]``/``[WARN]`` lines and an exit code.

Every command reports through a :class:`Report` so wording and exit codes stay
consistent, and tests can read what was reported without parsing stdout.
"""
from __future__ import annotations

import sys
from collections.abc import Callable
from dataclasses import dataclass
from typing import TextIO


@dataclass(frozen=True)
class Entry:
    """One reported result, for callers that render it themselves (the console)."""
    section: str
    level: str  # ok, bad, warn, info
    message: str


class Report:
    def __init__(self, out: TextIO | None = None, redact: Callable[[str], str] | None = None):
        """``redact`` (usually ``runner.redact``) is applied to every line before it is printed."""
        self.out = out or sys.stdout
        self.redact = redact or (lambda text: text)
        self.failures = 0
        self.warnings = 0
        self.lines: list[str] = []
        self.entries: list[Entry] = []
        self._section = ''

    def _emit(self, line: str) -> None:
        line = self.redact(line)
        self.lines.append(line)
        print(line, file=self.out)

    def _result(self, level: str, message: str) -> None:
        self.entries.append(Entry(self._section, level, self.redact(message)))
        self._emit(f'[{level.upper()}] {message}')

    def section(self, title: str) -> None:
        self._section = title
        self._emit(f'\n== {title} ==')

    def ok(self, message: str) -> None:
        self._result('ok', message)

    def bad(self, message: str) -> None:
        self.failures += 1
        self._result('bad', message)

    def warn(self, message: str) -> None:
        self.warnings += 1
        self._result('warn', message)

    def info(self, message: str) -> None:
        self._result('info', message)

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
