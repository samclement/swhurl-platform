"""A healthy cluster for verify-platform, as a FakeRunner put together from one part per area.

Each area below gives its answers (``<AREA>`` is a function, because some read this checkout) and
the commands they answer (``<area>_rules``). A check added to an area adds to that area's two; a test
changes one answer with ``healthy(<key>=...)``.
"""
import base64
import datetime as dt
import json

import yaml

from swhurl import ROOT, clickstack, flux, platform
from swhurl.apps import calling, channels, contract
from swhurl.run import FakeRunner, Result

KEY = 'fixture-team-ingestion-key-0001'
CONSOLE_TAG = 'b63d2aff9dfd4cf9df0307e382038dcbd15e29ed'
TOKEN = 'github_pat_fixture_token_0001'
INSTANCES = [(p.parent.parent.name, p.parent.name) for p in sorted((ROOT / 'apps').glob('*/*/helmrelease.yaml'))]
READY = {'conditions': [{'type': 'Ready', 'status': 'True'}]}


def b64(value: bytes | str) -> str:
    return base64.b64encode(value.encode() if isinstance(value, str) else value).decode()


def fresh_backup(hours=1):
    taken = dt.datetime.now(dt.UTC) - dt.timedelta(hours=hours)
    return f'clickstack-mongodb-{taken:%Y%m%dT%H%M%SZ}.archive.gz.age'


def recent_job():
    return {'spec': {}, 'status': {'lastSuccessfulTime': dt.datetime.now(dt.UTC).isoformat()}}


def flux_answers():
    return {
        'units': {'items': [{'metadata': {'name': 'homelab-b'}, 'status': READY},
                            {'metadata': {'name': 'homelab-a'}, 'status': READY}]},
        'kustomize-controller': {'metadata': {'labels': {'app.kubernetes.io/version': f'v{flux.pinned_version()}'}},
                                 'spec': {'template': {'spec': {'containers': [
                                     {'args': ['--log-level=info', *flux.required_args()['kustomize-controller']]}]}}}},
        'receiver': {'status': READY},
        'hooks': [{'active': True, 'events': ['push', 'package', 'registry_package'],
                   'config': {'url': f'https://flux-webhook.{platform.base_domain()}/hook/abc'},
                   'last_response': {'code': 200, 'message': 'OK'}}],
        'alerts': {'items': [{'metadata': {'name': 'failures'}, 'spec': {'providerRef': {'name': 'ntfy-platform'}}}]},
        'providers': {'items': [{'metadata': {'name': 'ntfy-platform'}}]},
    }


def flux_rules(runner, answer):
    get = ('kubectl', '-n', 'flux-system', 'get')
    (runner.on(*get, 'kustomizations.kustomize.toolkit.fluxcd.io', handler=answer('units'))
     .on(*get, 'deployment', handler=answer('kustomize-controller'))
     .on(*get, 'receivers.notification.toolkit.fluxcd.io', handler=answer('receiver'))
     .on('gh', 'api', handler=answer('hooks'))  # the push webhook's and each app's image webhook
     .on(*get, 'alerts.notification.toolkit.fluxcd.io', handler=answer('alerts'))
     .on(*get, 'providers.notification.toolkit.fluxcd.io', handler=answer('providers')))


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


def ingress_answers():
    return {'traefik': {'spec': {'template': {'spec': {'containers': [
        {'args': ['--entryPoints.web.http.redirections.entryPoint.scheme=https']}]}}}}}


def ingress_rules(runner, answer):
    runner.on('kubectl', '-n', 'kube-system', 'get', 'deploy', 'traefik', handler=answer('traefik'))


def staging_tag(app):
    return yaml.safe_load((ROOT / 'apps' / app / 'staging/helmrelease.yaml').read_text())[
        'spec']['values']['controllers']['main']['containers']['main']['image']['tag']


def policy(app, tag, ready=READY):
    """An app's ImagePolicy as Flux reports it: ``tag`` is the newest image it picked."""
    return {'metadata': {'name': f'{app}-staging', 'labels': {'platform.swhurl.com/app': app}},
            'status': ready | {'latestRef': {'tag': tag}}}


def apps_answers():
    """The isolation check walks the instances in this checkout: answer for each of them."""
    return {
        'policies': {'items': [policy(app, staging_tag(app)) for app, env in INSTANCES
                               if env == 'staging' and (ROOT / 'apps' / app / env / contract.IMAGE_AUTOMATION_FILE).is_file()]},
        'app-namespaces': {'items': [{'metadata': {'name': f'{app}-{env}', 'labels': {
            'platform.swhurl.com/app': app, contract.TEAM: 'demo'}}} for app, env in INSTANCES]},
        'quotas': {'items': [{'metadata': {'namespace': f'{app}-{env}'}, 'spec': {'hard': contract.QUOTA}}
                             for app, env in INSTANCES]},
        'network-policies': {'items': [{'metadata': {'namespace': f'{app}-{env}'}, 'spec': {
            'podSelector': {'matchLabels': {'app.kubernetes.io/instance': app}}}} for app, env in INSTANCES]},
        'nats': {'status': {'readyReplicas': 1}},
        'nats-controller': {'status': {'readyReplicas': 1}},
        'streams': {'items': [{'kind': 'Stream', 'metadata': {'namespace': 'w-staging', 'name': 'staging-w'}, 'status': READY}]},
        'jwks': json.loads((ROOT / calling.SIGNING_KEYS_FILE).read_text()),
        'openid': {'issuer': calling.ISSUER},
        'deployments': {'items': []},  # where app SQLite databases are looked for
    }


def apps_rules(runner, answer):
    get = ('kubectl', '-n', 'flux-system', 'get')
    (runner.on(*get, 'imagepolicies.image.toolkit.fluxcd.io', handler=answer('policies'))
     .on('kubectl', 'get', 'namespaces', '-l', 'platform.swhurl.com/app', handler=answer('app-namespaces'))
     .on('kubectl', 'get', 'resourcequotas', handler=answer('quotas'))
     .on('kubectl', 'get', 'networkpolicies.networking.k8s.io', handler=answer('network-policies'))
     .on('kubectl', '-n', 'messaging', 'get', 'statefulset', 'nats', handler=answer('nats'))
     .on('kubectl', '-n', 'messaging', 'get', 'deployment', 'nats-controller', handler=answer('nats-controller'))
     .on('kubectl', 'get', 'streams.jetstream.nats.io,consumers.jetstream.nats.io', handler=answer('streams'))
     .on('kubectl', 'get', '--raw', '/openid/v1/jwks', handler=answer('jwks'))
     .on('kubectl', 'get', '--raw', '/.well-known/openid-configuration', handler=answer('openid'))
     .on('kubectl', 'get', 'deployments', '--all-namespaces', handler=answer('deployments')))


def notification_answers():
    return {
        'notifications': recent_job(),
        'routes': {'items': [{'metadata': {'name': channels.ROUTES_NAME}, 'data': {channels.ROUTES_KEY: json.dumps(
            channels.routes(ROOT))}}]},
        'channel-secrets': Result((), 0, ''.join(f'secret/channel-{name}\n' for name in channels.channels(ROOT))),
        'notify': {'spec': {'template': {'spec': {'containers': [
            {'image': f'ghcr.io/samclement/swhurl-console:{CONSOLE_TAG}@sha256:abc'}]}}}, 'status': {'readyReplicas': 1}},
    }


def notification_rules(runner, answer):
    (runner.on('systemctl', 'is-active', stdout='active\n')
     .on('systemctl', 'show', stdout=('Result=success\nExecMainStatus=0\nExecMainExitTimestamp='
                                      + dt.datetime.now().astimezone().strftime('%a %Y-%m-%d %H:%M:%S %Z') + '\n'))
     .on('kubectl', '-n', 'console', 'get', 'cronjob', 'console-notifications', handler=answer('notifications'))
     .on('kubectl', '-n', 'notify', 'get', 'deployment', 'notify', handler=answer('notify'))
     .on('kubectl', '-n', 'notify', 'get', 'configmaps', handler=answer('routes'))
     .on('kubectl', '-n', 'notify', 'get', 'secrets', '-o', 'name', handler=answer('channel-secrets')))


def backup_answers():
    return {'remote': [f'clickstack-mongodb/{fresh_backup()}', f'clickstack-mongodb/{fresh_backup()[:-15]}.json']}


def backup_rules(runner, answer):
    runner.on('aws', 's3api', 'list-objects-v2', handler=answer('remote'))


def console_answers():
    return {
        'dashboards': recent_job(),
        'console-jobs': {'data': {}},
        'console': {'spec': {'values': {'controllers': {'main': {'containers': {'main': {'image': {'tag': CONSOLE_TAG}}}}}}}},
        'git-diff': Result((), 0),
        'token': {'data': {'GITHUB_TOKEN': b64(TOKEN)}},
        'github': Result((), 0, 'HTTP/2 200\r\ngithub-authentication-token-expiration: 2099-01-01 00:00:00 UTC\r\n\r\n'),
    }


def console_rules(runner, answer):
    def github(args, config):
        runner.github.append((args, config))
        return answer('github')(args, config)

    (runner.on('kubectl', '-n', 'console', 'get', 'cronjob', 'console-dashboards', handler=answer('dashboards'))
     .on('kubectl', '-n', 'console', 'get', 'helmrelease', 'console', handler=answer('console'))
     .on('kubectl', '-n', 'console', 'get', 'configmap', 'console-jobs', handler=answer('console-jobs'))
     .on('git', '-C', str(ROOT), 'diff', '--quiet', CONSOLE_TAG, 'HEAD', '--', handler=answer('git-diff'))
     .on('kubectl', '-n', 'console', 'get', 'secret', 'console-github', handler=answer('token'))
     .on('curl', handler=github))


AREAS = ((flux_answers, flux_rules), (clickstack_answers, clickstack_rules), (ingress_answers, ingress_rules),
         (apps_answers, apps_rules), (notification_answers, notification_rules), (backup_answers, backup_rules),
         (console_answers, console_rules))


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
