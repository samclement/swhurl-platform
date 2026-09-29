"""make clickstack-bootstrap, offline: registration, team key, secrecy."""
import base64
import json
import unittest

from swhurl import clickstack
from swhurl.report import Report
from swhurl.run import FakeRunner, Result

EMAIL, PASSWORD, KEY, OTHER = 'admin@example.com', 'Sekret-Passw0rd!', 'git-key-uuid', 'random-team-key'
URI = 'mongodb://hyperdx:mongo-pass@clickstack-mongodb-svc:27017/hyperdx'


def b64(value):
    return base64.b64encode(value.encode()).decode()


class Cluster:
    """Scripted ClickStack: answers kubectl get/exec like the real pods would."""

    def __init__(self, *, team=False, team_key=OTHER, register_status=200, teams=None, api_up=True):
        self.team, self.team_key, self.register_status, self.api_up = team, team_key, register_status, api_up
        self.teams = teams
        self.registered = None

    def runner(self, dry_run=False):
        inputs = {'CLICKSTACK_ADMIN_EMAIL': EMAIL, 'CLICKSTACK_ADMIN_PASSWORD': PASSWORD, 'CLICKSTACK_INGESTION_KEY': KEY}
        return (FakeRunner(dry_run=dry_run)
                .on('kubectl', '-n', 'observability', 'get', 'secret', clickstack.INPUTS_SECRET,
                    stdout=json.dumps({'data': {k: b64(v) for k, v in inputs.items()}}))
                .on('kubectl', '-n', 'observability', 'get', 'secret', clickstack.MONGO_URI_SECRET,
                    stdout=json.dumps({'data': {'connectionString.standard': b64(URI)}}))
                .on('kubectl', '-n', 'observability', 'exec', '-i', clickstack.APP, handler=self.api)
                .on('kubectl', '-n', 'observability', 'exec', '-i', clickstack.MONGO_POD, handler=self.mongo))

    def api(self, argv, program):
        if not self.api_up:
            return Result(argv, 1, '', 'error: no running pod')
        if '/ready' in program:
            answer = {'status': 200, 'body': {}}
        elif '/installation' in program:
            answer = {'status': 200, 'body': {'isTeamExisting': self.team}}
        elif '/register/password' in program:
            self.registered = program
            answer = {'status': self.register_status, 'body': {} if self.register_status == 200 else {'error': 'invalid'}}
            self.team = self.team or self.register_status == 200
        else:
            raise AssertionError(program)
        return Result(argv, 0, 'warning noise\nRESULT ' + json.dumps(answer) + '\n')

    def mongo(self, argv, program):
        assert program.startswith(f'db = connect({json.dumps(URI)});'), program
        if 'countDocuments({email: EMAIL})' in program:
            answer = {'admin': 1 if self.team else 0}
        elif 'updateOne' in program:
            self.team_key = json.loads(program.split('const KEY = ', 1)[1].split(';', 1)[0])
            answer = {'matched': 1}
        else:
            teams = self.teams if self.teams is not None else (1 if self.team else 0)
            answer = {'teams': teams, 'same': teams == 1 and self.team_key == KEY}
        return Result(argv, 0, 'RESULT ' + json.dumps(answer) + '\n', 'Warning: EACCES\n')


def run(cluster, dry_run=False):
    runner = cluster.runner(dry_run)
    report = Report(redact=runner.redact, out=open('/dev/null', 'w'))
    code = clickstack.bootstrap(runner, report, timeout=0)
    return code, report, runner


class BootstrapTests(unittest.TestCase):
    def assert_no_secret_leaks(self, report, runner):
        for value in (PASSWORD, KEY, URI):
            for line in report.lines:
                self.assertNotIn(value, line)
            for call in runner.calls:
                self.assertNotIn(value, ' '.join(call), 'secrets go over stdin, never argv')

    def test_fresh_install_registers_admin_and_sets_key(self):
        cluster = Cluster()
        code, report, runner = run(cluster)
        self.assertEqual(code, 0, report.lines)
        body = json.dumps(json.dumps({'email': EMAIL, 'password': PASSWORD, 'confirmPassword': PASSWORD}))
        self.assertIn(body, cluster.registered, 'the request body is the JSON string of the credentials')
        self.assertEqual(cluster.team_key, KEY)
        self.assertIn(f'[OK] registered the admin account {EMAIL}', report.lines)
        self.assertIn('[OK] set the team ingestion key to CLICKSTACK_INGESTION_KEY', report.lines)
        self.assert_no_secret_leaks(report, runner)

    def test_second_run_changes_nothing(self):
        cluster = Cluster(team=True, team_key=KEY)
        code, report, runner = run(cluster)
        self.assertEqual(code, 0)
        self.assertIsNone(cluster.registered)
        self.assertFalse(any('updateOne' in ' '.join(c) for c in runner.calls))
        self.assertIn('[OK] team ingestion key matches CLICKSTACK_INGESTION_KEY', report.lines)

    def test_existing_team_with_random_key_gets_the_git_key(self):
        cluster = Cluster(team=True)
        code, report, _ = run(cluster)
        self.assertEqual(code, 0)
        self.assertIsNone(cluster.registered, 'never registers while a team exists')
        self.assertEqual(cluster.team_key, KEY)

    def test_dry_run_changes_nothing(self):
        cluster = Cluster()
        code, report, runner = run(cluster, dry_run=True)
        self.assertEqual(code, 0, report.lines)
        self.assertIsNone(cluster.registered)
        self.assertEqual(cluster.team_key, OTHER)
        self.assertIn(f'[INFO] would register the admin account {EMAIL}', report.lines)

    def test_failures_are_reported(self):
        cases = {
            'registration rejected': (Cluster(register_status=400), 'registration returned HTTP 400 (invalid)'),
            'two teams': (Cluster(team=True, teams=2), '2 teams exist; expected one'),
        }
        for name, (cluster, message) in cases.items():
            with self.subTest(name):
                code, report, runner = run(cluster)
                self.assertEqual(code, 1)
                self.assertIn(f'[BAD] {message}', report.lines)
                self.assert_no_secret_leaks(report, runner)

    def test_api_not_ready_times_out(self):
        cluster = Cluster(api_up=False)
        code, report, runner = run(cluster)
        self.assertEqual(code, 1)
        self.assertTrue(report.lines[-1].startswith('[BAD] HyperDX API not ready'))
        self.assertIsNone(cluster.registered)
        self.assertFalse(any('mongosh' in c for c in runner.calls), 'nothing touches MongoDB before the API is up')


if __name__ == '__main__':
    unittest.main()
