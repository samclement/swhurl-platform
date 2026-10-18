"""The stages a change goes through and where it ends, for Activity and a job's page.

A change is more than its job: a promotion's job ends when its pull request is open, and the
change ends when production runs it. A ``Track`` is the whole path as labelled stages, the last
one always the terminal stop (``Live in prod``, ``Applied``, ``Removed from staging``), drawn from
the start so the outcome is known before it is reached.

Progress is derived from the job, pull request, main's render, the live render hash and instance
state. No stage or rollout verdict is persisted. Output stays with the job as one chronological log.
"""
from __future__ import annotations

import re
from dataclasses import dataclass

from swhurl.apps.state import State
from swhurl.console.actions import Job

NEW_APP_JOB, FINISH_JOB = 'new app and repository', 'finish new app'
NEW_APP_ACTIONS = (NEW_APP_JOB, FINISH_JOB, 'new')  # the jobs, and the action in their pull request's branch
LABELS = {'validate': 'Reviewed', 'merge': 'Merged', 'rollout': 'Applied'}
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


@dataclass(frozen=True)
class Target:
    """An instance's render in a Git tree; 'absent' is a proven missing HelmRelease."""
    hash: str
    digest: str = ''


@dataclass(frozen=True)
class Rollout:
    state: str  # live, zero, removed, superseded, applying, creating, removing, failing, held, unread
    detail: str = ''
    word: str = ''
    applied: bool = False


def judge(action: str, live: State | str | None, target: Target | None, current: Target | None,
          applied_hash: str = '', ready: bool = False, image_matches: bool = False) -> Rollout:
    """Five fresh facts, never a saved verdict. Hash equality proves application independently of
    unrelated Git commits. Absence must be proved in Git and on the cluster; a failed read is unknown."""
    if target is None:
        return Rollout('unread', 'the target or current render could not be read')
    if not target.hash:
        return Rollout('historical', 'this change predates render hashes')
    if current is None:
        return Rollout('unread', 'the target or current render could not be read')
    if not current.hash:
        return Rollout('unread', 'the current render has no hash')
    if target.hash != current.hash:
        return Rollout('superseded')
    if live is None:
        return Rollout('unread', 'the cluster could not be read')
    if action == 'remove':
        return Rollout('removed') if target.hash == ABSENT and live == ABSENT else Rollout('removing')
    if live == ABSENT:
        return Rollout('creating', 'not created yet')
    if applied_hash != target.hash:
        return Rollout('applying', 'the target render is not applied yet')
    applied = ready
    if live.key == 'failing':
        return Rollout('failing', live.detail, applied=applied)
    if live.key in ('suspended',):
        return Rollout('held', live.detail, live.key, applied)
    if not ready:
        return Rollout('applying', 'the HelmRelease is not Ready at its current generation')
    if live.key == 'stopped':
        return Rollout('zero', applied=True)
    if live.key == 'healthy' and image_matches and target.digest:
        return Rollout('live', applied=True)
    if live.key == 'unknown':
        return Rollout('unread', live.detail or 'the instance could not be read', applied=applied)
    return Rollout('applying', live.detail or 'waiting for the target image to be Healthy', applied=True)


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


def stage_of(job: Job, index: int) -> str:
    return 'prepared'


def error(job: Job) -> str:
    """Why a job stopped, from its output."""
    if job.state == 'interrupted':
        return 'the console restarted before it finished'
    said = next((line for line in reversed(job.lines) if line.startswith('[ERROR] ')), '')
    said = said.removeprefix('[ERROR] ')
    return said if len(said) <= NOTE else said[:NOTE].rstrip() + '…'


def job_stages(job: Job) -> list[Stage]:
    state = 'done' if job.state == 'succeeded' else 'running' if job.state == 'running' else 'failed'
    note = error(job) if state == 'failed' else 'PR opened' if state == 'done' and number(job) else ''
    return [Stage('prepared', 'Prepared', state, note)]


def sections(job: Job) -> list[Section]:
    return [Section(job_stages(job)[0], tuple(job.lines), True)]


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
    checks = {'passed': Stage('validate', 'Reviewed', 'done', link=pull.checks_url),
              'failed': Stage('validate', 'Reviewed', 'failed', 'its checks failed', pull.checks_url),
              'running': Stage('validate', 'Reviewed', 'running', link=pull.checks_url),
              'pending': Stage('validate', 'Reviewed', 'running', 'queued', pull.checks_url),
              }.get(pull.checks, Stage('validate', 'Reviewed', 'todo', link=pull.checks_url))
    rollout = ([Stage('rollout', 'Applied', 'todo'),
                *([] if action == 'remove' else [Stage('running', 'Running', 'todo')])] if instance else [])
    planned = Stage('end', end, 'planned')

    def track(merge: Stage, status: str, outcome: str, validate: Stage = checks, rest: list[Stage] | None = None):
        return [validate, merge, *(rollout + [planned] if rest is None else rest)], status, outcome

    name = f'#{pull.number}'
    if pull.state == 'open':
        why = f' ({pull.reason})' if pull.reason else ''
        if pull.checks == 'conflict':
            return track(Stage('merge', 'Merged', 'waiting', 'merge conflict', pull.url), 'waiting',
                         f'Waiting for you: {name} conflicts with main. Resolve it on the pull request, or close it and start again.')
        if pull.checks == 'failed':
            return track(Stage('merge', 'Merged', 'todo', link=pull.url), 'blocked',
                         f'Validate failed on {name}: nothing is merged. Fix it on the pull request, or close it.')
        if pull.auto:
            verifying = pull.checks == 'passed'
            return track(Stage('merge', 'Merged', 'running' if verifying else 'todo',
                               'the merge gate is checking it' if verifying else 'merges itself', pull.url), 'running',
                         f'{name} merges itself once its checks pass, {then}.')
        return track(Stage('merge', 'Merged', 'waiting', 'you merge it', pull.url), 'waiting',
                     f'Waiting for you: merge {name} on GitHub{why}; it is {then.removeprefix("and is ")}.'
                     if not instance else f'Waiting for you: merge {name} on GitHub{why}; {then}.')
    done = Stage('validate', 'Reviewed', 'done', link=pull.checks_url)
    if pull.state == 'merged':
        merged = Stage('merge', 'Merged', 'done', f'merged {pull.when}'.strip(), pull.url)
        reached = Stage('end', end, 'reached')
        if not instance:
            return track(merged, 'done', f'Merged {pull.when}.'.replace(' .', '.'), done, [reached])
        flux = 'Applied'
        found = landed or Rollout('unread', 'it was not read')

        if found.state == 'historical':
            return track(merged, 'done', f'Merged {pull.when}; this change predates render hashes, so its rollout cannot be tracked.'.replace(' ;', ';'),
                         done, [Stage('end', 'Merged (historical)', 'reached')])

        def applied(status: str, outcome: str, state: str, note: str = '', end: Stage = planned):
            reached = found.applied or found.state in ('live', 'zero', 'removed')
            running = [] if action == 'remove' else [Stage('running', 'Running', state if reached else 'todo', note)]
            return track(merged, status, outcome, done,
                         [Stage('rollout', flux, 'done' if reached else state, '' if reached else note), *running, end])

        if found.state == 'superseded':  # the instance now is a later change's doing
            return track(merged, 'done', f'Merged {pull.when}; a later render of {target} supersedes this change.'.replace(' ;', ';'),
                         done, [Stage('end', 'Superseded', 'reached')])
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
                     Stage('validate', 'Reviewed', 'todo'), [])
    return track(Stage('merge', 'Merged', 'todo', link=pull.url), 'unknown',
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
    if not flux and not control(job) and end != 'On main' and job.action != 'remove':
        after.append(Stage('running', 'Running', 'todo'))
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
