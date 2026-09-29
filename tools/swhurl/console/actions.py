"""The console's only cluster writes: reconcile, suspend and resume a Flux unit.

Each action runs ``flux`` as a background job whose output the job page shows
as it arrives. One job per unit at a time. Every start and finish is one
``[AUDIT]`` line on stdout, which reaches ClickStack with the pod's logs.
The root units are refused: they are applied by ``make flux-bootstrap``, not
by Flux.
"""
from __future__ import annotations

import datetime as dt
import itertools
import threading
from collections import OrderedDict
from collections.abc import Callable
from dataclasses import dataclass, field

from swhurl.console import cluster
from swhurl.run import CommandError, Runner

ACTIONS = ('reconcile', 'suspend', 'resume')
REFUSED = {'cluster-sources': 'applied by make flux-bootstrap, not by Flux',
           'cluster-stack': 'applied by make flux-bootstrap, not by Flux'}
KEEP = 50  # finished jobs kept in memory; they are lost on restart


class ActionError(Exception):
    """A refused or impossible action; the message is for the operator."""


def command(action: str, unit: str) -> list[str]:
    base = ['flux', action, 'kustomization', unit, '-n', 'flux-system']
    if action == 'reconcile':
        return [*base, '--with-source', '--timeout=10m']
    if action == 'resume':
        return [*base, '--timeout=10m']
    return base


@dataclass
class Job:
    id: int
    action: str
    unit: str
    identity: str
    started: dt.datetime
    lines: list[str] = field(default_factory=list)
    state: str = 'running'  # running, succeeded, failed
    finished: dt.datetime | None = None


class Jobs:
    def __init__(self, runner: Runner, *, audit: Callable[[str], None] = lambda line: print(line, flush=True),
                 now: Callable[[], dt.datetime] = lambda: dt.datetime.now(dt.UTC), inline: bool = False):
        """``inline`` runs each job before ``start`` returns instead of in a thread (tests)."""
        self.runner = runner
        self.inline = inline
        self.audit = audit
        self.now = now
        self._jobs: OrderedDict[int, Job] = OrderedDict()
        self._ids = itertools.count(1)
        self._lock = threading.Lock()

    def get(self, job_id: int) -> Job | None:
        with self._lock:
            return self._jobs.get(job_id)

    def recent(self) -> list[Job]:
        with self._lock:
            return list(reversed(self._jobs.values()))

    def start(self, action: str, unit: str, identity: str) -> Job:
        """Check the action, then run it in a thread."""
        if action not in ACTIONS:
            raise ActionError(f'unknown action {action!r}')
        if unit in REFUSED:
            raise ActionError(f'{unit} is {REFUSED[unit]}')
        if unit not in {u.name for u in cluster.units(self.runner)}:
            raise ActionError(f'no Flux unit named {unit!r}')
        with self._lock:
            if any(j.unit == unit and j.state == 'running' for j in self._jobs.values()):
                raise ActionError(f'a job for {unit} is already running')
            job = Job(next(self._ids), action, unit, identity, self.now())
            self._jobs[job.id] = job
            while len(self._jobs) > KEEP and next(iter(self._jobs.values())).state != 'running':
                self._jobs.popitem(last=False)
        self.audit(f'[AUDIT] {identity} {action} {unit}: started (job {job.id})')
        if self.inline:
            self._run(job)
        else:
            threading.Thread(target=self._run, args=(job,), daemon=True).start()
        return job

    def _run(self, job: Job) -> None:
        try:
            for line in self.runner.stream(command(job.action, job.unit), mutating=True):
                job.lines.append(line)
            job.state = 'succeeded'
        except CommandError as error:
            job.lines.append(f'[ERROR] {error}')
            job.state = 'failed'
        job.finished = self.now()
        level = 'AUDIT' if job.state == 'succeeded' else 'ERROR'
        self.audit(f'[{level}] {job.identity} {job.action} {job.unit}: {job.state} (job {job.id})')
