"""The stages a change goes through and where it ends, for Activity and a job's page.

A change is more than its job: a promotion's job ends when its pull request is open, and the
change ends when production runs it. A ``Track`` is the whole path as labelled stages, the last
one always the terminal stop (``Live in prod``, ``Applied``, ``Removed from staging``), drawn from
the start so the outcome is known before it is reached.

The stages inside the console come from the job: it marks each as it begins (``Job.stage``), which
also groups its output. The ones after it (Validate, Merge, Flux applying it) come from the pull
request and from how far the merge has got on the cluster (``judge``), read by the caller: nothing
here reads anything.
"""
from __future__ import annotations

import re
from dataclasses import dataclass

from swhurl.apps.state import State
from swhurl.console.actions import Job

NEW_APP_JOB, FINISH_JOB = 'new app and repository', 'finish new app'
NEW_APP_ACTIONS = (NEW_APP_JOB, FINISH_JOB, 'new')  # the jobs, and the action in their pull request's branch
LABELS = {'fetch': 'Fetch source', 'apply': 'Apply unit', 'suspend': 'Suspend', 'resume': 'Resume',
          'repo': 'Create repository', 'build': 'Build image', 'prepare': 'Prepare change',
          'open': 'Open pull request', 'label': 'Change its label', 'pull': 'Pull request',
          'validate': 'Validate', 'merge': 'Merge', 'rollout': 'Flux applies it'}
# A stage's state as the mark it is drawn with (_status.html) and the words a screen reader gets.
MARKS = {'done': ('healthy', 'done'), 'running': ('updating', 'running now'), 'failed': ('failing', 'failed'),
         'waiting': ('suspended', 'waiting for you'), 'stopped': ('stopped', 'stopped'), 'todo': ('todo', 'not reached'),
         'reached': ('reached', 'reached'), 'planned': ('planned', 'where it ends, not reached')}
# A whole change's status as the mark and word that lead its row.
STATUS = {'running': ('updating', 'In progress'), 'waiting': ('suspended', 'Waiting for you'), 'done': ('healthy', ''),
          'failed': ('failing', 'Failed'), 'interrupted': ('failing', 'Interrupted'), 'stopped': ('stopped', 'Closed'), 'unknown': ('unknown', 'Unknown'),
          # Failed, but not over: its pull request is still open, or the instance it reached is failing.
          'blocked': ('failing', 'Failed')}
ABSENT = 'absent'  # an instance read from the cluster and not there
PULL = re.compile(r'/pull/(\d+)$')
BRANCH = re.compile(r'console/([a-z]+)-(.+)-(staging|prod)-[0-9a-f]{7,40}')
NOTE = 160


@dataclass(frozen=True)
class Stage:
    key: str
    label: str
    state: str  # a key of MARKS
    note: str = ''
    link: str = ''

    @property
    def mark(self) -> str:
        return MARKS[self.state][0]

    @property
    def words(self) -> str:
        return MARKS[self.state][1]


@dataclass(frozen=True)
class Track:
    stages: tuple[Stage, ...]
    status: str  # a key of STATUS
    outcome: str  # one sentence: what happens next, or how it ended

    @property
    def mark(self) -> str:
        return STATUS[self.status][0]

    @property
    def label(self) -> str:
        """The word leading the row: for a finished change, where it ended."""
        return STATUS[self.status][1] or self.stages[-1].label

    @property
    def settled(self) -> bool:
        """Nothing more will happen to it without someone starting something."""
        return self.status in ('done', 'failed', 'interrupted', 'stopped')


@dataclass(frozen=True)
class Pull:
    """What a track needs to know of a job's pull request."""
    number: int
    url: str
    state: str = 'unknown'  # open, merged, closed, or unknown (it could not be read)
    checks: str = ''  # Validate on its head: passed, failed, running, pending, conflict, or '' (not read)
    checks_url: str = ''
    auto: bool = False  # labelled to merge itself
    reason: str = ''  # why it waits for a person, when known
    when: str = ''  # when it was merged or closed, as words ("3 h ago")
    commit: str = ''  # the commit that merged it into main


ENDINGS = ('live', 'zero', 'removed', 'superseded')


@dataclass(frozen=True)
class Rollout:
    """How far a merged change to an instance has got on the cluster (``judge``). One of ``ENDINGS`` is
    how the change ended: it is kept with the job and never judged again, so nothing that happens to the
    instance later is this change's doing. Anything else is still being judged."""
    state: str  # an ending, or applying, creating, removing, failing, held, unread
    detail: str = ''
    word: str = ''  # for held: the instance's state (suspended)


def judge(action: str, live: State | str | None, contained: bool | None, superseded: bool = False) -> Rollout:
    """``live`` is the instance's state, ``ABSENT``, or None when the cluster was not read. ``contained``:
    whether what Flux has applied to the instance (or, for one that is absent, fetched) includes the merge;
    None when GitHub could not say. ``superseded``: a later commit to the instance's own files is applied too."""
    if live is None:
        return Rollout('unread', 'the cluster could not be read')
    if contained is None:
        return Rollout('unread', 'GitHub could not say whether Flux has applied the merge')
    if contained and superseded:
        return Rollout('superseded')
    if action == 'remove':
        return Rollout('removed') if contained and live == ABSENT else Rollout('removing')
    if not contained:
        return Rollout('applying', 'the merge is not applied yet')
    if live == ABSENT:
        return Rollout('creating', 'not created yet')
    if live.key in ('healthy', 'stopped'):
        return Rollout('live' if live.key == 'healthy' else 'zero')
    if live.key == 'updating':
        return Rollout('applying', live.detail)
    if live.key == 'failing':
        return Rollout('failing', live.detail)
    if live.key == 'unknown':
        return Rollout('unread', live.detail or 'the instance could not be read')
    return Rollout('held', live.detail, live.key)


@dataclass(frozen=True)
class Section:
    """One stage's share of a job's output."""
    stage: Stage
    lines: tuple[str, ...]
    started: bool


def number(job: Job) -> int | None:
    """The pull request a job opened, from its link."""
    found = PULL.search(job.link or '')
    return int(found[1]) if found else None


def control(job: Job) -> bool:
    """A job that only changed a pull request's label (Hold, Request auto-merge)."""
    return job.unit.startswith('PR #')


def keys(job: Job) -> tuple[str, ...]:
    """The stages a job of this kind runs inside the console, in order."""
    if job.action in ('reconcile', 'suspend', 'resume'):
        return {'reconcile': ('fetch', 'apply'), 'suspend': ('suspend',), 'resume': ('resume', 'apply')}[job.action]
    if control(job):
        return ('label',)
    if job.action == NEW_APP_JOB:
        return ('repo', 'build', 'prepare', 'open')
    if job.action == FINISH_JOB:
        return ('build', 'prepare', 'open')
    return ('prepare', 'open')


def bounds(job: Job) -> list[tuple[str, int]]:
    """Where each stage's output starts. A job with no marks (a record from before they were kept, or
    one that failed before its first) is all its first stage."""
    known, seen = keys(job), set()
    found = []
    for key, index in job.marks:
        if key in known and key not in seen:
            seen.add(key)
            found.append((key, index if found else 0))
    return found or [(known[0], 0)]


def stage_of(job: Job, index: int) -> str:
    """The stage that output line ``index`` belongs to."""
    return [key for key, start in bounds(job) if start <= index][-1]


def error(job: Job) -> str:
    """Why a job stopped, from its output."""
    if job.state == 'interrupted':
        return 'the console restarted before it finished'
    said = next((line for line in reversed(job.lines) if line.startswith('[ERROR] ')), '')
    said = said.removeprefix('[ERROR] ')
    return said if len(said) <= NOTE else said[:NOTE].rstrip() + '…'


def job_stages(job: Job) -> list[Stage]:
    known = keys(job)
    at = known.index(bounds(job)[-1][0])
    stages = []
    for i, key in enumerate(known):
        if job.state == 'succeeded' or i < at:
            stages.append(Stage(key, LABELS[key], 'done'))
        elif i > at:
            stages.append(Stage(key, LABELS[key], 'todo'))
        elif job.state == 'running':
            stages.append(Stage(key, LABELS[key], 'running'))
        else:
            stages.append(Stage(key, LABELS[key], 'failed', error(job)))
    return stages


def sections(job: Job) -> list[Section]:
    """A job's output under the stage that wrote it, one section per stage of its kind."""
    found = bounds(job)
    # Each stage's output runs to the next stage's start: two can start at the same line (one wrote
    # nothing, or the record's cut output took their starts with it), and only the later one has it.
    spans = {key: (start, end) for (key, start), end in zip(found, [start for _, start in found[1:]] + [len(job.lines)], strict=True)}
    return [Section(stage, tuple(job.lines[slice(*spans[stage.key])]) if stage.key in spans else (), stage.key in spans)
            for stage in job_stages(job)]


def end_label(action: str, target: str) -> str:
    """Where a change to ``target`` ends once its pull request is merged and applied."""
    app, _, env = target.partition('/')
    if not env or target.startswith('team:'):
        return 'On main'
    return f'Removed from {env}' if action == 'remove' else f'Live in {env}'


def after_pull(pull: Pull, action: str, target: str, landed: Rollout | None) -> tuple[list[Stage], str, str]:
    """Validate, Merge, Flux applying it and the terminal stop: ``(stages, status, outcome)``.
    ``landed`` is how far a merged change to an instance has got; None when it was not judged."""
    end = end_label(action, target)
    instance = end != 'On main'
    then = f'then {end[0].lower()}{end[1:]}' if instance else 'and is then on main'
    checks = {'passed': Stage('validate', 'Validate', 'done', link=pull.checks_url),
              'failed': Stage('validate', 'Validate', 'failed', 'its checks failed', pull.checks_url),
              'running': Stage('validate', 'Validate', 'running', link=pull.checks_url),
              'pending': Stage('validate', 'Validate', 'running', 'queued', pull.checks_url),
              }.get(pull.checks, Stage('validate', 'Validate', 'todo', link=pull.checks_url))
    rollout = [Stage('rollout', LABELS['rollout'], 'todo')] if instance else []
    planned = Stage('end', end, 'planned')

    def track(merge: Stage, status: str, outcome: str, validate: Stage = checks, rest: list[Stage] | None = None):
        return [validate, merge, *(rollout + [planned] if rest is None else rest)], status, outcome

    name = f'#{pull.number}'
    if pull.state == 'open':
        why = f' ({pull.reason})' if pull.reason else ''
        if pull.checks == 'conflict':
            return track(Stage('merge', 'Merge', 'waiting', 'merge conflict', pull.url), 'waiting',
                         f'Waiting for you: {name} conflicts with main. Resolve it on the pull request, or close it and start again.')
        if pull.checks == 'failed':
            return track(Stage('merge', 'Merge', 'todo', link=pull.url), 'blocked',
                         f'Validate failed on {name}: nothing is merged. Fix it on the pull request, or close it.')
        if pull.auto:
            verifying = pull.checks == 'passed'
            return track(Stage('merge', 'Merge', 'running' if verifying else 'todo',
                               'the merge gate is checking it' if verifying else 'merges itself', pull.url), 'running',
                         f'{name} merges itself once its checks pass, {then}.')
        return track(Stage('merge', 'Merge', 'waiting', 'you merge it', pull.url), 'waiting',
                     f'Waiting for you: merge {name} on GitHub{why}; it is {then.removeprefix("and is ")}.'
                     if not instance else f'Waiting for you: merge {name} on GitHub{why}; {then}.')
    done = Stage('validate', 'Validate', 'done', link=pull.checks_url)
    if pull.state == 'merged':
        merged = Stage('merge', 'Merge', 'done', f'merged {pull.when}'.strip(), pull.url)
        reached = Stage('end', end, 'reached')
        if not instance:
            return track(merged, 'done', f'Merged {pull.when}.'.replace(' .', '.'), done, [reached])
        flux = LABELS['rollout']
        found = landed or Rollout('unread', 'it was not read')

        def applied(status: str, outcome: str, state: str, note: str = '', end: Stage = planned):
            return track(merged, status, outcome, done, [Stage('rollout', flux, state, note), end])

        if found.state == 'superseded':  # the instance now is a later change's doing
            return track(merged, 'done', f'Merged {pull.when}; a later change to {target} has been applied since.'.replace(' ;', ';'),
                         done, [Stage('end', 'Merged to main', 'reached')])
        if found.state == 'live':
            return applied('done', f'{target} runs it.', 'done', end=reached)
        if found.state == 'zero':
            return applied('done', f'{target} is applied and scaled to zero.', 'done', end=Stage('end', end, 'reached', 'scaled to zero'))
        if found.state == 'removed':
            return applied('done', f'{target} is removed.', 'done', end=reached)
        if found.state == 'removing':
            return applied('running', f'Merged; Flux is removing {target}.', 'running')
        if found.state == 'creating':
            return applied('running', f'Merged; waiting for Flux to create {target}.', 'running', found.detail)
        if found.state == 'applying':
            return applied('running', f'Merged; Flux is applying it to {target}.', 'running', found.detail)
        if found.state == 'failing':
            return applied('blocked', f'Merged, but {target} is failing: open its page.', 'failed', found.detail)
        if found.state == 'held':
            return applied('waiting', f'Merged, but {target} is {found.word}: open its page.', 'waiting', found.detail)
        return track(merged, 'unknown', f'Merged; whether {target} runs it is not known: {found.detail}.', done)
    if pull.state == 'closed':
        # A new app's repository and image were made before its pull request: closing that leaves them.
        left = ('no instance was added, but the repository and image made for it still exist'
                if action in NEW_APP_ACTIONS else 'nothing changed')
        return track(Stage('merge', 'Closed without merging', 'stopped', pull.when, pull.url), 'stopped',
                     f'{name} was closed without merging: {left}.',
                     Stage('validate', 'Validate', 'todo'), [])
    return track(Stage('merge', 'Merge', 'todo', link=pull.url), 'unknown',
                 f'{name} could not be read from GitHub: open it there.')


def track(job: Job, pull: Pull | None = None, rollout: Rollout | None = None) -> Track:
    """The whole path of the change ``job`` started."""
    stages = job_stages(job)
    flux = job.action in ('reconcile', 'suspend', 'resume')
    if flux:
        end = 'Suspended' if job.action == 'suspend' else 'Applied'
    elif control(job):
        end = 'Held for you to merge' if job.action.startswith('hold') else 'Auto-merge requested'
    else:
        end = end_label(job.action, job.unit)
    # What follows the job, not reached while it runs or once it has failed.
    after = [] if flux or control(job) else [Stage(key, LABELS[key], 'todo') for key in
                                             ('validate', 'merge', *(['rollout'] if end != 'On main' else []))]
    if job.state == 'running':
        current = next(stage.label for stage in stages if stage.state == 'running')
        ends = end if flux or control(job) else f'{end}, once its pull request is merged'
        return Track((*stages, *after, Stage('end', end, 'planned')), 'running', f'Now: {current}. Ends: {ends}.')
    if job.state != 'succeeded':
        failed = next(stage for stage in stages if stage.state == 'failed')
        stages = [Stage(stage.key, stage.label, stage.state) for stage in stages]  # the reason is the sentence under the track
        return Track((*stages, *after, Stage('end', end, 'planned')), 'interrupted' if job.state == 'interrupted' else 'failed',
                     f'Stopped at {failed.label}: {failed.note or "see its output"}.')
    if flux or control(job):
        said = next((line for line in reversed(job.lines) if line.startswith('✔ applied revision ')), '')
        return Track((*stages, Stage('end', end, 'reached', said.removeprefix('✔ applied revision '))), 'done', f'{end}.')
    if pull is None:  # a dry run, or a job that had nothing to propose
        return Track((*stages, Stage('end', 'No pull request opened', 'reached')), 'done', 'No pull request was opened.')
    rest, status, outcome = after_pull(pull, job.action, job.unit, rollout)
    return Track((*stages, *rest), status, outcome)


def target_of(branch: str) -> tuple[str, str]:
    """``(action, target)`` of a console pull request, from its branch; the target is '' if it names no instance."""
    found = BRANCH.fullmatch(branch)
    if not found:
        return '', ''
    action, app, env = found.groups()
    return action, f'{app}/{"prod" if action == "promote" else env}'


def pull_track(pull: Pull, branch: str) -> Track:
    """The path of an open pull request whose job is no longer remembered."""
    action, target = target_of(branch)
    rest, status, outcome = after_pull(pull, action, target, None)
    return Track((Stage('pull', f'Pull request #{pull.number}', 'done', link=pull.url), *rest), status, outcome)
