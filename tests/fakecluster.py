"""A healthy cluster for verify-platform, as a FakeRunner put together from one part per area.

Each area below gives its answers (``<AREA>`` is a function, because some read this checkout) and
the commands they answer (``<area>_rules``). A check added to an area adds to that area's two; a test
changes one answer with ``healthy(<key>=...)``.
"""
import base64
import datetime as dt
import json

from swhurl import flux
from swhurl.run import FakeRunner, Result

KEY = 'fixture-team-ingestion-key-0001'
CONSOLE_TAG = 'b63d2aff9dfd4cf9df0307e382038dcbd15e29ed'
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

    }


def flux_rules(runner, answer):
    get = ('kubectl', '-n', 'flux-system', 'get')
    runner.on(*get, 'deployment', handler=answer('kustomize-controller'))


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
    ('push-webhook', 'Push Webhook', [('ok', "Flux receiver flux-system/github is Ready"),
                                      ('ok', "GitHub's latest delivery to flux-webhook.example.test returned 200")]),
    ('image-webhooks', 'Image Webhooks', [('ok', 'Flux receiver flux-system/app-images is Ready'),
                                        ('ok', 'demo-shop: image webhook on samclement/demo-shop, latest delivery returned 200')]),
    ('console-token', 'Console GitHub Token', [('ok', 'GitHub accepts the console token (no expiry date)'),
                                             ('ok', 'GitHub accepts the console repository token (no expiry date)')]),
    ('runtime-secrets', 'Runtime Secrets', [('ok', 'logging/clickstack-ingestion-key.CLICKSTACK_INGESTION_KEY present')]),
    ('ingestion-key', 'Ingestion Key Sync', [('ok', 'ingestion Secret bytes match ClickStack; verify collector logs and fresh telemetry after a restart')]),
    ('registration', 'ClickStack Sign-up', [('ok', 'a team exists, so HyperDX registration is closed')]),
    ('retention', 'Retention', [('ok', 'all 9 telemetry tables expire after 30 days'),
                                ('ok', 'ClickHouse system logs expire after 7 days'),
                                ('ok', 'no ClickHouse merge failed in the last hour'),
                                ('ok', 'ClickStack MongoDB data volume is Retain (outlives its claim)')]),
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


AREAS = ((flux_answers, flux_rules), (cluster_answers, cluster_rules),
         (notification_answers, notification_rules))


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
    runner.on('kubectl', 'get', '--raw=/version', handler=answer('version'))
    for _, rules in AREAS:
        rules(runner, answer)
    return runner
