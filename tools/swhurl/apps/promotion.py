"""Shared staging → production decisions. Git decides existence; live status decides readiness."""
from __future__ import annotations

import hashlib
import json
import re
from dataclasses import dataclass
from pathlib import Path

from swhurl.apps import contract, ops
from swhurl.apps.new import NAME_RE
from swhurl.apps.yaml_file import EditError, YamlFile, mapping


@dataclass(frozen=True)
class Decision:
    outcome: str  # create, update, same
    image: str
    previous: str = ''
    rollback: bool = False


def production_alert(app: str) -> dict:
    """Notify once when this app's production HelmRelease completes an install or upgrade."""
    return {
        'apiVersion': 'notification.toolkit.fluxcd.io/v1beta3',
        'kind': 'Alert',
        'metadata': {'name': f'app-{app}-production', 'namespace': 'flux-system'},
        'spec': {
            'providerRef': {'name': 'ntfy-deploys'},
            'eventSeverity': 'info',
            'eventSources': [{'kind': 'HelmRelease', 'name': app, 'namespace': f'{app}-prod'}],
            'inclusionList': ['.*succeeded.*'],
        },
    }


def ensure_production_alert(root: Path, app: str) -> None:
    """Register the per-app alert in its production instance so app removal prunes it."""
    from swhurl.apps.new import dump

    instance = root / 'apps' / app / 'prod'
    alert_path = instance / 'production-alert.yaml'
    kustomization = YamlFile(instance / 'kustomization.yaml')
    resources = kustomization.data.get('resources')
    if not isinstance(resources, list):
        raise EditError('production kustomization needs a resources list for its promotion alert')
    if alert_path.exists():
        if 'production-alert.yaml' not in resources:
            raise EditError('production alert exists but is not registered in the kustomization')
        return
    alert_path.write_text(dump([production_alert(app)]))
    resources.append('production-alert.yaml')
    kustomization.save()


def image_of(release: dict) -> dict:
    values = mapping(mapping(release, 'spec'), 'values')
    controller = mapping(mapping(values, 'controllers'), 'main')
    return mapping(mapping(mapping(controller, 'containers'), 'main'), 'image')


def decide(source: dict, target: dict | None) -> Decision:
    image = image_of(source)
    if not image.get('digest'):
        raise EditError('staging has no image digest; promote only a digest-pinned image')
    reference = ops.image_reference(image)
    if target is None:
        return Decision('create', reference)
    previous = image_of(target)
    if previous.get('repository') != image.get('repository'):
        raise EditError('production uses a different image repository; change the repository by hand')
    if previous.get('digest') == image['digest']:
        return Decision('same', reference, ops.image_reference(previous))
    runs = [re.fullmatch(contract.AUTO_DEPLOY_TAG_PATTERN, i.get('tag', '')) for i in (image, previous)]
    rollback = all(runs) and int(runs[0]['run']) < int(runs[1]['run'])
    return Decision('update', reference, ops.image_reference(previous), bool(rollback))


def read(root: Path, app: str) -> tuple[dict, dict | None]:
    if not NAME_RE.fullmatch(app):
        raise EditError('APP must be a DNS label')
    base = root / 'apps' / app
    source = YamlFile(base / 'staging/helmrelease.yaml').data
    target_dir = base / 'prod'
    unit = root / 'clusters/home' / f'app-{app}-prod.yaml'
    if target_dir.exists() or unit.exists():
        # A partial target is a problem to repair, never evidence that production is missing.
        target = YamlFile(target_dir / 'helmrelease.yaml').data
    else:
        target = None
    return source, target


def fingerprint(root: Path, app: str, env: str, *, ignore_image: bool = False) -> str:
    """Hash effective configuration and resource files; never return Secret contents."""
    files = {}
    instance = root / 'apps' / app / env
    for path in sorted(instance.rglob('*')):
        if not path.is_file():
            continue
        if path.name == 'helmrelease.yaml':
            doc = YamlFile(path).data
            if ignore_image:
                image_of(doc).pop('tag', None)
                image_of(doc).pop('digest', None)
            files[path.name] = json.dumps(doc, sort_keys=True, default=str)
        else:
            files[path.relative_to(instance).as_posix()] = hashlib.sha256(path.read_bytes()).hexdigest()
    unit = root / 'clusters/home' / f'app-{app}-{env}.yaml'
    if unit.exists():
        files['unit'] = hashlib.sha256(unit.read_bytes()).hexdigest()
    return hashlib.sha256(json.dumps(files, sort_keys=True).encode()).hexdigest()


def source_problem(status: ops.InstanceStatus | None, image: str, revision: str) -> str:
    if status is None:
        return 'staging no longer exists; refresh the app page'
    if status.desired_image != image or status.applied_revision != revision:
        return 'staging changed since this page was loaded; refresh and review it again'
    healthy = (not status.unit_suspended and not status.release_suspended and not status.problems
               and status.unit[0] == 'True' and status.release and status.release[0] == 'True'
               and status.desired_revision and status.applied_revision and status.applied
               and status.replicas and all(r.wanted and r.ready == r.wanted for r in status.replicas)
               and status.image_state == 'matches')
    return '' if healthy else 'staging must be healthy and running the reviewed digest before promotion'


def only(value: dict, allowed: set[str], where: str) -> None:
    unknown = set(value) - allowed
    if unknown:
        raise EditError(f'{where}: unsupported fields {", ".join(sorted(unknown))}; edit custom production in Git')


def ordinary(value, where: str = 'staging') -> None:
    """No shared YAML nodes can cross the environment conversion boundary."""
    anchor = getattr(value, 'yaml_anchor', lambda: None)()
    if getattr(anchor, 'value', None) or getattr(value, 'merge', None):
        raise EditError(f'{where}: YAML anchor or merge is ambiguous; edit custom production in Git')
    if isinstance(value, dict):
        for key, child in value.items():
            ordinary(child, f'{where}.{key}')
    elif isinstance(value, list):
        for child in value:
            ordinary(child, where)


def first_args(root: Path, app: str, source: dict, *, host: str | None = None, preview: bool = False):
    """Validate the conversion boundary and build generator inputs without decrypting Secrets."""
    from swhurl.apps import new

    ordinary(source)
    only(source, {'apiVersion', 'kind', 'metadata', 'spec'}, 'HelmRelease')
    spec = mapping(source, 'spec')
    only(spec, {'interval', 'timeout', 'chart', 'install', 'upgrade', 'values'}, 'HelmRelease.spec')
    chart = mapping(mapping(spec, 'chart'), 'spec')
    ref = chart.get('sourceRef', {})
    if (chart.get('chart'), chart.get('version'), ref.get('name')) != (
            contract.CHART, contract.CHART_VERSION, contract.CHART_REPOSITORY):
        raise EditError('first promotion requires the supported app-template chart and version')
    values = mapping(spec, 'values')
    only(values, {'defaultPodOptions', 'controllers', 'persistence', 'service', 'ingress'}, 'values')
    controllers = mapping(values, 'controllers')
    only(controllers, {'main'}, 'controllers')
    controller = mapping(controllers, 'main')
    only(controller, {'containers', 'replicas', 'strategy', 'annotations', 'type'}, 'controller.main')
    if controller.get('type', 'deployment') != 'deployment':
        raise EditError('first promotion supports Deployment only')
    containers = mapping(controller, 'containers')
    only(containers, {'main'}, 'containers')
    container = mapping(containers, 'main')
    only(container, {'image', 'resources', 'securityContext', 'probes', 'command', 'args', 'env', 'envFrom'}, 'container.main')
    pod = mapping(values, 'defaultPodOptions')
    only(pod, {'automountServiceAccountToken', 'securityContext', 'labels', 'annotations'}, 'defaultPodOptions')
    instance = root / 'apps' / app / 'staging'
    namespace = YamlFile(instance / 'namespace.yaml').data
    ordinary(namespace, 'namespace')
    only(namespace, {'apiVersion', 'kind', 'metadata'}, 'namespace')
    only(mapping(namespace, 'metadata'), {'name', 'labels', 'annotations'}, 'namespace.metadata')
    if (namespace['metadata'].get('name') != f'{app}-staging'
            or source.get('metadata', {}).get('name') != app
            or source.get('metadata', {}).get('namespace') != f'{app}-staging'):
        raise EditError('first promotion requires the app staging namespace and release identity')
    exposure = namespace.get('metadata', {}).get('labels', {}).get(contract.EXPOSURE)
    if exposure not in contract.EXPOSURES:
        raise EditError('staging namespace needs a supported exposure label')
    kind = 'web' if 'service' in values else 'worker'
    if kind == 'worker' and exposure != 'private':
        raise EditError('a worker must be private')
    if 'service' in values:
        service = mapping(values, 'service')
        only(service, {'main'}, 'service')
        main = mapping(service, 'main')
        only(main, {'controller', 'ports'}, 'service.main')
        if main.get('controller') != 'main' or set(main.get('ports', {})) != {'http'}:
            raise EditError('first promotion supports the main HTTP service only')
    route = None
    if exposure != 'private':
        ingresses = mapping(values, 'ingress')
        only(ingresses, {'main'}, 'ingress')
        route = mapping(ingresses, 'main')
        only(route, {'className', 'annotations', 'hosts', 'tls'}, 'ingress.main')
        hosts, tls = route.get('hosts', []), route.get('tls', [])
        if (len(hosts) != 1 or len(tls) != 1 or tls[0].get('hosts') != [hosts[0].get('host')]):
            raise EditError('first promotion needs one host with matching TLS')
        if exposure == 'authenticated-web' and hosts[0].get('host') != contract.default_host(app, 'staging'):
            raise EditError('custom authenticated host: use the generated staging host before first promotion')
        if exposure == 'public' and not (preview and not host) and (not host or host == hosts[0].get('host')):
            raise EditError('first public promotion needs a distinct production --host')
    elif values.get('ingress'):
        raise EditError('private staging must have no ingress')
    if host and exposure != 'public':
        raise EditError('--host applies only to first promotion of a public app')
    persistence = mapping(values, 'persistence')
    only(persistence, {'tmp', 'data'}, 'persistence')
    for name, volume in persistence.items():
        only(volume, {'type', 'globalMounts', 'storageClass', 'accessMode', 'size', 'retain'}, f'persistence.{name}')
        if name == 'tmp' and volume.get('type') != 'emptyDir':
            raise EditError('tmp must be an emptyDir')
        if name == 'data' and (volume.get('type') != 'persistentVolumeClaim'
                               or volume.get('storageClass') != contract.RETAINED_STORAGE_CLASS
                               or volume.get('retain') is not True):
            raise EditError('data must be a platform-managed retained claim; external bindings are unsupported')
    keys = []
    refs = container.get('envFrom', [])
    if refs and refs != [{'secretRef': {'name': f'{app}-secret'}}]:
        raise EditError('only the platform-managed app Secret reference is supported on first promotion')
    if refs:
        secret = YamlFile(instance / 'secret.sops.yaml').data
        if 'sops' not in secret:
            raise EditError('staging app Secret must be encrypted')
        keys = list(secret.get('stringData') or secret.get('data') or {})
        if not keys or contract.secret_key_problem(keys):
            raise EditError('staging app Secret needs supported environment variable keys')
    kustomization = YamlFile(instance / 'kustomization.yaml').data
    only(kustomization, {'apiVersion', 'kind', 'resources'}, 'kustomization')
    expected = {'namespace.yaml', 'helmrelease.yaml'} | ({'secret.sops.yaml'} if refs else set())
    resources = set(kustomization.get('resources', [])) - {contract.IMAGE_AUTOMATION_FILE}
    if resources != expected or {p.name for p in instance.iterdir() if p.is_file()} - {
            contract.IMAGE_AUTOMATION_FILE} != expected | {'kustomization.yaml'}:
        raise EditError('first promotion refuses additional resources or files')
    env = container.get('env', {})
    if any(isinstance(v, dict) and 'valueFrom' in v and k != contract.OTLP_HOST_IP for k, v in env.items()):
        raise EditError('custom environment references need a reviewed production deployment in Git')
    args = new.parser().parse_args([app, '--env', 'prod', '--image', ops.image_reference(image_of(source)),
                                    '--kind', kind, '--exposure', exposure])
    args.host = host
    args.health_path = '/' if kind == 'web' else None  # the reviewed probes replace generator defaults
    args.secret_keys = keys
    args.issuer = (route or {}).get('annotations', {}).get('cert-manager.io/cluster-issuer', 'letsencrypt-prod')
    if 'data' in persistence:
        args.persistence = persistence['data']['size']
    new.resolve(args)
    if preview and exposure == 'public' and not host:
        args.host = route['hosts'][0]['host']
        new.validate(args)
        args.host = None
    else:
        new.validate(args)
    return args


def create(root: Path, app: str, source: dict, *, host: str | None = None, runner=None) -> Path:
    """Prepare and validate a complete instance in scratch space before publishing any Git edits."""
    import shutil
    import tempfile

    from swhurl.apps import new, policy
    from swhurl.run import Runner

    try:
        args = first_args(root, app, source, host=host)
    except new.GenerationError as error:
        raise EditError(str(error)) from None
    runner = runner or Runner()
    reviewed_config = fingerprint(root, app, 'staging')
    if (root / 'apps' / app / 'prod').exists() or (root / 'clusters/home' / f'app-{app}-prod.yaml').exists():
        raise EditError('production already exists; refresh and review it again')
    support = ('clusters/home/kustomization.yaml', 'platform/reloader/helmrelease.yaml', '.sops.yaml')
    original_support = {rel: (root / rel).read_bytes() if (root / rel).exists() else None for rel in support}
    with tempfile.TemporaryDirectory(prefix='promotion-') as tmp:
        work = Path(tmp)
        for rel in support:
            path = root / rel
            if path.exists():
                (work / rel).parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(path, work / rel)
        args.root = work
        try:
            written = new.generate(args, work, runner)
        except new.GenerationError as error:
            raise EditError(str(error)) from None
        dst = work / 'apps' / app / 'prod'
        generated_ns = json.loads(json.dumps(YamlFile(dst / 'namespace.yaml').data))
        source_ns = json.loads(json.dumps(YamlFile(root / 'apps' / app / 'staging/namespace.yaml').data))
        generated_ns['metadata']['labels'] = {**source_ns['metadata'].get('labels', {}),
                                               **generated_ns['metadata']['labels']}
        generated_ns['metadata']['annotations'] = {**source_ns['metadata'].get('annotations', {}),
                                                   **generated_ns['metadata'].get('annotations', {})}
        if not generated_ns['metadata']['annotations']:
            generated_ns['metadata'].pop('annotations')
        (dst / 'namespace.yaml').write_text(new.dump([generated_ns]))
        # Strip staging-only automation comments, then retain the effective reviewed release settings.
        release = json.loads(json.dumps(source))
        release['metadata']['namespace'] = f'{app}-prod'
        values = release['spec']['values']
        for options in (values.get('defaultPodOptions', {}), release['metadata']):
            labels = options.get('labels', {})
            if contract.ENVIRONMENT in labels:
                labels[contract.ENVIRONMENT] = 'prod'
        if args.exposure != 'private':
            route = values['ingress']['main']
            route['hosts'][0]['host'] = args.host
            route['tls'][0]['hosts'] = [args.host]
        # Same app-scoped Secret/PVC names resolve in the independent production namespace.
        if f'{app}-staging' in json.dumps(release) or contract.default_host(app, 'staging') in json.dumps(release):
            raise EditError('staging-specific reference remains in deployment settings; edit custom production in Git')
        (dst / 'helmrelease.yaml').write_text(new.dump([release]))
        # Keep the success alert with the production instance so it is installed and pruned with it.
        ensure_production_alert(work, app)
        problems = policy.evaluate(dst, runner)
        if problems:
            raise EditError('production policy refused: ' + '; '.join(problems))
        current_support = {rel: (root / rel).read_bytes() if (root / rel).exists() else None for rel in support}
        if (fingerprint(root, app, 'staging') != reviewed_config or current_support != original_support
                or (root / 'apps' / app / 'prod').exists()
                or (root / 'clusters/home' / f'app-{app}-prod.yaml').exists()):
            raise EditError('configuration changed while preparing production; refresh and review it again')
        files = [*written, dst / 'production-alert.yaml', work / 'clusters/home/kustomization.yaml']
        reloader = work / 'platform/reloader/helmrelease.yaml'
        if args.secret_keys and reloader.exists():
            files.append(reloader)
        # Preparation and external tools have succeeded. Roll back local writes on filesystem failure too.
        before = {p.relative_to(work): (root / p.relative_to(work)).read_bytes()
                  if (root / p.relative_to(work)).exists() else None for p in files}
        try:
            for p in files:
                target = root / p.relative_to(work)
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(p.read_bytes())
        except OSError:
            for rel, content in before.items():
                if content is None:
                    (root / rel).unlink(missing_ok=True)
                else:
                    (root / rel).write_bytes(content)
            shutil.rmtree(root / 'apps' / app / 'prod', ignore_errors=True)
            raise
    return root / 'apps' / app / 'prod'
