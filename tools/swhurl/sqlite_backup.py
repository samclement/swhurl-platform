"""Back up every app's SQLite database (the ``--database sqlite`` capability).

    backup-sqlite   for each instance whose HelmRelease sets DATABASE_PATH: copy the database
                    with SQLite's online backup, check it, encrypt it with age to BACKUP_DIR/sqlite/<ns>/,
                    prune, and copy new files to SQLITE_S3_URI<ns>/

The copy is taken by a short-lived pod in the app's namespace that mounts the app's claim
(ReadWriteOnce allows a second pod on the same node), runs as the app's own user (so it
can read the database and its WAL files) and runs ``sqlite3 .backup``, which is safe while
the app writes. The copy streams from ``kubectl exec`` straight into ``age``: plaintext never
reaches this host's disk. Settings come from the environment as for backup-mongodb
(BACKUP_DIR, DRY_RUN, PRUNE, KEEP_DAILY, KEEP_WEEKLY, AGE_RECIPIENT) plus SQLITE_S3_URI
(empty skips the upload).
"""
from __future__ import annotations

import datetime as dt
import json
import os
import re
import sys
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path

from swhurl import platform, retention
from swhurl.apps.contract import DATABASE_PATH_ENV
from swhurl.recovery import BackupSettings, RecoveryError, private_files, sha256, upload
from swhurl.run import CommandError, Runner

IMAGE = 'docker.io/keinos/sqlite3:3.53.4@sha256:637e4ac26b6b5c0bf0acf0f5625f7d18453f39407bee18787d1d16875ad07e24'
PATTERN = re.compile(r'^sqlite-(\d{8}T\d{6}Z)\.db\.age$')
BACKUP_LABEL = platform.label('sqlite-backup')
COPY = '/tmp/backup.db'


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
    def pod(self) -> str:
        return f'sqlite-backup-{self.app}'[:63]

    @property
    def in_pod(self) -> str:
        """The database's path in the backup pod, which mounts the claim at /data."""
        return '/data/' + self.path.removeprefix(self.mount).lstrip('/')


def find(runner: Runner) -> list[Database]:
    """Instances whose main container has DATABASE_PATH, from their live Deployments."""
    found = []
    deployments = runner.json(['kubectl', 'get', 'deployments', '--all-namespaces', '-o', 'json']).get('items') or []
    for deploy in deployments:
        spec = deploy['spec']['template']['spec']
        for container in spec.get('containers') or []:
            env = {v.get('name'): v.get('value') for v in container.get('env') or []}
            path = env.get(DATABASE_PATH_ENV)
            if not path:
                continue
            claims = {v['name']: v['persistentVolumeClaim']['claimName'] for v in spec.get('volumes') or []
                      if 'persistentVolumeClaim' in v}
            mounts = [m for m in container.get('volumeMounts') or []
                      if m['name'] in claims and path.startswith(m['mountPath'].rstrip('/') + '/')]
            if not mounts:
                raise RecoveryError(f"{deploy['metadata']['namespace']}/{deploy['metadata']['name']}: "
                                    f'{DATABASE_PATH_ENV}={path} is not on a mounted claim')
            uid = (spec.get('securityContext') or {}).get('runAsUser', 65532)
            found.append(Database(deploy['metadata']['namespace'], deploy['metadata']['name'],
                                  claims[mounts[0]['name']], mounts[0]['mountPath'], path, uid))
    return sorted(found, key=lambda d: d.name)


def pod_manifest(db: Database, name: str | None = None) -> dict:
    """The backup (or restore) pod: the app's user, the app's claim, nothing else (as hardened as an app pod)."""
    return {
        'apiVersion': 'v1', 'kind': 'Pod',
        'metadata': {'name': name or db.pod, 'namespace': db.namespace, 'labels': {BACKUP_LABEL: 'true'}},
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


def plain_fingerprint(runner: Runner, exec_pod: list[str], path: str) -> tuple[int, str]:
    """Size and SHA-256 of a file in the pod, so the stream can be checked without plaintext on this host."""
    size = int(runner.output([*exec_pod, 'wc', '-c', path]).split()[0])
    digest = runner.output([*exec_pod, 'sha256sum', path]).split()[0]
    return size, digest


def backup_one(runner: Runner, db: Database, settings: BackupSettings, *, stamp: str,
               out: Callable[[str], None] = print) -> Path:
    directory = settings.backup_dir / 'sqlite' / db.namespace
    archive = directory / f'sqlite-{stamp}.db.age'
    exec_pod = ['kubectl', '-n', db.namespace, 'exec', db.pod, '-c', 'sqlite', '--']
    if runner.dry_run:
        out(f'  - {db.name}: a pod as UID {db.uid} mounting {db.claim} copies {db.path} with sqlite3 .backup, '
            f'checks it, encrypts it to {archive}; the pod is then deleted')
        return archive
    runner.run(['kubectl', 'apply', '-f', '-'], input=json.dumps(pod_manifest(db)), mutating=True)
    try:
        runner.run(['kubectl', '-n', db.namespace, 'wait', '--for=condition=Ready', f'pod/{db.pod}', '--timeout=120s'])
        runner.run([*exec_pod, 'sqlite3', db.in_pod, f'.backup {COPY}'])
        check = runner.output([*exec_pod, 'sqlite3', COPY, 'PRAGMA integrity_check;']).strip()
        if check != 'ok':
            raise RecoveryError(f'{db.name}: the copy failed integrity_check: {check[:200]}')
        tables = int(runner.output([*exec_pod, 'sqlite3', COPY,
                                    "SELECT count(*) FROM sqlite_master WHERE type = 'table';"]).strip() or 0)
        size, digest = plain_fingerprint(runner, exec_pod, COPY)
        with private_files():
            directory.mkdir(parents=True, exist_ok=True)
            partial = archive.with_name(archive.name + '.partial')
            try:
                runner.pipe([*exec_pod, 'cat', COPY], ['age', '-r', settings.recipient, '-o', partial])
                # age adds a header and 16 bytes per 64 KiB, so a whole copy is never smaller than the plaintext;
                # an age file of a truncated or empty stream is (it still has a header).
                if not partial.is_file() or partial.stat().st_size < max(size, 1):
                    raise RecoveryError(f'{db.name}: the encrypted copy is smaller than the database '
                                        f'({size} bytes): the stream was cut short')
                partial.replace(archive)
            finally:
                partial.unlink(missing_ok=True)
            archive.with_name(archive.name.replace('.db.age', '.json')).write_text(json.dumps({
                'created': stamp, 'source': db.name, 'claim': db.claim, 'path': db.path, 'tables': tables,
                'integrity_check': check, 'bytes': size, 'plain_sha256': digest, 'age_recipient': settings.recipient, 'sha256': sha256(archive)}) + '\n')
    finally:
        runner.run(['kubectl', '-n', db.namespace, 'delete', 'pod', db.pod, '--ignore-not-found', '--wait=false'],
                   check=False, mutating=True)
    out(f'[OK] {db.name}: encrypted backup {archive} ({tables} tables, integrity ok)')
    return archive


def backup(runner: Runner, settings: BackupSettings, s3_uri: str, *, now: dt.datetime | None = None,
           out: Callable[[str], None] = print) -> list[Path]:
    stamp = (now or dt.datetime.now(dt.UTC)).strftime('%Y%m%dT%H%M%SZ')
    databases = find(runner)
    if runner.dry_run:
        out('Plan (backup-sqlite):')
    if not databases:
        out('[INFO] No app has a SQLite database (DATABASE_PATH): nothing to back up.')
        return []
    archives, failed = [], []
    for db in databases:
        try:
            archives.append(backup_one(runner, db, settings, stamp=stamp, out=out))
        except (RecoveryError, CommandError) as error:
            failed.append(db.name)
            out(f'[ERROR] {db.name}: {error}')
            continue
        if runner.dry_run:
            continue
        directory = settings.backup_dir / 'sqlite' / db.namespace
        if settings.prune:
            retention.prune(directory, settings.keep_daily, settings.keep_weekly, pattern=PATTERN, out=out)
        if s3_uri:
            upload(runner, directory, f'{s3_uri}{db.namespace}/', out, prefix='sqlite-', suffixes=('.db.age', '.json'))
    if failed:
        raise RecoveryError(f'backup failed for {", ".join(failed)}')
    return archives


def backup_sqlite(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    try:
        settings = BackupSettings.from_env(os.environ)
        backup(runner or Runner.from_environment(), settings, os.environ.get('SQLITE_S3_URI', platform.SQLITE_S3_URI))
    except (RecoveryError, CommandError, ValueError) as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 1
    return 0
