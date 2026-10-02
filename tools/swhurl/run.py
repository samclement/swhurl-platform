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

import contextlib
import json
import os
import shlex
import signal
import subprocess
import threading
from collections.abc import Callable, Iterable, Iterator, Mapping, Sequence
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


class RunningCommand:
    """A quiet external process whose caller controls graceful shutdown."""

    def __init__(self, args: Sequence[str], process: subprocess.Popen):
        self.args = tuple(args)
        self.process = process

    def poll(self) -> int | None:
        return self.process.poll()

    def interrupt(self) -> None:
        if self.poll() is None:
            self.process.send_signal(signal.SIGINT)

    def wait(self, timeout: float | None = None) -> int:
        try:
            return self.process.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.wait()
            raise CommandError(self.args, f'{self.args[0]} did not stop after interrupt') from None


class CompletedCommand:
    """Already-finished command returned by FakeRunner.start."""

    def __init__(self, result: Result):
        self.result = result
        self.interrupted = False

    def poll(self) -> int:
        return self.result.returncode

    def interrupt(self) -> None:
        self.interrupted = True

    def wait(self, timeout: float | None = None) -> int:
        return self.result.returncode


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

    def start(self, args: Sequence[str | Path], *, env: Mapping[str, str] | None = None,
              cwd: Path | None = None) -> RunningCommand:
        """Start a quiet process for a caller that will stop it and wait explicitly.

        Output is discarded so a long-running parser cannot fill pipes or leak
        fixture/secret payloads. Failures report the command and exit status.
        """
        argv = tuple(str(a) for a in args)
        try:
            process = subprocess.Popen(argv, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                       cwd=cwd or self.cwd,
                                       env={**os.environ, **self.env, **(env or {})})
        except FileNotFoundError:
            raise CommandError(argv, f'missing required command: {argv[0]}') from None
        return RunningCommand(argv, process)

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

    def stream(self, args: Sequence[str | Path], *, mutating: bool = False) -> Iterator[str]:
        """Yield the command's stdout and stderr line by line, redacted, as it is printed.

        For commands whose progress a caller shows live, such as ``flux
        reconcile`` in the console. Closing the iterator early stops the
        command. A non-zero exit raises :class:`CommandError` after the last
        line. Under ``DRY_RUN`` a ``mutating`` command is planned, not run.
        """
        argv = tuple(str(a) for a in args)
        if mutating and self.dry_run:
            self._plan(argv)
            return
        for line in self._stream(argv):
            yield self.redact(line)

    def pipe(self, producer: Sequence[str | Path], consumer: Sequence[str | Path], *,
             mutating: bool = False, env: Mapping[str, str] | None = None, input: str | None = None) -> Result:
        """Run ``producer | consumer`` and return the consumer's result.

        ``input`` is written to the producer's stdin (for example a config file
        holding a credential, so it never appears in a command line).

        The two processes are joined by an OS pipe, so the stream (for example a
        database dump before encryption) never passes through Python, is never
        written to disk by this code and never appears in an error message.
        Like ``set -o pipefail``: a failure on either side raises
        :class:`CommandError` with both sides' (redacted) stderr.
        """
        left = tuple(str(a) for a in producer)
        right = tuple(str(a) for a in consumer)
        if mutating and self.dry_run:
            self._plan(left + ('|',) + right, display=f'{self.describe(left)} | {self.describe(right)}')
            return Result(right)
        left_result, right_result = self._pipe(left, right, env={**self.env, **(env or {})}, input=input)
        failed = [r for r in (left_result, right_result) if r.returncode]
        if failed:
            details = '; '.join(self.redact(r.stderr.strip()) for r in failed if r.stderr.strip())
            message = f'{self.describe(left)} | {self.describe(right)} exited ' + \
                ', '.join(str(r.returncode) for r in (left_result, right_result))
            raise CommandError(right, message + (f': {details}' if details else ''), failed[0].returncode)
        return right_result

    def _plan(self, argv: tuple[str, ...], display: str | None = None) -> None:
        """Report a mutating command that DRY_RUN skipped."""
        self.echo(f'  would run: {display or self.describe(argv)}')

    def _pipe(self, left: tuple[str, ...], right: tuple[str, ...], *,
              env: Mapping[str, str], input: str | None = None) -> tuple[Result, Result]:
        full_env = {**os.environ, **env} if env else None
        try:
            producer = subprocess.Popen(left, stdin=subprocess.PIPE if input is not None else None,
                                        stdout=subprocess.PIPE, stderr=subprocess.PIPE, cwd=self.cwd,
                                        env=full_env)
        except FileNotFoundError:
            raise CommandError(left, f'missing required command: {left[0]}') from None
        errors: list[bytes] = []
        drain = threading.Thread(target=lambda: errors.append(producer.stderr.read()), daemon=True)
        drain.start()
        try:
            consumer = subprocess.Popen(right, stdin=producer.stdout, stdout=subprocess.PIPE,
                                        stderr=subprocess.PIPE, cwd=self.cwd, env=full_env)
        except FileNotFoundError:
            producer.kill()
            producer.wait()
            raise CommandError(right, f'missing required command: {right[0]}') from None
        finally:
            producer.stdout.close()  # the consumer holds the only read end now
        if input is not None:
            with contextlib.suppress(BrokenPipeError):
                producer.stdin.write(input.encode())
            producer.stdin.close()
        out, err = consumer.communicate()
        producer.wait()
        drain.join()
        decode = lambda b: b.decode(errors='replace')  # noqa: E731
        return (Result(left, producer.returncode, '', decode(errors[0] if errors else b'')),
                Result(right, consumer.returncode, decode(out), decode(err)))

    def _stream(self, argv: tuple[str, ...]) -> Iterator[str]:
        try:
            # One pipe for both streams keeps their order (flux prints its progress on stderr).
            process = subprocess.Popen(argv, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True,
                                       errors='replace', cwd=self.cwd,
                                       env={**os.environ, **self.env} if self.env else None)
        except FileNotFoundError:
            raise CommandError(argv, f'missing required command: {argv[0]}') from None
        last = ''
        finished = False
        try:
            for line in process.stdout:
                last = line.strip() or last
                yield line.rstrip('\n')
            finished = True
        finally:
            if not finished:
                process.terminate()
            process.stdout.close()
            process.wait()
        if process.returncode:
            detail = self.redact(last)
            raise CommandError(argv, f'{self.describe(argv)} exited {process.returncode}'
                               + (f': {detail}' if detail else ''), process.returncode)

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
        self.started: list[CompletedCommand] = []
        self.planned: list[tuple[str, ...]] = []
        self._rules: list[tuple[tuple[str, ...], Response]] = []

    def on(self, *prefix: str, stdout: str = '', stderr: str = '', returncode: int = 0,
           handler: Callable[[tuple[str, ...], str | None], Result] | None = None) -> FakeRunner:
        self._rules.append((prefix, handler or Result((), returncode, stdout, stderr)))
        return self

    def _plan(self, argv: tuple[str, ...], display: str | None = None) -> None:
        self.planned.append(argv)
        super()._plan(argv, display)

    def start(self, args: Sequence[str | Path], *, env: Mapping[str, str] | None = None,
              cwd: Path | None = None) -> CompletedCommand:
        argv = tuple(str(a) for a in args)
        command = CompletedCommand(self._execute(argv, input=None, env={**self.env, **(env or {})}, cwd=cwd or self.cwd))
        self.started.append(command)
        return command

    def _attach(self, argv: tuple[str, ...]) -> int:
        return self._execute(argv, input=None, env={}, cwd=None).returncode

    def _stream(self, argv: tuple[str, ...]) -> Iterator[str]:
        result = self._execute(argv, input=None, env={}, cwd=None)
        yield from result.stdout.splitlines()
        if result.returncode:
            raise CommandError(argv, f'{self.describe(argv)} exited {result.returncode}', result.returncode)

    def _pipe(self, left: tuple[str, ...], right: tuple[str, ...], *,
              env: Mapping[str, str], input: str | None = None) -> tuple[Result, Result]:
        produced = self._execute(left, input=input, env=env, cwd=None)
        consumed = self._execute(right, input=produced.stdout, env=env, cwd=None)
        return Result(left, produced.returncode, '', produced.stderr), consumed

    def _execute(self, argv: tuple[str, ...], *, input: str | None,
                 env: Mapping[str, str], cwd: Path | None) -> Result:
        self.calls.append(argv)
        for prefix, response in self._rules:
            if argv[:len(prefix)] == prefix:
                if callable(response):
                    return response(argv, input)
                return Result(argv, response.returncode, response.stdout, response.stderr)
        raise AssertionError(f'unexpected command: {shlex.join(argv)}')
