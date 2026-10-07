"""Offline dry run: collect, a fixture analysis and decide over checked-in fixtures.

No cluster, ClickHouse, provider, ntfy or GitHub call is made: telemetry and the
diagnosis come from ``tests/fixtures/incident-review``, state starts empty and
stays in memory, and messages are printed instead of sent.
"""
from __future__ import annotations

import argparse
import json
import sys
import tempfile
from pathlib import Path

from swhurl import ROOT

from . import (
    allowlist,
    collect,
    coverage_report,
    decide,
    evidence_bundle,
    prefilter,
    state,
    validate_diagnosis,
    validate_patch,
)
from .errors import PolicyError, ReviewFailure

FIXTURES = ROOT / 'tests/fixtures/incident-review'


def sweep(fixture: dict, work: Path) -> dict:
    """One whole run in ``work``; returns what each step produced."""
    allow, now = allowlist.current(), fixture['now']
    current = state.validate(fixture['state']) if fixture.get('state') else state.empty_state()
    for name in ('collect', 'bundle', 'diagnosis'):
        (work / name).mkdir(parents=True, exist_ok=True)
    images = collect.release_images_from(fixture['releases'])
    report, bundle = collect.collect(allow=allow, current=current,
                                     telemetry=collect.FixtureTelemetry(fixture['telemetry']), images=images, now=now)
    collect.write_handoff(work, report, bundle)
    status = dict(fixture['analysis']['status']) if bundle else {**fixture['analysis']['status'], 'status': 'skipped'}
    (work / 'diagnosis/status.json').write_text(json.dumps(status))
    if bundle:
        (work / 'diagnosis/diagnosis.json').write_text(json.dumps(fixture['analysis']['diagnosis']))
    outcome = decide.decide(allow=allow, current=current, report=decide.read_json(work / 'collect/report.json'),
                            bundle=decide.read_json(work / 'bundle/bundle.json'),
                            analysis=decide.read_json(work / 'diagnosis/status.json'),
                            last_message=decide.read_text(work / 'diagnosis/diagnosis.json'), now=now)
    messages = [{k: v for k, v in notice.items() if k != 'history'} for notice in current['pending']]
    return {'report': report, 'bundle': bundle, 'decision': outcome, 'messages': messages,
            'state_bytes': len(state.serialize(current, now, allow.defaults).encode())}


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--fixture', default=str(FIXTURES / 'sweep.json'), help='sweep fixture to run')
    parser.add_argument('--candidate', default=str(FIXTURES / 'candidate.json'),
                        help='stage 3 fixture: evidence, diagnosis and candidate patch')
    args = parser.parse_args(argv)
    try:
        with tempfile.TemporaryDirectory() as tmp:
            result = sweep(json.loads(Path(args.fixture).read_text()), Path(tmp))
        raw = json.loads(Path(args.candidate).read_text())
        bundle = evidence_bundle(raw['evidence'])
        validate_diagnosis(raw['diagnosis'], bundle)
        patch = validate_patch(raw['patch'], repository=bundle['repository'], base_revision=bundle['base_revision'])
        cases = json.loads((FIXTURES / 'prefilter.json').read_text())
        defaults = allowlist.current().defaults
        report = {'mode': 'offline-fixture-dry-run', 'external_calls': 0, **result,
                  'prefilter': [{'case': case['name'], **prefilter(case['finding'], case['state'], defaults,
                                                                   cases['now'])} for case in cases['prefilter_cases']],
                  'coverage': coverage_report(cases['findings'], cases['alert_rules']),
                  'proposed_patch': {'repository': patch['repository'], 'base_revision': patch['base_revision'],
                                     'files': [item['path'] for item in patch['files']],
                                     'changed_lines': patch['changed_lines']}}
        print(json.dumps(report, indent=2, sort_keys=True))
        return 0
    except (OSError, json.JSONDecodeError, KeyError, PolicyError, ReviewFailure) as error:
        print(f'incident review refused: {error}', file=sys.stderr)
        return 1
