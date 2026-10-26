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
                      automatically (its ImagePolicy in image-automation.yaml). No exceptions

and, for the whole tree:

  app relations       one record evaluator checks calls and subscriptions: absent apps or consent
                      are pending without access; present unsupported targets and missing events fail.
                      Exact generated comparisons check variables, tokens, policies, Consumers and NATS
                      permissions, including required identities and the users file
  egress-ports        every instance has its own port on the egress proxy, and platform/egress/proxy.yaml
                      is what the instances generate (make app-sync rewrites it)
  egress-grants       each host an instance may reach (make app-egress) is a host name or *.domain with
                      a port, granted once and not inside a wildcard grant of the same instance
  notify-channels     every channel a team notifies exists as a listed Secret in platform/notify, and
                      routes.yaml is what apps/teams.yaml generates (make app-sync rewrites it)

  app records         App, Release and Settings parse strictly; every committed manifest must be
                      exactly what its instance's records render. Settings belong to each environment;
                      a release may be ahead in staging, but its kind and command agree across
                      environments. Encrypted Secrets are never compared

A reviewed exception goes in Settings.exceptions in instance.yaml as
  exceptions: "rule-id=reason; other-rule=reason"
make app-sync renders the HelmRelease's platform.swhurl.com/policy-exceptions annotation;
do not edit that annotation by hand. Exceptions without a reason are rejected.

    python3 -m swhurl check-apps [INSTANCE_DIR ...]   (default: every instance under
                                                apps and tests/fixtures/apps)
"""
from __future__ import annotations

import os
import tempfile
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

import yaml

from swhurl import ROOT, app_alerts, interface, platform, yamlio
from swhurl.apps import channels, egress, events, helmrelease, offers
from swhurl.apps.calling import (
    TOKEN_SECONDS,
    allowed_callers,
    declared_calls,
    identity_variables,
    signing_keys,
    token_audiences,
)
from swhurl.apps.contract import (
    APP_OWNER,
    AUTH_MIDDLEWARE,
    AUTO_DEPLOY_ENV,
    BACKUP_LABEL,
    COOKIE_DOMAIN,
    IMAGE_AUTOMATION_FILE,
    QUOTA,
    QUOTA_HEADROOM,
    STORAGE_CLASSES,
    image_policy_name,
    in_cookie_domain,
    network_policy,
    over_quota,
    teams,
)
from swhurl.apps.instance import INSTANCE_ROOTS, Instance
from swhurl.apps.model import generated
from swhurl.apps.model.records import RELEASE_FILE, RecordError, Release
from swhurl.run import Runner

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
    return {d['metadata']['name']: d['spec']['url'] for d in yamlio.load_all(path.read_text())
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
    docs = [d for d in yamlio.load_all(runner.output(['kubectl', 'kustomize', str(instance)])) if d]
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
        for item in yamlio.load_all(out):
            if item:
                item['metadata'].setdefault('namespace', doc['metadata']['namespace'])
                rendered.append(item)
    return docs + rendered


def exceptions(docs: list[dict]) -> tuple[dict[str, str], list[str]]:
    allowed, problems = {}, []
    for doc in docs:
        if doc['kind'] != 'HelmRelease':
            continue
        raw = (doc['metadata'].get('annotations') or {}).get(interface.EXCEPTIONS, '')
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
    for doc in docs:
        spec = (doc['metadata'].get('annotations') or {}).get(interface.ALERTS_ANNOTATION) if doc['kind'] == 'Namespace' else None
        try:
            app_alerts.thresholds(str(spec or ''))
        except ValueError as error:
            violations.append(('alerts', f'{interface.ALERTS_ANNOTATION}: {error}; make app-alerts sets it'))
    if labels.get(interface.TEAM) not in known_teams:
        violations.append(('team', f'Namespace team {labels.get(interface.TEAM)!r} is not registered in apps/teams.yaml '
                                   f'({", ".join(known_teams)}); set it with make app-team'))
    releases = [d for d in docs if d['kind'] == 'HelmRelease']
    if not releases:
        return violations
    app, namespace = releases[0]['metadata']['name'], releases[0]['metadata']['namespace']

    quotas = [d for d in docs if d['kind'] == 'ResourceQuota']
    if len(quotas) != 1 or quotas[0]['spec'].get('hard') != QUOTA:
        violations.append(('quota', f'the instance needs exactly one ResourceQuota with the platform sizes {QUOTA}'))
    workloads = []
    for doc in docs:
        spec = pod_spec(doc)
        if spec is None or doc['kind'] not in ('Deployment', 'StatefulSet', 'CronJob'):
            continue
        rolling = doc['kind'] == 'Deployment' and (doc['spec'].get('strategy') or {}).get('type') != 'Recreate'
        # A job runs one pod at a time (concurrencyPolicy Forbid).
        pods = 1 if doc['kind'] == 'CronJob' else doc['spec'].get('replicas', 1) + (1 if rolling else 0)
        workloads.append((pods, [container.get('resources') or {} for container in spec['containers']]))
    over = over_quota(workloads)
    if over:
        violations.append(('quota', f'replicas and sizes exceed the namespace quota for {", ".join(over)} '
                                    f'(allowed {QUOTA}, less {QUOTA_HEADROOM} kept free)'))

    routed = labels.get(interface.EXPOSURE) in ('authenticated-web', 'public')
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
    wanted = identity_variables(app, labels.get(interface.ENVIRONMENT), helmrelease.port(releases[0]) is not None, keys)
    wrong = [key for key, value in wanted.items() if variables.get(key) != value]
    if wrong:
        violations.append(('identity', f'{", ".join(wrong)} missing or not what the platform sets; make app-sync writes them'))
    calls = declared_calls(variables)
    for doc in docs:
        spec = pod_spec(doc)
        if spec is None:
            continue
        where = f"{doc['kind']}/{doc['metadata']['name']}"
        tokens = [source['serviceAccountToken'] for volume in spec.get('volumes') or []
                  for source in (volume.get('projected') or {}).get('sources') or [] if 'serviceAccountToken' in source]
        if ((doc.get('metadata') or {}).get('labels') or {}).get(BACKUP_LABEL) == 'true':
            if tokens:  # the platform's backup job (apps/model/backup.py) calls no app
                violations.append(('call-tokens', f'{where} is a backup job and carries no service-account token'))
            continue
        for container in spec['containers']:
            names = [var['name'] for var in container.get('env') or []]
            uses = [var['name'] for var in container.get('env') or [] if f'$({interface.ENVIRONMENT_VARIABLE})' in str(var.get('value', ''))]
            late = [name for name in uses if interface.ENVIRONMENT_VARIABLE not in names or names.index(interface.ENVIRONMENT_VARIABLE) > names.index(name)]
            if late:
                violations.append(('identity', f'{where}: {", ".join(late)} uses $({interface.ENVIRONMENT_VARIABLE}) before it is defined'))
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
    app, env, variables = release['metadata']['name'], labels.get(interface.ENVIRONMENT), helmrelease.variables(release)
    publishes, subscribes = events.published(variables), events.subscribed(variables)
    declared = [d for d in docs if d['kind'] in ('Stream', 'Consumer')]
    referenced = events.secret_name(app) in helmrelease.secret_references(release)
    problems = []
    identity = referenced or variables.get(interface.URL_VARIABLE) or variables.get(interface.NKEY_VARIABLE)
    key = str(variables.get(interface.NKEY_VARIABLE, ''))
    if (identity or publishes or subscribes) and (variables.get(interface.URL_VARIABLE) != events.URL
                                                  or not (key.startswith('U') and len(key) == 56) or not referenced):
        # A NATS identity (every instance where the platform has NATS) is whole or absent.
        problems.append(f'needs {interface.URL_VARIABLE}, a public key in {interface.NKEY_VARIABLE} and the {events.secret_name(app)} Secret reference')
    if not (publishes or subscribes):
        if declared:
            problems.append('declares no events but carries a stream or consumer')
    else:
        wanted = events.manifests(app, env, publishes, subscribes)
        def shape(doc):
            return doc['kind'], doc['metadata']['name'], doc['spec']
        if sorted(map(shape, declared), key=str) != sorted(map(shape, wanted), key=str):
            problems.append('its streams and consumers are not what its declaration generates')
    return [('events', f'{problem}; make app-events sets them and make app-sync rewrites what follows') for problem in problems]


def check_container(container: dict, pod_ctx: dict, where: str, env: str | None) -> list[tuple[str, str]]:
    """Image, security, telemetry and resource rules for one container."""
    violations: list[tuple[str, str]] = []
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
        if f'$({interface.OTLP_HOST_IP})' in str(var.get('value', '')):
            source = defined.get(interface.OTLP_HOST_IP, {}).get('valueFrom', {}).get('fieldRef', {}).get('fieldPath')
            if source != 'status.hostIP':
                violations.append(('otlp-host-ip', f"{name} uses $({interface.OTLP_HOST_IP}) in {var['name']} without "
                                   f'defining {interface.OTLP_HOST_IP} from status.hostIP before it'))
        defined[var['name']] = var
    res = container.get('resources') or {}
    if not {'cpu', 'memory'} <= set(res.get('requests') or {}) or 'memory' not in (res.get('limits') or {}):
        violations.append(('resources', f'{name} lacks CPU/memory requests or a memory limit'))
    return violations


def check_pod(doc: dict, env: str | None) -> list[tuple[str, str]]:
    """Pod rules followed by init and regular container rules in manifest order."""
    violations: list[tuple[str, str]] = []
    spec = pod_spec(doc)
    if spec is None:
        return violations
    where = f"{doc['kind']}/{doc['metadata']['name']}"
    if spec.get('automountServiceAccountToken') is not False:
        violations.append(('no-sa-token', f'{where} mounts a service-account token'))
    if any(spec.get(k) for k in ('hostNetwork', 'hostPID', 'hostIPC')) or \
            any('hostPath' in v for v in spec.get('volumes') or []):
        violations.append(('no-host-access', f'{where} uses host namespaces or hostPath'))
    pod_ctx = spec.get('securityContext') or {}
    for container in (spec.get('initContainers') or []) + spec['containers']:
        violations += check_container(container, pod_ctx, where, env)
    return violations


def check_single_writer(doc: dict, rwo: set[str]) -> list[tuple[str, str]]:
    """Replica and rollout rules for a workload mounting a ReadWriteOnce claim."""
    violations: list[tuple[str, str]] = []
    spec = pod_spec(doc)
    claims = {v['persistentVolumeClaim']['claimName'] for v in (spec or {}).get('volumes') or []
              if 'persistentVolumeClaim' in v}
    if spec is None or not claims & rwo or doc['kind'] in ('Job', 'CronJob'):
        return violations
    where = f"{doc['kind']}/{doc['metadata']['name']}"
    if doc['spec'].get('replicas', 1) > 1:
        violations.append(('single-writer', f'{where} mounts a ReadWriteOnce claim and runs {doc["spec"]["replicas"]} replicas; at most 1'))
    if doc['kind'] == 'Deployment' and (doc['spec'].get('strategy') or {}).get('type') != 'Recreate':
        violations.append(('single-writer', f'{where} mounts a ReadWriteOnce claim, so it must stop the old pod first (strategy Recreate)'))
    return violations


def check_ingress(doc: dict, exposure: str | None) -> list[tuple[str, str]]:
    """Exposure, shared-cookie and TLS rules for one Ingress."""
    violations: list[tuple[str, str]] = []
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
    return violations


def check(docs: list[dict], known_teams: dict[str, str] | None = None, keys: str | None = None) -> list[tuple[str, str]]:
    """Return (rule, message) violations for one rendered instance."""
    violations: list[tuple[str, str]] = []
    namespaces = [d for d in docs if d['kind'] == 'Namespace']
    labels = (namespaces[0]['metadata'].get('labels') or {}) if namespaces else {}
    env = labels.get(interface.ENVIRONMENT)
    exposure = labels.get(interface.EXPOSURE)
    if not env or not exposure:
        violations.append(('exposure', 'instance Namespace needs platform.swhurl.com/environment and /exposure labels'))
    violations += check_isolation(docs, labels, teams() if known_teams is None else known_teams)
    violations += check_identity(docs, labels, signing_keys() if keys is None else keys)

    for doc in docs:
        violations += check_pod(doc, env)

    rwo = {d['metadata']['name'] for d in docs if d['kind'] == 'PersistentVolumeClaim'
           and 'ReadWriteOnce' in (d['spec'].get('accessModes') or [])}
    for doc in docs:
        violations += check_single_writer(doc, rwo)

    for doc in docs:
        if doc['kind'] == 'Ingress':
            violations += check_ingress(doc, exposure)
        if doc['kind'] == 'PersistentVolumeClaim' and doc['spec'].get('storageClassName') not in STORAGE_CLASSES:
            violations.append(('storage-class', f"PersistentVolumeClaim/{doc['metadata']['name']} must use one of {sorted(STORAGE_CLASSES)}"))
    return violations


def platform_image(instance: Path) -> list[str]:
    """Why ``<app>/<env>`` is not an app made from a stack template (rule platform-image)."""
    app, env = instance.parent.name, instance.name
    text = (instance / 'helmrelease.yaml').read_text()
    image = helmrelease.image(yamlio.load(text))
    expected = f'ghcr.io/{APP_OWNER}/{app}'
    problems = []
    if image.get('repository') != expected:
        problems.append(f'image {image.get("repository")} is not {expected}; apps are made with make app-repo or the console')
    if env == AUTO_DEPLOY_ENV:
        listed = IMAGE_AUTOMATION_FILE in (yamlio.load((instance / 'kustomization.yaml').read_text()).get('resources') or [])
        if not (instance / IMAGE_AUTOMATION_FILE).is_file() or not listed:
            problems.append(f'{env} has no {IMAGE_AUTOMATION_FILE} in its kustomization.yaml: new images would not deploy')
        elif image_policy_name(app) not in {(d or {}).get('metadata', {}).get('name') for d in
                                             yamlio.load_all((instance / IMAGE_AUTOMATION_FILE).read_text())
                                             if (d or {}).get('kind') == 'ImagePolicy'}:
            problems.append(f'{IMAGE_AUTOMATION_FILE} has no ImagePolicy {image_policy_name(app)}: new images would not deploy')
    return [f'platform-image: {problem}' for problem in problems]


def evaluate(instance: Path, runner: Runner | None = None) -> list[str]:
    docs = render(instance, runner)
    allowed, problems = exceptions(docs)
    if instance.resolve().parent.parent == (ROOT / INSTANCE_ROOTS[0]).resolve():
        problems += platform_image(instance)
    root = instance.resolve().parents[2]  # <root>/apps/<app>/<env>
    release = yamlio.load((instance / 'helmrelease.yaml').read_text())
    try:
        published = list(Release.parse(yamlio.load((instance / RELEASE_FILE).read_text())).publishes)
    except (OSError, UnicodeError, yaml.YAMLError, RecordError) as error:
        problems.append(f'offers: cannot read {RELEASE_FILE}: {error}')
        published = []
    problems += offers.problems(instance, published,
                                str(((release.get('metadata') or {}).get('annotations') or {}).get(interface.OFFERS_ANNOTATION, '')))
    return problems + [f'{rule}: {message}' for rule, message in check(docs, teams(root), signing_keys(root))
                       if rule not in allowed]


def main(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    paths = [Path(a).resolve() for a in argv or []] or instances()
    failed = 0
    with ThreadPoolExecutor(max_workers=os.cpu_count() or 4) as pool:  # each render is kustomize and helm processes
        evaluated = list(pool.map(lambda instance: evaluate(instance, runner), paths))
    for instance, problems in zip(paths, evaluated, strict=True):
        label = instance.relative_to(ROOT) if instance.is_relative_to(ROOT) else instance
        if problems:
            failed += 1
            print(f'[BAD] {label}')
            for problem in problems:
                print(f'       {problem}')
        else:
            print(f'[OK] {label}')
    if argv:
        roots: dict[Path, set[Instance]] = {}
        for path in paths:
            roots.setdefault(path.parents[2], set()).add(Instance(path.parent.name, path.name))
        for root, selected in roots.items():
            problems = generated.problems(root, only=frozenset(selected))
            if problems:
                failed += 1
                print(f'[BAD] app records: {root}')
                for problem in problems:
                    print(f'       {problem}')
    else:  # the whole tree
        problems = egress.problems(ROOT)
        if problems:
            failed += 1
            print('[BAD] egress ports and grants')
            for problem in problems:
                print(f'       {problem}')
        problems = channels.problems(ROOT)
        if problems:
            failed += 1
            print(f'[BAD] {channels.NOTIFY_DIR}')
            for problem in problems:
                print(f'       {problem}')
        problems = generated.check(ROOT)
        if problems:
            failed += 1
            print('[BAD] app model: records are invalid or their rendered files differ (docs/apps.md#the-app-policy)')
            for problem in problems:
                print(f'       {problem}')
        else:
            print('[OK] app model: rendering every instance\'s records reproduces its manifests')
    if failed:
        print(f'\n{failed} instance(s) or app(s) violate the app contract.')
        return 1
    print(f'\nApp policy passed for {len(paths)} instance(s).')
    return 0
