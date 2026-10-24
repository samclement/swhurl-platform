"""The one failure type. A primitive that fails raises it and returns nothing partial."""
from __future__ import annotations

CODES = frozenset({'unknown-ref', 'git-failed', 'invalid-document', 'unsupported-version', 'unknown-node', 'usage'})


class CodegraphError(Exception):
    """A failure with a stable code; ``detail`` names the field or input, never file contents."""

    def __init__(self, code: str, detail: str):
        if code not in CODES:
            raise ValueError(f'unknown error code {code!r}')
        super().__init__(f'{code}: {detail}')
        self.code = code
        self.detail = detail
