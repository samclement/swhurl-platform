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
  otlp-host-ip        a container that uses $(HOST_IP) (the OTLP endpoint) defines HOST_IP
                      from status.hostIP before it; otherwise the SDK gets the literal text

and, across the environments of one app (source manifests, not rendered):

  env-drift           environments differ only in namespace, hosts, image tag/digest,
                      replicas, resources and issuer; encrypted Secrets and the staging-only
                      image-automation.yaml (automatic deploys) are skipped

A reviewed exception goes on the HelmRelease as
  platform.swhurl.com/policy-exceptions: "rule-id=reason; other-rule=reason"
Exceptions without a reason are rejected.

    python3 -m swhurl check-apps [INSTANCE_DIR ...]   (default: every instance under
                                                apps and tests/fixtures/apps)
"""
from __future__ import annotations

import os
import tempfile
from pathlib import Path

import yaml

from swhurl import ROOT, platform
from swhurl.apps.contract import (
    AUTH_MIDDLEWARE,
    COOKIE_DOMAIN,
    ENVIRONMENT,
    EXCEPTIONS,
    EXPOSURE,
    IMAGE_AUTOMATION_FILE,
    INSTANCE_ROOTS,
    OTLP_HOST_IP,
    STORAGE_CLASSES,
    in_cookie_domain,
)
from swhurl.run import Runner

EXCEPTIONS_KEY = EXCEPTIONS
CACHE = Path(os.environ.get('XDG_CACHE_HOME', Path.home() / '.cache')) / 'swhurl-platform/charts'
WORKLOAD_KINDS = {'Deployment', 'StatefulSet', 'DaemonSet', 'Job', 'CronJob'}


def instances() -> list[Path]:
    found = []
    for base in (ROOT / r for r in INSTANCE_ROOTS):
        for helmrelease in sorted(base.glob('*/*/helmrelease.yaml')):
            found.append(helmrelease.parent)
    return found


def helm_repositories() -> dict[str, str]:
    path = ROOT / platform.HELM_REPOSITORIES
    return {d['metadata']['name']: d['spec']['url'] for d in yaml.safe_load_all(path.read_text())
            if d and d.get('kind') == 'HelmRepository'}


def chart_dir(name: str, version: str, repo: str, runner: Runner) -> Path:
    target = CACHE / f'{name}-{version}'
    if not (target / 'Chart.yaml').exists():
        CACHE.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(dir=CACHE) as tmp:
            runner.run(['helm', 'pull', name, '--repo', repo, '--version', version, '--untar', '--untardir', tmp])
            try:
                (Path(tmp) / name).rename(target)
            except OSError:
                # A parallel check (make check runs its targets together) pulled it first.
                if not (target / 'Chart.yaml').exists():
                    raise
    return target


def render(instance: Path, runner: Runner | None = None) -> list[dict]:
    """Kustomize the instance, then render any app-template HelmRelease with Helm."""
    runner = runner or Runner()
    docs = [d for d in yaml.safe_load_all(runner.output(['kubectl', 'kustomize', str(instance)])) if d]
    repos = helm_repositories()
    rendered = []
    for doc in docs:
        if doc['kind'] != 'HelmRelease':
            continue
        spec = doc['spec']['chart']['spec']
        chart = chart_dir(spec['chart'], spec['version'], repos[spec['sourceRef']['name']], runner)
        with tempfile.NamedTemporaryFile('w', suffix='.yaml') as values:
            yaml.safe_dump(doc['spec'].get('values', {}), values)
            values.flush()
            out = runner.output(['helm', 'template', doc['spec'].get('releaseName', doc['metadata']['name']),
                                 str(chart), '-n', doc['metadata']['namespace'], '-f', values.name])
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


def check(docs: list[dict]) -> list[tuple[str, str]]:
    """Return (rule, message) violations for one rendered instance."""
    violations: list[tuple[str, str]] = []
    namespaces = [d for d in docs if d['kind'] == 'Namespace']
    labels = (namespaces[0]['metadata'].get('labels') or {}) if namespaces else {}
    env = labels.get(ENVIRONMENT)
    exposure = labels.get(EXPOSURE)
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
            if ctx.get('runAsNonRoot', pod_ctx.get('runAsNonRoot')) is not True:
                violations.append(('non-root', f'{name} does not set runAsNonRoot'))
            caps = (ctx.get('capabilities') or {}).get('drop') or []
            if ctx.get('privileged') or ctx.get('allowPrivilegeEscalation') is not False or 'ALL' not in caps \
                    or (ctx.get('capabilities') or {}).get('add'):
                violations.append(('no-escalation', f'{name} allows privilege escalation or extra capabilities'))
            defined: dict[str, dict] = {}
            for var in container.get('env') or []:
                if f'$({OTLP_HOST_IP})' in str(var.get('value', '')):
                    source = defined.get(OTLP_HOST_IP, {}).get('valueFrom', {}).get('fieldRef', {}).get('fieldPath')
                    if source != 'status.hostIP':
                        violations.append(('otlp-host-ip', f"{name} uses $({OTLP_HOST_IP}) in {var['name']} without "
                                           f'defining {OTLP_HOST_IP} from status.hostIP before it'))
                defined[var['name']] = var
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


VARIES = {'namespace', 'host', 'tag', 'digest', 'replicas', 'resources', 'cert-manager.io/cluster-issuer', ENVIRONMENT}
VARIES_LISTS = {'hosts'}  # lists of host names (TLS); lists of rules are compared


def flatten(node, path: str = '') -> dict[str, object]:
    """Leaf values by dotted path, with the settings environments may vary removed."""
    if isinstance(node, dict):
        leaves = {}
        for key, value in node.items():
            if key in VARIES or (key in VARIES_LISTS and all(isinstance(v, str) for v in value or [])):
                continue
            leaves.update(flatten(value, f'{path}.{key}' if path else str(key)))
        return leaves
    if isinstance(node, list):
        leaves = {}
        for index, value in enumerate(node):
            leaves.update(flatten(value, f'{path}[{index}]'))
        return leaves
    return {path: node}


def source(instance: Path) -> dict[str, object]:
    leaves = {}
    for path in sorted(instance.glob('*.yaml')):
        if path.name.endswith('.sops.yaml') or path.name == IMAGE_AUTOMATION_FILE:
            continue  # automatic deploys exist in staging only
        for index, doc in enumerate(d for d in yaml.safe_load_all(path.read_text()) if d):
            if doc.get('kind') == 'Namespace':
                doc['metadata'].pop('name', None)
            if doc.get('kind') == 'Kustomization' and IMAGE_AUTOMATION_FILE in (doc.get('resources') or []):
                doc['resources'] = [r for r in doc['resources'] if r != IMAGE_AUTOMATION_FILE]
            leaves.update(flatten(doc, f'{path.name}#{index}'))
    return leaves


def drift(environments: list[Path]) -> list[str]:
    """Differences between an app's environments beyond the allowed settings."""
    if len(environments) < 2:
        return []
    base, *others = environments
    reference = source(base)
    problems = []
    for other in others:
        allowed, _ = exceptions([d for d in yaml.safe_load_all((other / 'helmrelease.yaml').read_text()) if d])
        if 'env-drift' in allowed:
            continue
        leaves = source(other)
        changed = sorted(k for k in reference.keys() | leaves.keys() if reference.get(k, '<absent>') != leaves.get(k, '<absent>'))
        if changed:
            shown = ', '.join(changed[:5]) + (f' (+{len(changed) - 5} more)' if len(changed) > 5 else '')
            problems.append(f'env-drift: {other.name} differs from {base.name} at {shown}')
    return problems


def evaluate(instance: Path, runner: Runner | None = None) -> list[str]:
    docs = render(instance, runner)
    allowed, problems = exceptions(docs)
    return problems + [f'{rule}: {message}' for rule, message in check(docs) if rule not in allowed]


def main(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    paths = [Path(a).resolve() for a in argv or []] or instances()
    failed = 0
    for instance in paths:
        label = instance.relative_to(ROOT) if instance.is_relative_to(ROOT) else instance
        problems = evaluate(instance, runner)
        if problems:
            failed += 1
            print(f'[BAD] {label}')
            for problem in problems:
                print(f'       {problem}')
        else:
            print(f'[OK] {label}')
    apps: dict[Path, list[Path]] = {}
    for instance in paths:
        apps.setdefault(instance.parent, []).append(instance)
    for app, environments in apps.items():
        problems = drift(sorted(environments, key=lambda e: e.name != 'prod'))
        if problems:
            failed += 1
            print(f'[BAD] {app.relative_to(ROOT) if app.is_relative_to(ROOT) else app}')
            for problem in problems:
                print(f'       {problem}')
    if failed:
        print(f'\n{failed} instance(s) or app(s) violate the app contract.')
        return 1
    print(f'\nApp policy passed for {len(paths)} instance(s).')
    return 0
