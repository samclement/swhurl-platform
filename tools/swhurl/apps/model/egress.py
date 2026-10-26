"""Egress: every instance reaches outside the cluster through its own port on the egress proxy; its
grants are the hosts it may reach. The instance's side is all this renders: v2 renders the proxy from
the allocation (v2/kernel/proxy.go)."""
from __future__ import annotations

from swhurl import interface
from swhurl.apps import egress
from swhurl.apps.model.draft import Draft, Entry, Platform, RenderError


def apply(draft: Draft, entry: Entry, platform: Platform) -> None:
    if not platform.egress:
        return
    port = entry.settings.allocations.egress_port
    if port is None:
        raise RenderError(f'{draft.instance} has no egress port allocated')
    draft.env.update(egress.variables(port))
    if entry.settings.grants.egress:
        draft.annotate(draft.release, interface.EGRESS_ANNOTATION, ','.join(entry.settings.grants.egress))
