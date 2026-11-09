"""Background jobs: the console's unit actions (reconcile, suspend and resume a
Flux unit) and its Git changes (opening a PR, ``proposal.py``).

A unit action is one ``swhurl-v2 unit`` run in this pod, which reads and patches
the API server through Go and prints the lines the job keeps (the console's
``v2.py`` names the binary and the credential). The console still decides what
it refuses before a job starts: an unknown unit, and the root units that ``make
flux-bootstrap`` applies rather than Flux.

A job's output is shown on its page as it arrives. One job per target (a unit,
or an app instance) at a time. Every start and finish is one ``[AUDIT]`` line
on stdout, which reaches ClickStack with the pod's logs.

A job outlives a restart of the console in two ways (``jobstore.py``): the pod
being stopped finishes its running jobs first (``drain``), and every job's
record is kept outside the pod, so the pod that replaces it shows them and
calls a job ``interrupted`` when its pod went away without finishing it.
"""
from __future__ import annotations

import datetime as dt
import itertools
import logging
import threading
import time
from collections import OrderedDict
from collections.abc import Callable, Iterator
from dataclasses import dataclass, field

from swhurl.console import cluster
from swhurl.run import CommandError, Runner

ACTIONS = ('reconcile', 'suspend', 'resume')
REFUSED = {'cluster-sources': 'applied by make flux-bootstrap, not by Flux',
           'cluster-stack': 'applied by make flux-bootstrap, not by Flux'}
KEEP = 50  # finished jobs kept in memory; the newest are also kept outside the pod (jobstore.KEEP)
SAVE_EVERY = 2.0  # seconds between writes of a running job's record while its output grows
FOREIGN_TTL = 5.0  # seconds a record of another pod's running job is shown before it is read again
DRAIN = 840.0  # seconds a stopping pod waits for its jobs: the longest job, inside the pod's grace period


class ActionError(Exception):
    """A refused or impossible action; the message is for the operator."""


@dataclass
class Job:
    id: int
    action: str
    unit: str  # the target: a Flux unit, or <app>/<env> for a PR
    identity: str
    started: dt.datetime
    lines: list[str] = field(default_factory=list)
    state: str = 'running'  # running, succeeded, failed; interrupted: its pod went away before it ended
    finished: dt.datetime | None = None
    link: str = ''  # for example the PR a job opened
    owner: str = ''  # the pod running it
    resume: dict | None = None  # what a later job needs to finish this one, if it can be finished (never a secret)
    live: bool = True  # False for a record read from the store: another pod's job, or one from before a restart
    reached: str = ''  # when its change was first seen at its end (Live, scaled to zero, removed), ISO 8601


class Jobs:
    def __init__(self, runner: Runner, *, audit: Callable[[str], None] | None = None,
                 now: Callable[[], dt.datetime] = lambda: dt.datetime.now(dt.UTC), inline: bool = False,
                 sleep: Callable[[float], None] = time.sleep, clock: Callable[[], float] = time.monotonic,
                 store=None, flux: Callable[[str, str], Iterator[str]] | None = None):
        """``inline`` runs each job before ``start`` returns instead of in a thread (tests). ``store``
        (a ``jobstore.JobStore``) keeps each job's record outside this process; without one, jobs
        are in memory only. ``flux`` streams one unit action's lines: the ``swhurl-v2 unit`` run
        ``v2.unit`` starts, or a fake in a test."""
        self.runner = runner
        self.flux = flux
        self.inline = inline
        self.audit = audit
        self.now = now
        self.store, self.clock, self.sleep = store, clock, sleep
        self.draining = False
        self._jobs: OrderedDict[int, Job] = OrderedDict()
        self._before: dict[int, Job] = {}  # records from the store: what ran before this process, or runs in another pod
        self._read = clock()
        self._lock = threading.Lock()
        for job in store.load() if store else []:
            job.live = False
            self._before[job.id] = job
        self._ids = itertools.count(max(self._before, default=0) + 1)
        self._settle()

    def _settle(self) -> None:
        """Call a remembered job interrupted once the pod that ran it is gone."""
        for job in list(self._before.values()):
            if job.state == 'running' and not self.store.alive(job.owner):
                job.state, job.finished = 'interrupted', self.now()
                job.lines.append('[ERROR] interrupted: the console restarted before this job finished. '
                                 'What it had done by then is above; nothing later happened')
                self.store.save(job)
                self._audit(job, f'[ERROR] {job.identity} {job.action} {job.unit}: interrupted (job {job.id})', 'failed')

    def _refresh(self) -> None:
        """Read again the jobs another pod is still running: their output grows there, not here."""
        if not self.store or not any(job.state == 'running' for job in self._before.values()):
            return
        if self.clock() - self._read < FOREIGN_TTL:
            return
        self._read = self.clock()
        for job in self.store.load():
            if job.id in self._before and job.id not in self._jobs:
                job.live = False
                self._before[job.id] = job
        self._settle()

    def get(self, job_id: int) -> Job | None:
        with self._lock:
            if job_id in self._jobs:
                return self._jobs[job_id]
            self._refresh()
            return self._before.get(job_id)

    def recent(self) -> list[Job]:
        with self._lock:
            self._refresh()
            return [*reversed(self._jobs.values()), *sorted(self._before.values(), key=lambda job: -job.id)]

    def drain(self, timeout: float = DRAIN) -> bool:
        """Stop taking jobs and wait for the running ones (the pod is being stopped); False if some outlast ``timeout``."""
        self.draining = True
        deadline = self.clock() + timeout
        while any(job.state == 'running' for job in list(self._jobs.values())):
            if self.clock() >= deadline:
                return False
            self.sleep(0.5)
        return True

    def start(self, action: str, unit: str, identity: str) -> Job:
        """Check a unit action, then run v2's command for it in the background."""
        if action not in ACTIONS:
            raise ActionError(f'unknown action {action!r}')
        if unit in REFUSED:
            raise ActionError(f'{unit} is {REFUSED[unit]}')
        if self.flux is None:
            raise ActionError('this console has no swhurl-v2 binary to run a unit action')
        if unit not in {u.name for u in cluster.units(self.runner)}:
            raise ActionError(f'no Flux unit named {unit!r}')

        def work(job: Job) -> None:
            for line in self.flux(action, unit):
                job.lines.append(line)
        return self.submit(action, unit, identity, work)

    def submit(self, action: str, target: str, identity: str, work: Callable[[Job], None], *,
               resume: dict | None = None) -> Job:
        """Run ``work(job)`` in the background; it appends to ``job.lines`` and raises to fail.
        ``resume`` is kept with the job's record: what a later job needs to finish it."""
        with self._lock:
            if self.draining:
                raise ActionError('the console is restarting and takes no new jobs; try again in a minute')
            self._refresh()
            if any(j.unit == target and j.state == 'running' for j in (*self._jobs.values(), *self._before.values())):
                raise ActionError(f'a job for {target} is already running')
            job = Job(next(self._ids), action, target, identity, self.now(),
                      owner=self.store.owner if self.store else '', resume=resume)
            self._jobs[job.id] = job
            while len(self._jobs) > KEEP and next(iter(self._jobs.values())).state != 'running':
                self._jobs.popitem(last=False)
            forget = self._forget()
        if self.store:
            self.store.save(job, forget)
        self._audit(job, f'[AUDIT] {identity} {action} {target}: started (job {job.id})', 'started')
        if self.inline:
            self._run(job, work)
        else:
            threading.Thread(target=self._run, args=(job, work), daemon=True).start()
        return job

    def reach(self, job: Job) -> None:
        """Record that ``job``'s change was seen at its end, once: a later change may replace it, but it ran."""
        if job.reached:
            return
        job.reached = self.now().isoformat()
        if self.store:
            self.store.save(job)

    def _forget(self) -> tuple[int, ...]:
        """The remembered jobs beyond what the store keeps, oldest first: finished ones only."""
        if not self.store:
            return ()
        from swhurl.console import jobstore  # it imports this module
        known = sorted({*self._before, *self._jobs})
        old = [i for i in known[:-jobstore.KEEP] if (self._jobs.get(i) or self._before[i]).state != 'running']
        for i in old:
            self._before.pop(i, None)
        return tuple(old)

    def _keep(self, job: Job) -> None:
        """Write a running job's record as its output grows, so another pod can show it."""
        seen = (len(job.lines), job.link)
        while job.state == 'running':
            self.sleep(SAVE_EVERY)
            now = (len(job.lines), job.link)
            if now != seen and job.state == 'running':
                self.store.save(job)
                seen = now

    def _run(self, job: Job, work: Callable[[Job], None]) -> None:
        if self.store and not self.inline:
            threading.Thread(target=self._keep, args=(job,), daemon=True).start()
        try:
            work(job)
            state = 'succeeded'
        except (CommandError, ActionError) as error:
            job.lines.append(f'[ERROR] {error}')
            state = 'failed'
        except Exception as error:  # a bug must still finish the job and reach the audit log
            job.lines.append(f'[ERROR] unexpected {type(error).__name__}: {self.runner.redact(str(error))}')
            state = 'failed'
        job.state = state
        job.finished = self.now()
        if self.store:
            self.store.save(job)
        level = 'AUDIT' if job.state == 'succeeded' else 'ERROR'
        link = f' {job.link}' if job.link else ''
        self._audit(job, f'[{level}] {job.identity} {job.action} {job.unit}: {job.state} (job {job.id}){link}', job.state)

    def _audit(self, job: Job, line: str, state: str) -> None:
        if self.audit is not None:
            self.audit(line)
            return
        logging.getLogger('swhurl.console.audit').log(
            logging.ERROR if state == 'failed' else logging.INFO, line,
            extra={'identity': job.identity, 'action': job.action, 'unit': job.unit,
                   'state': state, 'job_id': job.id, 'pr_url': job.link})
