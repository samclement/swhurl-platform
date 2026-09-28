#!/usr/bin/env python3
"""Check rendered app instances against the platform's app contract.

Renders each instance directory (kustomize, then any app-template HelmRelease
with Helm) and checks the resulting Kubernetes objects:

  image-pinned        every image has a version tag (not latest) or digest
  prod-digest         production images pin a digest
  non-root            pods/containers set runAsNonRoot
  no-escalation       no privilege escalation or privileged mode; drop ALL capabilities
  resources           CPU/memory requests and a memory limit
  no-sa-token         automountServiceAccountToken: false
  no-host-access      no hostNetwork/PID/IPC or hostPath volumes
  exposure            private: no Ingress; authenticated-web: sign-in middleware and a
                      host under the cookie domain; public: host outside it
  ingress-tls         every Ingress host is covered by TLS
  storage-class       claims name local-path or local-path-retain

A reviewed exception goes on the HelmRelease as
  platform.swhurl.com/policy-exceptions: "rule-id=reason; other-rule=reason"
Exceptions without a reason are rejected.

    scripts/app_policy.py [INSTANCE_DIR ...]   (default: every instance under
                                                tenants/apps and tests/fixtures/apps)
"""
from __future__ import annotations

import os
import subprocess
import sys
import tempfile
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[1]
COOKIE_DOMAIN = 'homelab.swhurl.com'
AUTH_MIDDLEWARE = 'ingress-oauth-auth-shared@kubernetescrd'
STORAGE_CLASSES = {'local-path', 'local-path-retain'}
EXCEPTIONS_KEY = 'platform.swhurl.com/policy-exceptions'
CACHE = Path(os.environ.get('XDG_CACHE_HOME', Path.home() / '.cache')) / 'swhurl-platform/charts'
WORKLOAD_KINDS = {'Deployment', 'StatefulSet', 'DaemonSet', 'Job', 'CronJob'}


def instances() -> list[Path]:
    found = []
    for base in (ROOT / 'tenants/apps', ROOT / 'tests/fixtures/apps/tenants/apps'):
        for helmrelease in sorted(base.glob('*/*/helmrelease.yaml')):
            found.append(helmrelease.parent)
    return found


def helm_repositories() -> dict[str, str]:
    path = ROOT / 'clusters/home/flux-system/sources/helmrepositories.yaml'
    return {d['metadata']['name']: d['spec']['url'] for d in yaml.safe_load_all(path.read_text())
            if d and d.get('kind') == 'HelmRepository'}


def chart_dir(name: str, version: str, repo: str) -> Path:
    target = CACHE / f'{name}-{version}'
    if not (target / 'Chart.yaml').exists():
        CACHE.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(dir=CACHE) as tmp:
            subprocess.run(['helm', 'pull', name, '--repo', repo, '--version', version, '--untar',
                            '--untardir', tmp], check=True, capture_output=True, text=True)
            (Path(tmp) / name).rename(target)
    return target


def render(instance: Path) -> list[dict]:
    docs = [d for d in yaml.safe_load_all(subprocess.run(
        ['kubectl', 'kustomize', str(instance)], check=True, capture_output=True, text=True).stdout) if d]
    repos = helm_repositories()
    rendered = []
    for doc in docs:
        if doc['kind'] != 'HelmRelease':
            continue
        spec = doc['spec']['chart']['spec']
        chart = chart_dir(spec['chart'], spec['version'], repos[spec['sourceRef']['name']])
        with tempfile.NamedTemporaryFile('w', suffix='.yaml') as values:
            yaml.safe_dump(doc['spec'].get('values', {}), values)
            values.flush()
            out = subprocess.run(['helm', 'template', doc['spec'].get('releaseName', doc['metadata']['name']),
                                  str(chart), '-n', doc['metadata']['namespace'], '-f', values.name],
                                 check=True, capture_output=True, text=True).stdout
        for item in yaml.safe_load_all(out):
            if item:
                item['metadata'].setdefault('namespace', doc['metadata']['namespace'])
                rendered.append(item)
    return docs + rendered


def exceptions(docs: list[dict]) -> tuple[dict[str, str], list[str]]:
    allowed, problems = {}, []
    for doc in docs:
        if doc['kind'] != 'HelmRelease':
            continue
        raw = (doc['metadata'].get('annotations') or {}).get(EXCEPTIONS_KEY, '')
        for entry in filter(None, (e.strip() for e in raw.split(';'))):
            rule, _, reason = entry.partition('=')
            if not reason.strip():
                problems.append(f'exception for {rule.strip()} has no reason')
            else:
                allowed[rule.strip()] = reason.strip()
    return allowed, problems


def pod_spec(doc: dict) -> dict | None:
    if doc['kind'] == 'CronJob':
        return doc['spec']['jobTemplate']['spec']['template']['spec']
    if doc['kind'] in WORKLOAD_KINDS:
        return doc['spec']['template']['spec']
    return None


def in_cookie_domain(host: str) -> bool:
    return host == COOKIE_DOMAIN or host.endswith('.' + COOKIE_DOMAIN)


def check(docs: list[dict]) -> list[tuple[str, str]]:
    """Return (rule, message) violations for one rendered instance."""
    violations: list[tuple[str, str]] = []
    namespaces = [d for d in docs if d['kind'] == 'Namespace']
    labels = (namespaces[0]['metadata'].get('labels') or {}) if namespaces else {}
    env = labels.get('platform.swhurl.com/environment')
    exposure = labels.get('platform.swhurl.com/exposure')
    if not env or not exposure:
        violations.append(('exposure', 'instance Namespace needs platform.swhurl.com/environment and /exposure labels'))

    for doc in docs:
        spec = pod_spec(doc)
        if spec is None:
            continue
        where = f"{doc['kind']}/{doc['metadata']['name']}"
        pod_ctx = spec.get('securityContext') or {}
        if spec.get('automountServiceAccountToken') is not False:
            violations.append(('no-sa-token', f'{where} mounts a service-account token'))
        if any(spec.get(k) for k in ('hostNetwork', 'hostPID', 'hostIPC')) or \
                any('hostPath' in v for v in spec.get('volumes') or []):
            violations.append(('no-host-access', f'{where} uses host namespaces or hostPath'))
        for container in (spec.get('initContainers') or []) + spec['containers']:
            name = f"{where}/{container['name']}"
            ctx = container.get('securityContext') or {}
            image = container['image']
            digest = '@sha256:' in image
            tag = image.split('@')[0].rsplit('/', 1)[-1].partition(':')[2]
            if not digest and (not tag or tag == 'latest'):
                violations.append(('image-pinned', f'{name} image {image} has no version tag or digest'))
            if env == 'prod' and not digest:
                violations.append(('prod-digest', f'{name} image {image} must pin a digest in prod'))
            if not (ctx.get('runAsNonRoot', pod_ctx.get('runAsNonRoot')) is True):
                violations.append(('non-root', f'{name} does not set runAsNonRoot'))
            caps = (ctx.get('capabilities') or {}).get('drop') or []
            if ctx.get('privileged') or ctx.get('allowPrivilegeEscalation') is not False or 'ALL' not in caps \
                    or (ctx.get('capabilities') or {}).get('add'):
                violations.append(('no-escalation', f'{name} allows privilege escalation or extra capabilities'))
            res = container.get('resources') or {}
            if not {'cpu', 'memory'} <= set(res.get('requests') or {}) or 'memory' not in (res.get('limits') or {}):
                violations.append(('resources', f'{name} lacks CPU/memory requests or a memory limit'))

    for doc in docs:
        if doc['kind'] == 'Ingress':
            where = f"Ingress/{doc['metadata']['name']}"
            hosts = [r.get('host', '') for r in doc['spec'].get('rules') or []]
            middleware = (doc['metadata'].get('annotations') or {}).get('traefik.ingress.kubernetes.io/router.middlewares', '')
            if exposure == 'private':
                violations.append(('exposure', f'{where}: private instances must not have an Ingress'))
            elif exposure == 'authenticated-web':
                if AUTH_MIDDLEWARE not in middleware:
                    violations.append(('exposure', f'{where} lacks the {AUTH_MIDDLEWARE} sign-in middleware'))
                if not all(in_cookie_domain(h) for h in hosts):
                    violations.append(('exposure', f'{where}: authenticated hosts must be under {COOKIE_DOMAIN}'))
            elif exposure == 'public' and any(in_cookie_domain(h) for h in hosts):
                violations.append(('exposure', f'{where}: public hosts must be outside {COOKIE_DOMAIN} (shared sign-in cookie)'))
            if any(in_cookie_domain(h) for h in hosts) and AUTH_MIDDLEWARE not in middleware and exposure != 'authenticated-web':
                violations.append(('exposure', f'{where}: every route under {COOKIE_DOMAIN} needs sign-in'))
            tls_hosts = {h for t in doc['spec'].get('tls') or [] for h in t.get('hosts') or []}
            if set(hosts) - tls_hosts:
                violations.append(('ingress-tls', f'{where}: hosts without TLS: {sorted(set(hosts) - tls_hosts)}'))
        if doc['kind'] == 'PersistentVolumeClaim' and doc['spec'].get('storageClassName') not in STORAGE_CLASSES:
            violations.append(('storage-class', f"PersistentVolumeClaim/{doc['metadata']['name']} must use one of {sorted(STORAGE_CLASSES)}"))
    return violations


def evaluate(instance: Path) -> list[str]:
    docs = render(instance)
    allowed, problems = exceptions(docs)
    return problems + [f'{rule}: {message}' for rule, message in check(docs) if rule not in allowed]


def main(argv: list[str]) -> int:
    paths = [Path(a).resolve() for a in argv] or instances()
    failed = 0
    for instance in paths:
        label = instance.relative_to(ROOT) if instance.is_relative_to(ROOT) else instance
        problems = evaluate(instance)
        if problems:
            failed += 1
            print(f'[BAD] {label}')
            for problem in problems:
                print(f'       {problem}')
        else:
            print(f'[OK] {label}')
    if failed:
        print(f'\n{failed} instance(s) violate the app contract.')
        return 1
    print(f'\nApp policy passed for {len(paths)} instance(s).')
    return 0


if __name__ == '__main__':
    sys.exit(main(sys.argv[1:]))
