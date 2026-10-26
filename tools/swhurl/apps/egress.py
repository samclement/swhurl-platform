"""Outbound traffic: every app instance reaches outside the cluster through the egress proxy
(platform/egress, docs/services.md#egress-proxy), on a port of its own, so the proxy knows which
instance is asking without trusting anything the app sends.

The port is kept in instance.yaml as Settings.allocations.egressPort, so regenerating never
moves it: make app-sync (apps/model/write.py) gives an instance without one the lowest free port.
This module owns the instance's side — the variables that point its clients at that port, and its
grants. The proxy's own configuration is v2's (v2/kernel/proxy.go): it renders the module's ports and
grants from the allocation, with grants enforced, and this module no longer writes it.

Grants (make app-egress) are the hosts an instance may reach, kept in instance.yaml as
Settings.egress, not in the rendered HelmRelease annotation. make app-sync renders that
platform.swhurl.com/egress annotation as comma-separated ``host:port`` names; ``*.example.com`` is
any name under example.com, and example.com itself (Squid's ``.example.com``). For now the proxy still allows every host: it logs each request with the
instance's name and whether a grant covers it (``granted=yes``, else ``-``), so refusing the rest
(docs/plan.md section 21, stage 5, E3) can be judged from the log first.

Plain data, and only modules the offline policy check may import (no ruamel.yaml).
"""
from __future__ import annotations

import re
from pathlib import Path

from swhurl import interface
from swhurl.apps.contract import otlp_variables
from swhurl.apps.instance import Instance

NAMESPACE = NAME = 'egress'
DIRECTORY = Path('platform/egress')
HOST = f'{NAME}.{NAMESPACE}.svc.cluster.local'
FIRST_PORT = 30101
HEALTH_PORT = 3128  # probes only: the NetworkPolicy admits no pod to it

# The chart writes env in name order, and Kubernetes expands $(HOST_IP) only after HOST_IP is defined:
# every name here that uses it sorts after it.
VARIABLES = (interface.OTLP_HOST_IP, interface.HTTP_VARIABLE, interface.HTTPS_VARIABLE, interface.JAVA_VARIABLE, interface.NODE_VARIABLE, interface.NO_PROXY_VARIABLE)
# Inside the cluster and the node (the OTLP collector listens on the node's address): never the proxy.
DIRECT = ('localhost', '127.0.0.1', '.svc', '.cluster.local', f'$({interface.OTLP_HOST_IP})')
PORT = re.compile(rf'^http://{re.escape(HOST)}:(\d+)$')
DEFAULT_PORT = 443
# A DNS name with at least two labels and a top label of letters (no IP address), optionally *. first.
GRANT_RE = re.compile(r'^(\*\.)?(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}:(\d{1,5})$')


def address(port: int) -> str:
    return f'http://{HOST}:{port}'


def variables(port: int) -> dict:
    """What tells an instance's HTTP clients to use its proxy port: the variables Node (with
    NODE_USE_ENV_PROXY), curl and most libraries read, and the system properties the JVM's own
    clients read (JAVA_TOOL_OPTIONS: the templates' images set no options of their own there)."""
    java_direct = '|'.join(f'*{host}' if host.startswith('.') else host.replace('127.0.0.1', '127.*') for host in DIRECT)
    return {
        interface.OTLP_HOST_IP: otlp_variables('')[interface.OTLP_HOST_IP],
        interface.HTTP_VARIABLE: address(port),
        interface.HTTPS_VARIABLE: address(port),
        interface.JAVA_VARIABLE: ' '.join([f'-Dhttp.proxyHost={HOST}', f'-Dhttp.proxyPort={port}', f'-Dhttps.proxyHost={HOST}',
                                 f'-Dhttps.proxyPort={port}', f'-Dhttp.nonProxyHosts={java_direct}']),
        interface.NODE_VARIABLE: '1',
        interface.NO_PROXY_VARIABLE: ','.join(DIRECT),
    }


def grant(text: str) -> str:
    """A grant as kept: lowercase, with its port (443 when none is given)."""
    text = text.strip().lower().rstrip('.')
    return text if re.search(r':\d+$', text) else f'{text}:{DEFAULT_PORT}'


def grant_problem(text: str) -> str:
    """Why ``text`` (as ``grant`` keeps it) is not a grant, or ''."""
    found = GRANT_RE.match(text)
    if not found or not 0 < int(found[2]) < 65536:
        return f'{text}: a grant is a host name (or *.domain) with an optional port, such as api.stripe.com or *.example.com:8443'
    return ''


def granted(release: dict) -> list[str]:
    """The hosts an instance may reach, as its release's annotation names them."""
    value = str((((release or {}).get('metadata') or {}).get('annotations') or {}).get(interface.EGRESS_ANNOTATION, ''))
    return [name for name in value.split(',') if name]


def covered(names: list[str]) -> list[str]:
    """The grants a wildcard grant on the same port already covers (example.com:443 and
    api.example.com:443 under *.example.com:443): Squid refuses a list holding both."""
    return [name for name in names if any(other != name and other.startswith('*.')
                                          and (name == other[2:] or name.endswith(other[1:])) for other in names)]


def port_of(env: dict) -> int | None:
    """The instance's proxy port, from its HTTPS_PROXY variable."""
    found = PORT.match(str(env.get(interface.HTTPS_VARIABLE, '')))
    return int(found[1]) if found else None


def free_port(taken: set[int]) -> int:
    port = FIRST_PORT
    while port in taken:
        port += 1
    return port


def grant_problems(each: Instance, names: list[str]) -> list[str]:
    """Rule egress-grants: each grant well formed, once, and not inside another of the same instance."""
    found = [f'egress-grants: {each.app}/{each.env}: {problem}' for name in names if (problem := grant_problem(name))]
    found += [f'egress-grants: {each.app}/{each.env} grants {name} twice' for name in sorted(set(names)) if names.count(name) > 1]
    found += [f'egress-grants: {each.app}/{each.env} grants {name}, which a wildcard grant already covers'
              for name in sorted(set(covered(names)))]
    return found


def problems(root: Path) -> list[str]:
    """Whether every instance has a port of its own and well-formed grants (rules egress-ports and
    egress-grants). The proxy's configuration is v2's to render, so this no longer compares a file."""
    from swhurl.apps.model import store

    world, errors = store.world(root)
    if errors:
        return [f'egress-ports: {error}' for error in errors]
    try:
        platform = store.platform(root)
    except ValueError as error:  # RenderError: an unreadable platform input
        return [f'egress-ports: {error}']
    if not platform.egress:
        return []
    found = [problem for each, entry in world.items() for problem in grant_problems(each, list(entry.settings.grants.egress))]
    owners: dict[int, list[Instance]] = {}
    for each, entry in world.items():
        port = entry.settings.allocations.egress_port
        if port is None:
            found.append(f'egress-ports: {each} has no proxy port; make app-sync gives it one')
        else:
            owners.setdefault(port, []).append(each)
    for port, held in sorted(owners.items()):
        if len(held) > 1:
            found.append(f'egress-ports: port {port} is in {", ".join(str(e) for e in sorted(held, key=lambda e: e.env != "staging"))}; '
                         'remove allocations.egressPort from all but one instance.yaml and run make app-sync')
    return found
