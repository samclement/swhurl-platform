"""Outbound traffic: every app instance reaches outside the cluster through the egress proxy
(platform/egress, docs/services.md#egress-proxy), on a port of its own, so the proxy knows which
instance is asking without trusting anything the app sends.

The port is kept in instance.yaml as Settings.allocations.egressPort, so regenerating never
moves it: make app-sync (apps/model/write.py) gives an instance without one the lowest free port.
This module owns the instance's side — the variables that point its clients at that port, and its
grants. The proxy's own configuration is v2's (v2/kernel/proxy.go): it renders the module's ports and
grants from the allocation, with grants enforced, and this module no longer writes it.

Grants are the hosts an instance may reach, and they are v2's now: its records render the allocation, the
annotation the release carries (``platform.swhurl.com/egress``, comma-separated ``host:port`` names) and the
proxy's configuration, which enforces them. What stays here is the instance's side — the variables that point
its clients at its port — and the reader of the annotation (``granted``), which is what the console shows.

Plain data, and only modules the offline policy check may import (no ruamel.yaml).
"""
from __future__ import annotations

import re
from pathlib import Path

from swhurl import interface
from swhurl.apps.contract import otlp_variables

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


def granted(release: dict) -> list[str]:
    """The hosts an instance may reach, as its release's annotation names them."""
    value = str((((release or {}).get('metadata') or {}).get('annotations') or {}).get(interface.EGRESS_ANNOTATION, ''))
    return [name for name in value.split(',') if name]


def port_of(env: dict) -> int | None:
    """The instance's proxy port, from its HTTPS_PROXY variable."""
    found = PORT.match(str(env.get(interface.HTTPS_VARIABLE, '')))
    return int(found[1]) if found else None


def free_port(taken: set[int]) -> int:
    port = FIRST_PORT
    while port in taken:
        port += 1
    return port


