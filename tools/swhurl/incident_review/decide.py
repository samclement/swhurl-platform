"""Decide step: validate the handoff, update state, notify, and set the Job's exit code.

The only writer of reviewer state and the only sender of reviewer messages. Model
output reaches a message solely through ``validate_diagnosis``; a failure message
carries one word from ``errors.REASONS`` and nothing else (docs/plan.md section 14).
"""
from __future__ import annotations

import argparse
import json
import logging
import os
import time
from collections.abc import Callable
from pathlib import Path
from typing import Any

from swhurl.notifications.delivery import deliver, publish
from swhurl.notifications.errors import NotificationError
from swhurl.run import Runner

from . import (
    MAX_AGENT_OUTPUT_BYTES,
    allowlist,
    decode_codex_output,
    describe_finding,
    evidence_bundle,
    state,
    validate_diagnosis,
)
from .allowlist import Allowlist
from .errors import REASONS, PolicyError, ReviewFailure

log = logging.getLogger(__name__)

REPORT_STATUSES = {'quiet', 'fired', 'budget', 'failed'}
ANALYSIS_STATUSES = {'skipped', 'disabled', 'ok', 'timeout', 'error'}
FINDING_FIELDS = {'fingerprint', 'app', 'env', 'signal', 'key', 'count', 'baseline_mean', 'decision', 'reason',
                  'coverage'}
DECISIONS = {'quiet', 'fired', 'fired-deferred'}
PRIORITY = {'low': 2, 'default': 3, 'high': 4}
FAILURE_HISTORY = 86400
PAUSE_HISTORY = 40 * 86400
EVIDENCE_LINES = 3
LINE_TEXT = 200
FIELD_TEXT = 500


def check_report(report: Any) -> dict:
    """The collect report, or a ``contract`` failure; nothing in it is trusted beyond its shape."""
    try:
        if (not isinstance(report, dict) or report['version'] != 1 or report['status'] not in REPORT_STATUSES
                or not isinstance(report['findings'], list) or not isinstance(report['counts'], dict)
                or not isinstance(report['trigger'], str) or set(report['window']) != {'start', 'end'}):
            raise ValueError
        if report['status'] == 'failed' and report['reason'] not in REASONS:
            raise ValueError
        if report['status'] == 'budget' and report['budget'] not in {'daily', 'monthly'}:
            raise ValueError
        for finding in report['findings']:
            if (set(finding) != FINDING_FIELDS or finding['decision'] not in DECISIONS
                    or finding['coverage'] not in {'covered', 'alert-gap'}
                    or isinstance(finding['count'], bool) or not isinstance(finding['count'], int)
                    or not all(isinstance(finding[name], str) for name in ('fingerprint', 'app', 'env', 'signal'))):
                raise ValueError
        for key, count in report['counts'].items():
            if not isinstance(key, str) or isinstance(count, bool) or not isinstance(count, int) or count < 0:
                raise ValueError
    except (AttributeError, KeyError, TypeError, ValueError):
        raise ReviewFailure('contract') from None
    return report


def check_analysis(status: Any) -> dict:
    try:
        if (not isinstance(status, dict) or status['version'] != 1 or status['status'] not in ANALYSIS_STATUSES
                or not isinstance(status['cli'], str) or not isinstance(status['model'], str)
                or len(status['cli']) > 100 or len(status['model']) > 100):
            raise ValueError
    except (KeyError, TypeError, ValueError):
        raise ReviewFailure('contract') from None
    return status


def queue(current: dict, notice: dict, now: float) -> None:
    """Add a notice to the outbox unless the same key is pending or was sent within its history."""
    if current['seen'].get(notice['key'], 0) > now or any(n['key'] == notice['key'] for n in current['pending']):
        return
    current['pending'].append(notice)


def failure_notice(reason: str) -> dict:
    return {'key': f'failure:{reason}', 'title': 'incident review failed',
            'message': f'Reason: {reason}. No diagnosis was sent. Check with make incident-review-status.',
            'priority': 4, 'tags': ['warning', 'incident-review'], 'history': FAILURE_HISTORY}


def evidence_line(ref: str, bundle: dict) -> str:
    kind, index = ref.split('[')
    record = bundle[kind][int(index.rstrip(']'))]
    if kind == 'logs':
        text = f"{record.get('level', '')} {record.get('exception_type', '')}: {record.get('message', '')}"
    elif kind == 'traces':
        text = f"span {record.get('span', '')} {record.get('status', '')} {record.get('http_status', '')}"
    else:
        text = f"{record.get('signal', '')} {record.get('key', '')}: {record.get('count', '')} against {record.get('baseline_mean', '')}"
    return ' '.join(text.split())[:LINE_TEXT]


def diagnosis_notice(allow: Allowlist, finding: dict, bundle: dict, diagnosis: dict, trigger: str) -> dict:
    app = allow.app(finding['app'])
    lines = [diagnosis['summary'][:FIELD_TEXT], f"Likely cause: {diagnosis['likely_cause'][:FIELD_TEXT]}",
             f"Confidence: {round(diagnosis['confidence'] * 100)}%",
             f"Signal: {describe_finding(finding)}",
             f"Window: {bundle['window']['start']} to {bundle['window']['end']} (trigger: {trigger})",
             f"Image: {bundle['image']['tag'] or 'unknown'}"]
    lines += [f'Evidence: {evidence_line(ref, bundle)}' for ref in diagnosis['evidence_refs'][:EVIDENCE_LINES]]
    lines.append('Code fix attempted: no')
    notice = {'key': f"diagnosis:{finding['fingerprint']}", 'title': f"{finding['app']}/{finding['env']} diagnosis",
              'message': '\n'.join(lines), 'priority': PRIORITY[app.severity[finding['signal']]],
              'tags': ['incident-review'], 'history': allow.defaults['cooldown_hours'] * 3600}
    if bundle['links'] and isinstance(bundle['links'][0], str) and bundle['links'][0].startswith('https://'):
        notice['click'] = bundle['links'][0]
    return notice


def record(current: dict, finding: dict, status: str, analysis: dict, now: float, defaults: dict) -> dict:
    incident = current['incidents'].setdefault(finding['fingerprint'], {
        'app': finding['app'], 'env': finding['env'], 'signal': finding['signal'], 'first_seen': now,
        'last_seen': now, 'last_analysis': 0, 'cooldown_until': 0, 'status': 'seen', 'coverage': finding['coverage'],
        'cli': '', 'model': '', 'notified': 0, 'pr': None, 'check': None})
    incident.update(last_seen=now, last_analysis=now, cooldown_until=now + defaults['cooldown_hours'] * 3600,
                    status=status, coverage=finding['coverage'], cli=analysis['cli'], model=analysis['model'])
    return incident


def charge(current: dict, defaults: dict, now: float) -> None:
    """Count one model call at the per-run cap; token-based cost waits for a pinned model's prices."""
    spend = state.roll_spend(current, now)
    spend['cents'] += defaults['run_cost_cents']
    spend['analyses'] += 1
    spend['analyses_today'] += 1


def decide(*, allow: Allowlist, current: dict, report: Any, bundle: Any, analysis: Any, last_message: str | None,
           now: float) -> dict:
    """Apply one run to ``current`` (state) and return ``{'result', 'reason', 'exit'}``."""
    defaults = allow.defaults
    current['last_sweep'] = now

    def fail(reason: str) -> dict:
        current['last_result'] = 'failed'
        queue(current, failure_notice(reason), now)
        return {'result': 'failed', 'reason': reason, 'exit': 1}

    try:
        report = check_report(report)
        if report['status'] == 'failed':
            return fail(report['reason'])
        # One count per window: a second run over the same hour (a manual Job, a missed schedule
        # caught up) must not weigh that hour twice in the baseline.
        if report['window']['start'] != current['last_window']:
            for key, count in report['counts'].items():
                current['baselines'].setdefault(key, []).append(count)
            current['last_window'] = str(report['window']['start'])[:40]
        for finding in report['findings']:
            if finding['fingerprint'] in current['incidents'] and (finding['count'] > 0
                                                                   or finding['decision'] != 'quiet'):
                current['incidents'][finding['fingerprint']].update(last_seen=now, coverage=finding['coverage'])
        if report['status'] == 'quiet':
            current['last_result'] = 'quiet'
            return {'result': 'quiet', 'reason': None, 'exit': 0}
        if report['status'] == 'budget':
            current['last_result'] = 'budget'
            if report['budget'] == 'monthly':
                month = state.roll_spend(current, now)['month']
                queue(current, {'key': f'paused:{month}', 'title': 'incident review paused: budget',
                                'message': f'The monthly model budget for {month} is used up; sweeps continue '
                                           'without diagnoses until next month.',
                                'priority': 3, 'tags': ['incident-review'], 'history': PAUSE_HISTORY}, now)
            return {'result': 'budget', 'reason': report['budget'], 'exit': 0}

        try:
            bundle = evidence_bundle(bundle)
        except PolicyError:
            raise ReviewFailure('contract') from None
        fired = [f for f in report['findings'] if f['decision'] == 'fired']
        if len(fired) != 1 or fired[0]['fingerprint'] != bundle['fingerprint']:
            raise ReviewFailure('contract')
        finding = fired[0]
        allow.app(finding['app'])
        analysis = check_analysis(analysis)
        if analysis['status'] == 'disabled':
            current['last_result'] = 'collect-only'
            return {'result': 'collect-only', 'reason': None, 'exit': 0}
        if analysis['status'] == 'skipped':
            raise ReviewFailure('contract')
        charge(current, defaults, now)
        if analysis['status'] != 'ok':
            record(current, finding, 'no-diagnosis', analysis, now, defaults)
            return fail('timeout' if analysis['status'] == 'timeout' else 'provider')
        try:
            diagnosis = validate_diagnosis(decode_codex_output(0, last_message), bundle)
        except PolicyError as error:
            uncited = 'cites missing evidence' in str(error)
            record(current, finding, 'suppressed-uncited' if uncited else 'no-diagnosis', analysis, now, defaults)
            return fail('schema')
        current['last_result'] = 'analysed'
        if diagnosis['recommend_no_change']:
            record(current, finding, 'no-change', analysis, now, defaults)
            return {'result': 'analysed', 'reason': 'no-change', 'exit': 0}
        if diagnosis['confidence'] < defaults['confidence_min']:
            record(current, finding, 'suppressed-low-confidence', analysis, now, defaults)
            return {'result': 'analysed', 'reason': 'low-confidence', 'exit': 0}
        record(current, finding, 'notified', analysis, now, defaults)['notified'] = now
        queue(current, diagnosis_notice(allow, finding, bundle, diagnosis, report['trigger']), now)
        return {'result': 'analysed', 'reason': 'notified', 'exit': 0}
    except ReviewFailure as failure:
        return fail(failure.reason)
    except PolicyError:
        return fail('contract')


def read_json(path: Path) -> Any:
    """Decoded JSON, or None when the file is absent, oversized or malformed (the checks then refuse it)."""
    try:
        raw = path.read_bytes()
        return json.loads(raw) if len(raw) <= 2 * MAX_AGENT_OUTPUT_BYTES else None
    except (OSError, UnicodeDecodeError, json.JSONDecodeError):
        return None


def read_text(path: Path) -> str | None:
    try:
        raw = path.read_bytes()
        return raw.decode() if len(raw) <= MAX_AGENT_OUTPUT_BYTES else 'x' * (MAX_AGENT_OUTPUT_BYTES + 1)
    except (OSError, UnicodeDecodeError):
        return None


def run(runner: Runner, work: Path, *, sender: Callable[[dict], None], now: float,
        allow: Allowlist | None = None) -> int:
    allow = allow or allowlist.current()
    try:
        current = state.read_state(runner)
    except ReviewFailure as failure:
        # Without state there is nothing to deduplicate against: say nothing and let the
        # heartbeat report the failing Job instead of sending the same message every hour.
        log.error('incident review decide: %s; no message sent', failure.reason)
        return 1
    outcome = decide(allow=allow, current=current, report=read_json(work / 'collect/report.json'),
                     bundle=read_json(work / 'bundle/bundle.json'),
                     analysis=read_json(work / 'diagnosis/status.json'),
                     last_message=read_text(work / 'diagnosis/diagnosis.json'), now=now)
    try:
        deliver(runner, current, sender, now,
                save=lambda r, s: state.save_state(r, s, now=now, defaults=allow.defaults))
    except ReviewFailure as failure:
        log.error('incident review decide: %s; state not saved', failure.reason)
        return 1
    except NotificationError:
        log.error('incident review decide: delivery; the message stays pending')
        return 1
    log.info('incident review decide: result=%s reason=%s pending=%d', outcome['result'], outcome['reason'],
             len(current['pending']))
    return outcome['exit']


def main(argv: list[str] | None = None, runner: Runner | None = None, *,
         sender: Callable[[dict], None] | None = None, now: float | None = None) -> int:
    parser = argparse.ArgumentParser(description='Incident review decide step (runs in the reviewer pod)')
    parser.add_argument('--work', type=Path, default=Path('/work'))
    args = parser.parse_args(argv)
    logging.basicConfig(level=logging.INFO, format='%(levelname)s %(name)s %(message)s')
    logging.getLogger('httpx').setLevel(logging.WARNING)  # its INFO lines repeat every request URL
    runner = runner or Runner.from_environment()

    def post(notice: dict) -> None:
        url = os.environ.get('NTFY_REVIEW_URL', '')
        runner.add_secret(url)
        if not url:
            raise NotificationError('NTFY_REVIEW_URL is missing; notification remains pending')
        publish(url, notice)

    return run(runner, args.work, sender=sender or post, now=now if now is not None else time.time())
