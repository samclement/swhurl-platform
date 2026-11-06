"""A healthy cluster for verify-platform, as a FakeRunner put together from one part per area.

Each area below gives its answers (``<AREA>`` is a function, because some read this checkout) and
the commands they answer (``<area>_rules``). A check added to an area adds to that area's two; a test
changes one answer with ``healthy(<key>=...)``.
"""
import base64
import datetime as dt
import json

from swhurl import clickstack, flux, platform
from swhurl.run import FakeRunner, Result

KEY = 'fixture-team-ingestion-key-0001'
CONSOLE_TAG = 'b63d2aff9dfd4cf9df0307e382038dcbd15e29ed'
TOKEN = 'github_pat_fixture_token_0001'
READY = {'conditions': [{'type': 'Ready', 'status': 'True'}]}


def b64(value: bytes | str) -> str:
    return base64.b64encode(value.encode() if isinstance(value, str) else value).decode()


def recent_job():
    return {'spec': {}, 'status': {'lastSuccessfulTime': dt.datetime.now(dt.UTC).isoformat()}}


def flux_answers():
    return {
        'kustomize-controller': {'metadata': {'labels': {'app.kubernetes.io/version': f'v{flux.pinned_version()}'}},
                                 'spec': {'template': {'spec': {'containers': [
                                     {'args': ['--log-level=info', *flux.required_args()['kustomize-controller']]}]}}}},
        'receiver': {'status': READY},
        'hooks': [{'active': True, 'events': ['push', 'package', 'registry_package'],
                   'config': {'url': f'https://flux-webhook.{platform.base_domain()}/hook/abc'},
                   'last_response': {'code': 200, 'message': 'OK'}}],
    }


def flux_rules(runner, answer):
    get = ('kubectl', '-n', 'flux-system', 'get')
    (runner.on(*get, 'deployment', handler=answer('kustomize-controller'))
     .on(*get, 'receivers.notification.toolkit.fluxcd.io', handler=answer('receiver'))
     .on('gh', 'api', handler=answer('hooks')))  # the push webhook's and each app's image webhook


def clickstack_answers():
    return {
        'secret': {'data': {'CLICKSTACK_INGESTION_KEY': b64(KEY)}},
        'mongouri': {'data': {'connectionString.standard': b64('mongodb://u:p@db/hyperdx')}},
        'team': Result((), 0, 'Warning: EACCES\nRESULT ' + json.dumps({'keys': [KEY]}) + '\n'),
        'installation': Result((), 0, 'RESULT ' + json.dumps({'status': 200, 'body': {'isTeamExisting': True}}) + '\n'),
        'ttl30': Result((), 0, '9\t9\n'),
        'untimed': Result((), 0, '0\n'),
        'merges': Result((), 0, '0\t\n'),
        'pvc': {'spec': {'volumeName': 'pv-1'}},
        'pv': {'spec': {'persistentVolumeReclaimPolicy': 'Retain'}},
    }


def clickstack_rules(runner, answer):
    def clickhouse(args, _input):
        query = args[-1]
        return answer('ttl30' if 'toIntervalDay(30)' in query else 'merges' if 'MergeParts' in query else 'untimed')(args, _input)

    def recorded(key):
        def handler(args, program):
            runner.stdin.append((args, program))
            return answer(key)(args, program)
        return handler

    (runner.on('kubectl', '-n', 'logging', 'get', 'secret', 'clickstack-ingestion-key', handler=answer('secret'))
     .on('kubectl', '-n', 'observability', 'get', 'secret', clickstack.MONGO_URI_SECRET, handler=answer('mongouri'))
     .on('kubectl', '-n', 'observability', 'exec', '-i', clickstack.MONGO_POD, handler=recorded('team'))
     .on('kubectl', '-n', 'observability', 'exec', '-i', clickstack.APP, handler=recorded('installation'))
     .on('kubectl', '-n', 'observability', 'exec', clickstack.CLICKHOUSE_POD, handler=clickhouse)
     .on('kubectl', '-n', 'observability', 'get', 'pvc', handler=answer('pvc'))
     .on('kubectl', 'get', 'pv', handler=answer('pv')))


def verify_report(*checks):
    """swhurl-v2 verify's document: ``checks`` are ``(name, section, [(level, message), ...])``."""
    return {'kind': 'v2-platform-checks', 'version': 1, 'source': 'verify-platform', 'observedAt': '2026-11-06T12:00:00Z',
            'passed': all(level != 'bad' for _, _, entries in checks for level, _ in entries),
            'checks': [{'name': name, 'section': section, 'entries': [{'level': level, 'message': message} for level, message in entries]}
                       for name, section, entries in checks]}


HEALTHY_CHECKS = (
    ('flux-units', 'Flux Kustomizations', [('ok', 'homelab-a'), ('ok', 'homelab-b')]),
    ('ingress', 'Ingress', [('ok', 'Traefik redirects HTTP to HTTPS')]),
    ('alerts', 'Alerts', [('ok', 'alert failures sends to ntfy-platform')]),
    ('notify-service', 'Notify service', [('ok', f'the notify service is running {CONSOLE_TAG}, as the console')]),
    ('dashboards', 'App dashboards and alerts', [('ok', 'dashboard and alert sync succeeded in the last 5 minutes')]),
    ('notifications', 'Notifications', [('ok', 'notification checker succeeded in the last 5 minutes')]),
)
"""What swhurl-v2 verify (its cluster scope, the console's) reports for a healthy cluster: Go decides these checks, so
here they are only an answer."""

PLATFORM_CHECKS = (
    *HEALTHY_CHECKS,
    ('image-automation', 'Image Automation', [('ok', 'demo-shop-staging: newest image 1-abc1234, deployed')]),
    ('isolation', 'App isolation', [('ok', '1 app namespace(s) have a registered team, the platform quota and a NetworkPolicy')]),
    ('signing-keys', 'Call identity', [('ok', "v2/testdata/profile.json states the cluster's 1 signing key(s)"),
                                       ('ok', 'token issuer is https://kubernetes.default.svc.cluster.local')]),
    ('events', 'Events', [('ok', 'NATS is running'), ('ok', 'the stream controller is running')]),
    ('egress', 'Egress proxy', [('ok', 'the egress proxy runs 2 replicas'), ('ok', '1 instance(s) each have a port of their own')]),
    ('channels', 'Notification channels', [('ok', '1 team(s) notify a channel, as in Git'), ('ok', '1 channel(s) have their Secret')]),
    ('backups', 'Backups', [('ok', 'newest MongoDB backup in s3://bucket/clickstack-mongodb/ is 1.0 h old')]),
    ('sqlite-backups', 'App SQLite backups', [('ok', 'no app has a SQLite database')]),
    ('console', 'Console', [('ok', f'console image {CONSOLE_TAG[:7]} is built from the current tooling')]),
)
"""What swhurl-v2 verify --scope platform, verify-platform's, reports for a healthy platform."""


def cluster_answers():
    return {'v2-verify': verify_report(*PLATFORM_CHECKS)}


def cluster_rules(runner, answer):
    runner.on('swhurl-v2', 'verify', handler=answer('v2-verify'))


def notification_answers():
    return {}


def notification_rules(runner, answer):
    (runner.on('systemctl', 'is-active', stdout='active\n')
     .on('systemctl', 'show', stdout=('Result=success\nExecMainStatus=0\nExecMainExitTimestamp='
                                      + dt.datetime.now().astimezone().strftime('%a %Y-%m-%d %H:%M:%S %Z') + '\n')))


def console_answers():
    return {
        'token': {'data': {'GITHUB_TOKEN': b64(TOKEN)}},
        'github': Result((), 0, 'HTTP/2 200\r\ngithub-authentication-token-expiration: 2099-01-01 00:00:00 UTC\r\n\r\n'),
    }


def console_rules(runner, answer):
    def github(args, config):
        runner.github.append((args, config))
        return answer('github')(args, config)

    (runner.on('kubectl', '-n', 'console', 'get', 'secret', 'console-github', handler=answer('token'))
     .on('curl', handler=github))


AREAS = ((flux_answers, flux_rules), (clickstack_answers, clickstack_rules), (cluster_answers, cluster_rules),
         (notification_answers, notification_rules), (console_answers, console_rules))


def healthy(**overrides):
    """A FakeRunner answering every verify-platform call for a healthy cluster; ``overrides`` replace answers by key."""
    responses = {'version': Result((), 0, '{}')}
    for answers, _ in AREAS:
        found = answers()
        assert not found.keys() & responses.keys(), f'two areas answer {found.keys() & responses.keys()}'
        responses.update(found)
    assert overrides.keys() <= responses.keys(), f'no area answers {overrides.keys() - responses.keys()}'
    responses.update(overrides)

    def answer(key):
        def handler(args, _input):
            value = responses[key]
            if isinstance(value, Result):
                return Result(args, value.returncode, value.stdout, value.stderr)
            return Result(args, 0, json.dumps(value))
        return handler

    runner = FakeRunner()
    runner.stdin = []  # (args, program) of every program sent into a pod (mongosh scripts, API requests)
    runner.github = []  # (args, stdin) of each GitHub API call
    runner.on('kubectl', 'get', '--raw=/version', handler=answer('version'))
    for _, rules in AREAS:
        rules(runner, answer)
    return runner
