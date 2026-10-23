"""One app page's platform files: a pinned Git commit, shared answers and bounded reads."""
from __future__ import annotations

import contextvars
import logging
import threading
import time
from collections.abc import Callable
from concurrent.futures import Future, ThreadPoolExecutor
from typing import Any
from urllib.parse import quote

from swhurl.console.actions import ActionError
from swhurl.console.github import GitHub, GitHubAPI
from swhurl.run import Runner

LOG = logging.getLogger('swhurl.console.timing')
FILE_WORKERS = 4


class Files:
    """Request-owned file reader. None means absent; exceptions mean unavailable.

    Even failures are shared within this page, never across pages. Only the GitHub
    reader resolves main; injected/local readers retain their existing contract.
    The owner must keep this scope open through template rendering.
    """

    def __init__(self, runner: Runner, github: GitHub | None, source: Callable[[str], str | None] | None = None):
        self.api = GitHubAPI(runner, github) if github and source is None else None
        self.source = source
        self.answers: dict[str | None, Future[str | None]] = {}
        self.lock = threading.Lock()
        self.slots = threading.BoundedSemaphore(FILE_WORKERS)
        self.hits = self.failed = 0
        self.file_ms = self.resolve_ms = 0.0
        self.started = time.monotonic()

    def __enter__(self) -> Files:
        return self

    def __exit__(self, *_exc) -> None:
        try:
            if self.api:
                self.api.client.close()
        finally:
            LOG.info('page files files=%d hits=%d failed=%d resolve_ms=%.1f file_ms=%.1f elapsed_ms=%.1f',
                     len(self.answers) - (None in self.answers), self.hits, self.failed, self.resolve_ms, self.file_ms,
                     (time.monotonic() - self.started) * 1000)

    def once(self, key: str | None, read: Callable[[], str | None]) -> str | None:
        with self.lock:
            first = key not in self.answers
            if first:
                self.answers[key] = Future()
            elif key is not None:
                self.hits += 1
            answer = self.answers[key]
        if first:
            try:
                answer.set_result(read())
            except BaseException as error:
                with self.lock:
                    self.failed += 1
                answer.set_exception(error)
        return answer.result()

    def revision(self) -> str:
        """Resolve once, including a failed resolution: never fall back to moving main."""
        def resolve() -> str:
            started = time.monotonic()
            try:
                revision = self.api.json('GET', '/git/ref/heads/main')['object']['sha']
                if not isinstance(revision, str) or len(revision) != 40 or any(c not in '0123456789abcdef' for c in revision):
                    raise ActionError('GitHub main did not name a commit')
                return revision
            finally:
                self.resolve_ms = (time.monotonic() - started) * 1000
        return self.once(None, resolve)

    def __call__(self, path: str) -> str | None:
        def read() -> str | None:
            # Resolve before taking a slot: concurrent files may all wait for this one read.
            revision = self.revision() if self.api else ''
            with self.slots:
                started = time.monotonic()
                try:
                    if not self.api:
                        return self.source(path) if self.source else None
                    reply = self.api.call('GET', f'/contents/{quote(path, safe="/")}?ref={revision}', missing_ok=True,
                                          headers={'Accept': 'application/vnd.github.raw+json'})
                    return None if reply.status_code == 404 else reply.text
                finally:
                    with self.lock:
                        self.file_ms += (time.monotonic() - started) * 1000
        return self.once(path, read)

    def together(self, *reads: Callable[[], Any]) -> list[Any]:
        """Bound the worker pool as well as simultaneous file reads; preserve result order."""
        if len(reads) < 2:
            return [read() for read in reads]
        with ThreadPoolExecutor(max_workers=FILE_WORKERS) as pool:
            started = [pool.submit(contextvars.copy_context().run, read) for read in reads]
        return [each.result() for each in started]
