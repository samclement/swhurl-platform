"""Reviewer state: validation, pruning, size ceiling, budgets and ConfigMap access."""
from __future__ import annotations

import copy
import json
import unittest

from swhurl.incident_review import allowlist, fingerprint, prefilter, state
from swhurl.incident_review.errors import ReviewFailure
from swhurl.run import FakeRunner

NOW = 1791342000.0  # 2026-10-07T02:20:00Z
DEFAULTS = allowlist.current().defaults
GET = ('kubectl', '-n', 'incident-review', 'get', 'configmap', 'incident-review-state', '-o', 'json')
PATCH = ('kubectl', '-n', 'incident-review', 'patch', 'configmap', 'incident-review-state')


def incident(**changes):
    base = {'app': 'hello-ts', 'env': 'staging', 'signal': 'error-logs', 'first_seen': NOW, 'last_seen': NOW,
            'last_analysis': 0, 'cooldown_until': 0, 'status': 'seen', 'coverage': 'alert-gap', 'cli': '',
            'model': '', 'notified': 0, 'pr': None, 'check': None}
    return {**base, **changes}


class StateTests(unittest.TestCase):
    def reason(self, call, *args, **kwargs):
        with self.assertRaises(ReviewFailure) as caught:
            call(*args, **kwargs)
        return caught.exception.reason

    def test_first_run_reads_empty_state_and_absent_key_is_not_corrupt(self):
        for doc in ({}, {'data': {}}, {'data': {'state.json': ''}}):
            runner = FakeRunner().on(*GET, stdout=json.dumps(doc))
            self.assertEqual(state.read_state(runner), state.empty_state())

    def test_state_saved_before_last_window_existed_still_reads(self):
        old = {k: v for k, v in state.empty_state().items() if k != 'last_window'}
        runner = FakeRunner().on(*GET, stdout=json.dumps({'data': {'state.json': json.dumps(old)}}))
        self.assertEqual(state.read_state(runner), state.empty_state())

    def test_corrupt_or_unreadable_state_stops_and_is_never_reset(self):
        good = state.empty_state()
        bad = [
            '{not json', '[]', json.dumps({**good, 'version': 2}), json.dumps({**good, 'extra': 1}),
            json.dumps({**good, 'incidents': {'f': incident(status='invented')}}),
            json.dumps({**good, 'incidents': {'f': incident(first_seen='yesterday')}}),
            json.dumps({**good, 'incidents': {'f': {**incident(), 'body': 'log text'}}}),
            json.dumps({**good, 'baselines': {'hello-ts/staging/error-logs': [1, 'x']}}),
            json.dumps({**good, 'spend': {**good['spend'], 'cents': -1}}),
            json.dumps({**good, 'pending': [{'title': 'no key'}]}),
            json.dumps({**good, 'last_result': 'fine'}),
        ]
        for raw in bad:
            with self.subTest(raw=raw[:40]):
                runner = FakeRunner().on(*GET, stdout=json.dumps({'data': {'state.json': raw}}))
                self.assertEqual(self.reason(state.read_state, runner), 'state-corrupt')
                self.assertEqual(len(runner.calls), 1)
        self.assertEqual(self.reason(state.read_state, FakeRunner().on(*GET, returncode=1)), 'state-unavailable')

    def test_save_prunes_then_patches_only_the_state_key(self):
        current = state.empty_state()
        current['incidents'] = {'old': incident(last_seen=NOW - 31 * 86400), 'recent': incident(last_seen=NOW - 86400)}
        current['baselines'] = {'hello-ts/staging/error-logs': list(range(40))}
        current['seen'] = {'expired': NOW - 1, 'live': NOW + 60}
        runner = FakeRunner().on(*PATCH)
        state.save_state(runner, current, now=NOW, defaults=DEFAULTS)
        self.assertEqual(runner.calls[0][-1], '--field-manager=incident-review')
        self.assertEqual(list(current['incidents']), ['recent'])
        self.assertEqual(current['baselines']['hello-ts/staging/error-logs'], list(range(16, 40)))
        self.assertEqual(current['seen'], {'live': NOW + 60})
        self.assertEqual(current['spend']['month'], '2026-10')

    def test_state_over_the_ceiling_after_pruning_is_a_failure_and_nothing_is_written(self):
        current = state.empty_state()
        current['incidents'] = {f'{n:024d}': incident() for n in range(400)}
        runner = FakeRunner()
        self.assertEqual(self.reason(state.save_state, runner, current, now=NOW, defaults=DEFAULTS), 'state-size')
        self.assertEqual(runner.calls, [])
        self.assertEqual(self.reason(state.save_state, FakeRunner().on(*PATCH, returncode=1), state.empty_state(),
                                     now=NOW, defaults=DEFAULTS), 'state-unavailable')

    def test_spend_counters_roll_over_and_block_at_the_approved_limits(self):
        current = state.empty_state()
        self.assertIsNone(state.budget_block(current, DEFAULTS, NOW))
        current['spend'].update(cents=1600, analyses=32, analyses_today=1)
        self.assertEqual(state.budget_block(current, DEFAULTS, NOW), 'monthly')
        current['spend'].update(cents=100, analyses_today=4)
        self.assertEqual(state.budget_block(current, DEFAULTS, NOW), 'daily')
        self.assertIsNone(state.budget_block(current, DEFAULTS, NOW + 86400))
        self.assertEqual(current['spend']['cents'], 100)
        current['spend'].update(cents=1600)
        self.assertIsNone(state.budget_block(current, DEFAULTS, NOW + 31 * 86400))
        self.assertEqual((current['spend']['month'], current['spend']['cents']), ('2026-11', 0))

    def test_baseline_mean(self):
        current = state.empty_state()
        self.assertEqual(state.baseline_mean(current, 'a/b/c'), 0)
        current['baselines']['a/b/c'] = [0, 4, 8]
        self.assertEqual(state.baseline_mean(current, 'a/b/c'), 4)

    def test_one_failure_over_two_sweeps_is_one_fingerprint_and_one_model_call(self):
        identity = {'repository': 'samclement/hello-ts', 'app': 'hello-ts', 'env': 'staging',
                    'signal': 'error-logs', 'key': 'hello-ts RangeError'}
        current = state.empty_state()
        calls = []
        for sweep in range(2):
            now = NOW + sweep * 3600
            finding = {'fingerprint': fingerprint(identity), 'signal': 'error-logs', 'count': 6, 'baseline_mean': 0}
            decision = prefilter(finding, current, DEFAULTS, now)
            calls.append(decision['reason'])
            if decision['fire']:
                current['incidents'][finding['fingerprint']] = incident(
                    last_analysis=now, cooldown_until=now + DEFAULTS['cooldown_hours'] * 3600, status='notified')
        self.assertEqual(calls, ['error-rate-change', 'repeat-inside-cooldown'])  # rule order: rate before new
        self.assertEqual(len(current['incidents']), 1)
        after = copy.deepcopy(current)
        self.assertEqual(state.validate(after), current)


if __name__ == '__main__':
    unittest.main()
