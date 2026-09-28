"""
Deploy the generated app fixtures (tests/fixtures/apps, from the pushed Git
revision) through real Flux units, check them, and remove them:
  smoke-worker-staging  worker: runs, has no Service or Ingress
  smoke-web-staging     authenticated web: SOPS Secret decrypted by its own
                        unit and injected, route redirects to sign-in
  smoke-data-prod       persistent, digest-pinned: claim on local-path-retain,
                        data written
Touches only the homelab-app-smoke-* units, their namespaces and PVs.
"""
from __future__ import annotations

import base64
from pathlib import Path

import yaml

from swhurl import ROOT
from swhurl.apps.contract import APP, COOKIE_DOMAIN
from swhurl.livetests import LiveTest, Preflight, run_live_test

FIXTURES = Path('tests/fixtures/apps')
INSTANCES = ('smoke-worker-staging', 'smoke-web-staging', 'smoke-data-prod')
HOST = f'smoke-web.{COOKIE_DOMAIN}'
APP_LABEL = APP


def fixture_unit(instance: str) -> dict:
    """The generator's Flux unit for a fixture, pointed at the fixture path in Git."""
    unit = yaml.safe_load((ROOT / FIXTURES / 'clusters/home' / f'app-{instance}.yaml').read_text())
    unit['spec']['path'] = './' + FIXTURES.as_posix() + unit['spec']['path'][1:]
    return unit


def signed_in_redirect(t: LiveTest) -> str:
    return t.runner.output(['curl', '-sk', '-o', '/dev/null', '-w', '%{http_code} %{redirect_url}', f'https://{HOST}/'])


def cleanup(t: LiveTest) -> None:
    for instance in INSTANCES:
        t.quietly('-n', 'flux-system', 'delete', 'kustomization', f'homelab-app-{instance}', '--ignore-not-found',
                  '--wait=true')
        t.delete_namespace_if_labelled(instance, APP_LABEL, instance.rsplit('-', 1)[0], wait=True)
        t.destroy_volumes_of(instance)
    t.report.info('Cleaned up app-template test instances')


def body(t: LiveTest) -> None:
    for instance in INSTANCES:
        if t.get('get', 'namespace', instance) or t.get('-n', 'flux-system', 'get', 'kustomization',
                                                        f'homelab-app-{instance}'):
            raise Preflight(f'{instance} already exists; clean up first')
    t.cleanup.callback(cleanup, t)

    for instance in INSTANCES:
        t.apply(fixture_unit(instance))
    for instance in INSTANCES:
        ready = t.succeeds('-n', 'flux-system', 'wait', '--for=condition=Ready', f'kustomization/homelab-app-{instance}',
                           '--timeout=10m')
        conditions = ((t.get('-n', 'flux-system', 'get', 'kustomization', f'homelab-app-{instance}') or {})
                      .get('status') or {}).get('conditions') or [{}]
        t.check(ready, f'{instance} unit Ready (HelmRelease installed, workload healthy)',
                f"{instance} unit not Ready: {conditions[0].get('message', '')}")

    exposed = t.kubectl('-n', 'smoke-worker-staging', 'get', 'service,ingress', '-o', 'name').strip()
    t.check(not exposed, 'worker has no Service or Ingress', 'worker exposes a Service or Ingress')

    ns = 'smoke-web-staging'
    stored = ((t.get('-n', ns, 'get', 'secret', 'smoke-web-secret') or {}).get('data') or {}).get('GREETING', '')
    t.check(base64.b64decode(stored).decode() == 'REPLACE_ME',
            "SOPS Secret decrypted by the app's own Flux unit", 'Secret not decrypted')
    injected = t.runner.run(['kubectl', '-n', ns, 'exec', 'deploy/smoke-web', '--', 'printenv', 'GREETING'],
                            check=False).stdout.strip()
    t.check(injected == 'REPLACE_ME', 'Secret injected into the container', 'Secret not injected')
    pod_spec = ((t.get('-n', ns, 'get', 'deploy', 'smoke-web') or {}).get('spec') or {}).get('template', {}).get('spec', {})
    t.check((pod_spec.get('securityContext') or {}).get('runAsNonRoot') is True
            and pod_spec.get('automountServiceAccountToken') is False,
            'web pod runs non-root without a service-account token', 'web pod security defaults missing')
    location = ''

    def redirected() -> bool:
        nonlocal location
        location = signed_in_redirect(t)
        return location.startswith('302 https://accounts.google.com/')
    t.check(t.poll(redirected, attempts=30, interval=5), f'https://{HOST} redirects to Google sign-in',
            f'route not protected by sign-in: {location}')

    ns = 'smoke-data-prod'
    claims = (t.get('-n', ns, 'get', 'pvc') or {}).get('items') or [{}]
    t.check(claims[0].get('spec', {}).get('storageClassName') == 'local-path-retain'
            and claims[0].get('status', {}).get('phase') == 'Bound',
            'claim bound on local-path-retain', 'claim not bound on local-path-retain')
    t.check(t.succeeds('-n', ns, 'exec', 'deploy/smoke-data', '--', 'test', '-s', '/data/log'),
            'persistent volume written', 'no data written')
    image = ((t.get('-n', ns, 'get', 'deploy', 'smoke-data') or {}).get('spec') or {}).get('template', {}).get(
        'spec', {}).get('containers', [{}])[0].get('image', '')
    t.check('@sha256:' in image, 'prod image pinned by digest', 'prod image not digest-pinned')


def main(argv: list[str] | None = None, runner=None, report=None, sleep=None) -> int:
    kwargs = {'sleep': sleep} if sleep else {}
    return run_live_test(__doc__, body, passed='App template test passed.', runner=runner, report=report, **kwargs)
