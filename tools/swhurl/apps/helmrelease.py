"""Read an app instance's HelmRelease: the one place that knows where things are in its values.

For live cluster observations and validators examining rendered output, not Git semantics (those
come from apps/model/store.py). Every function takes plain data, tolerates a missing release or
section, and returns the real nested object where one exists.
"""
from __future__ import annotations


def values(release: dict | None) -> dict:
    return ((release or {}).get('spec') or {}).get('values') or {}


def workload(release: dict | None) -> dict:
    """The app's one controller: replicas, strategy, annotations and its containers."""
    return (values(release).get('controllers') or {}).get('main') or {}


def kind(release: dict | None) -> str:
    """What the instance runs as: web (it has a Service), job (a CronJob controller) or worker."""
    if 'service' in values(release):
        return 'web'
    return 'job' if workload(release).get('type') == 'cronjob' else 'worker'


def schedule(release: dict | None) -> str:
    """A job's schedule, or '' for anything else."""
    return str((workload(release).get('cronjob') or {}).get('schedule') or '')


def container(release: dict | None) -> dict:
    """The app's one container: image, resources, probes, variables."""
    return (workload(release).get('containers') or {}).get('main') or {}


def variables(release: dict | None) -> dict:
    """The container's environment variables by name, as the release sets them: a plain value, or
    the mapping (valueFrom) it comes from. Not the keys of its Secrets (secret_references)."""
    found = container(release).get('env') or {}
    if isinstance(found, list):  # the chart also takes Kubernetes' own list form
        return {entry.get('name'): entry.get('value', entry) for entry in found if isinstance(entry, dict)}
    return found if isinstance(found, dict) else {}


def secret_references(release: dict | None) -> list[str]:
    """The Secrets whose every key becomes a variable (envFrom)."""
    names = [(entry.get('secretRef') or {}).get('name') for entry in container(release).get('envFrom') or []
             if isinstance(entry, dict)]
    return [name for name in names if name]


def image(release: dict | None) -> dict:
    """``{repository, tag, digest}`` as far as the release names them."""
    return container(release).get('image') or {}


def port(release: dict | None) -> int | None:
    """The app's HTTP port; None for a worker, which has no Service."""
    main = (values(release).get('service') or {}).get('main') or {}
    return ((main.get('ports') or {}).get('http') or {}).get('port')


def hosts(release: dict | None) -> tuple[str, ...]:
    """The hosts of the app's routes."""
    ingresses = values(release).get('ingress') or {}
    return tuple(h['host'] for ingress in ingresses.values() for h in (ingress or {}).get('hosts') or [] if h.get('host'))
