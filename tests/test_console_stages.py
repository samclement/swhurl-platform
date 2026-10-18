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

    def test_flux_and_label_jobs_still_end_inside_the_console(self):
        done = stages.track(job('reconcile', 'infra-base', lines=['✔ applied revision main@sha1:abc'], link=''))
        self.assertEqual((done.label, done.stages[-1].note), ('Applied', 'main@sha1:abc'))
        self.assertEqual(stages.track(job('suspend', 'infra-base', link='')).label, 'Suspended')
        self.assertEqual(stages.track(job('hold promotion PR', 'PR #9')).label, 'Held for you to merge')
        self.assertEqual(stages.track(job(link='')).label, 'No pull request opened')

    def test_only_job_facts_and_target_survive_restart_and_old_progress_is_ignored(self):
        prepared = job(lines=['x' * 900] * 40 + ['opened'])
        prepared.target_hash = 'hash-a'
        raw = jobstore.record(prepared, str)
        doc = json.loads(raw)
        self.assertNotIn('marks', doc)
        self.assertNotIn('ending', doc)
        doc.update(marks=[['build', 9]], ending={'state': 'live'})
        kept = jobstore.parse(json.dumps(doc))
        self.assertEqual(kept.target_hash, 'hash-a')
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
              image=True, action='promote'):
        return stages.judge(action, live, target, current, applied, ready, image)

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
                                     ('unread', 'unknown', 'Unknown'), ('historical', 'done', 'Merged (historical)'), ('superseded', 'done', 'Superseded')):
            with self.subTest(state=state):
                track = self.track(stages.Rollout(state), state='merged')
                self.assertEqual((track.status, track.label), (status, label))
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
