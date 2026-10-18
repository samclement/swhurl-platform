"""Telemetry: an image with an OpenTelemetry SDK sends to the collector on its own node."""
from __future__ import annotations

from swhurl.apps.contract import otlp_variables
from swhurl.apps.model.draft import Draft, Entry


def apply(draft: Draft, entry: Entry) -> None:
    if entry.release.telemetry:
        draft.env.update(otlp_variables(draft.instance.app))
