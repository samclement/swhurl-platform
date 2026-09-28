#!/usr/bin/env python3
"""Check Git-managed SOPS Secrets without printing any value.

Needs the age private key (SOPS_AGE_KEY_FILE, default ./age.agekey), so it runs
locally, not in CI. Decrypts each tracked *.sops.yaml in memory and reports per key:

  ERROR  placeholder      value is still REPLACE_ME (outside tests/fixtures)
  ERROR  empty            value is empty
  WARN   double-encoded   `data` value decodes to printable base64 text that decodes
                          again: probably base64-encoded twice (the P0c bug). Use
                          stringData for human-authored values.
  WARN   keys-match       CLICKSTACK_API_KEY equals HYPERDX_API_KEY (allowed during
                          bootstrap; steady state is separate keys)

Exit status is non-zero only for ERRORs.
"""
from __future__ import annotations

import base64
import binascii
import hashlib
import os
import subprocess
import sys
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[1]


def secret_files() -> list[Path]:
    out = subprocess.run(['git', 'ls-files', '*.sops.yaml', '*.sops.yml'], cwd=ROOT,
                         check=True, capture_output=True, text=True).stdout.split()
    return [ROOT / f for f in out if Path(f).name not in ('.sops.yaml', '.sops.yml')]


def looks_double_encoded(raw: bytes) -> bool:
    if len(raw) < 16:
        return False
    try:
        inner = base64.b64decode(raw, validate=True)
    except (binascii.Error, ValueError):
        return False
    return len(inner) > 0 and all(32 <= c < 127 for c in inner)


def main() -> int:
    key_file = os.environ.get('SOPS_AGE_KEY_FILE') or str(ROOT / 'age.agekey')
    if not Path(key_file).is_file():
        print(f'[ERROR] age key not found at {key_file}; set SOPS_AGE_KEY_FILE', file=sys.stderr)
        return 2
    env = dict(os.environ, SOPS_AGE_KEY_FILE=key_file)
    errors = warnings = 0
    fingerprints: dict[str, str] = {}
    for path in secret_files():
        rel = path.relative_to(ROOT)
        fixture = rel.parts[:2] == ('tests', 'fixtures')
        try:
            doc = yaml.safe_load(subprocess.run(['sops', 'decrypt', str(path)], env=env, check=True,
                                                capture_output=True, text=True).stdout)
        except subprocess.CalledProcessError:
            print(f'[ERROR] {rel}: cannot decrypt')
            errors += 1
            continue
        fields = {f: doc.get(f) or {} for f in ('data', 'stringData')}
        summary = ', '.join(f'{len(v)} {f}' for f, v in fields.items() if v)
        print(f'[OK] {rel}: {doc["metadata"]["namespace"]}/{doc["metadata"]["name"]} ({summary})')
        for field, values in fields.items():
            for key, value in values.items():
                raw = base64.b64decode(value) if field == 'data' else str(value).encode()
                fingerprints[key] = hashlib.sha256(raw).hexdigest()
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
    if fingerprints.get('CLICKSTACK_API_KEY') and \
            fingerprints.get('CLICKSTACK_API_KEY') == fingerprints.get('HYPERDX_API_KEY'):
        print('[WARN] CLICKSTACK_API_KEY equals HYPERDX_API_KEY; expected only during bootstrap')
        warnings += 1
    print(f'\n{errors} error(s), {warnings} warning(s).')
    return 1 if errors else 0


if __name__ == '__main__':
    sys.exit(main())
