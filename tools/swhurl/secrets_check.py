"""Check Git-managed SOPS Secrets without printing any value.

Needs the age private key (SOPS_AGE_KEY_FILE, default ./age.agekey), so it runs
locally, not in CI. Decrypts each tracked *.sops.yaml in memory and reports per key:

  ERROR  placeholder      value is still REPLACE_ME (outside tests/fixtures)
  ERROR  empty            value is empty
  WARN   double-encoded   `data` value decodes to printable base64 text that decodes
                          again: probably base64-encoded twice (the P0c bug). Use
                          stringData for human-authored values.
  ERROR  ingestion-key    CLICKSTACK_INGESTION_KEY is not in exactly the ClickStack and
                          OTel Secrets, or the two copies differ

Exit status is non-zero only for ERRORs.
"""
from __future__ import annotations

import base64
import binascii
import hashlib
import os
import sys
from pathlib import Path
from urllib.parse import urlsplit, urlunsplit

import yaml

from swhurl import ROOT, platform
from swhurl.run import CommandError, Runner


def secret_files(runner: Runner | None = None) -> list[Path]:
    out = (runner or Runner(cwd=ROOT)).output(['git', 'ls-files', '*.sops.yaml', '*.sops.yml']).split()
    return [ROOT / f for f in out if Path(f).name not in ('.sops.yaml', '.sops.yml')]


def looks_double_encoded(raw: bytes) -> bool:
    if len(raw) < 16:
        return False
    try:
        inner = base64.b64decode(raw, validate=True)
    except (binascii.Error, ValueError):
        return False
    return len(inner) > 0 and all(32 <= c < 127 for c in inner)


INGESTION_KEY = 'CLICKSTACK_INGESTION_KEY'
INGESTION_FILES = ('platform/clickstack/secret.sops.yaml', 'platform/otel/secret.sops.yaml')
NOTIFICATION_SECRET = 'platform/console/notification-secret.sops.yaml'
# The incident reviewer reads telemetry as ClickHouse's existing read-only ``app`` user: (file, key) of the
# source and of the reviewer's copy, which must stay equal when the source is rotated.
CLICKHOUSE_COPIES = (('platform/clickstack/secret.sops.yaml', 'CLICKHOUSE_APP_PASSWORD'),
                     ('platform/incident-review/secret-clickhouse.sops.yaml', 'CLICKHOUSE_PASSWORD'))
NTFY_DESTINATIONS = {  # channel: (provider Secret file, provider key, checker key)
    'failures': ('platform/alerts/secret-failures.sops.yaml', 'address', 'NTFY_FAILURES_URL'),
    'deploys': ('platform/alerts/secret-deploys.sops.yaml', 'address', 'NTFY_DEPLOYS_URL'),
}


def destination_hash(raw: bytes) -> str:
    parts = urlsplit(raw.decode())
    normalized = urlunsplit((parts.scheme, parts.netloc, parts.path, '', ''))
    return hashlib.sha256(normalized.encode()).hexdigest()


def notification_destination_problem(copies: dict[str, str]) -> str | None:
    for channel in NTFY_DESTINATIONS:
        source, checker = f'source-{channel}', f'checker-{channel}'
        if source not in copies or checker not in copies:
            return f'ntfy {channel} destination is missing from its source or checker Secret'
        if copies[source] != copies[checker]:
            return f'ntfy {channel} destination differs between platform/alerts and {NOTIFICATION_SECRET}'
    return None


def clickhouse_copy_problem(copies: dict[str, str]) -> str | None:
    """``copies`` maps each file in ``CLICKHOUSE_COPIES`` to its password's fingerprint."""
    source, copy = (path for path, _ in CLICKHOUSE_COPIES)
    if copy not in copies:
        return None  # the reviewer is not deployed in this checkout
    if source not in copies:
        return f'{CLICKHOUSE_COPIES[0][1]} is missing from {source}'
    if copies[source] != copies[copy]:
        return f'the ClickHouse app password differs between {source} and {copy}'
    return None


def ingestion_key_problem(copies: dict[str, str]) -> str | None:
    """``copies`` maps each file holding the ingestion key to its value's fingerprint."""
    if sorted(copies) != sorted(INGESTION_FILES):
        return f'{INGESTION_KEY} must be in exactly {" and ".join(INGESTION_FILES)}; found in {sorted(copies) or "none"}'
    if len(set(copies.values())) != 1:
        return f'{INGESTION_KEY} differs between {" and ".join(INGESTION_FILES)}'
    return None


def main(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    key_file = os.environ.get('SOPS_AGE_KEY_FILE') or str(ROOT / platform.AGE_KEY)
    if not Path(key_file).is_file():
        print(f'[ERROR] age key not found at {key_file}; set SOPS_AGE_KEY_FILE', file=sys.stderr)
        return 2
    runner = runner or Runner(cwd=ROOT, env={'SOPS_AGE_KEY_FILE': key_file})
    errors = warnings = 0
    ingestion: dict[str, str] = {}
    destinations: dict[str, str] = {}
    clickhouse: dict[str, str] = {}
    for path in secret_files(runner):
        rel = path.relative_to(ROOT)
        fixture = rel.parts[:2] == ('tests', 'fixtures')
        try:
            doc = yaml.safe_load(runner.output(['sops', 'decrypt', str(path)], secret_output=True))
        except CommandError:
            print(f'[ERROR] {rel}: cannot decrypt')
            errors += 1
            continue
        fields = {f: doc.get(f) or {} for f in ('data', 'stringData')}
        summary = ', '.join(f'{len(v)} {f}' for f, v in fields.items() if v)
        print(f'[OK] {rel}: {doc["metadata"]["namespace"]}/{doc["metadata"]["name"]} ({summary})')
        for field, values in fields.items():
            for key, value in values.items():
                raw = base64.b64decode(value) if field == 'data' else str(value).encode()
                if key == INGESTION_KEY and not fixture:
                    ingestion[str(rel)] = hashlib.sha256(raw).hexdigest()
                if (str(rel), key) in CLICKHOUSE_COPIES:
                    clickhouse[str(rel)] = hashlib.sha256(raw).hexdigest()
                for channel, (provider_file, provider_key, checker_key) in NTFY_DESTINATIONS.items():
                    source = str(rel) == provider_file and key == provider_key
                    checker = str(rel) == NOTIFICATION_SECRET and key == checker_key
                    if source or checker:
                        destinations[f'{"source" if source else "checker"}-{channel}'] = destination_hash(raw)
                where = f'{rel}: {field}.{key}'
                if not raw:
                    print(f'  [ERROR] {where}: empty')
                    errors += 1
                elif raw == b'REPLACE_ME' and not fixture:
                    print(f'  [ERROR] {where}: still the REPLACE_ME placeholder; set it with sops')
                    errors += 1
                elif field == 'data' and looks_double_encoded(raw):
                    print(f'  [WARN] {where}: decodes to base64 text; probably encoded twice')
                    warnings += 1
    problem = ingestion_key_problem(ingestion)
    if problem:
        print(f'[ERROR] {problem}')
        errors += 1
    else:
        print(f'[OK] {INGESTION_KEY} is identical in {" and ".join(INGESTION_FILES)}')
    problem = notification_destination_problem(destinations)
    if problem:
        print(f'[ERROR] {problem}')
        errors += 1
    else:
        print('[OK] ntfy destinations match the notification checker copies (compared by hashes)')
    problem = clickhouse_copy_problem(clickhouse)
    if problem:
        print(f'[ERROR] {problem}')
        errors += 1
    elif CLICKHOUSE_COPIES[1][0] in clickhouse:
        print('[OK] the incident reviewer\'s ClickHouse password matches its source (compared by hashes)')
    print(f'\n{errors} error(s), {warnings} warning(s).')
    return 1 if errors else 0
