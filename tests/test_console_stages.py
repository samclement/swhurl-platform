"""Activity's facts: replayed targets, current renders and live state, with no saved verdicts."""
import datetime as dt
import json
import unittest
from unittest import mock

from swhurl.apps.state import State
from swhurl.console import cluster, jobstore, stages
from swhurl.console.actions import Job, Jobs
from swhurl.run import FakeRunner

NOW = dt.datetime(2026, 10, 18, 12, tzinfo=dt.UTC)
URL = 'https://github.com/samclement/swhurl-platform/pull/9'
TARGET = stages.Target('render-a', 'sha256:a')
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

    def test_only_job_facts_and_target_survive_restart_and_old_progress_is_ignored(self):
        prepared = job(lines=['x' * 900] * 40 + ['opened'])
        prepared.target_hash, prepared.reached = 'hash-a', NOW.isoformat()
        raw = jobstore.record(prepared, str)
        doc = json.loads(raw)
        self.assertNotIn('marks', doc)
        self.assertNotIn('ending', doc)
        doc.update(marks=[['build', 9]], ending={'state': 'live'})
        kept = jobstore.parse(json.dumps(doc))
        self.assertEqual((kept.target_hash, kept.reached), ('hash-a', NOW.isoformat()))
        self.assertEqual(kept.lines[-1], 'opened')
        self.assertLess(len(kept.lines), 41)
        self.assertFalse(hasattr(kept, 'ending'))

    def test_a_running_jobs_target_is_saved_even_without_another_log_line(self):
        prepared = job(state='running')
        saved = []
        store = mock.Mock(owner='pod', load=mock.Mock(return_value=[]))
        store.save.side_effect = lambda j: saved.append(j.target_hash)
        def advance(_seconds):
            if not saved:
                prepared.target_hash = 'render-a'
            else:
                prepared.state = 'succeeded'
        jobs = Jobs(FakeRunner(), store=store, sleep=advance)
        jobs._keep(prepared)
        self.assertEqual(saved, ['render-a'])


class RenderProgressTests(unittest.TestCase):
    def judge(self, live=HEALTHY, target=TARGET, current=TARGET, applied='render-a', ready=True,
              image=True, action='promote', reached=''):
        return stages.judge(action, live, target, current, applied, ready, image, reached)

    def test_a_newer_release_with_the_same_settings_carries_the_change_and_a_replaced_one_still_ran(self):
        mine = stages.Target('render-a', 'sha256:a', 'settings-1')
        deployed = stages.Target('render-b', 'sha256:b', 'settings-1')
        rescaled = stages.Target('render-c', 'sha256:a', 'settings-2')
        followed = self.judge(action='new', target=mine, current=deployed, applied='render-b')
        self.assertEqual((followed.state, followed.newer), ('live', 'sha256:b'), "a seed's own build 1 finishes its creation")
        self.assertEqual(self.judge(action='scale', target=mine, current=deployed, applied='render-a').state, 'applying',
                         'the newer release must itself be applied')
        self.assertEqual(self.judge(target=mine, current=deployed, applied='render-b').state, 'superseded',
                         'a promotion chooses its release: a newer one replaces it')
        self.assertEqual(self.judge(action='scale', target=mine, current=rescaled).state, 'superseded', 'new settings replace it')
        seen = NOW.isoformat()
        replaced = self.judge(action='scale', target=mine, current=rescaled, reached=seen)
        self.assertEqual((replaced.state, replaced.detail), ('replaced', seen), 'seen at its end, it ran')
        self.assertEqual(self.judge(action='scale', target=mine, current=deployed, applied='render-a', reached=seen).state,
                         'replaced', 'once it ran, a newer image rolling out is not this change in progress')
        self.assertEqual(self.judge(action='scale', target=stages.Target('render-a', 'sha256:a'), current=stages.Target('render-b')).state,
                         'superseded', 'a tree without settings is never followed')

    def test_main_rendering_this_changes_records_is_this_changes_render(self):
        """A merge commit's records still on main: main's render is this change's, whatever the commit held."""
        merged = stages.Target(stages.UNRENDERED, 'sha256:b', 'settings-1', 'records-1')  # merged on GitHub, records only
        rendered = stages.Target('render-b', 'sha256:b', 'settings-1', 'records-1')  # the Render workflow's commit
        stale = stages.Target('render-a', 'sha256:a', 'settings-1', 'records-1')  # a held PR merged with the old render
        self.assertEqual(self.judge(target=merged, current=merged).state, 'applying', 'main has not rendered it yet')
        self.assertEqual(self.judge(target=merged, current=merged).detail, 'main has not rendered it yet')
        for target in (merged, stale):
            with self.subTest(target=target.hash):
                found = self.judge(target=target, current=rendered, applied='render-b')
                self.assertEqual((found.state, found.newer), ('live', ''), 'not superseded by its own render')
        self.assertEqual(self.judge(target=stale, current=rendered, applied='render-a').state, 'applying',
                         "main's render must itself be applied")
        callers = stages.Target('render-c', 'sha256:b', 'settings-1', 'records-1')  # another app's call changed the render
        self.assertEqual(self.judge(target=rendered, current=callers, applied='render-c').state, 'live',
                         "another app's records do not replace it")
        newer = stages.Target('render-d', 'sha256:c', 'settings-1', 'records-2')  # a later promotion
        self.assertEqual(self.judge(target=rendered, current=newer, applied='render-d').state, 'superseded',
                         'a newer prod release replaces a promotion')
        self.assertEqual(self.judge(action='scale', target=rendered, current=newer, applied='render-d').state, 'live',
                         'a newer release carries a scale (same settings)')
        self.assertEqual(stages.aim('promote', rendered, callers), callers)
        self.assertEqual(stages.aim('promote', rendered, newer), rendered)
        self.assertEqual(self.judge(target=merged, current=newer).state, 'superseded',
                         'a promotion replaced before Render ran does not keep waiting for its render')
        pending_newer = stages.Target(stages.UNRENDERED, 'sha256:c', 'settings-1', 'records-2')
        self.assertEqual(self.judge(target=merged, current=pending_newer).state, 'superseded',
                         'two pending renders share a sentinel, not their records')
        absent = stages.Target(stages.ABSENT)
        self.assertEqual(self.judge(target=merged, current=absent).state, 'superseded',
                         'production removed before Render is proven absence, not an indefinitely pending render')
        seen = NOW.isoformat()
        self.assertEqual(self.judge(target=merged, current=absent, reached=seen).state, 'replaced')
        no_records = stages.Target('render-a', 'sha256:a')
        self.assertEqual(self.judge(target=no_records, current=stages.Target('render-b', 'sha256:a')).state, 'superseded',
                         'a tree without records is never matched')

    def test_a_healthy_old_release_cannot_complete_a_new_change(self):
        self.assertEqual(self.judge(applied='old').state, 'applying')
        self.assertEqual(self.judge(ready=False).state, 'applying', 'a stale Ready condition is insufficient')
        self.assertEqual(self.judge(image=False).state, 'applying', 'hash application is not image rollout')
        self.assertEqual(self.judge().state, 'live')
        self.assertEqual(self.judge(live=State('stopped')).state, 'zero')

    def test_main_supersedes_an_unfinished_change_before_flux_applies_the_replacement(self):
        self.assertEqual(self.judge(current=stages.Target('render-b', 'sha256:b')).state, 'superseded')
        self.assertEqual(self.judge(current=stages.Target(stages.ABSENT)).state, 'superseded')
        self.assertEqual(self.judge(live=State('failing', 'crash')).state, 'failing')
        self.assertEqual(self.judge(live=State('suspended')).state, 'held')
        self.assertEqual(self.judge(live=State('unknown')).state, 'unread')

    def test_remove_requires_git_absence_and_live_absence(self):
        absent = stages.Target(stages.ABSENT)
        self.assertEqual(self.judge(action='remove', target=absent, current=absent).state, 'removing')
        self.assertEqual(self.judge(action='remove', target=absent, current=absent, live=stages.ABSENT).state, 'removed')
        self.assertEqual(self.judge(action='remove', target=absent, current=TARGET, live=stages.ABSENT).state, 'superseded')
        self.assertEqual(self.judge(action='new', live=stages.ABSENT).state, 'creating')

    def test_failed_reads_are_unknown_and_merged_pre_hash_jobs_are_finished_history(self):
        historical = self.judge(target=stages.Target(''))
        self.assertEqual(historical.state, 'historical')
        self.assertEqual(self.judge(target=stages.Target(''), current=None, live=None).state, 'historical')
        track = stages.track(job(), stages.Pull(9, URL, state='merged'), historical)
        self.assertTrue(track.settled)
        self.assertEqual(track.label, 'Merged (historical)')
        self.assertIn('rollout cannot be tracked', track.outcome)
        for changes in ({'live': None}, {'target': None}, {'current': None},
                        {'current': stages.Target('')}):
            with self.subTest(changes=changes):
                self.assertEqual(self.judge(**changes).state, 'unread')


class PullStageTests(unittest.TestCase):
    def track(self, rollout=None, action='promote', unit='web/prod', **pull):
        return stages.track(job(action, unit), stages.Pull(9, URL, **pull), rollout)

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
                                     ('failing', 'blocked', 'Failed'), ('held', 'waiting', 'Waiting for you'),
                                     ('unread', 'unknown', 'Unknown'), ('historical', 'done', 'Merged (historical)'), ('superseded', 'done', 'Superseded'),
                                     ('replaced', 'done', 'Live in prod')):
            with self.subTest(state=state):
                track = self.track(stages.Rollout(state), state='merged')
                self.assertEqual((track.status, track.label), (status, label))
        replaced = self.track(stages.Rollout('replaced', NOW.isoformat()), state='merged')
        self.assertEqual(replaced.outcome, 'Live in prod from 18 Oct 12:00 UTC; a newer configuration has since replaced it.')
        self.assertEqual((replaced.stages[-1].state, replaced.stages[-1].note), ('reached', 'then replaced'))
        self.assertIn('replaced it before it was seen live in prod', self.track(stages.Rollout('superseded'), state='merged').outcome)
        self.assertIn('runs it, with a newer image', self.track(stages.Rollout('live', newer='sha256:b'), state='merged').outcome)
        removed = self.track(stages.Rollout('removed'), action='remove', unit='web/staging', state='merged')
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
