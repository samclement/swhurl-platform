"""Reading Kubernetes objects: what every area asks of one the same way, and the reads a pod can make of the
API server itself instead of starting ``kubectl`` (:class:`ApiReads`)."""
from __future__ import annotations

import datetime as dt
import json
import os
import ssl
from collections.abc import Mapping
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from swhurl.run import Result, Runner

SERVICE_ACCOUNT = Path('/var/run/secrets/kubernetes.io/serviceaccount')


def ready_condition(obj: dict) -> tuple[str, str]:
    for condition in (obj.get('status') or {}).get('conditions') or []:
        if condition.get('type') == 'Ready':
            return condition.get('status', 'Unknown'), condition.get('message', '')
    return 'Unknown', 'no Ready condition'


def cronjob_run(runner: Runner, namespace: str, name: str, now: dt.datetime | None = None, *, minutes: int = 5) -> str:
    """Whether a CronJob is ``suspended``, ``stale`` (no successful Job in the last ``minutes``) or ``fresh``.
    Raises CommandError, KeyError, ValueError or TypeError when it cannot be read."""
    now = now or dt.datetime.now(dt.UTC)
    job = runner.json(['kubectl', '-n', namespace, 'get', 'cronjob', name, '-o', 'json'])
    last = (job.get('status') or {}).get('lastSuccessfulTime', '')
    taken = dt.datetime.fromisoformat(last.replace('Z', '+00:00')) if last else None
    if job['spec'].get('suspend'):
        return 'suspended'
    return 'stale' if taken is None or now - taken > dt.timedelta(minutes=minutes) else 'fresh'



@dataclass(frozen=True)
class Resource:
    group: str  # '' for the core API
    version: str
    plural: str
    singular: str
    kind: str
    namespaced: bool = True
    aliases: tuple[str, ...] = ()

    @property
    def api_version(self) -> str:
        return f'{self.group}/{self.version}' if self.group else self.version

    def path(self, namespace: str | None) -> str:
        prefix = f'/apis/{self.group}/{self.version}' if self.group else f'/api/{self.version}'
        return f'{prefix}/namespaces/{namespace}/{self.plural}' if namespace else f'{prefix}/{self.plural}'


# What ApiReads may read without kubectl, each at the version this cluster serves (kubectl finds that out by
# asking; a test compares these with the manifests in Git). Secrets are left out on purpose, and a kind that
# is not here is still read, by kubectl.
RESOURCES = (
    Resource('', 'v1', 'namespaces', 'namespace', 'Namespace', namespaced=False, aliases=('ns',)),
    Resource('', 'v1', 'persistentvolumes', 'persistentvolume', 'PersistentVolume', namespaced=False, aliases=('pv',)),
    Resource('', 'v1', 'pods', 'pod', 'Pod'),
    Resource('', 'v1', 'configmaps', 'configmap', 'ConfigMap'),
    Resource('', 'v1', 'services', 'service', 'Service'),
    Resource('', 'v1', 'events', 'event', 'Event'),
    Resource('', 'v1', 'resourcequotas', 'resourcequota', 'ResourceQuota'),
    Resource('', 'v1', 'persistentvolumeclaims', 'persistentvolumeclaim', 'PersistentVolumeClaim', aliases=('pvc',)),
    Resource('apps', 'v1', 'deployments', 'deployment', 'Deployment', aliases=('deploy',)),
    Resource('apps', 'v1', 'statefulsets', 'statefulset', 'StatefulSet'),
    Resource('apps', 'v1', 'daemonsets', 'daemonset', 'DaemonSet'),
    Resource('batch', 'v1', 'cronjobs', 'cronjob', 'CronJob'),
    Resource('networking.k8s.io', 'v1', 'ingresses', 'ingress', 'Ingress'),
    Resource('networking.k8s.io', 'v1', 'networkpolicies', 'networkpolicy', 'NetworkPolicy'),
    Resource('cert-manager.io', 'v1', 'certificates', 'certificate', 'Certificate'),
    Resource('kustomize.toolkit.fluxcd.io', 'v1', 'kustomizations', 'kustomization', 'Kustomization'),
    Resource('helm.toolkit.fluxcd.io', 'v2', 'helmreleases', 'helmrelease', 'HelmRelease'),
    Resource('source.toolkit.fluxcd.io', 'v1', 'gitrepositories', 'gitrepository', 'GitRepository'),
    Resource('image.toolkit.fluxcd.io', 'v1', 'imagepolicies', 'imagepolicy', 'ImagePolicy'),
    Resource('image.toolkit.fluxcd.io', 'v1', 'imageupdateautomations', 'imageupdateautomation', 'ImageUpdateAutomation'),
    Resource('notification.toolkit.fluxcd.io', 'v1', 'receivers', 'receiver', 'Receiver'),
    Resource('notification.toolkit.fluxcd.io', 'v1beta3', 'alerts', 'alert', 'Alert'),
    Resource('notification.toolkit.fluxcd.io', 'v1beta3', 'providers', 'provider', 'Provider'),
    Resource('jetstream.nats.io', 'v1beta2', 'streams', 'stream', 'Stream'),
    Resource('jetstream.nats.io', 'v1beta2', 'consumers', 'consumer', 'Consumer'),
)
NAMES = {name: resource for resource in RESOURCES
         for name in (resource.plural, resource.singular, *resource.aliases,
                      *((f'{resource.plural}.{resource.group}', f'{resource.singular}.{resource.group}')
                        if resource.group else ()))}


@dataclass(frozen=True)
class Read:
    """One ``kubectl get`` as requests to the API server: a ``raw`` path, one ``named`` object, or a list of
    every object of each kind in ``paths``."""
    paths: tuple[tuple[str, Resource | None], ...]
    selector: str = ''
    named: bool = False
    ignore_not_found: bool = False
    raw: bool = False


def api_read(argv: tuple[str, ...]) -> Read | None:
    """``kubectl [-n <namespace>] get ... -o json`` (or ``get --raw <path>``) as a :class:`Read`, or None for
    anything else and for any part of it this does not know: that command is kubectl's to run."""
    if argv[:1] != ('kubectl',):
        return None
    namespace, rest = (argv[2], argv[3:]) if argv[1:2] == ('-n',) and len(argv) > 2 else (None, argv[1:])
    if rest[:1] != ('get',):
        return None
    words, flags, valued = [], set(), {}
    remaining = iter(rest[1:])
    for word in remaining:
        if word in ('-o', '-l', '--raw'):
            if word in valued or (value := next(remaining, None)) is None:
                return None
            valued[word] = value
        elif word.startswith('--raw='):
            valued['--raw'] = word.removeprefix('--raw=')
        elif word in ('--all-namespaces', '-A', '--ignore-not-found'):
            flags.add('-A' if word == '--all-namespaces' else word)
        elif word.startswith('-'):
            return None
        else:
            words.append(word)
    if '--raw' in valued:
        path = valued['--raw']
        return Read(((path, None),), raw=True) if path.startswith('/') and not (words or flags or len(valued) > 1) else None
    if valued.get('-o') != 'json' or not words or len(words) > 2:
        return None
    kinds, name = words[0], words[1] if len(words) == 2 else ''
    if '/' in kinds:
        if name or kinds.count('/') > 1:
            return None
        kinds, name = kinds.split('/')
    resources = [NAMES.get(kind) for kind in kinds.split(',')]
    if None in resources or (name and (len(resources) > 1 or '-l' in valued or '-A' in flags)):
        return None
    paths = []
    for resource in resources:
        within = None if '-A' in flags or not resource.namespaced else namespace
        if resource.namespaced and not within and (name or '-A' not in flags):
            return None  # kubectl would use the namespace of its context
        paths.append((resource.path(within) + (f'/{name}' if name else ''), resource))
    return Read(tuple(paths), valued.get('-l', ''), bool(name), '--ignore-not-found' in flags)


class ApiReads(Runner):
    """A Runner for a pod: what :func:`api_read` understands is one request to the API server as the pod's
    service account, about a tenth of the time of starting ``kubectl``, and everything else (changes, logs,
    Secrets, other tools) runs as it does in any Runner. The answer has kubectl's shape (a ``List`` whose items
    say their kind, an object, or nothing for an absent one with ``--ignore-not-found``) and a failure is a
    non-zero result with kubectl's wording, so callers and their FakeRunner tests cannot tell the two apart."""

    def __init__(self, server: str, token_file: Path, *, ca_file: Path | None = None, transport: Any = None,
                 **kwargs: Any):
        import httpx  # only the console's image is sure to have it
        super().__init__(**kwargs)
        self._errors = (httpx.HTTPError, OSError)
        self._token_file = token_file
        self._client = httpx.Client(base_url=server, transport=transport, timeout=30,
                                    verify=ssl.create_default_context(cafile=str(ca_file)) if ca_file else True)

    def _execute(self, argv: tuple[str, ...], *, input: str | None,
                 env: Mapping[str, str], cwd: Path | None) -> Result:
        read = api_read(argv) if input is None else None
        if read is None:
            return super()._execute(argv, input=input, env=env, cwd=cwd)
        try:
            return self._read(argv, read)
        except self._errors as error:
            return Result(argv, 1, '', f'Unable to connect to the server: {error}')

    def _read(self, argv: tuple[str, ...], read: Read) -> Result:
        # The kubelet replaces the token file before the token expires: read it for every request.
        headers = {'Authorization': f'Bearer {self._token_file.read_text().strip()}'}
        items = []
        for path, resource in read.paths:
            response = self._client.get(path, headers=headers,
                                        params={'labelSelector': read.selector} if read.selector else None)
            if response.status_code == 404 and read.named and read.ignore_not_found:
                return Result(argv, 0, '', '')
            if response.status_code != 200:
                return Result(argv, 1, '', failure(response))
            if read.raw or read.named:
                return Result(argv, 0, response.text, '')
            listed = response.json()
            for item in listed.get('items') or []:  # a list of a built-in kind leaves these out of its items
                item.setdefault('apiVersion', resource.api_version)
                item.setdefault('kind', resource.kind)
                items.append(item)
        return Result(argv, 0, json.dumps({'apiVersion': 'v1', 'kind': 'List', 'metadata': {'resourceVersion': ''},
                                           'items': items}), '')


def failure(response: Any) -> str:
    try:
        status = response.json()
        return f"Error from server ({status['reason']}): {status['message']}"
    except (ValueError, KeyError, TypeError):
        return f'Error from server: HTTP {response.status_code}'


def runner(**kwargs: Any) -> Runner:
    """The Runner for a long-running reader: :class:`ApiReads` in a pod, a plain Runner anywhere else."""
    host, token = os.environ.get('KUBERNETES_SERVICE_HOST', ''), SERVICE_ACCOUNT / 'token'
    if not host or not token.is_file():
        return Runner(**kwargs)
    port = os.environ.get('KUBERNETES_SERVICE_PORT', '443')
    return ApiReads(f"https://{f'[{host}]' if ':' in host else host}:{port}", token,
                    ca_file=SERVICE_ACCOUNT / 'ca.crt', **kwargs)
