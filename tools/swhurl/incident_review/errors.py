"""Errors shared by the incident review modules."""
from __future__ import annotations

# The only words a reviewer failure may carry into a log line or notification.
REASONS = frozenset({'query', 'redaction', 'state-unavailable', 'state-corrupt', 'state-size', 'contract',
                     'provider', 'timeout', 'schema', 'delivery'})


class PolicyError(ValueError):
    """Input is malformed or violates the static review policy."""


class ReviewFailure(Exception):
    """The run cannot continue; ``reason`` is one of ``REASONS`` and is safe to publish."""

    def __init__(self, reason: str):
        if reason not in REASONS:
            raise ValueError('unknown incident review failure reason')
        super().__init__(reason)
        self.reason = reason
