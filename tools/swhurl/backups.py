"""Prune ClickStack MongoDB backups to a daily + weekly retention set.

Keeps the newest backup for each of the last N days that have backups and for
each of the last M ISO weeks that have backups. Counting only days with backups
means a pause in (manual) backups never ages out the last good copies.
Removes each pruned archive together with its metadata file.
"""
from __future__ import annotations

import argparse
import datetime as dt
import re
from pathlib import Path

PATTERN = re.compile(r'^clickstack-mongodb-(\d{8}T\d{6}Z)\.archive\.gz\.age$')


def backups(directory: Path) -> list[tuple[dt.datetime, Path]]:
    found = []
    for path in directory.iterdir():
        match = PATTERN.match(path.name)
        if match:
            found.append((dt.datetime.strptime(match.group(1), '%Y%m%dT%H%M%SZ'), path))
    return sorted(found, reverse=True)


def keep_set(items: list[tuple[dt.datetime, Path]], daily: int, weekly: int) -> set[Path]:
    keep: set[Path] = set()
    for bucket, limit in ((lambda t: t.date(), daily), (lambda t: t.isocalendar()[:2], weekly)):
        seen = []
        for stamp, path in items:  # newest first, so the first per bucket is kept
            key = bucket(stamp)
            if key not in seen:
                if len(seen) == limit:
                    break
                seen.append(key)
                keep.add(path)
    return keep


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('directory', type=Path)
    parser.add_argument('--daily', type=int, default=7)
    parser.add_argument('--weekly', type=int, default=4)
    parser.add_argument('--dry-run', action='store_true')
    args = parser.parse_args(argv)

    items = backups(args.directory)
    keep = keep_set(items, args.daily, args.weekly)
    removed = 0
    for _, path in items:
        if path in keep:
            continue
        for victim in (path, path.with_name(path.name.replace('.archive.gz.age', '.json'))):
            if victim.exists():
                print(f"[INFO] {'Would prune' if args.dry_run else 'Pruned'} {victim.name}")
                if not args.dry_run:
                    victim.unlink()
        removed += 1
    print(f'[OK] Retention: kept {len(keep)} backup(s), pruned {removed} '
          f'(newest per day for {args.daily} days, per ISO week for {args.weekly} weeks)')
    return 0
