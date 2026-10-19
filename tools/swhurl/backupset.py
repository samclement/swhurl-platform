"""A backup set: the timestamped, age-encrypted archives of one thing, each with a metadata file beside it.

    <prefix>-<UTC stamp><suffix>   the archive, for example clickstack-mongodb-20260928T120000Z.archive.gz.age
    <prefix>-<UTC stamp>.json      its metadata: when it was taken, its SHA-256 and what its producer adds

A set owns its file names, which archive is the newest (by the stamp in the name), how old that one
is, the checksum, publishing to S3 and fetching back from it. A backup counts only when its metadata
is beside it: a job publishes the metadata last. The backup jobs (``backup_job``) fill archives in a
private scratch directory; the restores (``recovery`` for MongoDB, ``sqlite_restore`` for app
databases) fetch one from S3 into a private directory of their own. Retention is the bucket's
lifecycle rule (docs/operations.md#backups-and-recovery).
"""
from __future__ import annotations

import base64
import contextlib
import datetime as dt
import hashlib
import json
import os
import re
from collections.abc import Callable, Iterable, Iterator, Mapping
from dataclasses import dataclass
from pathlib import Path

from swhurl import ROOT, platform
from swhurl.report import Report
from swhurl.run import CommandError, Runner

STAMP = '%Y%m%dT%H%M%SZ'


class BackupError(Exception):
    pass


def stamp(now: dt.datetime | None = None) -> str:
    """The UTC time that names a backup, to the second."""
    return (now or dt.datetime.now(dt.UTC)).strftime(STAMP)


def age_key(env: Mapping[str, str]) -> Path:
    """The age private key a restore decrypts with."""
    return Path(env.get('AGE_KEY_FILE') or env.get('SOPS_AGE_KEY_FILE') or ROOT / platform.AGE_KEY)


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open('rb') as handle:
        for block in iter(lambda: handle.read(1 << 20), b''):
            digest.update(block)
    return digest.hexdigest()


@contextlib.contextmanager
def private_files() -> Iterator[None]:
    """Create files and directories readable by the owner only (umask 077)."""
    previous = os.umask(0o077)
    try:
        yield
    finally:
        os.umask(previous)


def s3_location(uri: str) -> tuple[str, str]:
    """``s3://bucket/prefix/`` as ``(bucket, 'prefix/')``."""
    bucket, _, prefix = uri.removeprefix('s3://').partition('/')
    if not uri.startswith('s3://') or not bucket or (prefix and not prefix.endswith('/')):
        raise BackupError(f'BACKUP_S3_URI must look like s3://bucket/prefix/ (got {uri!r})')
    return bucket, prefix


def remote_names(runner: Runner, uri: str) -> list[str]:
    """File names of the backups already under ``uri``."""
    bucket, prefix = s3_location(uri)
    keys = runner.json(['aws', 's3api', 'list-objects-v2', '--bucket', bucket, '--prefix', prefix,
                        '--query', 'Contents[].Key', '--output', 'json']) or []
    return [key.removeprefix(prefix) for key in keys]


@dataclass(frozen=True)
class BackupSet:
    directory: Path
    prefix: str  # 'clickstack-mongodb'
    suffix: str  # '.archive.gz.age'

    def path(self, stamp: str) -> Path:
        return self.directory / f'{self.prefix}-{stamp}{self.suffix}'

    def metadata_path(self, archive: Path) -> Path:
        return archive.with_name(archive.name.removesuffix(self.suffix) + '.json')

    def taken(self, name: str) -> dt.datetime | None:
        """When the archive with this file name was taken; None for any other file."""
        match = re.fullmatch(rf'{re.escape(self.prefix)}-(\d{{8}}T\d{{6}}Z){re.escape(self.suffix)}', name)
        return dt.datetime.strptime(match[1], STAMP).replace(tzinfo=dt.UTC) if match else None

    def newest(self, names: Iterable[str]) -> str | None:
        """The newest complete backup's archive name among ``names`` (an archive with its metadata, which is
        published last); None without one."""
        present = set(names)
        complete = [(taken, name) for name in present if (taken := self.taken(name))
                    and self.metadata_path(self.directory / name).name in present]
        return max(complete)[1] if complete else None

    def age(self, now: dt.datetime, names: Iterable[str]) -> dt.timedelta | None:
        """How old the newest complete backup among ``names`` is; None without one."""
        newest = self.newest(names)
        return now - self.taken(newest) if newest else None

    def fetch(self, runner: Runner, uri: str) -> Path:
        """Download the newest complete backup under ``uri`` and its metadata into this set's directory
        (private); return the archive. Reads with the AWS CLI's own credentials."""
        name = self.newest(remote_names(runner, uri))
        if name is None:
            raise BackupError(f'no complete backup in {uri}')
        archive = self.directory / name
        with private_files():
            self.directory.mkdir(parents=True, exist_ok=True)
            for path in (archive, self.metadata_path(archive)):
                runner.run(['aws', 's3', 'cp', '--only-show-errors', uri + path.name, path])
        return archive

    @contextlib.contextmanager
    def writing(self, stamp: str) -> Iterator[Path]:
        """A private ``.partial`` file to fill, which becomes the archive when the block ends without an error."""
        archive = self.path(stamp)
        partial = archive.with_name(archive.name + '.partial')
        with private_files():
            self.directory.mkdir(parents=True, exist_ok=True)
            try:
                yield partial
                partial.replace(archive)
            finally:
                partial.unlink(missing_ok=True)

    def record(self, archive: Path, facts: Mapping[str, object]) -> Path:
        """Write the archive's metadata: its stamp, the producer's ``facts`` and its SHA-256."""
        taken = self.taken(archive.name)
        if taken is None:
            raise BackupError(f'{archive.name} is not named like a backup in {self.directory}')
        metadata = self.metadata_path(archive)
        with private_files():
            metadata.write_text(json.dumps({'created': stamp(taken), **facts, 'sha256': sha256(archive)}) + '\n')
        return metadata

    def read(self, archive: Path) -> dict:
        """An archive's metadata, after checking the archive is the file the metadata describes."""
        if not archive.is_file():
            raise BackupError(f'{archive} does not exist')
        metadata_path = self.metadata_path(archive)
        if not metadata_path.is_file():
            raise BackupError(f'missing backup metadata: {metadata_path}')
        metadata = json.loads(metadata_path.read_text())
        if sha256(archive) != metadata.get('sha256'):
            raise BackupError(f'{archive.name}: checksum does not match its metadata')
        return metadata


def publish(runner: Runner, backups: BackupSet, archive: Path, uri: str, out: Callable[[str], None] = print) -> None:
    """Upload one archive, then its metadata: a backup counts only once its metadata is there. S3 checks
    each file's SHA-256 as it arrives (an upload-only key cannot read back, so this is the verification)
    and refuses to replace an object that exists."""
    bucket, prefix = s3_location(uri)
    for path in (archive, backups.metadata_path(archive)):
        checksum = base64.b64encode(bytes.fromhex(sha256(path))).decode()
        runner.run(['aws', 's3api', 'put-object', '--bucket', bucket, '--key', prefix + path.name, '--body', path,
                    '--checksum-algorithm', 'SHA256', '--checksum-sha256', checksum, '--if-none-match', '*',
                    '--output', 'json'], mutating=True)
    out(f'[OK] Published {archive.name} and its metadata to {uri}')


def check_backup_set(runner: Runner, report: Report, backups: BackupSet, uri: str, *, env: Mapping[str, str],
                     now: dt.datetime | None, what: str, hint: str, run: str, prefix: str = '') -> None:
    """The set's newest complete backup under ``uri`` (S3) must be younger than BACKUP_MAX_AGE_HOURS (26)."""
    now = now or dt.datetime.now(dt.UTC)
    limit = dt.timedelta(hours=float(env.get('BACKUP_MAX_AGE_HOURS') or 26))
    if not uri:
        report.bad(f'{prefix}no S3 location to check {what} backups in (BACKUP_S3_URI, SQLITE_S3_URI)')
        return
    try:
        age = backups.age(now, remote_names(runner, uri))
    except (CommandError, BackupError, TypeError) as error:
        report.bad(f'{prefix}cannot list backups in {uri}: {error}')
        return
    if age is None:
        report.bad(f'{prefix}no {what} backup in {uri} (run: {run})')
    elif age > limit:
        report.bad(f'{prefix}newest {what} backup in {uri} is {age.total_seconds() / 3600:.0f} h old; check: {hint}')
    else:
        report.ok(f'{prefix}newest {what} backup in {uri} is {age.total_seconds() / 3600:.1f} h old')
