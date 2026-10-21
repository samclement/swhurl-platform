"""Backups: an instance with a SQLite database gets a CronJob in its namespace that copies the database,
encrypts it and publishes it to S3 (tools/swhurl/backup_job.py), and a Secret with the upload-only key,
which its unit substitutes from platform-backup's flux-system Secret. The job runs as the app's user with
the app's claim mounted, no service-account token, and the instance's egress proxy port. Each runs
every night at a minute its namespace picks (docs/operations.md#backups-and-recovery)."""
from __future__ import annotations

import hashlib
from pathlib import Path

import yaml

from swhurl import interface, platform, yamlio
from swhurl.apps import egress
from swhurl.apps.contract import BACKUP_FILE, BACKUP_IMAGE, BACKUP_LABEL, BACKUP_RECIPIENT, BACKUP_UNIT, BACKUP_WRITER
from swhurl.apps.model.draft import Draft, Entry, Platform, RenderError

# The one backup switch: this job's spec.suspend also suspends every SQLite backup job (docs/operations.md).
MONGODB_JOB = Path('platform/backup/mongodb.yaml')
# What the AWS CLI reads to reach S3 through the instance's proxy port (HOST_IP: NO_PROXY names it).
PROXY_VARIABLES = (interface.OTLP_HOST_IP, interface.HTTP_VARIABLE, interface.HTTPS_VARIABLE, interface.NO_PROXY_VARIABLE)


def suspended(root: Path) -> bool:
    """``spec.suspend`` of the MongoDB backup job. A root without platform/backup (a scratch root) has no
    suspension; a present directory whose file is missing or malformed is an error, never "not suspended"."""
    path = root / MONGODB_JOB
    if not path.parent.is_dir():
        return False
    try:
        value = (yamlio.load(path.read_text())['spec'] or {}).get('suspend')
    except (OSError, yaml.YAMLError, TypeError, KeyError, AttributeError) as error:
        raise RenderError(f'{MONGODB_JOB}: cannot read spec.suspend ({type(error).__name__})') from None
    if not isinstance(value, bool):
        raise RenderError(f'{MONGODB_JOB}: spec.suspend must be true or false')
    return value


def schedule(namespace: str) -> str:
    """A minute between 03:35 and 04:24 UTC, the same for an instance every night, spread across instances
    (MongoDB's runs at 03:30)."""
    minute = 35 + int(hashlib.sha256(namespace.encode()).hexdigest(), 16) % 50
    return f'{minute % 60} {3 + minute // 60} * * *'


def variables(values: dict) -> list[dict]:
    """A container's env in the map form the app chart takes, as a Kubernetes list."""
    return [{'name': name, **(value if isinstance(value, dict) else {'value': str(value)})} for name, value in values.items()]


def manifests(app: str, namespace: str, uid: int, *, claim: str, proxy: dict, path: str = interface.SQLITE_PATH,
              suspend: bool = False, key: tuple[str, str] = ('${BACKUP_AWS_ACCESS_KEY_ID}', '${BACKUP_AWS_SECRET_ACCESS_KEY}')) -> tuple[dict, dict]:
    """The job's Secret (``key``: Flux substitutes the references) and CronJob; the SQLite restore live test
    runs the same job on its throwaway app."""
    name = f'{app}-backup'
    secret = {'apiVersion': 'v1', 'kind': 'Secret', 'metadata': {'name': name, 'namespace': namespace}, 'type': 'Opaque',
              'stringData': {'AWS_ACCESS_KEY_ID': key[0], 'AWS_SECRET_ACCESS_KEY': key[1]}}
    container = {
        'name': 'backup', 'image': BACKUP_IMAGE, 'args': ['backup-job-sqlite'],
        'env': variables({**proxy, 'AGE_RECIPIENT': BACKUP_RECIPIENT, 'BACKUP_S3_URI': f'{platform.SQLITE_S3_URI}{namespace}/',
                          'BACKUP_SOURCE': f'{namespace}/{app}', interface.DATABASE_PATH_VARIABLE: path}),
        'envFrom': [{'secretRef': {'name': name}}],
        'resources': {'requests': {'cpu': '10m', 'memory': '64Mi'}, 'limits': {'memory': '256Mi'}},
        'securityContext': {'allowPrivilegeEscalation': False, 'readOnlyRootFilesystem': True, 'capabilities': {'drop': ['ALL']}},
        'volumeMounts': [{'name': 'data', 'mountPath': interface.DATA_MOUNT}, {'name': 'scratch', 'mountPath': '/scratch'},
                         {'name': 'tmp', 'mountPath': '/tmp'}],
    }
    pod = {
        'metadata': {'labels': {BACKUP_LABEL: 'true'}},
        'spec': {
            'restartPolicy': 'Never', 'automountServiceAccountToken': False,
            'securityContext': {'runAsNonRoot': True, 'runAsUser': uid, 'runAsGroup': uid, 'fsGroup': uid,
                                'seccompProfile': {'type': 'RuntimeDefault'}},
            'containers': [container],
            # The app's own claim: ReadWriteOnce admits a second pod on the same (only) node.
            'volumes': [{'name': 'data', 'persistentVolumeClaim': {'claimName': claim}},
                        {'name': 'scratch', 'emptyDir': {'sizeLimit': '1Gi'}}, {'name': 'tmp', 'emptyDir': {}}],
        },
    }
    job = {
        'apiVersion': 'batch/v1', 'kind': 'CronJob', 'metadata': {'name': name, 'namespace': namespace, 'labels': {BACKUP_LABEL: 'true'}},
        'spec': {'schedule': schedule(namespace), 'timeZone': 'Etc/UTC', 'suspend': suspend,
                 'concurrencyPolicy': 'Forbid', 'startingDeadlineSeconds': 3600,
                 'successfulJobsHistoryLimit': 1, 'failedJobsHistoryLimit': 3,
                 'jobTemplate': {'spec': {'backoffLimit': 2, 'activeDeadlineSeconds': 900, 'template': pod}}},
    }
    return secret, job


def apply(draft: Draft, entry: Entry, platform: Platform) -> None:
    if entry.release.database != 'sqlite':
        return
    app, namespace = draft.instance.app, draft.instance.namespace
    uid = entry.release.uid
    port = entry.settings.allocations.egress_port
    proxied = egress.variables(port) if platform.egress and port is not None else {}
    proxy = {key: value for key, value in proxied.items() if key in PROXY_VARIABLES}
    secret, job = manifests(app, namespace, uid, claim=app, proxy=proxy, suspend=platform.backups_suspended)
    draft.add_file(BACKUP_FILE, [secret, job])
    draft.depend(BACKUP_UNIT)
    draft.unit['spec']['postBuild'] = {'substituteFrom': [{'kind': 'Secret', 'name': BACKUP_WRITER, 'optional': False}]}
