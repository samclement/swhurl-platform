"""What a declaration falls back to, and a Secret key's rule: the app contract's own constants.

They live apart from ``contract.py`` so the retained shared declaration readers do not import the
platform's deployment settings. The Go image producer owns its own source defaults; these remain for
Python's catalogue, repository creation and fixture oracle.
"""
from __future__ import annotations

import re

from swhurl import interface

DEFAULT_DATABASE_SIZE = '1Gi'
"""A new database's volume, unless the image's declaration states another."""

# What the stack templates (samclement/swhurl-app-template-*) use; swhurl.yaml's defaults.
TEMPLATE_PORT = 8080
TEMPLATE_UID = 65532


def secret_key_problem(keys: list[str]) -> str:
    """Why these secret keys cannot be used, or '' if they can."""
    bad = [k for k in keys if not re.fullmatch(r'[A-Z_][A-Z0-9_]*', k)]
    if bad:
        return f'secret keys must be environment variable names (A-Z, 0-9, _): {", ".join(bad)}'
    reserved = [k for k in keys if k in interface.RESERVED_VARIABLE_NAMES or k.startswith(interface.RESERVED_VARIABLE_PREFIXES)]
    if reserved:
        return (f'reserved for the platform: {", ".join(reserved)} (the platform sets {", ".join(interface.RESERVED_VARIABLE_NAMES)} '
                f'and names starting {", ".join(interface.RESERVED_VARIABLE_PREFIXES)}); choose another name')
    duplicates = sorted({k for k in keys if keys.count(k) > 1})
    if duplicates:
        return f'secret keys repeated: {", ".join(duplicates)}'
    return ''
