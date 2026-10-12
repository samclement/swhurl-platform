"""A backup set: the timestamped, age-encrypted archives of one thing, each with a metadata file beside it.

    <prefix>-<UTC stamp><suffix>   the archive, for example clickstack-mongodb-20260928T120000Z.archive.gz.age
    <prefix>-<UTC stamp>.json      its metadata: when it was taken, its SHA-256 and what its producer adds

A set owns its file names, which archive is the newest (by the stamp in the name, never by file
time: a file copied back from S3 carries the time of the copy), how old that one is, the retention
rule, the checksum and the copy to S3. What produces the bytes and what consumes them (``recovery``
for MongoDB, ``sqlite_backup`` and ``sqlite_restore`` for app databases) only fills and reads archives.

Retention keeps the newest backup for each of the last N days that have backups and for each of the
last M ISO weeks that have backups. Counting only days with backups means a pause in backups never
ages out the last good copies.
"""
from __future__ import annotations

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
from swhurl.run import Runner

DEFAULT_BACKUP_DIR = Path.home() / '.local/state/swhurl-platform/backups'
STAMP = '%Y%m%dT%H%M%SZ'


class BackupError(Exception):
    pass


def stamp(now: dt.datetime | None = None) -> str:
    """The UTC time that names a backup, to the second."""
    return (now or dt.datetime.now(dt.UTC)).strftime(STAMP)


def backup_dir(env: Mapping[str, str]) -> Path:
    return Path(env.get('BACKUP_DIR') or DEFAULT_BACKUP_DIR)


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

    def archives(self) -> list[Path]:
        """The set's archives on this host, newest first."""
        if not self.directory.is_dir():
            return []
        found = [(taken, path) for path in self.directory.iterdir() if (taken := self.taken(path.name))]
        return [path for _, path in sorted(found, reverse=True)]

    def newest(self) -> Path | None:
        return next(iter(self.archives()), None)

    def age(self, now: dt.datetime, names: Iterable[str] | None = None) -> dt.timedelta | None:
        """How old the newest archive is, here or among ``names`` (a listing from elsewhere); None without one."""
        names = [path.name for path in self.archives()] if names is None else names
        times = [taken for taken in map(self.taken, names) if taken]
        return now - max(times) if times else None

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

    def keep(self, daily: int, weekly: int) -> set[Path]:
        """The retention set: the newest archive of each of the last ``daily`` backup days and ``weekly`` ISO weeks."""
        kept: set[Path] = set()
        archives = self.archives()
        for bucket, limit in ((lambda t: t.date(), daily), (lambda t: t.isocalendar()[:2], weekly)):
            seen = []
            for path in archives:  # newest first, so the first per bucket is kept
                key = bucket(self.taken(path.name))
                if key not in seen:
                    if len(seen) == limit:
                        break
                    seen.append(key)
                    kept.add(path)
        return kept

    def prune(self, daily: int, weekly: int, *, dry_run: bool = False, out: Callable[[str], None] = print) -> int:
        """Remove the archives (and their metadata) outside the retention set; return how many were pruned."""
        kept = self.keep(daily, weekly)
        removed = 0
        for path in self.archives():
            if path in kept:
                continue
            for victim in (path, self.metadata_path(path)):
                if victim.exists():
                    out(f"[INFO] {'Would prune' if dry_run else 'Pruned'} {victim.name}")
                    if not dry_run:
                        victim.unlink()
            removed += 1
        out(f'[OK] Retention: kept {len(kept)} backup(s), pruned {removed} '
            f'(newest per day for {daily} days, per ISO week for {weekly} weeks)')
        return removed

    def upload(self, runner: Runner, uri: str, out: Callable[[str], None] = print) -> list[Path]:
        """Copy every archive and metadata file missing from ``uri``; archives are already age-encrypted."""
        present = set(remote_names(runner, uri))
        local = [path for archive in sorted(self.archives()) for path in (archive, self.metadata_path(archive))
                 if path.is_file()]
        missing = [path for path in local if path.name not in present]
        for path in missing:
            runner.run(['aws', 's3', 'cp', '--only-show-errors', path, uri + path.name], mutating=True)
        out(f'[OK] Off-host: {len(missing)} file(s) uploaded to {uri}, {len(local) - len(missing)} already there')
        return missing
