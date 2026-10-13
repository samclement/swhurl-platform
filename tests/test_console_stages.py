import datetime as dt
import unittest

from swhurl.apps.state import State
from swhurl.console import jobstore, stages
from swhurl.console.actions import Job

NOW = dt.datetime(2026, 10, 13, 12, tzinfo=dt.UTC)
URL = 'https://github.com/samclement/swhurl-platform/pull/9'


def job(action='promote', unit='web/prod', state='succeeded', lines=(), marks=(), link=URL):
    return Job(1, action, unit, 'sam@swhurl.com', NOW, list(lines), state, None if state == 'running' else NOW,
               link if state == 'succeeded' else '', marks=list(marks))


def shape(track):
    return [(stage.label, stage.state) for stage in track.stages]


class JobStageTests(unittest.TestCase):
    def test_a_running_job_shows_where_it_is_and_where_it_will_end(self):
        track = stages.track(job('new app and repository', 'web/staging', 'running', ['a', 'b'], [('repo', 0), ('build', 1)]))
        self.assertEqual(shape(track), [('Create repository', 'done'), ('Build image', 'running'), ('Prepare change', 'todo'),
                                        ('Open pull request', 'todo'), ('Validate', 'todo'), ('Merge', 'todo'),
                                        ('Flux applies it', 'todo'), ('Live in staging', 'planned')])
        self.assertEqual((track.status, track.settled), ('running', False))
        self.assertEqual(track.outcome, 'Now: Build image. Ends: Live in staging, once its pull request is merged.')

    def test_a_failed_job_stops_at_its_stage_with_the_reason_and_keeps_the_planned_end(self):
        failed = job('new app and repository', 'web/staging', 'failed', ['x', '[ERROR] the run ended failure'], [('repo', 0), ('build', 1)])
        track = stages.track(failed)
        self.assertEqual(shape(track)[1:], [('Build image', 'failed'), ('Prepare change', 'todo'), ('Open pull request', 'todo'),
                                            ('Validate', 'todo'), ('Merge', 'todo'), ('Flux applies it', 'todo'),
                                            ('Live in staging', 'planned')])
        self.assertEqual((track.status, track.outcome), ('failed', 'Stopped at Build image: the run ended failure.'))
        interrupted = stages.track(job('reconcile', 'infra-base', 'interrupted', ['a'], [('fetch', 0)]))
        self.assertEqual((interrupted.label, interrupted.settled), ('Interrupted', True))
        self.assertIn('the console restarted', interrupted.outcome)
        unmarked = stages.track(job(state='failed', lines=['[ERROR] ' + 'x' * 300]))  # a record from before marks were kept
        self.assertEqual(shape(unmarked)[0], ('Prepare change', 'failed'))
        self.assertLess(len(unmarked.outcome), 200)

    def test_flux_and_label_jobs_end_inside_the_console(self):
        done = stages.track(job('reconcile', 'infra-base', lines=['✔ applied revision main@sha1:abc'], link=''))
        self.assertEqual(shape(done), [('Fetch source', 'done'), ('Apply unit', 'done'), ('Applied', 'reached')])
        self.assertEqual((done.stages[-1].note, done.status, done.label), ('main@sha1:abc', 'done', 'Applied'))
        self.assertEqual(stages.track(job('suspend', 'infra-base', link='')).label, 'Suspended')
        held = stages.track(job('hold promotion PR', 'PR #9'))
        self.assertEqual(shape(held), [('Change its label', 'done'), ('Held for you to merge', 'reached')])
        self.assertEqual(stages.track(job(link='')).label, 'No pull request opened')

    def test_output_is_grouped_under_the_stage_that_wrote_it(self):
        running = job('new app and repository', 'web/staging', 'running', ['r1', 'r2', 'b1'], [('repo', 0), ('build', 2)])
        self.assertEqual([(s.stage.key, s.lines, s.started) for s in stages.sections(running)],
                         [('repo', ('r1', 'r2'), True), ('build', ('b1',), True), ('prepare', (), False), ('open', (), False)])
        self.assertEqual([stages.stage_of(running, i) for i in range(3)], ['repo', 'repo', 'build'])
        early = job(lines=['checked staging', 'Downloading'], marks=[('prepare', 1)])
        self.assertEqual(stages.sections(early)[0].lines, ('checked staging', 'Downloading'), 'what came before the first stage is its')
        self.assertEqual(stages.sections(job(lines=['a']))[0].lines, ('a',))

    def test_marks_survive_the_store_and_follow_output_cut_from_the_front(self):
        long = job('new app and repository', 'web/staging', lines=['x' * 900] * 40 + ['built'], marks=[('repo', 0), ('build', 40)])
        kept = jobstore.parse(jobstore.record(long, str))
        self.assertLess(len(kept.lines), 41)
        self.assertEqual(stages.sections(kept)[1].lines, ('built',))
        self.assertEqual(jobstore.parse(jobstore.record(job(), str)).marks, [])


class PullStageTests(unittest.TestCase):
    def track(self, rollout=None, action='promote', unit='web/prod', **pull):
        return stages.track(job(action, unit), stages.Pull(9, URL, **pull), rollout)

    def test_an_open_pull_request_says_who_merges_it(self):
        auto = self.track(state='open', checks='running', auto=True)
        self.assertEqual(shape(auto)[2:], [('Validate', 'running'), ('Merge', 'todo'), ('Flux applies it', 'todo'), ('Live in prod', 'planned')])
        self.assertEqual((auto.status, auto.outcome), ('running', '#9 merges itself once its checks pass, then live in prod.'))
        self.assertEqual(shape(self.track(state='open', checks='passed', auto=True))[3], ('Merge', 'running'))
        manual = self.track(state='open', checks='passed', reason='held by you')
        self.assertEqual(shape(manual)[2:4], [('Validate', 'done'), ('Merge', 'waiting')])
        self.assertEqual((manual.status, manual.outcome), ('waiting', 'Waiting for you: merge #9 on GitHub (held by you); then live in prod.'))
        failed = self.track(state='open', checks='failed', auto=True)
        self.assertEqual((failed.status, failed.label, failed.settled), ('blocked', 'Failed', False), 'open: someone still has to act')
        self.assertIn('conflicts with main', self.track(state='open', checks='conflict', auto=True).outcome)
        team = self.track(action='team-add', unit='team:payments', state='open')
        self.assertEqual(shape(team)[-1], ('On main', 'planned'))
        self.assertTrue(team.outcome.endswith('it is then on main.'))

    def test_a_merged_pull_request_ends_where_its_rollout_got(self):
        R = stages.Rollout
        cases = ((R('live'), 'done', ('Flux applies it', 'done'), ('Live in prod', 'reached')),
                 (R('zero'), 'done', ('Flux applies it', 'done'), ('Live in prod', 'reached')),
                 (R('applying', 'rolling out'), 'running', ('Flux applies it', 'running'), ('Live in prod', 'planned')),
                 (R('creating'), 'running', ('Flux applies it', 'running'), ('Live in prod', 'planned')),
                 (R('failing', 'crash'), 'blocked', ('Flux applies it', 'failed'), ('Live in prod', 'planned')),
                 (R('held', '', 'suspended'), 'waiting', ('Flux applies it', 'waiting'), ('Live in prod', 'planned')),
                 (R('unread', 'the cluster could not be read'), 'unknown', ('Flux applies it', 'todo'), ('Live in prod', 'planned')),
                 (None, 'unknown', ('Flux applies it', 'todo'), ('Live in prod', 'planned')))
        for rollout, status, flux, end in cases:
            with self.subTest(rollout=rollout):
                track = self.track(rollout, state='merged', when='2 min ago')
                self.assertEqual((track.status, shape(track)[-2], shape(track)[-1]), (status, flux, end))
                self.assertEqual(track.stages[3].note, 'merged 2 min ago')
        self.assertEqual(self.track(R('live'), state='merged').label, 'Live in prod')
        failing = self.track(R('failing', 'crash'), state='merged')
        self.assertEqual((failing.label, failing.settled), ('Failed', False), 'a failing rollout is not over')
        removed = self.track(R('removed'), action='remove', unit='web/staging', state='merged')
        self.assertEqual((removed.status, removed.label), ('done', 'Removed from staging'))
        self.assertEqual(self.track(R('removing'), action='remove', unit='web/staging', state='merged').status, 'running')
        team = self.track(action='team-add', unit='team:payments', state='merged', when='1 h ago')
        self.assertEqual((team.status, team.label), ('done', 'On main'))

    def test_a_superseded_merge_ends_on_main(self):
        old = self.track(stages.Rollout('superseded'), state='merged', when='3 days ago')
        self.assertEqual((old.status, shape(old)[-1]), ('done', ('Merged to main', 'reached')))
        self.assertEqual(old.outcome, 'Merged 3 days ago; a later change to web/prod has been applied since.')

    def test_a_rollout_is_judged_against_the_merge_not_the_instance_alone(self):
        judge = stages.judge
        healthy, failing = State('healthy'), State('failing', 'crash')
        self.assertEqual(judge('scale', healthy, True).state, 'live')
        self.assertEqual(judge('scale', State('stopped'), True).state, 'zero')
        self.assertEqual(judge('scale', healthy, False).state, 'applying', 'healthy on what ran before the merge')
        self.assertEqual(judge('scale', failing, False).state, 'applying', 'failing before the merge is not its doing')
        self.assertEqual(judge('scale', failing, True), stages.Rollout('failing', 'crash'))
        self.assertEqual(judge('scale', failing, True, superseded=True).state, 'superseded', 'a later change is failing')
        self.assertEqual(judge('scale', State('updating', 'x'), True).state, 'applying')
        self.assertEqual(judge('scale', State('suspended', 'x'), True), stages.Rollout('held', 'x', 'suspended'))
        self.assertEqual(judge('scale', State('unknown', 'no pods read'), True).state, 'unread')
        self.assertEqual(judge('new app and repository', stages.ABSENT, True).state, 'creating')
        self.assertEqual(judge('new app and repository', stages.ABSENT, True, superseded=True).state, 'superseded', 'added, then removed')
        self.assertEqual(judge('remove', stages.ABSENT, True).state, 'removed')
        self.assertEqual(judge('remove', stages.ABSENT, False).state, 'removing', 'Flux has not fetched the merge')
        self.assertEqual(judge('remove', healthy, False).state, 'removing')
        self.assertEqual(judge('remove', healthy, True, superseded=True).state, 'superseded', 'removed, then added again')
        self.assertEqual(judge('scale', None, None).state, 'unread')
        self.assertEqual(judge('scale', healthy, None).state, 'unread')
        self.assertEqual({s for s in stages.ENDINGS}, {'live', 'zero', 'removed', 'superseded'}, 'failing is never kept: it can recover')

    def test_an_ending_survives_the_store(self):
        kept = jobstore.parse(jobstore.record(Job(1, 'scale', 'web/prod', 'x', NOW, state='succeeded', ending={'state': 'live'}), str))
        self.assertEqual(kept.ending, {'state': 'live'})
        self.assertIsNone(jobstore.parse(jobstore.record(job(), str)).ending)

    def test_closed_or_unreadable(self):
        closed = self.track(state='closed', when='1 h ago')
        self.assertEqual((closed.status, shape(closed)[-1]), ('stopped', ('Closed without merging', 'stopped')))
        self.assertEqual(self.track().status, 'unknown')

    def test_closing_a_new_apps_pull_request_leaves_its_repository(self):
        closed = stages.Pull(9, URL, 'closed', when='1 h ago')
        self.assertIn('nothing changed', stages.track(job('scale', 'web/staging'), closed).outcome)
        for action in (stages.NEW_APP_JOB, stages.FINISH_JOB):
            self.assertEqual(stages.track(job(action, 'web/staging'), closed).outcome, '#9 was closed without merging: no instance '
                             'was added, but the repository and image made for it still exist.')

    def test_stages_that_start_at_the_same_line_keep_the_output(self):
        cut = job('new app and repository', 'web/staging', lines=['… earlier output is not kept', 'b', 'p', 'o'],
                  marks=[('repo', 0), ('build', 0), ('prepare', 2), ('open', 3)])
        self.assertEqual([(s.stage.key, s.lines) for s in stages.sections(cut)],
                         [('repo', ()), ('build', ('… earlier output is not kept', 'b')), ('prepare', ('p',)), ('open', ('o',))])

    def test_a_pull_request_without_its_job(self):
        track = stages.pull_track(stages.Pull(9, URL, 'open', 'passed', auto=True), 'console/promote-web-staging-abc1234')
        self.assertEqual(shape(track)[0], ('Pull request #9', 'done'))
        self.assertEqual(shape(track)[-1], ('Live in prod', 'planned'))
        self.assertEqual(stages.target_of('console/new-my-app-staging-abc1234'), ('new', 'my-app/staging'))
        self.assertEqual(stages.target_of('console/team-add-payments-abc1234'), ('', ''))


if __name__ == '__main__':
    unittest.main()
