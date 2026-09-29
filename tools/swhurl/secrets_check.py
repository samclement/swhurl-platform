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
    print(f'\n{errors} error(s), {warnings} warning(s).')
    return 1 if errors else 0
