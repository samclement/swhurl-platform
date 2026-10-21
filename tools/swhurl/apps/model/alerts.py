"""An app's own alert settings, the same on every environment's namespace."""
from __future__ import annotations

from swhurl import interface
from swhurl.apps.model.draft import Draft, Entry


def apply(draft: Draft, entry: Entry) -> None:
    if entry.app.alerts:
        draft.annotate(draft.namespace, interface.ALERTS_ANNOTATION, entry.app.alerts)
