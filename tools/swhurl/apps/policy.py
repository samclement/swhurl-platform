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
  single-writer       a workload that mounts a ReadWriteOnce claim runs at most one replica, and a
                      Deployment stops the old pod before starting the new one (strategy Recreate):
                      two processes must never write one volume (a SQLite file, for example)
  otlp-host-ip        a container that uses $(HOST_IP) (the OTLP endpoint) defines HOST_IP
                      from status.hostIP before it; otherwise the SDK gets the literal text
  team                the Namespace names a team registered in apps/teams.yaml
  quota               the namespace has the platform ResourceQuota, and the app's replicas (plus one
                      during a rolling update) fit in it beside a backup pod and a certificate solver
  network-policy      the app's pods, and only those, carry the platform NetworkPolicy: Traefik is
                      admitted on the app port when the instance has a route, nobody otherwise
  identity            the app is told who it is (SWHURL_APP, SWHURL_ENV, defined before anything that
                      uses $(SWHURL_ENV)); a web app also gets the token issuer and the signing keys
                      in apps/signing-keys.json
  call-tokens         the only service-account tokens a pod carries are one per app it declares it
                      calls, each for that audience alone and short-lived
  events              an instance that declares events (SWHURL_EVENTS_PUBLISH / _SUBSCRIBE) has the
                      NATS address, a public key, its seed's Secret reference, and exactly the stream
                      and consumers that follow from the declaration; one that declares none has none

for an instance of this platform (under apps/; the test fixtures are exempt), from its source:

  platform-image      the app was made from a stack template: it runs ghcr.io/<owner>/<app>, the
                      image its repository publishes, and staging deploys new images
                      automatically (image-automation.yaml and the setter markers). No exceptions

across the instances of one environment (source manifests, not rendered):

  calls               every declared call names a web app of the same environment at its real port,
                      and an app allows exactly the callers that declare it (make app-sync rewrites both)
  events              every subscription names an event that an app of the same environment publishes

and, for the whole tree:

  event-users         platform/nats/users.yaml is what the instances' declarations generate
  notify-channels     every channel a team notifies exists as a listed Secret in platform/notify, and
                      routes.yaml is what apps/teams.yaml generates (make app-sync rewrites it)

and, across the environments of one app (source manifests, not rendered):

  env-drift           environments differ only in namespace, hosts, image tag/digest,
                      replicas, resources, issuer, exposure (route and sign-in), calls and events; encrypted
                      Secrets and the staging-only
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
from swhurl.apps import channels, events, helmrelease
from swhurl.apps.calling import (
    CALLERS_VARIABLE,
    ENVIRONMENT_VARIABLE,
    TOKEN_SECONDS,
    TOKENS_VOLUME,
    allowed_callers,
    callee_address,
    callee_variable,
    declared_calls,
    identity_variables,
    signing_keys,
    token_audiences,
)
from swhurl.apps.contract import (
    APP_OWNER,
    AUTH_MIDDLEWARE,
    AUTO_DEPLOY_ENV,
    COOKIE_DOMAIN,
    ENVIRONMENT,
    EXCEPTIONS,
    EXPOSURE,
    IMAGE_AUTOMATION_FILE,
    INSTANCE_ROOTS,
    NETWORK_POLICY_FILE,
    OTLP_HOST_IP,
    QUOTA,
    QUOTA_HEADROOM,
    STORAGE_CLASSES,
    TEAM,
    image_policy_name,
    in_cookie_domain,
    network_policy,
    quantity,
    strip_image_markers,
    teams,
)
from swhurl.run import Runner

EXCEPTIONS_KEY = EXCEPTIONS
CACHE = Path(os.environ.get('XDG_CACHE_HOME', Path.home() / '.cache')) / 'swhurl-platform/charts'
WORKLOAD_KINDS = {'Deployment', 'StatefulSet', 'DaemonSet', 'Job', 'CronJob'}


def instances() -> list[Path]:
    found = []
    for base in (ROOT / r for r in INSTANCE_ROOTS):
        for release_file in sorted(base.glob('*/*/helmrelease.yaml')):
            found.append(release_file.parent)
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


def check_isolation(docs: list[dict], labels: dict, known_teams: dict[str, str]) -> list[tuple[str, str]]:
    """The team, quota and network-policy rules for one rendered instance."""
    violations: list[tuple[str, str]] = []
    if labels.get(TEAM) not in known_teams:
        violations.append(('team', f'Namespace team {labels.get(TEAM)!r} is not registered in apps/teams.yaml '
                                   f'({", ".join(known_teams)}); set it with make app-team'))
    releases = [d for d in docs if d['kind'] == 'HelmRelease']
    if not releases:
        return violations
    app, namespace = releases[0]['metadata']['name'], releases[0]['metadata']['namespace']

    quotas = [d for d in docs if d['kind'] == 'ResourceQuota']
    if len(quotas) != 1 or quotas[0]['spec'].get('hard') != QUOTA:
        violations.append(('quota', f'the instance needs exactly one ResourceQuota with the platform sizes {QUOTA}'))
    needed = {key: quantity(value) for key, value in QUOTA_HEADROOM.items()}
    for doc in docs:
        spec = pod_spec(doc)
        if spec is None or doc['kind'] not in ('Deployment', 'StatefulSet'):
            continue
        rolling = doc['kind'] == 'Deployment' and (doc['spec'].get('strategy') or {}).get('type') != 'Recreate'
        pods = doc['spec'].get('replicas', 1) + (1 if rolling else 0)
        needed['pods'] += pods
        for container in spec['containers']:
            resources = container.get('resources') or {}
            for key, section, name in (('requests.cpu', 'requests', 'cpu'), ('requests.memory', 'requests', 'memory'),
                                       ('limits.memory', 'limits', 'memory')):
                needed[key] += pods * quantity((resources.get(section) or {}).get(name, 0))
    over = [key for key, value in needed.items() if value > quantity(QUOTA[key])]
    if over:
        violations.append(('quota', f'replicas and sizes exceed the namespace quota for {", ".join(over)} '
                                    f'(allowed {QUOTA}, less {QUOTA_HEADROOM} kept free)'))

    routed = labels.get(EXPOSURE) in ('authenticated-web', 'public')
    port = helmrelease.port(releases[0])
    callers = allowed_callers(helmrelease.variables(releases[0]))
    expected = network_policy(app, namespace, port if routed else None, port, callers)['spec']
    policies = [d for d in docs if d['kind'] == 'NetworkPolicy']
    if any(not (d['spec'].get('podSelector') or {}).get('matchLabels') for d in policies):
        violations.append(('network-policy', 'a NetworkPolicy selects every pod in the namespace: that blocks '
                                             "cert-manager's HTTP-01 solver; select the app's pods"))
    elif len(policies) != 1 or policies[0]['spec'] != expected:
        admitted = (['Traefik'] if routed else []) + callers
        violations.append(('network-policy', "the instance needs exactly one NetworkPolicy, on the app's pods, admitting "
                           + (f'{", ".join(admitted)} on port {port}' if admitted else 'nobody')
                           + '; make app-sync writes it'))
    return violations


def check_identity(docs: list[dict], labels: dict, keys: str) -> list[tuple[str, str]]:
    """The identity and call-tokens rules for one rendered instance."""
    violations: list[tuple[str, str]] = []
    releases = [d for d in docs if d['kind'] == 'HelmRelease']
    if not releases:
        return violations
    app, variables = releases[0]['metadata']['name'], helmrelease.variables(releases[0])
    wanted = identity_variables(app, labels.get(ENVIRONMENT), helmrelease.port(releases[0]) is not None, keys)
    wrong = [key for key, value in wanted.items() if variables.get(key) != value]
    if wrong:
        violations.append(('identity', f'{", ".join(wrong)} missing or not what the platform sets; make app-sync writes them'))
    calls = declared_calls(variables)
    for doc in docs:
        spec = pod_spec(doc)
        if spec is None:
            continue
        where = f"{doc['kind']}/{doc['metadata']['name']}"
        for container in spec['containers']:
            names = [var['name'] for var in container.get('env') or []]
            uses = [var['name'] for var in container.get('env') or [] if f'$({ENVIRONMENT_VARIABLE})' in str(var.get('value', ''))]
            late = [name for name in uses if ENVIRONMENT_VARIABLE not in names or names.index(ENVIRONMENT_VARIABLE) > names.index(name)]
            if late:
                violations.append(('identity', f'{where}: {", ".join(late)} uses $({ENVIRONMENT_VARIABLE}) before it is defined'))
        tokens = [source['serviceAccountToken'] for volume in spec.get('volumes') or []
                  for source in (volume.get('projected') or {}).get('sources') or [] if 'serviceAccountToken' in source]
        expected = [{'audience': audience, 'expirationSeconds': TOKEN_SECONDS, 'path': f'{audience}/token'}
                    for audience in token_audiences(calls)]
        if tokens != expected:
            violations.append(('call-tokens', f'{where} must carry one {TOKEN_SECONDS}-second token per declared call '
                               f'({", ".join(calls) or "none declared"}) and one for the notify service, each for that '
                               'audience only; make app-sync writes them'))
    violations += check_events(docs, releases[0], labels)
    return violations


def check_events(docs: list[dict], release: dict, labels: dict) -> list[tuple[str, str]]:
    """The events rule for one rendered instance."""
    app, env, variables = release['metadata']['name'], labels.get(ENVIRONMENT), helmrelease.variables(release)
    publishes, subscribes = events.published(variables), events.subscribed(variables)
    declared = [d for d in docs if d['kind'] in ('Stream', 'Consumer')]
    referenced = events.secret_name(app) in helmrelease.secret_references(release)
    problems = []
    if not (publishes or subscribes):
        if declared or referenced or variables.get(events.URL_VARIABLE) or variables.get(events.NKEY_VARIABLE):
            problems.append('declares no events but carries a stream, consumer, NATS address, key or Secret reference')
    else:
        key = str(variables.get(events.NKEY_VARIABLE, ''))
        if variables.get(events.URL_VARIABLE) != events.URL or not (key.startswith('U') and len(key) == 56) or not referenced:
            problems.append(f'needs {events.URL_VARIABLE}, a public key in {events.NKEY_VARIABLE} and the {events.secret_name(app)} Secret reference')
        wanted = events.manifests(app, env, publishes, subscribes)
        def shape(doc):
            return doc['kind'], doc['metadata']['name'], doc['spec']
        if sorted(map(shape, declared), key=str) != sorted(map(shape, wanted), key=str):
            problems.append('its streams and consumers are not what its declaration generates')
    return [('events', f'{problem}; make app-events sets them and make app-sync rewrites what follows') for problem in problems]


def check(docs: list[dict], known_teams: dict[str, str] | None = None, keys: str | None = None) -> list[tuple[str, str]]:
    """Return (rule, message) violations for one rendered instance."""
    violations: list[tuple[str, str]] = []
    namespaces = [d for d in docs if d['kind'] == 'Namespace']
    labels = (namespaces[0]['metadata'].get('labels') or {}) if namespaces else {}
    env = labels.get(ENVIRONMENT)
    exposure = labels.get(EXPOSURE)
    if not env or not exposure:
        violations.append(('exposure', 'instance Namespace needs platform.swhurl.com/environment and /exposure labels'))
    violations += check_isolation(docs, labels, teams() if known_teams is None else known_teams)
    violations += check_identity(docs, labels, signing_keys() if keys is None else keys)

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

    rwo = {d['metadata']['name'] for d in docs if d['kind'] == 'PersistentVolumeClaim'
           and 'ReadWriteOnce' in (d['spec'].get('accessModes') or [])}
    for doc in docs:
        spec = pod_spec(doc)
        claims = {v['persistentVolumeClaim']['claimName'] for v in (spec or {}).get('volumes') or []
                  if 'persistentVolumeClaim' in v}
        if spec is None or not claims & rwo or doc['kind'] in ('Job', 'CronJob'):
            continue
        where = f"{doc['kind']}/{doc['metadata']['name']}"
        if doc['spec'].get('replicas', 1) > 1:
            violations.append(('single-writer', f'{where} mounts a ReadWriteOnce claim and runs {doc["spec"]["replicas"]} replicas; at most 1'))
        if doc['kind'] == 'Deployment' and (doc['spec'].get('strategy') or {}).get('type') != 'Recreate':
            violations.append(('single-writer', f'{where} mounts a ReadWriteOnce claim, so it must stop the old pod first (strategy Recreate)'))

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


VARIES = {'namespace', 'host', 'tag', 'digest', 'replicas', 'resources', 'cert-manager.io/cluster-issuer', ENVIRONMENT,
          # Calls are declared per environment (make app-calls), like replicas: what follows from them varies too.
          ENVIRONMENT_VARIABLE, CALLERS_VARIABLE, TOKENS_VOLUME,
          # Events too: each environment has its own declaration, key and Secret (make app-events).
          events.PUBLISH_VARIABLE, events.SUBSCRIBE_VARIABLE, events.URL_VARIABLE, events.NKEY_VARIABLE, 'secret.reloader.stakater.com/reload'}
# Exposure may differ per environment (for example a signed-in staging preview of a public app). When
# the environments' exposure labels differ, their routes are not compared; each environment is still
# checked against the exposure rules on its own. With the same exposure, routes must match.
EXPOSURE_VARIES = {'ingress', EXPOSURE}
VARIES_LISTS = {'hosts'}  # lists of host names (TLS); lists of rules are compared


def flatten(node, path: str = '', varies: frozenset[str] = frozenset(VARIES)) -> dict[str, object]:
    """Leaf values by dotted path, with the settings environments may vary removed."""
    if isinstance(node, dict):
        leaves = {}
        for key, value in node.items():
            if key in varies or str(key).startswith('SWHURL_SERVICE_') \
                    or (key in VARIES_LISTS and all(isinstance(v, str) for v in value or [])):
                continue
            leaves.update(flatten(value, f'{path}.{key}' if path else str(key), varies))
        return leaves
    if isinstance(node, list):
        leaves = {}
        for index, value in enumerate(node):
            leaves.update(flatten(value, f'{path}[{index}]', varies))
        return leaves
    return {path: node}


def exposure_of(instance: Path) -> str | None:
    path = instance / 'namespace.yaml'
    docs = [d for d in yaml.safe_load_all(path.read_text()) if d] if path.exists() else []
    return ((docs[0].get('metadata') or {}).get('labels') or {}).get(EXPOSURE) if docs else None


def source(instance: Path, varies: frozenset[str] = frozenset(VARIES)) -> dict[str, object]:
    leaves = {}
    for path in sorted(instance.glob('*.yaml')):
        if path.name.endswith('.sops.yaml') or path.name in {IMAGE_AUTOMATION_FILE, 'production-alert.yaml',
                                                             NETWORK_POLICY_FILE, events.EVENTS_FILE}:
            # Automatic deploys and production-only promotion alerts are environment-specific; the
            # NetworkPolicy follows from each environment's exposure and callers (rule network-policy).
            continue
        for index, doc in enumerate(d for d in yaml.safe_load_all(path.read_text()) if d):
            if doc.get('kind') == 'Namespace':
                doc['metadata'].pop('name', None)
            if doc.get('kind') == 'Kustomization':
                doc['resources'] = [r for r in doc.get('resources') or []
                                    if r not in (IMAGE_AUTOMATION_FILE, events.EVENTS_FILE, events.SECRET_FILE)]
            if doc.get('kind') == 'HelmRelease':  # the reference to this environment's event key
                container = helmrelease.container(doc)
                refs = [ref for ref in container.get('envFrom') or []
                        if ref != {'secretRef': {'name': events.secret_name(doc['metadata']['name'])}}]
                if 'envFrom' in container:
                    container['envFrom'] = refs
                    if not refs:
                        del container['envFrom']
            leaves.update(flatten(doc, f'{path.name}#{index}', varies))
    return leaves


def drift(environments: list[Path]) -> list[str]:
    """Differences between an app's environments beyond the allowed settings."""
    if len(environments) < 2:
        return []
    base, *others = environments
    problems = []
    for other in others:
        allowed, _ = exceptions([d for d in yaml.safe_load_all((other / 'helmrelease.yaml').read_text()) if d])
        if 'env-drift' in allowed:
            continue
        varies = frozenset(VARIES | (EXPOSURE_VARIES if exposure_of(base) != exposure_of(other) else set()))
        reference, leaves = source(base, varies), source(other, varies)
        changed = sorted(k for k in reference.keys() | leaves.keys() if reference.get(k, '<absent>') != leaves.get(k, '<absent>'))
        if changed:
            shown = ', '.join(changed[:5]) + (f' (+{len(changed) - 5} more)' if len(changed) > 5 else '')
            problems.append(f'env-drift: {other.name} differs from {base.name} at {shown}')
    return problems


def check_calls(instances: list[Path]) -> dict[Path, list[str]]:
    """Problems with the calls declared among the instances of each environment (rule calls), by instance."""
    releases = {instance: yaml.safe_load(strip_image_markers((instance / 'helmrelease.yaml').read_text())[0])
                for instance in instances}
    problems: dict[Path, list[str]] = {}
    for instance, release in releases.items():
        app, env = instance.parent.name, instance.name
        peers = {other.parent.name: doc for other, doc in releases.items()
                 if other.name == env and other.parent.parent == instance.parent.parent}
        found = []
        for callee in declared_calls(helmrelease.variables(release)):
            target = peers.get(callee)
            if target is None or helmrelease.port(target) is None:
                found.append(f'calls {callee}, which is not a web app with a {env} instance')
                continue
            if helmrelease.variables(release)[callee_variable(callee)] != callee_address(callee, helmrelease.port(target)):
                found.append(f"{callee_variable(callee)} is not {callee}'s address {callee_address(callee, helmrelease.port(target))}")
            if app not in allowed_callers(helmrelease.variables(target)):
                found.append(f'calls {callee}, which does not allow it')
        for caller in allowed_callers(helmrelease.variables(release)):
            if caller not in peers or app not in declared_calls(helmrelease.variables(peers[caller])):
                found.append(f'allows {caller}, which does not declare a call to it')
        if found:
            problems[instance] = [f'calls: {problem}; make app-sync rewrites both sides' for problem in found]
        missing = [f'{publisher}.{event}' for publisher, event in events.subscribed(helmrelease.variables(release))
                   if publisher not in peers or event not in events.published(helmrelease.variables(peers[publisher]))]
        if missing:
            problems.setdefault(instance, []).append(
                f'events: subscribes to {", ".join(missing)}, which no app of {env} publishes')
    return problems


def check_event_users(root: Path = ROOT) -> list[str]:
    """Whether the NATS user list is what the instances generate (rule event-users)."""
    wanted = events.users_file(root)
    if not wanted:
        return []
    path, text = wanted
    if path.is_file() and path.read_text() == text:
        return []
    return [f'event-users: {events.USERS_FILE} is not what the instances declare; make app-sync rewrites it']


def platform_image(instance: Path) -> list[str]:
    """Why ``<app>/<env>`` is not an app made from a stack template (rule platform-image)."""
    app, env = instance.parent.name, instance.name
    text = (instance / 'helmrelease.yaml').read_text()
    image = helmrelease.image(yaml.safe_load(text))
    expected = f'ghcr.io/{APP_OWNER}/{app}'
    problems = []
    if image.get('repository') != expected:
        problems.append(f'image {image.get("repository")} is not {expected}; apps are made with make app-repo or the console')
    if env == AUTO_DEPLOY_ENV:
        listed = IMAGE_AUTOMATION_FILE in (yaml.safe_load((instance / 'kustomization.yaml').read_text()).get('resources') or [])
        if not (instance / IMAGE_AUTOMATION_FILE).is_file() or not listed:
            problems.append(f'{env} has no {IMAGE_AUTOMATION_FILE} in its kustomization.yaml: new images would not deploy')
        elif strip_image_markers(text)[1] != image_policy_name(app):
            problems.append(f'the image tag and digest lines lack their {image_policy_name(app)} setter markers')
    return [f'platform-image: {problem}' for problem in problems]


def evaluate(instance: Path, runner: Runner | None = None) -> list[str]:
    docs = render(instance, runner)
    allowed, problems = exceptions(docs)
    if instance.resolve().parent.parent == (ROOT / INSTANCE_ROOTS[0]).resolve():
        problems += platform_image(instance)
    root = instance.resolve().parents[2]  # <root>/apps/<app>/<env>
    return problems + [f'{rule}: {message}' for rule, message in check(docs, teams(root), signing_keys(root))
                       if rule not in allowed]


def main(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    paths = [Path(a).resolve() for a in argv or []] or instances()
    failed = 0
    call_problems = check_calls(paths)
    for instance in paths:
        label = instance.relative_to(ROOT) if instance.is_relative_to(ROOT) else instance
        problems = evaluate(instance, runner) + call_problems.get(instance, [])
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
    if not argv:  # the whole tree
        problems = check_event_users()
        if problems:
            failed += 1
            print(f'[BAD] {events.USERS_FILE}')
            for problem in problems:
                print(f'       {problem}')
        problems = channels.problems(ROOT)
        if problems:
            failed += 1
            print(f'[BAD] {channels.NOTIFY_DIR}')
            for problem in problems:
                print(f'       {problem}')
    if failed:
        print(f'\n{failed} instance(s) or app(s) violate the app contract.')
        return 1
    print(f'\nApp policy passed for {len(paths)} instance(s).')
    return 0
