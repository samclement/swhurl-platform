"""The record of the console's jobs that outlives its pod (docs/console.md#jobs-and-restarts).

One key per job in the ConfigMap ``console/console-jobs``, written by the pod that runs the job as
it starts, as its output grows and as it ends. A pod that starts later reads them: it shows what
ran before it, and a job whose pod is gone without finishing it is ``interrupted``. Each pod
writes only its own jobs' keys while they run, so the pod finishing its jobs and the one replacing
it never undo each other; how a finished job's change ended (``Job.ending``) is added by whichever
pod finds out. Bounded: the newest ``KEEP`` jobs, each record at most ``RECORD_BYTES``.
"""
from __future__ import annotations

import datetime as dt
import json
import logging

from swhurl import statestore
from swhurl.console.actions import Job
from swhurl.run import CommandError, Runner

NAMESPACE = 'console'
NAME = 'console-jobs'
FIELD_MANAGER = 'console-jobs'
KEEP = 30  # jobs remembered; with RECORD_BYTES each, well inside a ConfigMap's 1 MiB
RECORD_BYTES = 20_000
LINE_CHARS = 1_000
log = logging.getLogger('swhurl.console.jobs')


def key(job_id: int) -> str:
    return f'job-{job_id}'


def record(job: Job, redact) -> str:
    """The job as JSON, its output cut from the front to fit: the end says how it went."""
    lines = [redact(line)[:LINE_CHARS] for line in job.lines]
    while True:
        raw = json.dumps({'id': job.id, 'action': job.action, 'unit': job.unit, 'identity': job.identity,
                          'started': job.started.isoformat(), 'finished': job.finished.isoformat() if job.finished else None,
                          'state': job.state, 'link': job.link, 'owner': job.owner, 'resume': job.resume, 'ending': job.ending, 'lines': lines,
                          # Output cut from the front moves every later line up: the stages' starts move with it.
                          'marks': [[stage, max(0, start - (len(job.lines) - len(lines)))] for stage, start in job.marks]},
                         ensure_ascii=False)
        if len(raw.encode()) <= RECORD_BYTES or len(lines) <= 1:
            return raw
        lines = ['… earlier output is not kept', *lines[max(2, len(lines) // 4):]]


def parse(raw: str) -> Job | None:
    """A stored record as a Job; None for anything that is not one (the store is never trusted to be well-formed)."""
    try:
        doc = json.loads(raw)
        found = Job(int(doc['id']), str(doc['action']), str(doc['unit']), str(doc['identity']),
                    dt.datetime.fromisoformat(doc['started']), [str(line) for line in doc.get('lines') or []],
                    str(doc['state']), dt.datetime.fromisoformat(doc['finished']) if doc.get('finished') else None,
                    str(doc.get('link') or ''), str(doc.get('owner') or ''),
                    doc['resume'] if isinstance(doc.get('resume'), dict) else None,
                    marks=[(str(stage), int(start)) for stage, start in doc.get('marks') or []],
                    ending=doc['ending'] if isinstance(doc.get('ending'), dict) else None)
    except (ValueError, KeyError, TypeError):
        return None
    return found if found.state in ('running', 'succeeded', 'failed', 'interrupted') else None


class JobStore:
    """Reads and writes fail soft: a job never fails because its record could not be kept."""

    def __init__(self, runner: Runner, owner: str):
        self.runner, self.owner = runner, owner

    def load(self) -> list[Job]:
        try:
            keys = statestore.read_keys(self.runner, namespace=NAMESPACE, name=NAME)
        except (CommandError, AttributeError, TypeError) as error:
            log.warning('Could not read the job records (%s/%s): %s', NAMESPACE, NAME, error)
            return []
        return sorted((job for job in map(parse, keys.values()) if job), key=lambda job: job.id)

    def save(self, job: Job, forget: tuple[int, ...] = ()) -> None:
        data: dict[str, str | None] = {key(job.id): record(job, self.runner.redact)}
        data.update({key(old): None for old in forget})
        try:
            statestore.write_keys(self.runner, namespace=NAMESPACE, name=NAME, data=data, field_manager=FIELD_MANAGER)
        except CommandError as error:
            log.warning('Could not keep the record of job %s: %s', job.id, error)

    def alive(self, owner: str) -> bool:
        """Whether the pod that ran a job still exists (finishing its jobs before it stops, perhaps).
        Unknown counts as alive: a job is called interrupted only when its pod is known to be gone."""
        if not owner:
            return False
        try:
            return bool(self.runner.json(['kubectl', '-n', NAMESPACE, 'get', 'pod', owner, '--ignore-not-found', '-o', 'json']))
        except CommandError:
            return True
