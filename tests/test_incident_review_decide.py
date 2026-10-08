"""The decide step: one decision, one state change and one exit code for every kind of run."""
from __future__ import annotations

import copy
import json
import tempfile
import unittest
from pathlib import Path

from swhurl.incident_review import allowlist, collect, decide, state
from swhurl.notifications.errors import NotificationError
from swhurl.run import FakeRunner

FIXTURE = json.loads((Path(__file__).parent / 'fixtures/incident-review/sweep.json').read_text())
NOW = FIXTURE['now']
ALLOW = allowlist.current()
GET = ('kubectl', '-n', 'incident-review', 'get', 'configmap', 'incident-review-state', '-o', 'json')
PATCH = ('kubectl', '-n', 'incident-review', 'patch', 'configmap', 'incident-review-state')
REPORT, BUNDLE = collect.collect(allow=ALLOW, current=state.empty_state(),
                                 telemetry=collect.FixtureTelemetry(FIXTURE['telemetry']),
                                 images={('hello-ts-staging', 'hello-ts'): {'tag': '35-54d7918', 'digest': 'sha256:abc'}},
                                 now=NOW)
OK = FIXTURE['analysis']['status']
DIAGNOSIS = FIXTURE['analysis']['diagnosis']
FINGERPRINT = BUNDLE['fingerprint']


def apply(report=REPORT, bundle=BUNDLE, analysis=OK, diagnosis=DIAGNOSIS, current=None, now=NOW):
    current = current if current is not None else state.empty_state()
    message = diagnosis if isinstance(diagnosis, str) or diagnosis is None else json.dumps(diagnosis)
    outcome = decide.decide(allow=ALLOW, current=current, report=copy.deepcopy(report), bundle=copy.deepcopy(bundle),
                            analysis=copy.deepcopy(analysis), last_message=message, now=now)
    return outcome, current


class DecideTests(unittest.TestCase):
    def test_every_run_shape_maps_to_one_result_state_change_and_exit_code(self):
        quiet = {**REPORT, 'status': 'quiet', 'findings': []}
        cases = [
            # name, arguments, (result, reason, exit), incident status, analyses charged, pending titles
            ('quiet', {'report': quiet, 'bundle': None, 'analysis': None, 'diagnosis': None},
             ('quiet', None, 0), None, 0, []),
            ('notified', {}, ('analysed', 'notified', 0), 'notified', 1, ['hello-ts/staging diagnosis']),
            ('no change', {'diagnosis': {**DIAGNOSIS, 'recommend_no_change': True}},
             ('analysed', 'no-change', 0), 'no-change', 1, []),
            ('low confidence', {'diagnosis': {**DIAGNOSIS, 'confidence': 0.69}},
             ('analysed', 'low-confidence', 0), 'suppressed-low-confidence', 1, []),
            ('uncited', {'diagnosis': {**DIAGNOSIS, 'evidence_refs': ['logs[99]']}},
             ('failed', 'schema', 1), 'suppressed-uncited', 1, ['incident review failed']),
            ('malformed', {'diagnosis': '{not json'}, ('failed', 'schema', 1), 'no-diagnosis', 1,
             ['incident review failed']),
            ('extra field', {'diagnosis': {**DIAGNOSIS, 'run': 'rm -rf'}}, ('failed', 'schema', 1), 'no-diagnosis', 1,
             ['incident review failed']),
            ('oversized', {'diagnosis': 'x' * 64_001}, ('failed', 'schema', 1), 'no-diagnosis', 1,
             ['incident review failed']),
            ('no output', {'diagnosis': None}, ('failed', 'schema', 1), 'no-diagnosis', 1, ['incident review failed']),
            ('timeout', {'analysis': {**OK, 'status': 'timeout'}, 'diagnosis': None},
             ('failed', 'timeout', 1), 'no-diagnosis', 1, ['incident review failed']),
            ('provider error', {'analysis': {**OK, 'status': 'error'}, 'diagnosis': None},
             ('failed', 'provider', 1), 'no-diagnosis', 1, ['incident review failed']),
            ('collect only', {'analysis': {**OK, 'status': 'disabled'}, 'diagnosis': None},
             ('collect-only', None, 0), None, 0, []),
            ('skipped with a bundle', {'analysis': {**OK, 'status': 'skipped'}}, ('failed', 'contract', 1), None, 0,
             ['incident review failed']),
            ('missing analysis status', {'analysis': None}, ('failed', 'contract', 1), None, 0,
             ['incident review failed']),
            ('unknown analysis status', {'analysis': {**OK, 'status': 'great'}}, ('failed', 'contract', 1), None, 0,
             ['incident review failed']),
            ('missing bundle', {'bundle': None}, ('failed', 'contract', 1), None, 0, ['incident review failed']),
            ('bundle for another incident', {'bundle': {k: v for k, v in {**BUNDLE, 'key': 'other'}.items()
                                                        if k != 'fingerprint'}},
             ('failed', 'contract', 1), None, 0, ['incident review failed']),
            ('missing report', {'report': None}, ('failed', 'contract', 1), None, 0, ['incident review failed']),
            ('wrong report version', {'report': {**REPORT, 'version': 2}}, ('failed', 'contract', 1), None, 0,
             ['incident review failed']),
            ('collector failed', {'report': {**quiet, 'status': 'failed', 'reason': 'query'}, 'bundle': None},
             ('failed', 'query', 1), None, 0, ['incident review failed']),
            ('collector failed with free text', {'report': {**quiet, 'status': 'failed', 'reason': 'DROP TABLE'}},
             ('failed', 'contract', 1), None, 0, ['incident review failed']),
            ('daily budget', {'report': {**REPORT, 'status': 'budget', 'budget': 'daily'}, 'bundle': None},
             ('budget', 'daily', 0), None, 0, []),
            ('monthly budget', {'report': {**REPORT, 'status': 'budget', 'budget': 'monthly'}, 'bundle': None},
             ('budget', 'monthly', 0), None, 0, ['incident review paused: budget']),
        ]
        for name, arguments, expected, status, charged, titles in cases:
            with self.subTest(case=name):
                outcome, current = apply(**arguments)
                self.assertEqual((outcome['result'], outcome['reason'], outcome['exit']), expected)
                incident = current['incidents'].get(FINGERPRINT)
                self.assertEqual(incident and incident['status'], status)
                self.assertEqual(current['spend']['analyses'], charged)
                self.assertEqual(current['spend']['cents'], charged * 50)
                self.assertEqual([n['title'] for n in current['pending']], titles)
                self.assertEqual(current['last_result'], expected[0])
                self.assertEqual(current['last_sweep'], NOW)
                state.validate(current)

    def test_notification_is_built_from_validated_fields_only(self):
        _, current = apply()
        notice = current['pending'][0]
        self.assertEqual((notice['key'], notice['priority'], notice['history']),
                         (f'diagnosis:{FINGERPRINT}', 3, 86400))
        for expected in ('GET /repeat answers 500', 'Confidence: 86%', 'Signal: error-logs, 6 in the window',
                         'Evidence: error RangeError: request failed', 'Evidence: span GET Error 500',
                         'Image: 35-54d7918', 'Code fix attempted: no', 'trigger: sweep'):
            self.assertIn(expected, notice['message'])
        self.assertNotIn('click', notice)
        incident = current['incidents'][FINGERPRINT]
        self.assertEqual((incident['cooldown_until'], incident['cli'], incident['model'], incident['notified']),
                         (NOW + 86400, 'fixture-cli', 'fixture-model', NOW))
        self.assertNotIn('RangeError: Invalid count', json.dumps(current['incidents']))

    def test_secret_shaped_model_text_is_redacted_before_it_is_queued(self):
        leaky = {**DIAGNOSIS, 'summary': 'Leaked token=abc123 in the handler.'}
        _, current = apply(diagnosis=leaky)
        self.assertNotIn('abc123', current['pending'][0]['message'])

    def test_baselines_grow_and_known_incidents_are_refreshed(self):
        _, current = apply()
        self.assertEqual(current['baselines']['hello-ts/staging/error-logs'], [6])
        later = {**REPORT, 'status': 'quiet',
                 'findings': [{**f, 'decision': 'quiet', 'reason': 'repeat-inside-cooldown'} for f in REPORT['findings']]}
        outcome, current = apply(report=later, bundle=None, analysis=None, diagnosis=None, current=current,
                                 now=NOW + 3600)
        self.assertEqual(outcome['exit'], 0)
        self.assertEqual(current['baselines']['hello-ts/staging/error-logs'], [6], 'the same window counts once')
        next_hour = {**later, 'window': {'start': '2026-10-07T03:00:00+00:00', 'end': '2026-10-07T04:00:00+00:00'}}
        _, current = apply(report=next_hour, bundle=None, analysis=None, diagnosis=None, current=current,
                           now=NOW + 3700)
        self.assertEqual(current['baselines']['hello-ts/staging/error-logs'], [6, 6])
        self.assertEqual(current['incidents'][FINGERPRINT]['last_seen'], NOW + 3700)
        self.assertEqual(len(current['incidents']), 1)

    def test_failure_and_diagnosis_messages_are_deduplicated(self):
        failing = {**REPORT, 'status': 'failed', 'reason': 'query', 'findings': []}
        _, current = apply(report=failing)
        _, current = apply(report=failing, current=current, now=NOW + 3600)
        self.assertEqual(len(current['pending']), 1)
        current['seen'][current['pending'].pop()['key']] = NOW + 86400
        _, current = apply(report=failing, current=current, now=NOW + 7200)
        self.assertEqual(current['pending'], [])
        _, current = apply(report=failing, current=current, now=NOW + 90000)
        self.assertEqual(len(current['pending']), 1)


class DecideRunTests(unittest.TestCase):
    def work(self, tmp, *, report=REPORT, bundle=BUNDLE, analysis=OK, diagnosis=DIAGNOSIS):
        root = Path(tmp)
        for name, file, content in (('collect', 'report.json', report), ('bundle', 'bundle.json', bundle),
                                    ('diagnosis', 'status.json', analysis), ('diagnosis', 'diagnosis.json', diagnosis)):
            (root / name).mkdir(exist_ok=True)
            if content is not None:
                (root / name / file).write_text(json.dumps(content))
        return root

    def runner(self, raw=None):
        saved = []

        def patch(argv, stdin):
            saved.append(json.loads(json.loads(stdin)['data']['state.json']))
            from swhurl.run import Result
            return Result(argv, 0, '', '')

        data = {} if raw is None else {'data': {'state.json': raw}}
        return FakeRunner().on(*GET, stdout=json.dumps(data)).on(*PATCH, handler=patch), saved

    def test_outbox_is_saved_before_posting_and_cleared_after(self):
        runner, saved = self.runner()
        sent = []
        with tempfile.TemporaryDirectory() as tmp:
            code = decide.run(runner, self.work(tmp), sender=sent.append, now=NOW)
        self.assertEqual((code, [n['title'] for n in sent]), (0, ['hello-ts/staging diagnosis']))
        self.assertEqual([len(s['pending']) for s in saved], [1, 0])
        self.assertEqual(saved[-1]['seen'], {f'diagnosis:{FINGERPRINT}': NOW + 86400})
        self.assertNotIn('RangeError: Invalid count', json.dumps(saved[-1]))

    def test_failed_delivery_keeps_the_message_pending_and_fails_the_job(self):
        runner, saved = self.runner()

        def refuse(notice):
            raise NotificationError('ntfy delivery failed; notification remains pending')

        with tempfile.TemporaryDirectory() as tmp:
            code = decide.run(runner, self.work(tmp), sender=refuse, now=NOW)
        self.assertEqual((code, len(saved), len(saved[0]['pending'])), (1, 1, 1))

    def test_missing_files_fail_with_contract_and_exit_one(self):
        runner, saved = self.runner()
        sent = []
        with tempfile.TemporaryDirectory() as tmp:
            code = decide.run(runner, self.work(tmp, report=None), sender=sent.append, now=NOW)
        self.assertEqual((code, [n['message'] for n in sent]),
                         (1, ['Reason: contract. No diagnosis was sent. Check with make incident-review-status.']))
        self.assertEqual(saved[-1]['last_result'], 'failed')

    def test_corrupt_or_unreachable_state_sends_nothing_and_writes_nothing(self):
        for runner in (self.runner('{corrupt')[0], FakeRunner().on(*GET, returncode=1)):
            sent = []
            with tempfile.TemporaryDirectory() as tmp:
                code = decide.run(runner, self.work(tmp), sender=sent.append, now=NOW)
            self.assertEqual((code, sent), (1, []))
            self.assertFalse([call for call in runner.calls if 'patch' in call])

    def test_main_needs_a_destination(self):
        runner, saved = self.runner()
        with tempfile.TemporaryDirectory() as tmp:
            self.work(tmp)
            code = decide.main(['--work', tmp], runner, now=NOW)
        self.assertEqual((code, len(saved[0]['pending'])), (1, 1))


class DryRunTests(unittest.TestCase):
    def test_dry_run_covers_the_whole_pipeline_without_external_calls(self):
        import contextlib
        import io

        from swhurl.incident_review import dryrun
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            self.assertEqual(dryrun.main([]), 0)
        report = json.loads(out.getvalue())
        self.assertEqual((report['external_calls'], report['report']['status'], report['decision']['reason']),
                         (0, 'fired', 'notified'))
        self.assertEqual([m['title'] for m in report['messages']], ['hello-ts/staging diagnosis'])
        self.assertEqual(report['proposed_patch']['files'], ['src/server.ts'])
        self.assertLess(report['state_bytes'], 64_000)

    def test_quiet_fixture_skips_analysis_and_sends_nothing(self):
        from swhurl.incident_review import dryrun
        fixture = copy.deepcopy(FIXTURE)
        fixture['telemetry'].update({'error-logs': [], 'error-spans': []})
        with tempfile.TemporaryDirectory() as tmp:
            result = dryrun.sweep(fixture, Path(tmp))
            self.assertEqual(json.loads((Path(tmp) / 'diagnosis/status.json').read_text())['status'], 'skipped')
            self.assertFalse((Path(tmp) / 'bundle/bundle.json').exists())
        self.assertEqual((result['decision']['result'], result['bundle'], result['messages']), ('quiet', None, []))


if __name__ == '__main__':
    unittest.main()
