"""Activity's facts: replayed targets, current renders and live state, with no saved verdicts."""
import datetime as dt
import json
import unittest
from unittest import mock

from swhurl.apps.instance import Instance
from swhurl.apps.state import State
from swhurl.console import cluster, jobstore, stages
from swhurl.console.actions import Job, Jobs
from swhurl.run import FakeRunner

NOW = dt.datetime(2026, 10, 18, 12, tzinfo=dt.UTC)
URL = 'https://github.com/samclement/swhurl-platform/pull/9'
HEALTHY = State('healthy')


def job(action='promote', unit='web/prod', state='succeeded', lines=(), link=URL):
    return Job(1, action, unit, 'sam@swhurl.com', NOW, list(lines), state, None if state == 'running' else NOW,
               link if state == 'succeeded' else '')


class JobStageTests(unittest.TestCase):
    def test_preparation_is_the_job_and_output_is_one_chronological_log(self):
        running = job('new app and repository', 'web/staging', 'running', ['created repository', 'building'])
        track = stages.track(running)
        self.assertEqual((track.stages[0].label, track.stages[0].state, track.status), ('Prepared', 'running', 'running'))
        self.assertEqual(track.stages[-1].label, 'Live in staging')
        self.assertEqual(stages.prepared(running).state, 'running')
        failed = stages.track(job(state='failed', lines=['[ERROR] build failed']))
        self.assertEqual((failed.status, failed.outcome), ('failed', 'Stopped at Prepared: build failed.'))
        interrupted = stages.track(job(state='interrupted'))
        self.assertIn('the console restarted', interrupted.outcome)
        self.assertEqual(interrupted.status, 'interrupted')

    def test_flux_jobs_still_end_inside_the_console(self):
        done = stages.track(job('reconcile', 'infra-base', lines=['✔ applied revision main@sha1:abc'], link=''))
        self.assertEqual((done.label, done.stages[-1].note), ('Applied', 'main@sha1:abc'))
        self.assertEqual(stages.track(job('suspend', 'infra-base', link='')).label, 'Suspended')
        self.assertEqual(stages.track(job(link='')).label, 'No pull request opened')

    def test_only_job_facts_survive_restart_and_old_target_and_progress_are_ignored(self):
        prepared = job(lines=['x' * 900] * 40 + ['opened'])
        prepared.reached = NOW.isoformat()
        raw = jobstore.record(prepared, str)
        doc = json.loads(raw)
        self.assertNotIn('marks', doc)
        self.assertNotIn('ending', doc)
        doc.update(marks=[['build', 9]], ending={'state': 'live'}, target_hash='obsolete')
        kept = jobstore.parse(json.dumps(doc))
        self.assertEqual(kept.reached, NOW.isoformat())
        self.assertNotIn('target_hash', json.loads(raw))
        self.assertFalse(hasattr(kept, 'target_hash'))
        self.assertEqual(kept.lines[-1], 'opened')
        self.assertLess(len(kept.lines), 41)
        self.assertFalse(hasattr(kept, 'ending'))

    def test_a_running_jobs_link_is_saved_even_without_another_log_line(self):
        prepared = job(state='running')
        saved = []
        store = mock.Mock(owner='pod', load=mock.Mock(return_value=[]))
        store.save.side_effect = lambda j: saved.append(j.link)
        def advance(_seconds):
            if not saved:
                prepared.link = URL
            else:
                prepared.state = 'succeeded'
        jobs = Jobs(FakeRunner(), store=store, sleep=advance)
        jobs._keep(prepared)
        self.assertEqual(saved, [URL])


def target(hash='render-a', digest='sha256:a', settings='settings-1', records='records-1', rendered=None):
    """An instance rendered in a tree: its records, and a HelmRelease with that render hash."""
    return stages.Target(records, settings, digest, hash, digest if rendered is None else rendered, hash)


def unrendered(digest='sha256:b', settings='settings-1', records='records-1'):
    """Records merged without a render: a records-only merge before the Render workflow's commit."""
    return stages.Target(records, settings, digest)


TARGET, ABSENT = target(), stages.Target()


def row(state=HEALTHY, applied='render-a', ready=True, image=True, digest='sha256:a'):
    return cluster.AppRow(Instance('web', 'prod'), state, False, 'x', digest=digest, render_hash=applied,
                          render_ready=ready, render_state=state, image_matches=image)


class RenderProgressTests(unittest.TestCase):
    def progress(self, live=None, merged=TARGET, main=TARGET, action='promote', reached='', **seen):
        return stages.progress(action, merged, main, row(**seen) if live is None else live, reached)

    def test_a_newer_release_with_the_same_settings_carries_the_change_and_a_replaced_one_still_ran(self):
        mine = target('render-a', 'sha256:a', 'settings-1', 'records-1')
        deployed = target('render-b', 'sha256:b', 'settings-1', 'records-2')
        rescaled = target('render-c', 'sha256:a', 'settings-2', 'records-3')
        followed = self.progress(action='new', merged=mine, main=deployed, applied='render-b', digest='sha256:b')
        self.assertEqual((followed.state, followed.newer), ('live', 'sha256:b'), "a seed's own build 1 finishes its creation")
        self.assertEqual(self.progress(action='scale', merged=mine, main=deployed, applied='render-a').state, 'applying',
                         'the newer release must itself be applied')
        self.assertEqual(self.progress(action='scale', merged=mine, main=deployed, applied='render-b').state, 'applying',
                         "the running image is judged against main's render, not the merge commit's")
        self.assertEqual(self.progress(merged=mine, main=deployed, applied='render-b', digest='sha256:b').state, 'superseded',
                         'a promotion chooses its release: a newer one replaces it')
        self.assertEqual(self.progress(action='scale', merged=mine, main=rescaled).state, 'superseded', 'new settings replace it')
        seen = NOW.isoformat()
        replaced = self.progress(action='scale', merged=mine, main=rescaled, reached=seen)
        self.assertEqual((replaced.state, replaced.detail), ('replaced', seen), 'seen at its end, it ran')
        self.assertEqual(self.progress(action='scale', merged=mine, main=deployed, applied='render-a', reached=seen).state,
                         'replaced', 'once it ran, a newer image rolling out is not this change in progress')
        self.assertEqual(self.progress(action='scale', merged=target(settings=''), main=target('render-b', records='records-2',
                                       settings='')).state, 'superseded', 'a tree without settings is never followed')

    def test_main_rendering_this_changes_records_is_this_changes_render(self):
        """A merge commit's records still on main: main's render is this change's, whatever the commit held."""
        merged = unrendered('sha256:b')  # merged on GitHub, records only
        rendered = target('render-b', 'sha256:b')  # the Render workflow's commit
        stale = target('render-a', 'sha256:b', rendered='sha256:a')  # a held PR merged beside the old render
        self.assertTrue(merged.pending and stale.pending and not rendered.pending)
        found = self.progress(merged=merged, main=merged)
        self.assertEqual((found.state, found.detail), ('rendering', 'main has not rendered it yet'))
        self.assertEqual(self.progress(merged=stale, main=stale).state, 'rendering', 'a healthy old pod is not its render')
        for mine in (merged, stale):
            with self.subTest(merged=mine.hash):
                found = self.progress(merged=mine, main=rendered, applied='render-b', digest='sha256:b')
                self.assertEqual((found.state, found.newer), ('live', ''), 'not superseded by its own render')
        self.assertEqual(self.progress(merged=stale, main=rendered, applied='render-a').state, 'applying',
                         "main's render must itself be applied")
        callers = target('render-c', 'sha256:b')  # another app's call changed the render, not this instance's records
        self.assertEqual(self.progress(merged=rendered, main=callers, applied='render-c', digest='sha256:b').state, 'live',
                         "another app's records do not replace it")
        newer = target('render-d', 'sha256:c', records='records-2')  # a later release
        self.assertEqual(self.progress(merged=rendered, main=newer, applied='render-d', digest='sha256:c').state, 'superseded',
                         'a newer prod release replaces a promotion')
        self.assertEqual(self.progress(action='scale', merged=rendered, main=newer, applied='render-d', digest='sha256:c').state,
                         'live', 'a newer release carries a scale (same settings)')
        self.assertEqual(self.progress(merged=merged, main=newer).state, 'superseded',
                         'a promotion replaced before Render ran does not keep waiting for its render')
        self.assertEqual(self.progress(merged=merged, main=unrendered('sha256:c', records='records-2')).state, 'superseded',
                         'two pending renders are told apart by their records')
        self.assertEqual(self.progress(action='scale', merged=merged, main=unrendered('sha256:c', records='records-2')).state,
                         'rendering', "a newer release carries a scale, but main has not rendered it yet")
        self.assertEqual(self.progress(merged=merged, main=ABSENT).state, 'superseded',
                         'production removed before Render is proven absence, not an indefinitely pending render')
        self.assertEqual(self.progress(merged=merged, main=ABSENT, reached=NOW.isoformat()).state, 'replaced')

    def test_a_healthy_old_release_cannot_complete_a_new_change(self):
        self.assertEqual(self.progress(applied='old').state, 'applying')
        self.assertEqual(self.progress(ready=False).state, 'applying', 'a stale Ready condition is insufficient')
        self.assertEqual(self.progress(image=False).state, 'applying', 'hash application is not image rollout')
        self.assertEqual(self.progress(digest='sha256:old').state, 'applying', 'the running digest must be the target')
        found = self.progress()
        self.assertEqual((found.state, found.applied), ('live', True))
        self.assertEqual(self.progress(live=row(State('stopped'))).state, 'zero')

    def test_main_supersedes_an_unfinished_change_before_flux_applies_the_replacement(self):
        self.assertEqual(self.progress(main=target('render-b', 'sha256:b', records='records-2')).state, 'superseded')
        self.assertEqual(self.progress(main=ABSENT).state, 'superseded')
        self.assertEqual(self.progress(live=row(State('failing', 'crash'))).state, 'failing')
        self.assertEqual(self.progress(live=row(State('suspended'))).state, 'held')
        self.assertEqual(self.progress(live=row(State('unknown'))).state, 'unread')

    def test_remove_requires_git_absence_and_live_absence(self):
        self.assertEqual(self.progress(action='remove', merged=ABSENT, main=ABSENT).state, 'removing', 'Flux is still uninstalling')
        self.assertEqual(self.progress(stages.ABSENT, action='remove', merged=ABSENT, main=ABSENT).state, 'removed')
        self.assertEqual(self.progress(stages.ABSENT, action='remove', merged=ABSENT, main=TARGET).state, 'superseded')
        self.assertEqual(self.progress(stages.ABSENT, action='new').state, 'creating')

    def test_failed_reads_are_unknown_and_merged_pre_hash_jobs_are_finished_history(self):
        before_hashes = target('', '', records='records-0')  # main has since changed its records
        historical = self.progress(merged=before_hashes)
        self.assertEqual(historical.state, 'historical')
        self.assertEqual(stages.progress('promote', before_hashes, None, None).state, 'historical')
        track = stages.track(job(), stages.Pull(9, URL, state='merged'), historical)
        self.assertTrue(track.settled)
        self.assertEqual(track.label, 'Merged (historical)')
        self.assertIn('rollout cannot be tracked', track.outcome)
        for case in ({'merged': None}, {'main': None}, {'main': target('', '', records='records-2')}):
            with self.subTest(case=case):
                self.assertEqual(self.progress(**case).state, 'unread')
        # No failed read proves absence or completion.
        self.assertEqual(stages.progress('promote', TARGET, TARGET, None).state, 'unread', 'the cluster could not be read')
        for merged, main in ((None, ABSENT), (ABSENT, None)):
            with self.subTest(merged=merged, main=main):
                self.assertEqual(stages.progress('remove', merged, main, stages.ABSENT).state, 'unread', 'Git could not be read')
        self.assertEqual(stages.progress('remove', ABSENT, ABSENT, None).state, 'unread')


class PullStageTests(unittest.TestCase):
    def track(self, landed=None, action='promote', unit='web/prod', **pull):
        return stages.track(job(action, unit), stages.Pull(9, URL, **pull), landed)

    def test_open_review_validation_failure_and_auto_merge(self):
        self.assertEqual(self.track(state='open', checks='running', auto=True).status, 'running')
        manual = self.track(state='open', checks='passed', reason='held by you')
        self.assertEqual(manual.status, 'waiting')
        self.assertIn('merge #9 on GitHub (held by you)', manual.outcome)
        self.assertEqual(self.track(state='open', checks='failed', auto=True).status, 'blocked')
        self.assertIn('conflicts with main', self.track(state='open', checks='conflict').outcome)
        self.assertEqual(self.track().status, 'unknown')

    def test_merged_changes_show_applied_and_running_as_separate_facts(self):
        for state, status, label in (('live', 'done', 'Live in prod'), ('zero', 'done', 'Live in prod'),
                                     ('applying', 'running', 'In progress'), ('creating', 'running', 'In progress'),
                                     ('rendering', 'running', 'In progress'), ('removing', 'running', 'In progress'),
                                     ('failing', 'blocked', 'Failed'), ('held', 'waiting', 'Waiting for you'),
                                     ('unread', 'unknown', 'Unknown'), ('historical', 'done', 'Merged (historical)'), ('superseded', 'done', 'Superseded'),
                                     ('replaced', 'done', 'Live in prod')):
            with self.subTest(state=state):
                track = self.track(stages.Progress(state), state='merged')
                self.assertEqual((track.status, track.label), (status, label))
        replaced = self.track(stages.Progress('replaced', NOW.isoformat()), state='merged')
        self.assertEqual(replaced.outcome, 'Live in prod from 18 Oct 12:00 UTC; a newer configuration has since replaced it.')
        self.assertEqual((replaced.stages[-1].state, replaced.stages[-1].note), ('reached', 'then replaced'))
        self.assertIn('replaced it before it was seen live in prod', self.track(stages.Progress('superseded'), state='merged').outcome)
        self.assertIn('runs it, with a newer image', self.track(stages.Progress('live', newer='sha256:b'), state='merged').outcome)
        rendering = self.track(stages.Progress('rendering', 'main has not rendered it yet'), state='merged')
        self.assertIn('waiting for the Render workflow', rendering.outcome)
        self.assertEqual([(s.key, s.state, s.note) for s in rendering.stages[3:]],
                         [('rollout', 'running', 'main has not rendered it yet'), ('running', 'todo', 'main has not rendered it yet'),
                          ('end', 'planned', '')])
        applied = self.track(stages.Progress('failing', 'crash', applied=True), state='merged')
        self.assertEqual([(s.key, s.state) for s in applied.stages[3:5]], [('rollout', 'done'), ('running', 'failed')],
                         'applied and running are separate facts')
        removed = self.track(stages.Progress('removed'), action='remove', unit='web/staging', state='merged')
        self.assertEqual(removed.label, 'Removed from staging')
        team = self.track(action='team-add', unit='team:payments', state='merged')
        self.assertEqual(team.label, 'On main')

    def test_closed_new_app_keeps_repository_and_resume_is_not_confused_with_completion(self):
        closed = stages.Pull(9, URL, 'closed', when='1 h ago')
        self.assertIn('nothing changed', stages.track(job('scale', 'web/staging'), closed).outcome)
        for action in (stages.NEW_APP_JOB, stages.FINISH_JOB):
            self.assertIn('repository and image made for it still exist', stages.track(job(action, 'web/staging'), closed).outcome)
        track = stages.pull_track(stages.Pull(9, URL, 'open', 'passed', auto=True), 'console/promote-web-staging-abc1234')
        self.assertEqual(track.stages[-1].label, 'Live in prod')
        self.assertEqual(stages.target_of('console/new-my-app-staging-abc1234'), ('new', 'my-app/staging'))


class ReleaseReadinessTests(unittest.TestCase):
    def test_a_stale_ready_condition_cannot_complete_application(self):
        release = {'metadata': {'generation': 3}, 'status': {'conditions': [
            {'type': 'Ready', 'status': 'True', 'observedGeneration': 2}]}}
        self.assertFalse(cluster.release_ready(release))
        release['status']['conditions'][0]['observedGeneration'] = 3
        self.assertTrue(cluster.release_ready(release))
        release['status']['conditions'][0]['status'] = 'Unknown'
        self.assertFalse(cluster.release_ready(release))
        self.assertFalse(cluster.release_ready(None))
