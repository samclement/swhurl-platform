"""How far a change has got, for Activity, a job's page and an app's change list.

A change is more than its job: a promotion's job ends when its pull request is open, and the
change ends when production runs it. ``progress`` is the one answer to how far a merged change to an
instance has got, from Git's facts (``Target``, read by ``render_targets``) and what the
cluster shows (``cluster.AppRow``), read fresh every time. A matching render hash means the intended
render reached the cluster; Ready at the current generation with the target image running means the
rollout is complete. Proven absence, a render main has not made yet and a failed read are different
facts: no failed read proves absence or completion. The one fact kept is when the change was first
seen at its end (``Job.reached``), so a later change can replace it without unsaying that it ran.

A ``Track`` presents that answer as labelled stages, the last one always the terminal stop
(``Live in prod``, ``Applied``, ``Removed from staging``), drawn from the start so the outcome is
known before it is reached. Output stays with the job as one chronological log.
"""
from __future__ import annotations

import datetime as dt
import hashlib
import io
import json
import logging
import re
import tarfile
import tempfile
import time
from dataclasses import dataclass, replace
from pathlib import Path
from typing import TYPE_CHECKING

import yaml

from swhurl import interface, yamlio
from swhurl.apps import calling, egress, events, helmrelease
from swhurl.apps.model import backup, render, store
from swhurl.apps.model.draft import Platform
from swhurl.apps.model.records import Settings
from swhurl.console import merge
from swhurl.console.actions import ActionError, Job
from swhurl.console.github import GitHub, GitHubAPI
from swhurl.run import Runner

if TYPE_CHECKING:
    from swhurl.console.cluster import AppRow

NEW_APP_JOB, FINISH_JOB = 'new app and repository', 'finish new app'
NEW_APP_ACTIONS = (NEW_APP_JOB, FINISH_JOB, 'new')  # the jobs, and the action in their pull request's branch
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
    """A job's whole path as the console shows it, including the stages before its merge; after the merge
    its status and outcome come from Progress through MERGED."""
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
    """An instance in one Git tree, as facts. No records (the default) is proven absence once the tree was read."""
    records: str = ''  # a hash of its own app.yaml, release.yaml and instance.yaml: the requested change
    settings: str = ''  # a hash of its instance.yaml and app.yaml: what the platform, not the image, decides
    digest: str = ''  # the image digest its release.yaml requests
    hash: str | None = None  # its HelmRelease's render hash; None: no HelmRelease; '': one from before render hashes
    rendered: str = ''  # the image digest its HelmRelease runs
    expected: str = ''  # render hash from this tree's complete inputs; '' before an allocation is made

    @property
    def pending(self) -> bool:
        """Records main has not rendered yet: a records-only merge before the Render workflow's commit. A held
        promotion or Settings-only edit can leave a healthy old HelmRelease with the same image digest."""
        return bool(self.records) and (self.hash is None or self.rendered != self.digest
                                       or (bool(self.hash) and self.hash != self.expected))

    def same(self, other: Target) -> bool:
        """The same render: the same records while either is unrendered, else the same render hash."""
        return self.records == other.records if self.pending or other.pending else self.hash == other.hash


def render_targets(runner: Runner, github: GitHub, revision: str) -> dict[str, Target]:
    """Immutable Git evidence, not a verdict. Re-render the complete public inputs to identify the
    intended output, including Settings and other instances' relationships. Encrypted files contribute
    only their presence, never their contents. No records means absent even beside stale output."""
    api = GitHubAPI(runner, github)
    started = time.monotonic()
    downloaded = extracted = None
    size = instances = 0
    phase = 'download'
    try:
        content = api.call('GET', f'/tarball/{revision}').content
        size = len(content)
        downloaded = time.monotonic()
        phase = 'extract'
        with tempfile.TemporaryDirectory(prefix='console-targets-') as tmp:
            root = Path(tmp)
            public = {calling.SIGNING_KEYS_FILE, events.CONTROLLER_KEY_FILE, events.CONSOLE_KEY_FILE, backup.MONGODB_JOB}
            proxy = False
            with tarfile.open(fileobj=io.BytesIO(content), mode='r:gz') as archive:
                for member in archive.getmembers():
                    path = Path(*Path(member.name).parts[1:])
                    if path.is_absolute() or '..' in path.parts:
                        raise ValueError('invalid archive path')
                    proxy |= egress.DIRECTORY in path.parents
                    if path == backup.MONGODB_JOB.parent or backup.MONGODB_JOB.parent in path.parents:
                        (root / backup.MONGODB_JOB.parent).mkdir(parents=True, exist_ok=True)  # present: its file must be read
                    if not member.isfile():
                        continue
                    instance = len(path.parts) == 4 and path.parts[0] == 'apps'
                    encrypted = instance and path.name in ('events.sops.yaml', 'secret.sops.yaml')
                    if (path in public or encrypted or
                            (len(path.parts) == 3 and path.parts[0] == 'apps' and path.name == 'app.yaml') or
                            (instance and path.name in ('instance.yaml', 'release.yaml', 'helmrelease.yaml'))):
                        target = root / path
                        target.parent.mkdir(parents=True, exist_ok=True)
                        target.write_bytes(b'' if encrypted else archive.extractfile(member).read())
            extracted = time.monotonic()
            phase = 'render'
            world, problems = store.world(root)
            if problems:
                raise ValueError('; '.join(problems))
            def text(path: Path) -> str:
                return (root / path).read_text().strip() if (root / path).is_file() else ''
            keys = text(calling.SIGNING_KEYS_FILE)
            # Do not use store.platform here: its scratch-root signing-key fallback is not a Git fact.
            platform_inputs = Platform(json.dumps(json.loads(keys), sort_keys=True, separators=(',', ':')) if keys else '',
                                       proxy, backups_suspended=backup.suspended(root))
            def digest(*parts: bytes) -> str:
                return hashlib.sha256(b'\0'.join(parts)).hexdigest()[:16]
            found = {}
            for each, entry in world.items():
                folder = each.folder(root)
                app, record = (folder.parent / 'app.yaml').read_bytes(), (folder / 'release.yaml').read_bytes()
                mine = creation_inputs(entry.settings)
                path = folder / 'helmrelease.yaml'
                release = yamlio.load(path.read_text()) if path.is_file() else None
                if path.is_file() and not isinstance(release, dict):
                    raise ValueError(f'{each}: malformed HelmRelease')
                expected = ''
                if not proxy or entry.settings.allocations.egress_port is not None:
                    output = render.instance(each, world, platform_inputs)[path.relative_to(root)][0]
                    expected = output['metadata']['annotations'][interface.RENDER_ANNOTATION]
                found[str(each)] = Target(
                    digest(app, record, mine), digest(app, mine), entry.release.image.digest,
                    (release.get('metadata', {}).get('annotations') or {}).get(interface.RENDER_ANNOTATION, '') if release else None,
                    helmrelease.image(release).get('digest', '') if release else '', expected)
            instances = len(found)
        phase = 'done'
        return found
    except (tarfile.TarError, OSError, ValueError, AttributeError, TypeError, KeyError, yaml.YAMLError) as error:
        raise ActionError(f'cannot read render targets: {error}') from None
    finally:
        finished = time.monotonic()
        logging.getLogger('swhurl.console.timing').info(
            'render targets revision=%s phase=%s bytes=%d instances=%d download_ms=%.1f extract_ms=%.1f render_ms=%.1f total_ms=%.1f',
            revision[:12], phase, size, instances,
            ((downloaded or finished) - started) * 1000,
            ((extracted or finished) - downloaded) * 1000 if downloaded is not None else 0,
            (finished - extracted) * 1000 if extracted is not None else 0, (finished - started) * 1000)
        api.client.close()

def creation_inputs(settings: Settings) -> bytes:
    """What identifies a change: every Settings field but the allocated port, which completes creation, not a choice."""
    return yamlio.dump(replace(settings, allocations=replace(settings.allocations, egress_port=None)).document()).encode()



@dataclass(frozen=True)
class Progress:
    """How far a merged change to an instance has got: the facts after its merge, never saved. ``applied`` is the
    one field named so: the intended render reached the cluster."""
    state: str  # REACHED; replaced, superseded, historical; rendering, creating, applying, removing; failing, held, unread
    detail: str = ''  # for replaced: when it was first seen at its end
    applied: bool = False  # Flux applied its render: the render hash matches and the HelmRelease is Ready at its generation
    newer: str = ''  # carried by main's newer release: its image digest, or '-' when only the render differs


REACHED = ('live', 'zero', 'removed')  # where a change ends; Job.reached records the first time it was seen there


def action_kind(action: str) -> str:
    """The stage vocabulary's action for a console branch action: v2's scale is still a scale."""
    return action.removeprefix('v2-')


def progress(action: str, merged: Target | None, main: Target | None, live: AppRow | str | None,
             reached: str = '') -> Progress:
    """How far the change ``action`` merged has got. ``merged`` and ``main`` are the instance in the merge
    commit's tree and in main's (``Target()`` when proven absent, None when the tree could not be read);
    ``live`` is its cluster row, ABSENT when the cluster was read without it, None when it could not be read.

    Main's render is this change's while main keeps the records it merged (a records-only merge rendered
    later, or another app's records changing what the platform derives for it), and carries it when only a
    newer release arrived (same settings; the Deploy workflow). A promotion chooses its release, so a newer
    one replaces it. ``reached`` is when the change was first seen at its end: a replaced change still ran."""
    if merged is None:
        return Progress('unread', 'the target or current render could not be read')
    if not merged.records and action != 'remove':  # its own merge has no records for it: from before records
        return Progress('historical', 'its merge commit has no records for this instance')
    if main is not None and merged.records and merged.records == main.records:
        merged = main
    if merged.hash == '' and not merged.pending:
        return Progress('historical', 'this change predates render hashes')
    if main is None:
        return Progress('unread', 'the target or current render could not be read')
    if main.hash == '' and not main.pending:
        return Progress('unread', 'the current render has no hash')
    newer = ''
    if not merged.same(main):
        if action == 'promote' or not merged.settings or merged.settings != main.settings:
            return Progress('replaced', reached) if reached else Progress('superseded')
        newer = main.digest if main.digest != merged.digest else '-'
        merged = main
    found = landing(action, live, merged)
    if not newer:
        return found
    if reached and found.state not in REACHED:  # it ran; what happens to the newer release is not this change's
        return Progress('replaced', reached)
    return replace(found, newer=newer)


def landing(action: str, live: AppRow | str | None, target: Target) -> Progress:
    """
    action = action_kind(action)How far the cluster has got to ``target``, main's render of the change."""
    if target.pending:
        return Progress('rendering', 'main has not rendered it yet')
    if live is None:
        return Progress('unread', 'the cluster could not be read')
    if action == 'remove':
        return Progress('removed') if not target.records and live == ABSENT else Progress('removing')
    if live == ABSENT:
        return Progress('creating', 'not created yet')
    if live.render_hash != target.hash:
        return Progress('applying', 'the target render is not applied yet')
    state, ready = live.render_state, live.render_ready
    if state.key == 'failing':
        return Progress('failing', state.detail, applied=ready)
    if state.key == 'suspended':
        return Progress('held', state.detail, applied=ready)
    if not ready:
        return Progress('applying', 'the HelmRelease is not Ready at its current generation')
    if state.key == 'stopped':
        return Progress('zero', applied=True)
    if state.key == 'healthy' and target.digest and live.image_matches and live.digest == target.digest:
        return Progress('live', applied=True)
    if state.key == 'unknown':
        return Progress('unread', state.detail or 'the instance could not be read', applied=True)
    return Progress('applying', state.detail or 'waiting for the target image to be Healthy', applied=True)


# How a merged change's Progress reads, keyed by Progress.state: (the Track status, the sentence under it, the
# note on its end once there or None). A lookup table, not a second state model.
MERGED = {'live': ('done', '{target} runs it{newer}.', ''),
          'zero': ('done', '{target} is applied and scaled to zero.', 'scaled to zero'),
          'removed': ('done', '{target} is removed.', ''),
          'replaced': ('done', '{end} from {seen}; a newer configuration has since replaced it.', 'then replaced'),
          'removing': ('running', 'Merged; Flux is removing {target}.', None),
          'creating': ('running', 'Merged; waiting for Flux to create {target}.', None),
          'rendering': ('running', 'Merged; waiting for the Render workflow to render it for {target}.', None),
          'applying': ('running', 'Merged; Flux is applying it to {target}.', None),
          'failing': ('blocked', 'Merged, but {target} is failing: open its page.', None),
          'held': ('waiting', 'Merged, but {target} is suspended: open its page.', None)}
STAGE_OF = {'done': 'done', 'running': 'running', 'blocked': 'failed', 'waiting': 'waiting'}  # a status as a stage's state


def number(job: Job) -> int | None:
    """The pull request a job opened, from its link."""
    found = PULL.search(job.link or '')
    return int(found[1]) if found else None


def error(job: Job) -> str:
    """Why a job stopped, from its output."""
    if job.state == 'interrupted':
        return 'the console restarted before it finished'
    said = next((line for line in reversed(job.lines) if line.startswith('[ERROR] ')), '')
    said = said.removeprefix('[ERROR] ')
    return said if len(said) <= NOTE else said[:NOTE].rstrip() + '…'


def prepared(job: Job) -> Stage:
    """The job's own stage; its output is one chronological log under it."""
    state = 'done' if job.state == 'succeeded' else 'running' if job.state == 'running' else 'failed'
    note = error(job) if state == 'failed' else 'PR opened' if state == 'done' and number(job) else ''
    return Stage('prepared', 'Prepared', state, note)


def end_label(action: str, target: str) -> str:
    action = action_kind(action)
    """Where a change to ``target`` ends once its pull request is merged and applied."""
    app, _, env = target.partition('/')
    if not env or target.startswith('team:'):
        return 'On main'
    return f'Removed from {env}' if action == 'remove' else f'Live in {env}'


def landing_stages(action: str, end: str) -> list[Stage]:
    action = action_kind(action)
    """The stages after a merge, not reached yet: Flux applying it and, unless it removes, the instance running it."""
    if end == 'On main':
        return []
    return [Stage('rollout', 'Applied', 'todo'), *([] if action == 'remove' else [Stage('running', 'Running', 'todo')])]


def seen(when: str) -> str:
    """When a change was first seen at its end, as words."""
    try:
        return dt.datetime.fromisoformat(when).strftime('%-d %b %H:%M UTC')
    except ValueError:
        return 'an earlier read'


def after_pull(pull: Pull, action: str, target: str, landed: Progress | None) -> tuple[list[Stage], str, str]:
    action = action_kind(action)
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
    rollout = landing_stages(action, end)
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
        if not instance:
            return track(merged, 'done', f'Merged {pull.when}.'.replace(' .', '.'), done, [Stage('end', end, 'reached')])
        found = landed or Progress('unread', 'it was not read')
        if found.state == 'historical':
            return track(merged, 'done', f'Merged {pull.when}; this change predates render hashes, so its rollout cannot be tracked.'.replace(' ;', ';'),
                         done, [Stage('end', 'Merged (historical)', 'reached')])
        if found.state == 'superseded':
            return track(merged, 'done', f'Merged {pull.when}; a newer configuration of {target} replaced it before it was seen '
                         f'{end[0].lower()}{end[1:]}.'.replace(' ;', ';'), done, [Stage('end', 'Superseded', 'reached')])
        if found.state not in MERGED:
            return track(merged, 'unknown', f'Merged; whether {target} runs it is not known: {found.detail}.', done)
        status, said, there = MERGED[found.state]
        ended = found.state in (*REACHED, 'replaced')
        got = found.applied or ended  # Flux applied it, whatever has happened since
        state, note = STAGE_OF[status], '' if ended else found.detail
        running = [] if action == 'remove' else [Stage('running', 'Running', state if got else 'todo', note)]
        newer = '' if not found.newer else ', with a newer image' if found.newer != '-' else ', with later changes from main'
        return track(merged, status, said.format(target=target, end=end, seen=seen(found.detail), newer=newer), done,
                     [Stage('rollout', 'Applied', 'done' if got else state, '' if got else note), *running,
                      planned if there is None else Stage('end', end, 'reached', there)])
    if pull.state == 'closed':
        # A new app's repository and image were made before its pull request: closing that leaves them.
        left = ('no instance was added, but the repository and image made for it still exist'
                if action in NEW_APP_ACTIONS else 'nothing changed')
        return track(Stage('merge', 'Closed without merging', 'stopped', pull.when, pull.url), 'stopped',
                     f'{name} was closed without merging: {left}.',
                     Stage('validate', 'Reviewed', 'todo'), [])
    return track(Stage('merge', 'Merged', 'todo', link=pull.url), 'unknown',
                 f'{name} could not be read from GitHub: open it there.')


def track(job: Job, pull: Pull | None = None, landed: Progress | None = None) -> Track:
    """The whole path of the change ``job`` started."""
    first = prepared(job)
    flux = job.action in ('reconcile', 'suspend', 'resume')
    if flux:
        end = 'Suspended' if job.action == 'suspend' else 'Applied'
    else:
        end = end_label(job.action, job.unit)
    # What follows the job, not reached while it runs or once it has failed.
    after = [] if flux else [Stage('validate', 'Reviewed', 'todo'), Stage('merge', 'Merged', 'todo'),
                             *landing_stages(job.action, end)]
    if job.state == 'running':
        ends = end if flux else f'{end}, once its pull request is merged'
        return Track((first, *after, Stage('end', end, 'planned')), 'running', f'Now: {first.label}. Ends: {ends}.')
    if job.state != 'succeeded':
        shown = Stage(first.key, first.label, first.state)  # the reason is the sentence under the track
        return Track((shown, *after, Stage('end', end, 'planned')), 'interrupted' if job.state == 'interrupted' else 'failed',
                     f'Stopped at {first.label}: {first.note or "see its output"}.')
    if flux:
        said = next((line for line in reversed(job.lines) if line.startswith('✔ applied revision ')), '')
        return Track((first, Stage('end', end, 'reached', said.removeprefix('✔ applied revision '))), 'done', f'{end}.')
    if pull is None:  # a dry run, or a job that had nothing to propose
        return Track((first, Stage('end', 'No pull request opened', 'reached')), 'done', 'No pull request was opened.')
    rest, status, outcome = after_pull(pull, job.action, job.unit, landed)
    return Track((first, *rest), status, outcome)


def target_of(branch: str) -> tuple[str, str]:
    """``(action, target)`` of a console pull request, from its branch (merge.parse); ``('', '')`` for any other."""
    found = merge.parse(branch)
    return (found[0], merge.target(*found)) if found else ('', '')


def pull_track(pull: Pull, branch: str) -> Track:
    """The path of an open pull request whose job is no longer remembered."""
    action, target = target_of(branch)
    rest, status, outcome = after_pull(pull, action, target, None)
    return Track((Stage('pull', f'Pull request #{pull.number}', 'done', link=pull.url), *rest), status, outcome)
