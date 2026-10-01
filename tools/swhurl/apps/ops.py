"""Operate one generated app instance (namespace ``<app>-<env>``, Flux unit
``app-<app>-<env>``, HelmRelease ``<app>``).

    app status    APP ENV   desired vs applied revision and image, replicas, route, failure reason
    app logs      APP ENV   recent logs from the instance's workload (FOLLOW=true to stream, TAIL=N,
                            PREVIOUS=true for the last crashed container)
    app reconcile APP ENV   fetch Git and reconcile only this instance
    app check     APP ENV   render and check this instance against the app contract (offline)
"""
from __future__ import annotations

import os
import sys
from dataclasses import dataclass, field

from swhurl import ROOT
from swhurl.apps import policy
from swhurl.apps.contract import AUTH_MIDDLEWARE, INSTANCE_ROOTS
from swhurl.run import CommandError, Runner
from swhurl.verify import ready_condition

ACTIONS = ('status', 'logs', 'reconcile', 'check')
WORKLOAD_KINDS = 'deploy,statefulset,daemonset'


@dataclass(frozen=True)
class Instance:
    app: str
    env: str

    @property
    def namespace(self) -> str:
        return f'{self.app}-{self.env}'

    @property
    def unit(self) -> str:
        return f'app-{self.app}-{self.env}'


def get(runner: Runner, *args: str) -> dict | None:
    """``kubectl get ... -o json``, or None if it does not exist."""
    try:
        return runner.json(['kubectl', *args, '-o', 'json'])
    except CommandError:
        return None


def desired_image(release: dict | None) -> str:
    image = (((((release or {}).get('spec') or {}).get('values') or {}).get('controllers') or {})
             .get('main', {}).get('containers', {}).get('main', {}).get('image') or {})
    text = f"{image.get('repository', '?')}:{image.get('tag', '')}"
    return text + (f"@{image['digest']}" if image.get('digest') else '')


def running_images(pods: dict | None) -> list[str]:
    """The image each started container runs, as ``repository@digest``.

    When the pod's spec pins the image by digest, that digest is what runs: a pull by digest
    guarantees the content. The node's ``imageID`` can name a different digest for the same
    content: containerd keeps the first digest it pulled an image under, and two builds can
    publish the same image under different index digests (only their attestations differ).
    Without a pinned digest the node's ``imageID`` is all there is.
    """
    ids = set()
    for pod in (pods or {}).get('items', []):
        spec = {c.get('name'): c.get('image', '') for c in (pod.get('spec') or {}).get('containers') or []}
        for status in (pod.get('status') or {}).get('containerStatuses') or []:
            if not status.get('imageID'):
                continue  # not pulled yet: nothing is running
            pinned = spec.get(status.get('name'), '')
            if '@sha256:' in pinned:
                repository, digest = pinned.split('@', 1)
                ids.add(f"{repository.rsplit(':', 1)[0] if ':' in repository.rsplit('/', 1)[-1] else repository}@{digest}")
            else:
                ids.add(status['imageID'].removeprefix('docker-pullable://'))
    return sorted(ids)


@dataclass(frozen=True)
class Replicas:
    workload: str  # <Kind>/<name>
    ready: int
    wanted: int | None


@dataclass(frozen=True)
class Problem:
    pod: str
    reason: str
    message: str
    hint: str = ''  # what usually causes this on the platform, and the fix


@dataclass(frozen=True)
class InstanceStatus:
    """What ``app status`` shows, gathered once so the CLI and the console read the same facts."""
    instance: Instance
    desired_revision: str
    applied_revision: str
    unit: tuple[str, str]          # Ready status and message
    release: tuple[str, str] | None
    desired_image: str
    running_images: list[str]
    replicas: list[Replicas]
    routes: list[str]
    certificates: list[tuple[str, str]]  # name, Ready status
    problems: list[Problem]
    settings: dict[str, str] = field(default_factory=dict)  # replicas, cpu, memory, memory_limit as in Git
    exposure: str = ''  # private, authenticated-web or public, from the live routes

    @property
    def applied(self) -> bool:
        return self.desired_revision == self.applied_revision

    @property
    def image_state(self) -> str:
        """Compare by digest: Kubernetes records a running image as repository@digest, never by tag.

        'matches' (every pod runs the desired digest), 'different' (some pod runs another image:
        a rollout in progress or failing), 'none' (no pod running) or 'unpinned' (Git pins no digest,
        so only the tag is known and nothing can be compared).
        """
        if not self.running_images:
            return 'none'
        if '@' not in self.desired_image:
            return 'unpinned'
        want = self.desired_image.split('@', 1)[1]
        return 'matches' if all(image.endswith('@' + want) for image in self.running_images) else 'different'


EXPOSURE_LABELS = {'private': 'private (no route)', 'authenticated-web': 'signed-in (Google sign-in)',
                   'public': 'public (no sign-in)'}


def live_exposure(ingresses: list[dict]) -> str:
    """Who can reach the instance, from its routes: none, all behind sign-in, or open."""
    if not ingresses:
        return 'private'
    signed_in = all(AUTH_MIDDLEWARE in ((i.get('metadata') or {}).get('annotations') or {}).get(
        'traefik.ingress.kubernetes.io/router.middlewares', '') for i in ingresses)
    return 'authenticated-web' if signed_in else 'public'


def replicas(workloads: dict | None) -> list[Replicas]:
    found = []
    for item in (workloads or {}).get('items', []):
        status, spec = item.get('status') or {}, item.get('spec') or {}
        found.append(Replicas(f"{item['kind']}/{item['metadata']['name']}",
                              status.get('readyReplicas', status.get('numberReady', 0)),
                              spec.get('replicas', status.get('desiredNumberScheduled'))))
    return found


def problems(pods: dict | None, instance: Instance | None = None, settings: dict[str, str] | None = None) -> list[Problem]:
    found = []
    for pod in (pods or {}).get('items', []):
        for status in (pod.get('status') or {}).get('containerStatuses') or []:
            if status.get('ready'):
                continue
            state = status.get('state') or {}
            waiting, terminated = state.get('waiting') or {}, state.get('terminated') or {}
            last = ((status.get('lastState') or {}).get('terminated') or {}).get('reason', '')
            reason = waiting.get('reason') or terminated.get('reason') or 'not ready'
            message = (waiting.get('message') or '')[:160]
            if last and last != reason:
                message = f'last exit: {last}' + (f'; {message}' if message else '')
            found.append(Problem(pod['metadata']['name'], reason, message,
                                 hint(reason, last, instance, settings or {}) if instance else ''))
    return found


def hint(reason: str, last: str, instance: Instance, settings: dict[str, str]) -> str:
    """The usual cause of a container problem on this platform, and the command that fixes it."""
    app, env = instance.app, instance.env
    if 'OOMKilled' in (reason, last):
        return (f'It ran out of memory (limit {settings.get("memory_limit", "?")}): raise it with '
                f'make app-scale APP={app} ENV={env} ARGS="--memory-limit 256Mi", or Scale in the console.')
    if reason in ('ImagePullBackOff', 'ErrImagePull', 'InvalidImageName'):
        return ('The node cannot pull the image: check the tag and digest exist, and that the GHCR package is public '
                '(the cluster has no registry credentials; on GitHub: the package\'s settings, Change visibility).')
    if reason == 'CreateContainerConfigError':
        return (f'A Secret or key it uses is missing: set the values in apps/{app}/{env}/secret.sops.yaml '
                '(make check-secrets finds REPLACE_ME), commit and push.')
    if reason in ('CrashLoopBackOff', 'Error'):
        port = settings.get('port', "the instance's port")
        return (f'It starts and exits: read why with make app-logs APP={app} ENV={env} PREVIOUS=true. Usual causes: '
                f'it listens on a port other than {port}, or writes outside /tmp (the root filesystem is read-only).')
    if reason == 'not ready':
        probe = f'{settings["health_path"]} on port {settings["port"]}' if 'health_path' in settings else 'its health path'
        return (f'Running, but not answering its readiness check ({probe}): make the app answer that path, or change '
                '--health-path/--port to match it (edit the HelmRelease).')
    return ''


def release_settings(release: dict | None) -> dict[str, str]:
    """Replicas and resources as the HelmRelease sets them (what app-scale edits)."""
    controller = ((((release or {}).get('spec') or {}).get('values') or {}).get('controllers') or {}).get('main') or {}
    resources = ((controller.get('containers') or {}).get('main') or {}).get('resources') or {}
    found = {'replicas': str(controller.get('replicas', 1)),
             'cpu': (resources.get('requests') or {}).get('cpu'),
             'memory': (resources.get('requests') or {}).get('memory'),
             'memory_limit': (resources.get('limits') or {}).get('memory')}
    probe = (((((controller.get('containers') or {}).get('main') or {}).get('probes') or {}).get('readiness') or {})
             .get('spec') or {}).get('httpGet') or {}
    found |= {'health_path': probe.get('path'), 'port': probe.get('port')}
    return {k: str(v) for k, v in found.items() if v is not None} if release else {}


def gather_status(runner: Runner, instance: Instance) -> InstanceStatus | None:
    """Read the instance from the cluster; None if its Flux unit does not exist."""
    ns = instance.namespace
    unit = get(runner, '-n', 'flux-system', 'get', 'kustomization', instance.unit)
    if unit is None:
        return None
    source = get(runner, '-n', 'flux-system', 'get', 'gitrepository', 'swhurl-platform') or {}
    release = get(runner, '-n', ns, 'get', 'helmrelease', instance.app)
    selector = f'app.kubernetes.io/instance={instance.app}'
    pods = get(runner, '-n', ns, 'get', 'pods', '-l', selector)
    workloads = get(runner, '-n', ns, 'get', WORKLOAD_KINDS, '-l', selector)
    ingresses = (get(runner, '-n', ns, 'get', 'ingress') or {}).get('items', [])
    certificates = (get(runner, '-n', ns, 'get', 'certificate') or {}).get('items', [])
    return InstanceStatus(
        instance=instance,
        desired_revision=((source.get('status') or {}).get('artifact') or {}).get('revision', ''),
        applied_revision=(unit.get('status') or {}).get('lastAppliedRevision', ''),
        unit=ready_condition(unit),
        release=ready_condition(release) if release else None,
        desired_image=desired_image(release),
        running_images=running_images(pods),
        replicas=replicas(workloads),
        routes=[rule.get('host') for ingress in ingresses for rule in ingress['spec'].get('rules') or []],
        certificates=[(cert['metadata']['name'], ready_condition(cert)[0]) for cert in certificates],
        problems=problems(pods, instance, release_settings(release)),
        settings=release_settings(release),
        exposure=live_exposure(ingresses),
    )


def status_lines(found: InstanceStatus) -> list[str]:
    instance = found.instance
    note = '' if found.applied else '  <- not yet applied'
    lines = [
        f'Instance   {instance.app}/{instance.env} (namespace {instance.namespace})',
        f"Git        desired {found.desired_revision.split(':')[-1]}, "
        f"applied {found.applied_revision.split(':')[-1]}{note}",
        f'Flux unit  {found.unit[0]}: {found.unit[1]}',
        'Release    ' + (f'{found.release[0]}: {found.release[1]}' if found.release else 'Unknown: not found'),
    ]
    if found.image_state == 'matches':
        lines += [f'Image      {found.desired_image}', '           running: matches desired']
    else:
        note = {'different': '  <- different image (rollout in progress or failing)',
                'unpinned': '  (Git pins no digest, so this cannot be compared)'}.get(found.image_state, '')
        lines += [f'Image      desired {found.desired_image}',
                  f"           running {', '.join(found.running_images) or 'none'}{note}"]
    lines += [f'Replicas   {r.workload}: {r.ready}/{r.wanted} ready' for r in found.replicas]
    lines.append(f'Exposure   {EXPOSURE_LABELS.get(found.exposure, found.exposure)}')
    lines += [f'Route      https://{host}' for host in found.routes]
    lines += [f'TLS        {name}: {ready}' for name, ready in found.certificates]
    if found.problems:
        lines.append('Problems')
        for p in found.problems:
            lines.append(f'  {p.pod}: {p.reason} {p.message}'.rstrip())
            if p.hint:
                lines.append(f'    fix: {p.hint}')
    return lines


def status(runner: Runner, instance: Instance) -> int:
    found = gather_status(runner, instance)
    if found is None:
        print(f'[ERROR] Flux unit {instance.unit} not found', file=sys.stderr)
        return 1
    for line in status_lines(found):
        print(line)
    return 0


def logs(runner: Runner, instance: Instance, *, tail: str = '100', follow: bool = False,
         previous: bool = False) -> int:
    """Recent logs; ``previous`` shows the last crashed container's output (for CrashLoopBackOff)."""
    args = ['kubectl', '-n', instance.namespace, 'logs', f'deploy/{instance.app}', '--all-containers',
            f'--tail={tail}', *(['--previous'] if previous else [])]
    return runner.attached([*args, '--follow'] if follow else args)


def reconcile(runner: Runner, instance: Instance) -> int:
    for args in (['flux', 'reconcile', 'source', 'git', 'swhurl-platform', '-n', 'flux-system', '--timeout=5m'],
                 ['flux', 'reconcile', 'kustomization', instance.unit, '-n', 'flux-system', '--timeout=10m']):
        code = runner.attached(args, mutating=True)
        if code:
            return code
    return 0


def main(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    argv = argv or []
    if len(argv) != 3 or argv[0] not in ACTIONS:
        print(__doc__.split('\n\n', 1)[1].rstrip(), file=sys.stderr)
        return 2
    action, instance = argv[0], Instance(argv[1], argv[2])
    if action == 'check':
        return policy.main([str(ROOT / INSTANCE_ROOTS[0] / instance.app / instance.env)])
    runner = runner or Runner.from_environment()
    try:
        if action == 'status':
            return status(runner, instance)
        if action == 'logs':
            return logs(runner, instance, tail=os.environ.get('TAIL', '100'),
                        follow=os.environ.get('FOLLOW', 'false') == 'true',
                        previous=os.environ.get('PREVIOUS', 'false') == 'true')
        return reconcile(runner, instance)
    except CommandError as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 1
