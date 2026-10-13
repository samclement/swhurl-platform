"""Background jobs: the console's cluster writes (reconcile, suspend and resume
a Flux unit) and its Git changes (opening a PR, ``changes.py``).

Flux actions are the patches the ``flux`` CLI would make, sent with ``kubectl``
(the image has no ``flux`` binary): suspend and resume set ``spec.suspend``;
reconcile and resume set the ``reconcile.fluxcd.io/requestedAt`` annotation
(on the unit's source first, for reconcile) and wait until the controller
reports that request handled and the object Ready or failed.

A job's output is shown on its page as it arrives. One job per target (a unit,
or an app instance) at a time. Every start and finish is one ``[AUDIT]`` line
on stdout, which reaches ClickStack with the pod's logs. The root units are
refused: they are applied by ``make flux-bootstrap``, not by Flux.

A job outlives a restart of the console in two ways (``jobstore.py``): the pod
being stopped finishes its running jobs first (``drain``), and every job's
record is kept outside the pod, so the pod that replaces it shows them and
calls a job ``interrupted`` when its pod went away without finishing it.
"""
from __future__ import annotations

import datetime as dt
import itertools
import json
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
NAMESPACE = 'flux-system'
KUSTOMIZATION = 'kustomizations.kustomize.toolkit.fluxcd.io'
SOURCES = {'GitRepository': 'gitrepositories.source.toolkit.fluxcd.io',
           'OCIRepository': 'ocirepositories.source.toolkit.fluxcd.io',
           'Bucket': 'buckets.source.toolkit.fluxcd.io'}
REQUESTED_AT = 'reconcile.fluxcd.io/requestedAt'
TIMEOUT = 600.0  # seconds, as the flux CLI's --timeout=10m
POLL = 2.0


class ActionError(Exception):
    """A refused or impossible action; the message is for the operator."""


class Flux:
    """Suspend, resume and reconcile a Flux unit through the API, yielding progress lines."""

    def __init__(self, runner: Runner, *, now: Callable[[], dt.datetime],
                 sleep: Callable[[float], None] = time.sleep, clock: Callable[[], float] = time.monotonic):
        self.runner, self.now, self.sleep, self.clock = runner, now, sleep, clock

    def run(self, action: str, unit: str, stage: Callable[[str], None] = lambda key: None) -> Iterator[str]:
        """``stage`` is told each stage as it begins (``Job.stage``)."""
        if action == 'reconcile':
            spec = self.runner.json(['kubectl', '-n', NAMESPACE, 'get', f'{KUSTOMIZATION}/{unit}', '-o', 'json'])['spec']
            if spec.get('suspend'):
                raise ActionError(f'{unit} is suspended; resume it instead (resume also reconciles)')
            ref = spec.get('sourceRef') or {}
            if ref.get('kind') not in SOURCES:
                raise ActionError(f'{unit} has an unsupported source kind {ref.get("kind")!r}')
            stage('fetch')
            yield from self.request(ref['kind'], SOURCES[ref['kind']], ref['name'], ref.get('namespace', NAMESPACE))
        else:
            suspend = action == 'suspend'
            stage(action)
            yield f'► {"suspending" if suspend else "resuming"} Kustomization {unit}'
            self.runner.run(['kubectl', '-n', NAMESPACE, 'patch', f'{KUSTOMIZATION}/{unit}', '--type=merge',
                             '--patch', json.dumps({'spec': {'suspend': suspend}})], mutating=True)
            if suspend:
                yield f'✔ Kustomization {unit} suspended'
                return
        stage('apply')
        yield from self.request('Kustomization', KUSTOMIZATION, unit, NAMESPACE)

    def request(self, kind: str, resource: str, name: str, namespace: str) -> Iterator[str]:
        """Annotate, then wait until the controller has handled this request."""
        token = self.now().isoformat()
        target = f'{resource}/{name}'
        yield f'► annotating {kind} {name} in {namespace} namespace'
        self.runner.run(['kubectl', '-n', namespace, 'annotate', '--overwrite', target, f'{REQUESTED_AT}={token}'],
                        mutating=True)
        if self.runner.dry_run:
            return
        yield f'◎ waiting for {kind} reconciliation'
        deadline = self.clock() + TIMEOUT
        while True:
            obj = self.runner.json(['kubectl', '-n', namespace, 'get', target, '-o', 'json'])
            status = obj.get('status') or {}
            ready = next((c for c in status.get('conditions') or [] if c.get('type') == 'Ready'), {})
            handled = (status.get('lastHandledReconcileAt') == token
                       and status.get('observedGeneration') == obj.get('metadata', {}).get('generation'))
            if handled and ready.get('status') == 'True':
                if kind == 'Kustomization':
                    yield f'✔ applied revision {status.get("lastAppliedRevision", "")}'
                else:
                    yield f'✔ fetched revision {(status.get("artifact") or {}).get("revision", "")}'
                return
            if handled and ready.get('status') == 'False':
                raise ActionError(f'{kind} {name} reconciliation failed: {ready.get("message", "")}')
            if self.clock() >= deadline:
                raise ActionError(f'timed out after {TIMEOUT / 60:.0f}m waiting for {kind} {name}; '
                                  'it may still finish (check the unit page)')
            self.sleep(POLL)


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
    marks: list[tuple[str, int]] = field(default_factory=list)  # (stage, the output line it starts at): stages.py
    ending: dict | None = None  # how its merged change ended on the cluster, once known (stages.Rollout): never judged again

    def stage(self, key: str) -> None:
        """A stage of the job begins: its output from here on is that stage's."""
        self.marks.append((key, len(self.lines)))


class Jobs:
    def __init__(self, runner: Runner, *, audit: Callable[[str], None] | None = None,
                 now: Callable[[], dt.datetime] = lambda: dt.datetime.now(dt.UTC), inline: bool = False,
                 sleep: Callable[[float], None] = time.sleep, clock: Callable[[], float] = time.monotonic,
                 store=None):
        """``inline`` runs each job before ``start`` returns instead of in a thread (tests). ``store``
        (a ``jobstore.JobStore``) keeps each job's record outside this process; without one, jobs
        are in memory only."""
        self.runner = runner
        self.flux = Flux(runner, now=now, sleep=sleep, clock=clock)
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

    def settle(self, job: Job, ending: dict) -> None:
        """Keep how a finished job's change ended. Any pod may: the one that ran it is done with its record."""
        job.ending = ending
        if self.store:
            self.store.save(job)

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
        """Check a flux action on a unit, then run it in the background."""
        if action not in ACTIONS:
            raise ActionError(f'unknown action {action!r}')
        if unit in REFUSED:
            raise ActionError(f'{unit} is {REFUSED[unit]}')
        if unit not in {u.name for u in cluster.units(self.runner)}:
            raise ActionError(f'no Flux unit named {unit!r}')

        def work(job: Job) -> None:
            for line in self.flux.run(action, unit, job.stage):
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
        seen = (len(job.lines), job.link, len(job.marks))
        while job.state == 'running':
            self.sleep(SAVE_EVERY)
            now = (len(job.lines), job.link, len(job.marks))
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
