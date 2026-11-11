"""check-otel (platform/otel/check.sh): render, validate in the rendered image, stop on tool failures; the fixture verdict.

The script runs with a fake helm and a fake container runtime, and the real kubectl, jq and openssl; the real
collector's verdicts are `make check-otel`'s. fixtures.jq is tested with jq itself.
"""
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

import yaml

from swhurl import ROOT

RELAY = 'receivers:\n  otlp: {}\nexporters:\n  debug: {}\nservice:\n  pipelines:\n    logs: {receivers: [otlp], exporters: [debug]}\n'
RENDERED = yaml.safe_dump_all([
    {'apiVersion': 'v1', 'kind': 'ConfigMap', 'metadata': {'name': 'x'}, 'data': {'relay': RELAY}},
    {'apiVersion': 'apps/v1', 'kind': 'DaemonSet', 'metadata': {'name': 'x'}, 'spec': {'template': {'spec': {'containers': [{
        'name': 'opentelemetry-collector', 'image': 'otel/opentelemetry-collector-k8s:0.1.0',
        'args': ['--config=/conf/relay.yaml', '--feature-gates=ottl.functions.enableLambda'],
        'env': [{'name': 'GOMEMLIMIT', 'value': '152MiB'},
                {'name': 'MY_POD_IP', 'valueFrom': {'fieldRef': {'fieldPath': 'status.podIP'}}}],
        'volumeMounts': [{'name': 'hostfs', 'mountPath': '/hostfs'}]}]}}}}])


def fixtures(expression, data, **arguments):
    """One fixtures.jq expression over data."""
    command = ['jq', '-c', '-L', str(ROOT / 'platform/otel')]
    for name, value in arguments.items():
        command += ['--argjson', name, json.dumps(value)]
    result = subprocess.run([*command, 'include "fixtures"; ' + expression], input=json.dumps(data),
                            capture_output=True, text=True, check=True)
    return json.loads(result.stdout)


def record(name, body='x', **attrs):
    return {'body': fixtures('any_value', body), 'attributes': fixtures('attributes', {'test.case': name, **attrs})}


class FixtureVerdictTests(unittest.TestCase):
    def test_otlp_values_round_trip(self):
        for value in ['text', 42, 3.5, True, ['one', 2], {'nested': {'key': 'value'}}]:
            with self.subTest(value=value):
                self.assertEqual(fixtures('any_value | unpack', value), value)

    def test_comparison_detects_drop_duplicate_field_loss_and_overwrites(self):
        case = {'name': 'fixture', 'input': 'original', 'expected': {'body': 'message', 'severityNumber': 9,
                'attributes': {'status': 200}}, 'attributes': {'existing': 'retained'},
                'record': {'traceId': '0123456789abcdef0123456789abcdef'}}
        good = {**record('fixture', 'message', status=200, existing='retained', **{'log.record.original': 'original'}),
                'severityNumber': 9, **case['record']}
        self.assertEqual(fixtures('verdict($c)', [good], c=[case]), [])
        for found in ([], [good, good], [{**good, 'attributes': []}], [{**good, 'traceId': 'changed'}]):
            with self.subTest(found=found):
                self.assertTrue(fixtures('verdict($c)', found, c=[case]))

    def test_a_dropped_fixture_must_not_arrive_and_completion_ignores_it(self):
        kept = {'name': 'kept', 'input': 'x', 'expected': {'body': 'x'}}
        dropped = {'name': 'noise', 'input': 'y', 'dropped': True}
        self.assertEqual(fixtures('verdict($c)', [record('kept')], c=[kept, dropped]), [])
        self.assertEqual(fixtures('verdict($c)', [record('kept'), record('noise', 'y')], c=[kept, dropped]),
                         ['noise: expected the record to be dropped, got 1'])
        self.assertTrue(fixtures('complete($c)', [record('kept')], c=[kept, dropped]), 'a dropped case is never waited for')
        self.assertFalse(fixtures('complete($c)', [record('noise', 'y')], c=[kept, dropped]))

    def test_missing_fields_default_and_attributes_are_a_subset(self):
        case = {'name': 'defaults', 'input': {}, 'expected': {'body': None, 'severityNumber': 0,
                'attributes': {'absent': None}}, 'record': {'traceId': None}}
        found = [{'attributes': fixtures('attributes', {'test.case': 'defaults', 'extra': 'allowed'})}]
        self.assertEqual(fixtures('verdict($c)', found, c=[case]), [])
        case['expected']['severityNumber'] = 9
        self.assertEqual(fixtures('verdict($c)', found, c=[case]), ['defaults: incorrect severityNumber'])

    def test_every_failure_in_order_without_values(self):
        sensitive = 'SENSITIVE-FIXTURE-CONTENT'
        case = {'name': 'order', 'input': sensitive,
                'expected': {'severityNumber': 9, 'attributes': {'first': sensitive, 'second': sensitive},
                             'body': 'changed', 'traceId': 'expected-override'},
                'record': {'traceId': 'initial-trace', 'spanId': sensitive},
                'attributes': {'first': 'initial-value', 'kept': sensitive}}
        errors = fixtures('verdict($c)', [record('order', sensitive)], c=[case])
        self.assertEqual(errors, ['order: incorrect severityNumber', 'order: incorrect attribute first',
                                  'order: incorrect attribute second', 'order: incorrect body',
                                  'order: incorrect traceId', 'order: existing spanId changed',
                                  'order: existing attribute first changed', 'order: existing attribute kept changed',
                                  'order: original line missing or changed'])
        self.assertNotIn(sensitive, '\n'.join(errors))

    def test_the_original_line_is_required_only_for_a_changed_string_body(self):
        for fields, attrs in (({'input': 'same', 'expected': {'body': 'same'}}, {}),
                              ({'input': 'same', 'expected': {}}, {}),
                              ({'input': {'message': 'structured'}, 'expected': {'body': 'parsed'}}, {}),
                              ({'input': 'framed', 'original': 'line', 'expected': {'body': 'parsed'}},
                               {'log.record.original': 'line'})):
            with self.subTest(fields=fields):
                found = [record('original', fields['expected'].get('body', fields['input']), **attrs)]
                self.assertEqual(fixtures('verdict($c)', found, c=[{'name': 'original', **fields}]), [])

    def test_errors_follow_case_order_and_ignore_unrequested_records_and_duplicates_count(self):
        cases = [{'name': 'second', 'input': 'x', 'expected': {}}, {'name': 'first', 'input': 'x', 'expected': {}}]
        self.assertEqual(fixtures('verdict($c)', [record('unrequested')], c=cases),
                         ['second: expected one record, got 0', 'first: expected one record, got 0'])
        found = [record('second'), record('second'), record('first')]
        self.assertTrue(fixtures('complete($c)', found, c=cases))
        self.assertEqual(fixtures('verdict($c)', found, c=cases), ['second: expected one record, got 2'])

    def test_the_payload_carries_every_unframed_case_with_its_resource_and_record(self):
        cases = [{'name': 'a', 'input': 'line', 'resource': {'k8s.pod.name': 'p'}, 'attributes': {'status': 200},
                  'record': {'severityNumber': 13}},
                 {'name': 'framed', 'input': 'line', 'framing': ['x'], 'resource': {'k8s.pod.name': 'q'}}]
        self.assertEqual(fixtures('payload', cases), {'resourceLogs': [{
            'resource': {'attributes': [{'key': 'k8s.pod.name', 'value': {'stringValue': 'p'}}]},
            'scopeLogs': [{'logRecords': [{'body': {'stringValue': 'line'}, 'severityNumber': 13, 'attributes': [
                {'key': 'test.case', 'value': {'stringValue': 'a'}}, {'key': 'status', 'value': {'intValue': '200'}}]}]}]}]})

    def test_the_runtime_runs_the_logs_pipelines_transforms_and_filters_in_order(self):
        config = {'receivers': {'file_log': {'include': ['/var/log/pods/*/*/*.log'], 'exclude': ['/x'],
                                             'operators': [{'type': 'container'}]}},
                  'processors': {'transform/a': {'log_statements': ['set(body, body)']}, 'filter/platform-noise': {},
                                 'k8s_attributes': {}, 'batch': {}},
                  'service': {'pipelines': {'logs': {'processors': ['transform/a', 'filter/platform-noise',
                                                                    'k8s_attributes', 'batch']}}}}
        framed = {'name': 'cri', 'input': 'x', 'framing': ['line'], 'resource': {'k8s.pod.name': 'pod'}}
        runtime = fixtures('runtime($c)', config, c=[framed, {'name': 'plain', 'input': 'x'}])
        self.assertEqual(runtime['service']['pipelines']['logs'], {
            'receivers': ['otlp', 'file_log/cri'], 'processors': ['transform/a', 'filter/platform-noise'], 'exporters': ['file']})
        self.assertEqual(runtime['processors'], {'transform/a': config['processors']['transform/a'], 'filter/platform-noise': {}})
        self.assertEqual(runtime['receivers']['file_log/cri'], {
            'include': ['/var/log/pods/fixture_pod_00000000-0000-0000-0000-000000000000/main/0.log'], 'exclude': [],
            'start_at': 'beginning', 'poll_interval': '100ms',
            'operators': [{'type': 'container'}, {'type': 'add', 'field': 'attributes["test.case"]', 'value': 'cri'}]})
        self.assertEqual(runtime['exporters'], {'file': {'path': '/dev/stdout', 'flush_interval': '100ms'}})


class CheckScriptTests(unittest.TestCase):
    """The fake helm renders RENDERED; the fake runtime exits with the next of VALIDATE_STATUSES."""

    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        self.addCleanup(subprocess.run, ['rm', '-rf', str(self.tmp)])
        self.bin = self.tmp / 'bin'
        self.bin.mkdir()
        (self.tmp / 'rendered.yaml').write_text(RENDERED)
        self.executable('helm', '''printf '%s\\n' "$*" >> "$CHECK_TMP/helm.calls"
while [ "$1" != -f ]; do shift; done
cp "$2" "$CHECK_TMP/values-$(wc -l < "$CHECK_TMP/helm.calls").json"
if [ "$HELM_STATUS" != 0 ]; then echo 'Error: chart not found' >&2; exit "$HELM_STATUS"; fi
cat "$CHECK_TMP/rendered.yaml"
''')
        self.executable('runtime', '''printf '%s\\n' "$*" >> "$CHECK_TMP/runtime.calls"
for arg in "$@"; do case $arg in *:/check:ro) cp "${arg%:/check:ro}/relay.yaml" "$CHECK_TMP/validated.yaml" ;; esac; done
n=$(wc -l < "$CHECK_TMP/runtime.calls")
status=$(echo "$VALIDATE_STATUSES" | cut -d, -f"$n")
[ "$status" = 0 ] || echo "Error: invalid configuration: service::pipelines::logs: references processor \\"x\\"" >&2
exit "$status"
''')
        self.executable('python3', 'echo unexpected-python >&2; exit 88\n')

    def executable(self, name, content):
        path = self.bin / name
        path.write_text('#!/bin/bash\nset -eu\n' + content)
        path.chmod(0o700)

    def check(self, helm=0, validate='0,0'):
        env = dict(os.environ, PATH=f'{self.bin}:{os.environ["PATH"]}', CHECK_TMP=str(self.tmp),
                   CONTAINER_RUNTIME=str(self.bin / 'runtime'), HELM_STATUS=str(helm), VALIDATE_STATUSES=validate)
        return subprocess.run(['bash', 'platform/otel/check.sh'], cwd=ROOT, env=env, capture_output=True, text=True)

    def calls(self, tool):
        path = self.tmp / f'{tool}.calls'
        return path.read_text().splitlines() if path.exists() else []

    def test_each_release_is_rendered_from_its_pin_and_validated_unmodified_in_its_image(self):
        result = self.check()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn('unexpected-python', result.stderr)
        releases = sorted((ROOT / 'platform/otel').glob('helmrelease-*.yaml'))
        helm = self.calls('helm')
        self.assertEqual(len(helm), len(releases))
        for i, (path, call) in enumerate(zip(releases, helm, strict=True), 1):
            release = yaml.safe_load(path.read_text())
            spec = release['spec']['chart']['spec']
            self.assertTrue(call.startswith(f"template {release['metadata']['name']} {spec['chart']} --repo "
                                            f"https://open-telemetry.github.io/opentelemetry-helm-charts "
                                            f"--version {spec['version']} --namespace logging -f "), call)
            self.assertEqual(json.loads((self.tmp / f'values-{i}.json').read_text()), release['spec']['values'])
        validate = self.calls('runtime')[0].split()
        self.assertEqual(validate[:3], ['run', '--rm', '--network'])
        self.assertEqual(validate[validate.index('docker.io/otel/opentelemetry-collector-k8s:0.1.0') + 1:],
                         ['validate', '--feature-gates=ottl.functions.enableLambda', '--config=file:/check/relay.yaml'])
        for stub in ('GOMEMLIMIT=152MiB', 'MY_POD_IP=127.0.0.1', 'KUBERNETES_SERVICE_HOST=127.0.0.1'):
            self.assertIn(stub, validate)
        self.assertTrue(any(arg.endswith('/empty:/hostfs:ro') for arg in validate))
        self.assertTrue(any(arg.endswith(':/var/run/secrets/kubernetes.io/serviceaccount:ro') for arg in validate))
        self.assertEqual((self.tmp / 'validated.yaml').read_text(), RELAY, 'the rendered config, byte for byte')
        self.assertEqual(result.stdout.count('[OK]'), len(releases))

    def test_a_rejected_config_is_reported_with_the_collectors_reason_and_the_rest_still_checked(self):
        result = self.check(validate='1,0')
        self.assertEqual(result.returncode, 1)
        self.assertIn('rejects the rendered config', result.stdout)
        self.assertIn('references processor "x"', result.stdout)
        self.assertEqual(len(self.calls('runtime')), 2, 'the next release is still validated')
        self.assertEqual(result.stdout.count('[OK]'), 1)

    def test_a_render_failure_stops_before_any_container_runs(self):
        result = self.check(helm=1)
        self.assertEqual(result.returncode, 1)
        self.assertIn('[BAD]', result.stderr)
        self.assertIn('helm template opentelemetry-collector', result.stderr)
        self.assertIn('chart not found', result.stderr)
        self.assertEqual(len(self.calls('helm')), 1)
        self.assertEqual(self.calls('runtime'), [])

    def test_a_runtime_failure_stops_the_script(self):
        result = self.check(validate='125,0')
        self.assertEqual(result.returncode, 1)
        self.assertIn('could not run docker.io/otel/opentelemetry-collector-k8s:0.1.0 (exit 125)', result.stderr)
        self.assertEqual(len(self.calls('runtime')), 1, 'no later release is checked')
        self.assertNotIn('[OK]', result.stdout)

    def test_arguments_are_refused(self):
        result = subprocess.run(['bash', 'platform/otel/check.sh', '--help'], cwd=ROOT, capture_output=True, text=True)
        self.assertEqual(result.returncode, 2)
        self.assertIn('usage:', result.stderr)


if __name__ == '__main__':
    unittest.main()
