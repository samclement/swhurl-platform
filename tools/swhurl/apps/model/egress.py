"""Egress: every instance reaches outside the cluster through its own port on the egress proxy; its
grants are the hosts it may reach. The proxy's configuration is generated from every instance."""
from __future__ import annotations

from swhurl import interface
from swhurl.apps import egress
from swhurl.apps.instance import ENVIRONMENTS, Instance
from swhurl.apps.model.draft import Draft, Entry, Platform, RenderError, World


def apply(draft: Draft, entry: Entry, platform: Platform) -> None:
    if not platform.egress:
        return
    port = entry.settings.allocations.egress_port
    if port is None:
        raise RenderError(f'{draft.instance} has no egress port allocated')
    draft.env.update(egress.variables(port))
    if entry.settings.grants.egress:
        draft.annotate(draft.release, interface.EGRESS_ANNOTATION, ','.join(entry.settings.grants.egress))


def proxy(world: World, platform: Platform) -> list[dict] | None:
    """The proxy's generated documents, or None where there is no proxy."""
    if not platform.egress:
        return None
    ordered = [each for env in ENVIRONMENTS for each in sorted(world, key=lambda i: i.app) if each.env == env]
    ports: dict[Instance, int] = {each: world[each].settings.allocations.egress_port for each in ordered
                                  if world[each].settings.allocations.egress_port is not None}
    grants = {each: [name for name in world[each].settings.grants.egress if not egress.grant_problem(name)] for each in ordered}
    return egress.proxy_documents(ports, grants)
