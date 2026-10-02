"""Restore one app's SQLite database from its backup (the ``--database sqlite`` capability).

    restore-sqlite <app> <env>   CONFIRM=<app>/<env>, optional BACKUP_FILE, BACKUP_DIR, AGE_KEY_FILE

Takes the newest ``BACKUP_DIR/sqlite/<app>-<env>/sqlite-*.db.age`` (or BACKUP_FILE), checks its
checksum and that it came from this instance, then:

  1. suspends the instance's HelmRelease (so no upgrade starts the app mid-restore) and scales
     the Deployment to 0, waiting until its pods are gone;
  2. starts a pod as the app's user on the app's claim (as backup-sqlite does) and streams
     ``age -d`` into it, so plaintext never reaches this host's disk;
  3. checks the restored file against the backup's metadata (SHA-256 of the plaintext, recorded by
     backup-sqlite in the pod, then ``integrity_check`` and the table count);
  4. moves the current database and its ``-wal``/``-shm`` files into ``before-restore-<UTC>/``
     beside it, then puts the restored file in their place;
  5. scales back, resumes the HelmRelease and waits for the rollout.

Steps 5 and the pod's deletion run even when an earlier step fails. A restored file that fails
its checks never replaces the live database. DRY_RUN=true checks the backup and prints the plan.
"""
from __future__ import annotations

import contextlib
import datetime as dt
import json
import os
import posixpath
import sys
import time
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path

from swhurl import ROOT, platform
from swhurl.recovery import DEFAULT_BACKUP_DIR, RecoveryError, sha256
from swhurl.run import CommandError, Runner
from swhurl.sqlite_backup import Database, find, plain_fingerprint, pod_manifest

HELM_NAME = 'helm.toolkit.fluxcd.io/name'


@dataclass
class RestoreSettings:
    backup_dir: Path
    age_key: Path
    backup_file: Path | None = None

    @classmethod
    def from_env(cls, env: dict[str, str] | os._Environ) -> RestoreSettings:
        return cls(
            backup_dir=Path(env.get('BACKUP_DIR') or DEFAULT_BACKUP_DIR),
            age_key=Path(env.get('AGE_KEY_FILE') or env.get('SOPS_AGE_KEY_FILE') or ROOT / platform.AGE_KEY),
            backup_file=Path(env['BACKUP_FILE']) if env.get('BACKUP_FILE') else None,
        )

    def archive(self, namespace: str) -> Path:
        if self.backup_file:
            return self.backup_file
        directory = self.backup_dir / 'sqlite' / namespace
        candidates = sorted(directory.glob('sqlite-*.db.age')) if directory.is_dir() else []
        if not candidates:
            raise RecoveryError(f'no backup in {directory} (set BACKUP_FILE, or copy one from S3 there)')
        return candidates[-1]  # names sort by their UTC stamp


def checked_metadata(db: Database, archive: Path) -> dict:
    """The backup's metadata, after checking the archive is intact and belongs to ``db``."""
    if not archive.is_file():
        raise RecoveryError(f'{archive} does not exist')
    metadata_path = archive.with_name(archive.name.replace('.db.age', '.json'))
    if not metadata_path.is_file():
        raise RecoveryError(f'missing backup metadata: {metadata_path}')
    metadata = json.loads(metadata_path.read_text())
    if sha256(archive) != metadata.get('sha256'):
        raise RecoveryError(f'{archive.name}: checksum does not match its metadata')
    if metadata.get('source') != db.name:
        raise RecoveryError(f'{archive.name} is a backup of {metadata.get("source")}, not {db.name}')
    return metadata


def selector(deploy: dict) -> str:
    labels = ((deploy.get('spec') or {}).get('selector') or {}).get('matchLabels') or {}
    return ','.join(f'{k}={v}' for k, v in sorted(labels.items()))


def restore(runner: Runner, db: Database, archive: Path, age_key: Path, *, stamp: str,
            out: Callable[[str], None] = print, sleep: Callable[[float], None] = time.sleep) -> str:
    """Restore ``archive`` into ``db``'s claim; return where the replaced files were kept (app's path)."""
    metadata = checked_metadata(db, archive)
    if not os.access(age_key, os.R_OK):
        raise RecoveryError(f'cannot read the age key {age_key} (set AGE_KEY_FILE)')
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
            raise RecoveryError(f'{db.name}: the app\'s pods did not stop within 3 minutes')
        out(f'[OK] Stopped deployment/{db.app}')

        runner.run(['kubectl', 'apply', '-f', '-'], input=json.dumps(pod_manifest(db, pod)), mutating=True)
        cleanup.callback(runner.run, ['kubectl', '-n', ns, 'delete', 'pod', pod, '--ignore-not-found', '--wait=false'],
                         check=False, mutating=True)
        runner.run(['kubectl', '-n', ns, 'wait', '--for=condition=Ready', f'pod/{pod}', '--timeout=120s'])
        runner.pipe(['age', '-d', '-i', age_key, archive],
                    ['kubectl', '-n', ns, 'exec', '-i', pod, '-c', 'sqlite', '--', 'sh', '-c', 'cat > "$1"', 'sh',
                     incoming], mutating=True)
        _, digest = plain_fingerprint(runner, exec_pod, incoming)
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
            raise RecoveryError(f'{db.name}: the restored copy failed its checks ({"; ".join(problems)}); '
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


def database(runner: Runner, namespace: str) -> Database:
    found = [db for db in find(runner) if db.namespace == namespace]
    if len(found) != 1:
        raise RecoveryError(f'expected one SQLite database in {namespace}, found {len(found)}')
    return found[0]


def main(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    argv = argv or []
    if len(argv) != 2:
        print(__doc__.split('\n\n')[1], file=sys.stderr)
        return 2
    app, env = argv
    runner = runner or Runner.from_environment()
    if not runner.dry_run and os.environ.get('CONFIRM') != f'{app}/{env}':
        print(f'[ERROR] restore-sqlite replaces the live database of {app}/{env} and stops the app meanwhile. '
              f'Re-run with CONFIRM={app}/{env}', file=sys.stderr)
        return 2
    try:
        settings = RestoreSettings.from_env(os.environ)
        db = database(runner, f'{app}-{env}')
        restore(runner, db, settings.archive(db.namespace), settings.age_key,
                stamp=dt.datetime.now(dt.UTC).strftime('%Y%m%dT%H%M%SZ'))
    except (RecoveryError, CommandError, ValueError) as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 1
    return 0
