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
"""
from __future__ import annotations

import datetime as dt
import itertools
import json
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
KEEP = 50  # finished jobs kept in memory; they are lost on restart
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

    def run(self, action: str, unit: str) -> Iterator[str]:
        if action == 'reconcile':
            spec = self.runner.json(['kubectl', '-n', NAMESPACE, 'get', f'{KUSTOMIZATION}/{unit}', '-o', 'json'])['spec']
            if spec.get('suspend'):
                raise ActionError(f'{unit} is suspended; resume it instead (resume also reconciles)')
            ref = spec.get('sourceRef') or {}
            if ref.get('kind') not in SOURCES:
                raise ActionError(f'{unit} has an unsupported source kind {ref.get("kind")!r}')
            yield from self.request(ref['kind'], SOURCES[ref['kind']], ref['name'], ref.get('namespace', NAMESPACE))
        else:
            suspend = action == 'suspend'
            yield f'► {"suspending" if suspend else "resuming"} Kustomization {unit}'
            self.runner.run(['kubectl', '-n', NAMESPACE, 'patch', f'{KUSTOMIZATION}/{unit}', '--type=merge',
                             '--patch', json.dumps({'spec': {'suspend': suspend}})], mutating=True)
            if suspend:
                yield f'✔ Kustomization {unit} suspended'
                return
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
    state: str = 'running'  # running, succeeded, failed
    finished: dt.datetime | None = None
    link: str = ''  # for example the PR a job opened


class Jobs:
    def __init__(self, runner: Runner, *, audit: Callable[[str], None] = lambda line: print(line, flush=True),
                 now: Callable[[], dt.datetime] = lambda: dt.datetime.now(dt.UTC), inline: bool = False,
                 sleep: Callable[[float], None] = time.sleep, clock: Callable[[], float] = time.monotonic):
        """``inline`` runs each job before ``start`` returns instead of in a thread (tests)."""
        self.runner = runner
        self.flux = Flux(runner, now=now, sleep=sleep, clock=clock)
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
            for line in self.flux.run(action, unit):
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
