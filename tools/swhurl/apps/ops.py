"""One generated app instance's live status (namespace ``<app>-<env>``, Flux unit
``app-<app>-<env>``, HelmRelease ``<app>``), read for the console, the promotion gate and the
notification checker. ``make app-status`` is ``swhurl-v2 status``; logs and a reconcile are one
or two kubectl or flux calls: host/ops.sh runs them.
"""
from __future__ import annotations

import functools
from dataclasses import dataclass, field

from swhurl import interface
from swhurl.apps import helmrelease
from swhurl.apps.contract import AUTH_MIDDLEWARE, app_secret, declared_secrets
from swhurl.apps.instance import Instance
from swhurl.apps.state import ready_reason
from swhurl.kube import ready_condition
from swhurl.run import CommandError, Runner

WORKLOAD_KINDS = 'deploy,statefulset,daemonset,cronjob'
SOURCE = 'swhurl-platform'  # the GitRepository every instance's unit applies
INSTANCE_LABEL = 'app.kubernetes.io/instance'  # on an instance's pods and workloads: the app's name




def get(runner: Runner, *args: str) -> dict | None:
    """Return None only for an absent object; permission and connection failures propagate."""
    return runner.json(['kubectl', *args, '-o', 'json', '--ignore-not-found'])


def image_reference(image: dict) -> str:
    text = image.get('repository', '?') + (f":{image['tag']}" if image.get('tag') else '')
    return text + (f"@{image['digest']}" if image.get('digest') else '')


def desired_image(release: dict | None) -> str:
    return image_reference(helmrelease.image(release))


def running_images(pods: dict | None) -> list[str]:
    """The main controller/container image each started pod runs, as ``repository@digest``.

    When the pod's spec pins the image by digest, that digest is what runs: a pull by digest
    guarantees the content. The node's ``imageID`` can name a different digest for the same
    content: containerd keeps the first digest it pulled an image under, and two builds can
    publish the same image under different index digests (only their attestations differ).
    Without a pinned digest the node's ``imageID`` is all there is.
    """
    ids = set()
    for pod in (pods or {}).get('items', []):
        if (pod.get('metadata', {}).get('labels') or {}).get('app.kubernetes.io/controller', 'main') != 'main':
            continue
        spec = {c.get('name'): c.get('image', '') for c in (pod.get('spec') or {}).get('containers') or []}
        for status in (pod.get('status') or {}).get('containerStatuses') or []:
            if status.get('name', 'main') != 'main':
                continue
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
class Runs:
    """A job's runs, as its CronJob records them (times are RFC 3339, so they compare as text)."""
    schedule: str
    suspended: bool = False
    active: int = 0  # runs going now
    last_schedule: str = ''  # when the newest run started
    last_success: str = ''  # when the newest successful run finished

    @property
    def failed(self) -> bool:
        """The newest run has ended without succeeding: no success since it started, and nothing running."""
        return bool(self.last_schedule) and not self.active and self.last_success < self.last_schedule


def runs_of(cronjob: dict | None) -> Runs | None:
    if not cronjob:
        return None
    spec, status = cronjob.get('spec') or {}, cronjob.get('status') or {}
    return Runs(str(spec.get('schedule', '')), bool(spec.get('suspend')), len(status.get('active') or []),
                str(status.get('lastScheduleTime') or ''), str(status.get('lastSuccessfulTime') or ''))


@dataclass(frozen=True)
class Problem:
    pod: str
    reason: str
    message: str
    hint: str = ''  # what usually causes this on the platform, and the fix


@dataclass(frozen=True)
class InstanceStatus:
    """An instance's live facts, gathered once so the console and the promotion gate read the same ones."""
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
    variables: dict[str, str] = field(default_factory=dict)  # the container's variables, as the HelmRelease sets them
    secret: str = ''  # the Secret whose keys become variables (envFrom), if any
    secret_names: list[str] = field(default_factory=list)  # the secret names its image needs (swhurl.yaml's secrets)
    unit_suspended: bool = False
    release_suspended: bool = False
    unit_reason: str = ''  # the unit's Ready reason: tells waiting from failing
    own_change: bool | None = None  # whether the commits its unit has yet to apply change its own files; None: not asked
    job: bool = False  # a scheduled job: judged by its runs, not its replicas
    runs: Runs | None = None  # a job's CronJob status; None when it has none yet

    @property
    def at_newest(self) -> bool:
        """Revision equality: its unit last applied Git's newest revision. Any commit to main makes this False for a
        while; whether this instance's intended render reached the cluster is a render hash (console.stages.progress)."""
        return bool(self.desired_revision) and self.desired_revision == self.applied_revision

    @property
    def unaffected(self) -> bool:
        """Its unit is behind Git's newest revision, but only by commits that leave its own files alone
        (another app's deploy, a console change): whatever its unit waits for is not about this instance."""
        return bool(self.desired_revision and self.applied_revision) and not self.at_newest and self.own_change is False

    @property
    def current(self) -> bool:
        """Nothing of this instance waits in Git to be applied."""
        return self.at_newest or self.unaffected

    @property
    def image_state(self) -> str:
        """Compare by digest: Kubernetes records a running image as repository@digest, never by tag.

        'matches' (every pod runs the desired digest), 'different' (some pod runs another image:
        a rollout in progress or failing), 'none' (no pod running) or 'unpinned' (Git pins no digest,
        so only the tag is known and nothing can be compared).
        """
        return image_state(self.desired_image, self.running_images)


def image_state(desired: str, running: list[str]) -> str:
    if not running:
        return 'none'
    if '@' not in desired:
        return 'unpinned'
    want = desired.split('@', 1)[1]
    return 'matches' if all(image.endswith('@' + want) for image in running) else 'different'


EXPOSURE_LABELS = {'private': 'private (no route)', 'authenticated-web': 'signed-in (Google sign-in)',
                   'public': 'public (no sign-in)'}


ACME_SOLVER = 'acme.cert-manager.io/http01-solver'


def app_routes(ingresses: list[dict]) -> list[dict]:
    """The app's own Ingresses: not cert-manager's temporary HTTP-01 challenge routes (no sign-in by design)."""
    return [i for i in ingresses if ((i.get('metadata') or {}).get('labels') or {}).get(ACME_SOLVER) != 'true']


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
                f'swhurl-v2 edit --set memoryMiB=256, or Scale in the console.')
    if reason in ('ImagePullBackOff', 'ErrImagePull', 'InvalidImageName'):
        return ('The node cannot pull the image: check the tag and digest exist, and that the GHCR package is public '
                '(the cluster has no registry credentials; on GitHub: the package\'s settings, Change visibility).')
    if reason == 'CreateContainerConfigError':
        return ('A Secret or key it uses is missing: set the value by hand in its secret.sops.yaml '
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
    """Replicas and resources as the HelmRelease sets them (what Scale edits)."""
    controller = helmrelease.workload(release)
    resources = helmrelease.container(release).get('resources') or {}
    found = {'replicas': str(controller.get('replicas', 1)),
             'cpu': (resources.get('requests') or {}).get('cpu'),
             'memory': (resources.get('requests') or {}).get('memory'),
             'memory_limit': (resources.get('limits') or {}).get('memory')}
    probe = (((helmrelease.container(release).get('probes') or {}).get('readiness') or {}).get('spec') or {}).get('httpGet') or {}
    found |= {'health_path': probe.get('path'), 'port': probe.get('port')}
    return {k: str(v) for k, v in found.items() if v is not None} if release else {}


def shown_variables(release: dict | None) -> tuple[dict[str, str], str]:
    """``({name: value}, secret name)``: the variables the HelmRelease sets (a ``valueFrom`` shown by its source)
    and the Secret whose keys arrive through ``envFrom``. Secret values are never read."""
    shown = {}
    for name, value in helmrelease.variables(release).items():
        if isinstance(value, dict):
            ref = (value.get('valueFrom') or {}).get('fieldRef') or {}
            value = f"from the pod ({ref['fieldPath']})" if ref.get('fieldPath') else 'from a reference'
        shown[str(name)] = str(value)
    references = helmrelease.secret_references(release)
    own = app_secret(str(((release or {}).get('metadata') or {}).get('name', '')))
    return shown, own if own in references else next(iter(references), '')


def team_of(runner: Runner, instance: Instance) -> str:
    """The instance's team, from its namespace label; '' when it has none or the account may not read namespaces."""
    try:
        namespace = get(runner, 'get', 'namespace', instance.namespace) or {}
    except CommandError:
        return ''
    return ((namespace.get('metadata') or {}).get('labels') or {}).get(interface.TEAM, '')


def gather_status(runner: Runner, instance: Instance) -> InstanceStatus | None:
    """Read the instance from the cluster; None if its Flux unit does not exist."""
    ns = instance.namespace
    unit = get(runner, '-n', 'flux-system', 'get', 'kustomization', instance.unit)
    if unit is None:
        return None
    selector = f'{INSTANCE_LABEL}={instance.app}'
    source, release, pods, workloads, ingresses, certificates = runner.together(*(
        functools.partial(get, runner, *args) for args in (
            ('-n', 'flux-system', 'get', 'gitrepository', SOURCE), ('-n', ns, 'get', 'helmrelease', instance.app),
            ('-n', ns, 'get', 'pods', '-l', selector), ('-n', ns, 'get', WORKLOAD_KINDS, '-l', selector),
            ('-n', ns, 'get', 'ingress'), ('-n', ns, 'get', 'certificate'))))
    return status_of(instance, unit, source or {}, release, pods, workloads,
                     (ingresses or {}).get('items', []), (certificates or {}).get('items', []))


def status_of(instance: Instance, unit: dict, source: dict, release: dict | None, pods: dict | None,
              workloads: dict | None, ingresses: list[dict] = (), certificates: list[dict] = ()) -> InstanceStatus:
    """The facts, from the objects as the cluster returns them: the instance's unit, the Git source, its release,
    and its own pods and workloads. A reader that only wants the verdict (``state.verdict``) may leave the routes out."""
    ingresses = app_routes(list(ingresses))
    job = helmrelease.kind(release) == 'job'
    if job:
        # A job's pods have ended: the image is the newest successful run's, and a failed run is judged by
        # the CronJob's record, not by its pod's exit (state.verdict).
        succeeded = sorted((pod for pod in (pods or {}).get('items', []) if (pod.get('status') or {}).get('phase') == 'Succeeded'),
                           key=lambda pod: (pod.get('status') or {}).get('startTime', ''))
        pods = {'items': succeeded[-1:]}
        cronjob = next((item for item in (workloads or {}).get('items', []) if item.get('kind') == 'CronJob'), None)
        workloads = {'items': []}
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
        problems=[] if job else problems(pods, instance, release_settings(release)),  # a job's are its runs
        settings=release_settings(release),
        exposure=live_exposure(ingresses),
        variables=shown_variables(release)[0],
        secret=shown_variables(release)[1],
        secret_names=declared_secrets(release),
        unit_suspended=bool((unit.get('spec') or {}).get('suspend')),
        release_suspended=bool(((release or {}).get('spec') or {}).get('suspend')),
        unit_reason=ready_reason(unit),
        job=job,
        runs=runs_of(cronjob) if job else None,
    )
