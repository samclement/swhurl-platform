"""Shared staging → production decisions. Git decides existence; live status decides readiness."""
from __future__ import annotations

import dataclasses
import hashlib
import json
import re
from collections.abc import Callable, Iterable
from dataclasses import dataclass
from pathlib import Path

from swhurl.apps import calling, contract, events, helmrelease, ops, per_environment, state
from swhurl.apps.instance import NAME_RE, Instance, instance_folder, instance_unit_file
from swhurl.apps.yaml_file import EditError, YamlFile, mapping


@dataclass(frozen=True)
class Decision:
    outcome: str  # create, update, same
    image: str
    previous: str = ''
    rollback: bool = False


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
    unit = instance_unit_file(root, app, 'prod')
    if target_dir.exists() or unit.exists():
        # A partial target is a problem to repair, never evidence that production is missing.
        target = YamlFile(target_dir / 'helmrelease.yaml').data
    else:
        target = None
    return source, target


def fingerprint(root: Path, app: str, env: str, *, ignore_image: bool = False) -> str:
    """Hash effective configuration and resource files; never return Secret contents."""
    files = {}
    instance = instance_folder(root, app, env)
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
    unit = instance_unit_file(root, app, env)
    if unit.exists():
        files['unit'] = hashlib.sha256(unit.read_bytes()).hexdigest()
    return hashlib.sha256(json.dumps(files, sort_keys=True).encode()).hexdigest()


def sha(revision: str) -> str:
    """The commit of a Flux revision (``main@sha1:<commit>``)."""
    return revision.rsplit(':', 1)[-1]


def touches(instance: Instance, paths: Iterable[str]) -> bool:
    """Whether any of ``paths`` is one of the instance's own files: its folder or its unit file."""
    root = Path()
    folder, unit = instance.folder(root).as_posix() + '/', instance.unit_file(root).as_posix()
    return any(p == unit or p.startswith(folder) for p in paths)


def settled(status: ops.InstanceStatus | None,
            changed: Callable[[str, str], Iterable[str] | None]) -> ops.InstanceStatus | None:
    """``status`` with ``own_change`` answered, when its unit is behind Git's newest revision: any commit to main
    puts every unit behind for a few seconds, and only one that changes the instance's own files (its folder,
    its unit file) is a change it waits for. ``changed(applied, newest)`` gives the paths that differ between
    the two commits, or None when it cannot tell, which leaves the question open (and the instance not current)."""
    if status is None or status.applied or not (status.desired_revision and status.applied_revision):
        return status
    paths = changed(sha(status.applied_revision), sha(status.desired_revision))
    if paths is None:
        return status
    return dataclasses.replace(status, own_change=touches(status.instance, paths))


def source_problem(status: ops.InstanceStatus | None, image: str) -> str:
    """Why staging cannot be promoted now, or ''. The image pins what was reviewed; the caller pins staging's
    configuration by its ``fingerprint``. ``status`` should be ``settled``, or any commit to main refuses."""
    if status is None:
        return 'staging no longer exists; refresh the app page'
    if status.desired_image != image:
        return 'staging changed since this page was loaded; refresh and review it again'
    if status.desired_revision and status.applied_revision and not status.current:
        if status.own_change:
            return ('a change to staging is in Git and Flux has not applied it yet; wait a few seconds, refresh '
                    'and review it again')
        # Whether the unapplied commits change this app could not be read: any commit to main is "newer" until applied here.
        return ('Flux is still applying the latest commit on main to staging (a change landed moments ago, perhaps '
                "another app's); wait a few seconds, refresh and promote again")
    return '' if state.runs(status) else 'staging must be healthy and running the reviewed digest before promotion'


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
    instance = instance_folder(root, app, 'staging')
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
    only(persistence, {'tmp', 'data', calling.TOKENS_VOLUME}, 'persistence')
    for name, volume in persistence.items():
        if name == calling.TOKENS_VOLUME:  # derived from the declared calls; the policy checks it
            continue
        only(volume, {'type', 'globalMounts', 'storageClass', 'accessMode', 'size', 'retain'}, f'persistence.{name}')
        if name == 'tmp' and volume.get('type') != 'emptyDir':
            raise EditError('tmp must be an emptyDir')
        if name == 'data' and (volume.get('type') != 'persistentVolumeClaim'
                               or volume.get('storageClass') != contract.RETAINED_STORAGE_CLASS
                               or volume.get('retain') is not True):
            raise EditError('data must be a platform-managed retained claim; external bindings are unsupported')
    keys = []
    # Events are declared per environment (make app-events ENV=prod): the staging key stays in staging.
    refs = [ref for ref in container.get('envFrom', []) if ref != {'secretRef': {'name': events.secret_name(app)}}]
    if refs and refs not in ([{'secretRef': {'name': contract.app_secret(app)}}],
                             [{'secretRef': {'name': contract.app_secret(app), 'optional': True}}]):
        raise EditError('only the platform-managed app Secret reference is supported on first promotion')
    stored = (instance / contract.SECRET_FILE).is_file()
    if stored:
        secret = YamlFile(instance / contract.SECRET_FILE).data
        if 'sops' not in secret:
            raise EditError('staging app Secret must be encrypted')
    if refs:
        # Production gets a placeholder for every name the image needs and every value staging has.
        keys = list(dict.fromkeys(contract.declared_secrets(source)
                                  + (contract.stored_secrets((instance / contract.SECRET_FILE).read_text()) if stored else [])))
        if not keys or contract.secret_key_problem(keys):
            raise EditError('staging app Secret needs supported environment variable keys')
    kustomization = YamlFile(instance / 'kustomization.yaml').data
    only(kustomization, {'apiVersion', 'kind', 'resources'}, 'kustomization')
    expected = {'namespace.yaml', 'helmrelease.yaml', *contract.ISOLATION_FILES} | ({contract.SECRET_FILE} if stored else set())
    resources = set(kustomization.get('resources', [])) - per_environment.NOT_CARRIED
    if resources != expected or {p.name for p in instance.iterdir() if p.is_file()} - per_environment.NOT_CARRIED != expected | {
            'kustomization.yaml'}:
        raise EditError('first promotion refuses additional resources or files')
    variables = container.get('env', {})
    if any(isinstance(v, dict) and 'valueFrom' in v and k != contract.OTLP_HOST_IP for k, v in variables.items()):
        raise EditError('custom environment references need a reviewed production deployment in Git')
    args = new.parser().parse_args([app, '--env', 'prod', '--image', ops.image_reference(image_of(source)),
                                    '--kind', kind, '--exposure', exposure])
    args.host = host
    args.team = namespace['metadata'].get('labels', {}).get(contract.TEAM)
    if not args.team:
        raise EditError('staging names no team (its namespace label); set it with make app-team before promoting')
    if kind == 'web':  # the NetworkPolicy admits Traefik on the app's own port
        args.port = helmrelease.port(source) or args.port
    args.health_path = '/' if kind == 'web' else None  # the reviewed probes replace generator defaults
    args.secret_keys = keys
    args.issuer = (route or {}).get('annotations', {}).get('cert-manager.io/cluster-issuer', 'letsencrypt-prod')
    if 'data' in persistence:
        args.persistence = persistence['data']['size']
    new.resolve(args)
    if preview and exposure == 'public' and not host:
        args.host = route['hosts'][0]['host']
        new.validate(args, root)
        args.host = None
    else:
        new.validate(args, root)
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
    if instance_folder(root, app, 'prod').exists() or instance_unit_file(root, app, 'prod').exists():
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
        dst = instance_folder(work, app, 'prod')
        generated_ns = json.loads(json.dumps(YamlFile(dst / 'namespace.yaml').data))
        source_ns = json.loads(json.dumps(YamlFile(instance_folder(root, app, 'staging') / 'namespace.yaml').data))
        generated_ns['metadata']['labels'] = {**source_ns['metadata'].get('labels', {}),
                                               **generated_ns['metadata']['labels']}
        generated_ns['metadata']['annotations'] = {**source_ns['metadata'].get('annotations', {}),
                                                   **generated_ns['metadata'].get('annotations', {})}
        if not generated_ns['metadata']['annotations']:
            generated_ns['metadata'].pop('annotations')
        (dst / 'namespace.yaml').write_text(new.dump([generated_ns]))
        # Staging's reviewed release with what differs in production (per_environment.py): its own
        # identity and address; no callers, events or offers yet. Its callers are worked out from the
        # production instances that exist (derive.sync_env) and what the image offers is carried
        # (edit.carry_offers), both after this: the note of the offers' commit without the documents
        # would fail the policy check below. Plain data, so staging's automation comments go too.
        release = per_environment.for_production(source, app, args.host if args.exposure != 'private' else None)
        # Same app-scoped Secret/PVC names resolve in the independent production namespace.
        if f'{app}-staging' in json.dumps(release) or contract.default_host(app, 'staging') in json.dumps(release):
            raise EditError('staging-specific reference remains in deployment settings; edit custom production in Git')
        (dst / 'helmrelease.yaml').write_text(new.dump([release]))
        problems = policy.evaluate(dst, runner)
        if problems:
            raise EditError('production policy refused: ' + '; '.join(problems))
        current_support = {rel: (root / rel).read_bytes() if (root / rel).exists() else None for rel in support}
        if (fingerprint(root, app, 'staging') != reviewed_config or current_support != original_support
                or instance_folder(root, app, 'prod').exists()
                or instance_unit_file(root, app, 'prod').exists()):
            raise EditError('configuration changed while preparing production; refresh and review it again')
        files = [*written, work / 'clusters/home/kustomization.yaml']
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
            shutil.rmtree(instance_folder(root, app, 'prod'), ignore_errors=True)
            raise
    return instance_folder(root, app, 'prod')
