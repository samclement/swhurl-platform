"""The single seam for running external commands.

Every call to kubectl, flux, helm, sops, age or git goes through a
:class:`Runner`. That gives three guarantees in one place:

* ``DRY_RUN``: calls marked ``mutating=True`` are printed, not run. Read-only
  calls still run, so dry runs can check real state.
* Secrecy: values registered with :meth:`Runner.add_secret` are replaced by
  ``<redacted>`` in every message and error; ``secret_output=True`` keeps a
  command's output out of error messages entirely.
* Testability: :class:`FakeRunner` records calls and answers from scripted
  rules, so command logic is unit-tested without a cluster.
"""
from __future__ import annotations

import json
import os
import shlex
import subprocess
from collections.abc import Callable, Iterable, Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import Any

REDACTED = '<redacted>'


@dataclass(frozen=True)
class Result:
    args: tuple[str, ...]
    returncode: int = 0
    stdout: str = ''
    stderr: str = ''


class CommandError(Exception):
    """A command could not run or exited non-zero. The message is already redacted."""

    def __init__(self, args: Sequence[str], message: str, returncode: int | None = None):
        super().__init__(message)
        self.args_run = tuple(args)
        self.returncode = returncode


class Runner:
    def __init__(self, *, dry_run: bool = False, cwd: Path | None = None,
                 env: Mapping[str, str] | None = None, echo: Callable[[str], None] = print):
        self.dry_run = dry_run
        self.cwd = cwd
        self.env = dict(env or {})
        self.echo = echo
        self._secrets: set[str] = set()

    @classmethod
    def from_environment(cls, **kwargs: Any) -> Runner:
        """A runner honouring the ``DRY_RUN=true`` convention used by the Makefile."""
        return cls(dry_run=os.environ.get('DRY_RUN', 'false') == 'true', **kwargs)

    # Secrecy -----------------------------------------------------------------

    def add_secret(self, value: str | bytes | None) -> None:
        """Never show ``value`` in any message this runner produces."""
        if isinstance(value, bytes):
            value = value.decode(errors='replace')
        if value:
            self._secrets.add(value)

    def redact(self, text: str) -> str:
        for secret in sorted(self._secrets, key=len, reverse=True):
            text = text.replace(secret, REDACTED)
        return text

    def describe(self, args: Iterable[str]) -> str:
        return self.redact(shlex.join(args))

    # Running -----------------------------------------------------------------

    def run(self, args: Sequence[str | Path], *, input: str | None = None, check: bool = True,
            mutating: bool = False, secret_output: bool = False,
            env: Mapping[str, str] | None = None, cwd: Path | None = None) -> Result:
        """Run ``args``; raise :class:`CommandError` on failure when ``check``."""
        argv = tuple(str(a) for a in args)
        if mutating and self.dry_run:
            self._plan(argv)
            return Result(argv)
        result = self._execute(argv, input=input, env={**self.env, **(env or {})}, cwd=cwd or self.cwd)
        if check and result.returncode:
            detail = '' if secret_output else self.redact((result.stderr or result.stdout).strip())
            message = f'{self.describe(argv)} exited {result.returncode}' + (f': {detail}' if detail else '')
            raise CommandError(argv, message, result.returncode)
        return result

    def output(self, args: Sequence[str | Path], **kwargs: Any) -> str:
        return self.run(args, **kwargs).stdout

    def json(self, args: Sequence[str | Path], **kwargs: Any) -> Any:
        text = self.output(args, **kwargs)
        return json.loads(text) if text.strip() else None

    def attached(self, args: Sequence[str | Path], *, mutating: bool = False) -> int:
        """Run with the terminal attached (output streams live) and return the exit code.

        For long or interactive commands such as ``kubectl logs --follow`` and
        ``flux reconcile``. Never use it for commands that print Secret values.
        """
        argv = tuple(str(a) for a in args)
        if mutating and self.dry_run:
            self._plan(argv)
            return 0
        return self._attach(argv)

    def _plan(self, argv: tuple[str, ...]) -> None:
        self.echo(f'  would run: {self.describe(argv)}')

    def _attach(self, argv: tuple[str, ...]) -> int:
        try:
            return subprocess.run(argv, cwd=self.cwd, env={**os.environ, **self.env} if self.env else None,
                                  check=False).returncode
        except FileNotFoundError:
            raise CommandError(argv, f'missing required command: {argv[0]}') from None

    def _execute(self, argv: tuple[str, ...], *, input: str | None,
                 env: Mapping[str, str], cwd: Path | None) -> Result:
        try:
            done = subprocess.run(argv, input=input, capture_output=True, text=True, check=False,
                                  cwd=cwd, env={**os.environ, **env} if env else None)
        except FileNotFoundError:
            raise CommandError(argv, f'missing required command: {argv[0]}') from None
        return Result(argv, done.returncode, done.stdout, done.stderr)


Response = Result | Callable[[tuple[str, ...], str | None], Result]


class FakeRunner(Runner):
    """A Runner for tests: records every call and answers from rules.

    Rules match on an argv prefix; the first match wins. An unmatched call
    fails the test, so tests state every command they expect::

        fake = FakeRunner().on('kubectl', 'get', stdout='{}')
        do_something(fake)
        assert fake.calls == [('kubectl', 'get', 'pods', '-o', 'json')]
    """

    def __init__(self, *, dry_run: bool = False):
        self.echoed: list[str] = []
        super().__init__(dry_run=dry_run, echo=self.echoed.append)
        self.calls: list[tuple[str, ...]] = []
        self.planned: list[tuple[str, ...]] = []
        self._rules: list[tuple[tuple[str, ...], Response]] = []

    def on(self, *prefix: str, stdout: str = '', stderr: str = '', returncode: int = 0,
           handler: Callable[[tuple[str, ...], str | None], Result] | None = None) -> FakeRunner:
        self._rules.append((prefix, handler or Result((), returncode, stdout, stderr)))
        return self

    def _plan(self, argv: tuple[str, ...]) -> None:
        self.planned.append(argv)
        super()._plan(argv)

    def _attach(self, argv: tuple[str, ...]) -> int:
        return self._execute(argv, input=None, env={}, cwd=None).returncode

    def _execute(self, argv: tuple[str, ...], *, input: str | None,
                 env: Mapping[str, str], cwd: Path | None) -> Result:
        self.calls.append(argv)
        for prefix, response in self._rules:
            if argv[:len(prefix)] == prefix:
                if callable(response):
                    return response(argv, input)
                return Result(argv, response.returncode, response.stdout, response.stderr)
        raise AssertionError(f'unexpected command: {shlex.join(argv)}')
