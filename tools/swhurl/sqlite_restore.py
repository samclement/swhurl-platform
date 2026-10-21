"""Restore one app's SQLite database from its backup (the ``--database sqlite`` capability).

    restore-sqlite <app> <env>   CONFIRM=<app>/<env>, optional BACKUP_FILE, AGE_KEY_FILE, SQLITE_S3_URI

Downloads the newest complete backup from ``SQLITE_S3_URI<app>-<env>/`` into a private temporary
directory with the AWS CLI's own credentials (S3 read access), or takes BACKUP_FILE (a local
``sqlite-*.db.age`` with its metadata beside it), checks its checksum and that it came from this
instance, then:

  1. suspends the instance's HelmRelease (so no upgrade starts the app mid-restore) and scales
     the Deployment to 0, waiting until its pods are gone;
  2. starts a pod as the app's user on the app's claim (as its backup job does) and streams
     ``age -d`` into it, so plaintext never reaches this host's disk;
  3. checks the restored file against the backup's metadata (SHA-256 of the plaintext, recorded by
     the backup job, then ``integrity_check`` and the table count);
  4. moves the current database and its ``-wal``/``-shm`` files into ``before-restore-<UTC>/``
     beside it, then puts the restored file in their place;
  5. scales back, resumes the HelmRelease and waits for the rollout.

Steps 5 and the pod's deletion run even when an earlier step fails. A restored file that fails
its checks never replaces the live database. DRY_RUN=true checks the backup and prints the plan.
"""
from __future__ import annotations

import contextlib
import json
import os
import posixpath
import sys
import tempfile
import time
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path

from swhurl import backupset, interface
from swhurl.apps.instance import Instance
from swhurl.backupset import BackupError
from swhurl.run import CommandError, Runner
from swhurl.sqlite_backup import backups_in, uri

# The restore pod's sqlite3 and coreutils.
IMAGE = 'docker.io/keinos/sqlite3:3.53.4@sha256:637e4ac26b6b5c0bf0acf0f5625f7d18453f39407bee18787d1d16875ad07e24'
HELM_NAME = 'helm.toolkit.fluxcd.io/name'


@dataclass(frozen=True)
class Database:
    namespace: str
    app: str
    claim: str
    mount: str  # where the app mounts the claim
    path: str  # DATABASE_PATH, inside the mount
    uid: int

    @property
    def name(self) -> str:
        return f'{self.namespace}/{self.app}'

    @property
    def in_pod(self) -> str:
        """The database's path in the restore pod, which mounts the claim at /data."""
        return '/data/' + self.path.removeprefix(self.mount).lstrip('/')


def database(runner: Runner, namespace: str) -> Database:
    """The one Deployment in ``namespace`` whose main container has DATABASE_PATH, and the claim it is on."""
    found = []
    for deploy in runner.json(['kubectl', '-n', namespace, 'get', 'deployments', '-o', 'json']).get('items') or []:
        spec = deploy['spec']['template']['spec']
        for container in spec.get('containers') or []:
            path = {v.get('name'): v.get('value') for v in container.get('env') or []}.get(interface.DATABASE_PATH_VARIABLE)
            if not path:
                continue
            claims = {v['name']: v['persistentVolumeClaim']['claimName'] for v in spec.get('volumes') or []
                      if 'persistentVolumeClaim' in v}
            mounts = [m for m in container.get('volumeMounts') or []
                      if m['name'] in claims and path.startswith(m['mountPath'].rstrip('/') + '/')]
            if not mounts:
                raise BackupError(f"{namespace}/{deploy['metadata']['name']}: "
                                  f'{interface.DATABASE_PATH_VARIABLE}={path} is not on a mounted claim')
            uid = (spec.get('securityContext') or {}).get('runAsUser', 65532)
            found.append(Database(namespace, deploy['metadata']['name'], claims[mounts[0]['name']],
                                  mounts[0]['mountPath'], path, uid))
    if len(found) != 1:
        raise BackupError(f'expected one SQLite database in {namespace}, found {len(found)}')
    return found[0]


def pod_manifest(db: Database, name: str) -> dict:
    """The restore pod: the app's user, the app's claim, nothing else (as hardened as an app pod)."""
    return {
        'apiVersion': 'v1', 'kind': 'Pod',
        'metadata': {'name': name, 'namespace': db.namespace},
        'spec': {
            'restartPolicy': 'Never', 'automountServiceAccountToken': False, 'activeDeadlineSeconds': 900,
            'securityContext': {'runAsNonRoot': True, 'runAsUser': db.uid, 'runAsGroup': db.uid, 'fsGroup': db.uid,
                                'seccompProfile': {'type': 'RuntimeDefault'}},
            'containers': [{
                'name': 'sqlite', 'image': IMAGE, 'command': ['sleep', '900'],
                'resources': {'requests': {'cpu': '10m', 'memory': '32Mi'}, 'limits': {'memory': '256Mi'}},
                'securityContext': {'allowPrivilegeEscalation': False, 'readOnlyRootFilesystem': True,
                                    'capabilities': {'drop': ['ALL']}},
                'volumeMounts': [{'name': 'data', 'mountPath': '/data'}, {'name': 'tmp', 'mountPath': '/tmp'}],
            }],
            'volumes': [{'name': 'data', 'persistentVolumeClaim': {'claimName': db.claim}},
                        {'name': 'tmp', 'emptyDir': {}}],
        },
    }


def sha256_in_pod(runner: Runner, exec_pod: list[str], path: str) -> str:
    """SHA-256 of a file in the pod, so the restored copy is checked without plaintext on this host."""
    return runner.output([*exec_pod, 'sha256sum', path]).split()[0]


def checked_metadata(db: Database, archive: Path) -> dict:
    """The backup's metadata, after checking the archive is intact and belongs to ``db``."""
    metadata = backups_in(archive.parent).read(archive)
    if metadata.get('source') != db.name:
        raise BackupError(f'{archive.name} is a backup of {metadata.get("source")}, not {db.name}')
    return metadata


def selector(deploy: dict) -> str:
    labels = ((deploy.get('spec') or {}).get('selector') or {}).get('matchLabels') or {}
    return ','.join(f'{k}={v}' for k, v in sorted(labels.items()))


def restore(runner: Runner, db: Database, archive: Path, age_key: Path, *, stamp: str,
            out: Callable[[str], None] = print, sleep: Callable[[float], None] = time.sleep) -> str:
    """Restore ``archive`` into ``db``'s claim; return where the replaced files were kept (app's path)."""
    metadata = checked_metadata(db, archive)
    if not os.access(age_key, os.R_OK):
        raise BackupError(f'cannot read the age key {age_key} (set AGE_KEY_FILE)')
    ns = db.namespace
    pod = f'sqlite-restore-{db.app}'[:63]
    incoming = db.in_pod + '.restore'
    aside = posixpath.join(posixpath.dirname(db.in_pod), f'before-restore-{stamp}')
    aside_app = posixpath.join(posixpath.dirname(db.path), f'before-restore-{stamp}')
    deploy = runner.json(['kubectl', '-n', ns, 'get', 'deployment', db.app, '-o', 'json']) or {}
    replicas = (deploy.get('spec') or {}).get('replicas', 1)
    release = ((deploy.get('metadata') or {}).get('labels') or {}).get(HELM_NAME)
    if runner.dry_run:
        out(f'Plan (restore-sqlite {db.name}):')
        out(f'  - backup {archive.name} ({metadata.get("tables")} tables, checksum ok)')
        out(f'  - suspend helmrelease/{ns}/{release}' if release else '  - no HelmRelease manages the Deployment')
        out(f'  - scale deployment/{db.app} from {replicas} to 0 and wait for its pods to stop')
        out(f'  - a pod as UID {db.uid} on claim {db.claim} receives the decrypted copy, checks it, '
            f'moves {db.path} and its -wal/-shm files to {aside_app}/ and puts the copy in place')
        out(f'  - scale back to {replicas}' + (', resume the HelmRelease' if release else '') + ', wait for the rollout')
        return aside_app

    exec_pod = ['kubectl', '-n', ns, 'exec', pod, '-c', 'sqlite', '--']
    with contextlib.ExitStack() as cleanup:
        if release:
            runner.run(['flux', 'suspend', 'helmrelease', release, '-n', ns], mutating=True)
            cleanup.callback(runner.run, ['flux', 'resume', 'helmrelease', release, '-n', ns], check=False,
                             mutating=True)
            out(f'[OK] Suspended helmrelease/{ns}/{release}')
        runner.run(['kubectl', '-n', ns, 'scale', f'deployment/{db.app}', '--replicas=0'], mutating=True)
        cleanup.callback(runner.run, ['kubectl', '-n', ns, 'scale', f'deployment/{db.app}', f'--replicas={replicas}'],
                         check=False, mutating=True)
        pods = ['kubectl', '-n', ns, 'get', 'pods', '-l', selector(deploy), '-o', 'name']
        for _ in range(90):
            if not runner.output(pods).strip():
                break
            sleep(2)
        else:
            raise BackupError(f'{db.name}: the app\'s pods did not stop within 3 minutes')
        out(f'[OK] Stopped deployment/{db.app}')

        runner.run(['kubectl', 'apply', '-f', '-'], input=json.dumps(pod_manifest(db, pod)), mutating=True)
        cleanup.callback(runner.run, ['kubectl', '-n', ns, 'delete', 'pod', pod, '--ignore-not-found', '--wait=false'],
                         check=False, mutating=True)
        runner.run(['kubectl', '-n', ns, 'wait', '--for=condition=Ready', f'pod/{pod}', '--timeout=120s'])
        runner.pipe(['age', '-d', '-i', age_key, archive],
                    ['kubectl', '-n', ns, 'exec', '-i', pod, '-c', 'sqlite', '--', 'sh', '-c', 'cat > "$1"', 'sh',
                     incoming], mutating=True)
        digest = sha256_in_pod(runner, exec_pod, incoming)
        check = runner.output([*exec_pod, 'sqlite3', incoming, 'PRAGMA integrity_check;']).strip()
        tables = runner.output([*exec_pod, 'sqlite3', incoming,
                                "SELECT count(*) FROM sqlite_master WHERE type = 'table';"]).strip()
        problems = [f'SHA-256 differs from the backup\'s ({digest[:12]})'] \
            if metadata.get('plain_sha256') and digest != metadata['plain_sha256'] else []
        problems += [f'integrity_check: {check[:200]}'] if check != 'ok' else []
        problems += [f'{tables} tables, expected {metadata.get("tables")}'] \
            if tables != str(metadata.get('tables')) else []
        if problems:
            runner.run([*exec_pod, 'rm', '-f', incoming], check=False, mutating=True)
            raise BackupError(f'{db.name}: the restored copy failed its checks ({"; ".join(problems)}); '
                                'the database is unchanged')
        runner.run([*exec_pod, 'sh', '-c',
                    'set -e; mkdir "$2"; for f in "$1" "$1-wal" "$1-shm"; do [ ! -e "$f" ] || mv "$f" "$2/"; done; '
                    'mv "$1.restore" "$1"', 'sh', db.in_pod, aside], mutating=True)
        out(f'[OK] Restored {archive.name} into {db.path} ({tables} tables, integrity ok); '
            f'the replaced files are in {aside_app}/')
    if replicas:
        runner.run(['kubectl', '-n', ns, 'rollout', 'status', f'deployment/{db.app}', '--timeout=180s'])
    out(f'[OK] {db.name} is running again' + (f'; helmrelease/{ns}/{release} resumed' if release else ''))
    return aside_app


def main(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    argv = argv or []
    if len(argv) != 2:
        print(__doc__.split('\n\n')[1], file=sys.stderr)
        return 2
    app, env = argv
    instance = Instance.named(app, env)
    if instance is None:
        print(f'[ERROR] no instance {app}/{env}', file=sys.stderr)
        return 2
    runner = runner or Runner.from_environment()
    if not runner.dry_run and os.environ.get('CONFIRM') != f'{app}/{env}':
        print(f'[ERROR] restore-sqlite replaces the live database of {app}/{env} and stops the app meanwhile. '
              f'Re-run with CONFIRM={app}/{env}', file=sys.stderr)
        return 2
    try:
        db = database(runner, instance.namespace)
        named = os.environ.get('BACKUP_FILE')
        with tempfile.TemporaryDirectory(prefix='restore-sqlite-') as scratch:
            archive = Path(named) if named else backups_in(Path(scratch)).fetch(runner, uri(db.namespace))
            restore(runner, db, archive, backupset.age_key(os.environ), stamp=backupset.stamp())
    except (BackupError, CommandError, ValueError) as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 1
    return 0
