"""Background jobs: the console's cluster writes (reconcile, suspend and resume
a Flux unit) and its Git changes (opening a PR, ``changes.py``).

A job's output is shown on its page as it arrives. One job per target (a unit,
or an app instance) at a time. Every start and finish is one ``[AUDIT]`` line
on stdout, which reaches ClickStack with the pod's logs. The root units are
refused: they are applied by ``make flux-bootstrap``, not by Flux.
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
    unit: str  # the target: a Flux unit, or <app>/<env> for a PR
    identity: str
    started: dt.datetime
    lines: list[str] = field(default_factory=list)
    state: str = 'running'  # running, succeeded, failed
    finished: dt.datetime | None = None
    link: str = ''  # for example the PR a job opened


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
        """Check a flux action on a unit, then run it in the background."""
        if action not in ACTIONS:
            raise ActionError(f'unknown action {action!r}')
        if unit in REFUSED:
            raise ActionError(f'{unit} is {REFUSED[unit]}')
        if unit not in {u.name for u in cluster.units(self.runner)}:
            raise ActionError(f'no Flux unit named {unit!r}')

        def work(job: Job) -> None:
            for line in self.runner.stream(command(action, unit), mutating=True):
                job.lines.append(line)
        return self.submit(action, unit, identity, work)

    def submit(self, action: str, target: str, identity: str, work: Callable[[Job], None]) -> Job:
        """Run ``work(job)`` in the background; it appends to ``job.lines`` and raises to fail."""
        with self._lock:
            if any(j.unit == target and j.state == 'running' for j in self._jobs.values()):
                raise ActionError(f'a job for {target} is already running')
            job = Job(next(self._ids), action, target, identity, self.now())
            self._jobs[job.id] = job
            while len(self._jobs) > KEEP and next(iter(self._jobs.values())).state != 'running':
                self._jobs.popitem(last=False)
        self.audit(f'[AUDIT] {identity} {action} {target}: started (job {job.id})')
        if self.inline:
            self._run(job, work)
        else:
            threading.Thread(target=self._run, args=(job, work), daemon=True).start()
        return job

    def _run(self, job: Job, work: Callable[[Job], None]) -> None:
        try:
            work(job)
            job.state = 'succeeded'
        except (CommandError, ActionError) as error:
            job.lines.append(f'[ERROR] {error}')
            job.state = 'failed'
        except Exception as error:  # a bug must still finish the job and reach the audit log
            job.lines.append(f'[ERROR] unexpected {type(error).__name__}: {self.runner.redact(str(error))}')
            job.state = 'failed'
        job.finished = self.now()
        level = 'AUDIT' if job.state == 'succeeded' else 'ERROR'
        link = f' {job.link}' if job.link else ''
        self.audit(f'[{level}] {job.identity} {job.action} {job.unit}: {job.state} (job {job.id}){link}')
