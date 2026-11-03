"""Notification image/workload helpers and display-only desired HelmRelease facts.

The console consumes Go observations; these helpers remain for the notification checker.
"""
from __future__ import annotations

from dataclasses import dataclass

from swhurl import interface
from swhurl.apps import helmrelease
from swhurl.apps.contract import app_secret
from swhurl.apps.instance import Instance
from swhurl.run import CommandError, Runner


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


def image_state(desired: str, running: list[str]) -> str:
    if not running:
        return 'none'
    if '@' not in desired:
        return 'unpinned'
    want = desired.split('@', 1)[1]
    return 'matches' if all(image.endswith('@' + want) for image in running) else 'different'


EXPOSURE_LABELS = {'private': 'private (no route)', 'authenticated-web': 'signed-in (Google sign-in)',
                   'public': 'public (no sign-in)'}


def replicas(workloads: dict | None) -> list[Replicas]:
    found = []
    for item in (workloads or {}).get('items', []):
        status, spec = item.get('status') or {}, item.get('spec') or {}
        found.append(Replicas(f"{item['kind']}/{item['metadata']['name']}",
                              status.get('readyReplicas', status.get('numberReady', 0)),
                              spec.get('replicas', status.get('desiredNumberScheduled'))))
    return found


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
