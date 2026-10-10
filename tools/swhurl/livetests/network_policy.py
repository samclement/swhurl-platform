"""
Prove a NetworkPolicy on an app's pods isolates it without breaking the platform:
  - before the policy, a pod in another namespace reaches the app
  - with a policy that selects only the app's pods and admits Traefik, that pod
    is blocked, Traefik still serves the route and the app stays Ready (probes)
  - an unselected pod in the same namespace on port 8089 stays reachable, as the
    HTTP-01 solver must (no certificate is requested)
  - deleting the policy lets the other namespace in again
Creates only two namespaces labelled platform.swhurl.com/network-policy-test=true
and removes them.
"""
from __future__ import annotations

from swhurl.apps.contract import COOKIE_DOMAIN
from swhurl.livetests import LiveTest, Preflight, run_live_test
from swhurl.platform import label

APP_NS = 'network-policy-test-app'
CLIENT_NS = 'network-policy-test-client'
LABEL = label('network-policy-test')
IMAGE = 'node:24-bookworm-slim'
HOST = f'network-policy-test.{COOKIE_DOMAIN}'
SERVER = "require('http').createServer((q, s) => s.end('ok')).listen(Number(process.env.PORT), '0.0.0.0')"
FETCH = ("fetch(process.argv[1], {signal: AbortSignal.timeout(4000)}).then((r) => r.text())"
         ".then((t) => console.log(t), () => console.log('blocked'))")


def namespace(name: str) -> dict:
    return {'apiVersion': 'v1', 'kind': 'Namespace', 'metadata': {'name': name, 'labels': {LABEL: 'true'}}}


def workload(name: str, port: int | None) -> list[dict]:
    """A Deployment answering "ok" on ``port`` with its Service, or an idle client when there is no port."""
    container = {'name': 'c', 'image': IMAGE, 'command': ['node', '-e', SERVER] if port else ['sleep', '3600000'],
                 'resources': {'requests': {'cpu': '5m', 'memory': '48Mi'}, 'limits': {'memory': '128Mi'}}}
    if port:
        container['env'] = [{'name': 'PORT', 'value': str(port)}]
        container['readinessProbe'] = {'httpGet': {'path': '/', 'port': port}, 'periodSeconds': 5,
                                       'failureThreshold': 2}
    docs = [{'apiVersion': 'apps/v1', 'kind': 'Deployment', 'metadata': {'name': name, 'labels': {LABEL: 'true'}},
             'spec': {'replicas': 1, 'selector': {'matchLabels': {'app': name}},
                      'template': {'metadata': {'labels': {'app': name, LABEL: 'true'}},
                                   'spec': {'automountServiceAccountToken': False,
                                            'securityContext': {'runAsNonRoot': True, 'runAsUser': 65532},
                                            'containers': [container]}}}}]
    if port:
        docs.append({'apiVersion': 'v1', 'kind': 'Service', 'metadata': {'name': name, 'labels': {LABEL: 'true'}},
                     'spec': {'selector': {'app': name}, 'ports': [{'port': port}]}})
    return docs


def ingress() -> dict:
    """A route on Traefik's default certificate: nothing is asked of cert-manager."""
    return {'apiVersion': 'networking.k8s.io/v1', 'kind': 'Ingress',
            'metadata': {'name': 'web', 'labels': {LABEL: 'true'},
                         'annotations': {'traefik.ingress.kubernetes.io/router.entrypoints': 'websecure'}},
            'spec': {'ingressClassName': 'traefik', 'tls': [{'hosts': [HOST]}],
                     'rules': [{'host': HOST, 'http': {'paths': [{'path': '/', 'pathType': 'Prefix', 'backend': {
                         'service': {'name': 'web', 'port': {'number': 8080}}}}]}}]}}


def policy() -> dict:
    """What an app instance will carry: its own pods only, Traefik admitted on the app port."""
    traefik = {'namespaceSelector': {'matchLabels': {'kubernetes.io/metadata.name': 'kube-system'}},
               'podSelector': {'matchLabels': {'app.kubernetes.io/name': 'traefik'}}}
    return {'apiVersion': 'networking.k8s.io/v1', 'kind': 'NetworkPolicy',
            'metadata': {'name': 'web', 'labels': {LABEL: 'true'}},
            'spec': {'podSelector': {'matchLabels': {'app': 'web'}}, 'policyTypes': ['Ingress'],
                     'ingress': [{'from': [traefik], 'ports': [{'port': 8080}]}]}}


def reaches(t: LiveTest, service: str, port: int) -> bool:
    """Whether the client pod in the other namespace gets an answer from ``service``."""
    result = t.runner.run(['kubectl', '-n', CLIENT_NS, 'exec', 'deploy/client', '--', 'node', '-e', FETCH,
                           f'http://{service}.{APP_NS}.svc:{port}/'], check=False)
    return result.stdout.strip() == 'ok'


def through_traefik(t: LiveTest) -> str:
    return t.runner.run(['curl', '-sk', '--max-time', '10', '-o', '/dev/null', '-w', '%{http_code}',
                         f'https://{HOST}/'], check=False).stdout.strip()


def ready(t: LiveTest) -> bool:
    return ((t.get('-n', APP_NS, 'get', 'deploy', 'web') or {}).get('status') or {}).get('readyReplicas') == 1


def cleanup(t: LiveTest) -> None:
    for name in (APP_NS, CLIENT_NS):
        t.delete_namespace_if_labelled(name, LABEL, wait=True)
    t.report.info('Cleaned up network-policy test resources')


def body(t: LiveTest) -> None:
    if any(t.get('get', 'namespace', name) for name in (APP_NS, CLIENT_NS)):
        raise Preflight('Test resources already exist; clean up first')
    t.cleanup.callback(cleanup, t)

    t.step('Deploy an app, a solver stand-in and a client in another namespace')
    t.apply(namespace(APP_NS), namespace(CLIENT_NS))
    t.apply(*workload('web', 8080), *workload('solver', 8089), ingress(), namespace=APP_NS)
    t.apply(*workload('client', None), namespace=CLIENT_NS)
    for ns, name in ((APP_NS, 'web'), (APP_NS, 'solver'), (CLIENT_NS, 'client')):
        t.kubectl('-n', ns, 'rollout', 'status', f'deploy/{name}', '--timeout=5m')

    t.step('Before the policy')
    t.check(t.poll(lambda: reaches(t, 'web', 8080), attempts=10, interval=3),
            'another namespace reaches the app', 'another namespace cannot reach the app even without a policy')
    status = ''

    def served() -> bool:
        nonlocal status
        status = through_traefik(t)
        return status == '200'
    t.check(t.poll(served, attempts=20, interval=3), f'Traefik serves https://{HOST}',
            f'Traefik does not serve the route: HTTP {status}')

    t.step('With the policy')
    t.apply(policy(), namespace=APP_NS)
    t.check(t.poll(lambda: not reaches(t, 'web', 8080), attempts=20, interval=3),
            'another namespace is blocked', 'another namespace still reaches the app: the policy is not enforced')
    t.sleep(20)  # longer than the probe's failure window
    t.check(not reaches(t, 'web', 8080), 'still blocked after 20 seconds', 'the block did not hold')
    t.check(served(), 'Traefik still serves the route', f'Traefik is blocked too: HTTP {status}')
    t.check(ready(t), 'the app stays Ready (kubelet probes pass)', 'the app went unready: probes are blocked')
    t.check(reaches(t, 'solver', 8089), 'an unselected pod on 8089 in the same namespace stays reachable',
            'the policy also blocks pods it does not select')

    t.step('Without the policy again')
    t.kubectl('-n', APP_NS, 'delete', 'networkpolicy', 'web')
    t.check(t.poll(lambda: reaches(t, 'web', 8080), attempts=20, interval=3),
            'another namespace reaches the app once the policy is deleted', 'still blocked after deleting the policy')


def main(argv: list[str] | None = None, runner=None, report=None, sleep=None) -> int:
    kwargs = {'sleep': sleep} if sleep else {}
    return run_live_test(__doc__, body, passed='Network policy test passed.', runner=runner, report=report, **kwargs)
