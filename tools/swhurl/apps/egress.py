"""Outbound traffic: every app instance reaches outside the cluster through the egress proxy
(platform/egress, docs/services.md#egress-proxy), on a port of its own, so the proxy knows which
instance is asking without trusting anything the app sends.

The port is kept in instance.yaml as Settings.allocations.egressPort, so regenerating never
moves it: make app-sync (apps/model/write.py) gives an instance without one the lowest free port.
It renders the instance's proxy variables (including HTTPS_PROXY) and platform/egress/proxy.yaml:
the proxy's configuration and Service ports (values for its HelmRelease) and the NetworkPolicy
that admits each port only from its instance's namespace.

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

import yaml

from swhurl.apps.contract import OTLP_HOST_IP, otlp_variables
from swhurl.apps.instance import Instance

NAMESPACE = NAME = 'egress'
PROXY_FILE = Path('platform/egress/proxy.yaml')
HOST = f'{NAME}.{NAMESPACE}.svc.cluster.local'
FIRST_PORT = 30101
HEALTH_PORT = 3128  # probes only: the NetworkPolicy admits no pod to it

HTTPS_VARIABLE, HTTP_VARIABLE, NO_PROXY_VARIABLE = 'HTTPS_PROXY', 'HTTP_PROXY', 'NO_PROXY'
NODE_VARIABLE, JAVA_VARIABLE = 'NODE_USE_ENV_PROXY', 'JAVA_TOOL_OPTIONS'
# The chart writes env in name order, and Kubernetes expands $(HOST_IP) only after HOST_IP is defined:
# every name here that uses it sorts after it.
VARIABLES = (OTLP_HOST_IP, HTTP_VARIABLE, HTTPS_VARIABLE, JAVA_VARIABLE, NODE_VARIABLE, NO_PROXY_VARIABLE)
# Inside the cluster and the node (the OTLP collector listens on the node's address): never the proxy.
DIRECT = ('localhost', '127.0.0.1', '.svc', '.cluster.local', f'$({OTLP_HOST_IP})')
PORT = re.compile(rf'^http://{re.escape(HOST)}:(\d+)$')
ANNOTATION = 'platform.swhurl.com/egress'
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
        OTLP_HOST_IP: otlp_variables('')[OTLP_HOST_IP],
        HTTP_VARIABLE: address(port),
        HTTPS_VARIABLE: address(port),
        JAVA_VARIABLE: ' '.join([f'-Dhttp.proxyHost={HOST}', f'-Dhttp.proxyPort={port}', f'-Dhttps.proxyHost={HOST}',
                                 f'-Dhttps.proxyPort={port}', f'-Dhttp.nonProxyHosts={java_direct}']),
        NODE_VARIABLE: '1',
        NO_PROXY_VARIABLE: ','.join(DIRECT),
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
    value = str((((release or {}).get('metadata') or {}).get('annotations') or {}).get(ANNOTATION, ''))
    return [name for name in value.split(',') if name]


def covered(names: list[str]) -> list[str]:
    """The grants a wildcard grant on the same port already covers (example.com:443 and
    api.example.com:443 under *.example.com:443): Squid refuses a list holding both."""
    return [name for name in names if any(other != name and other.startswith('*.')
                                          and (name == other[2:] or name.endswith(other[1:])) for other in names)]


def port_of(env: dict) -> int | None:
    """The instance's proxy port, from its HTTPS_PROXY variable."""
    found = PORT.match(str(env.get(HTTPS_VARIABLE, '')))
    return int(found[1]) if found else None


def free_port(taken: set[int]) -> int:
    port = FIRST_PORT
    while port in taken:
        port += 1
    return port


def squid_conf(assigned: dict[Instance, int], allowed: dict[Instance, list[str]] | None = None) -> str:
    """Squid's configuration: a port per instance and, for each grant's port, a list of the hosts it
    may reach there (``dstdomain``, where ``.example.com`` is example.com and every name under it)."""
    allowed = allowed or {}
    lines = [
        '# Squid, one port per app instance; every request is logged with the instance that sent it,',
        '# and whether a grant (make app-egress) covers it.',
        'pid_filename none',
        'pinger_enable off',
        'cache deny all',
        'via off',
        'forwarded_for delete',
        'shutdown_lifetime 3 seconds',
        'logformat egress instance=%{instance}note granted=%{granted}note client=%>a method=%rm host=%>rd:%>rP '
        'status=%>Hs/%Ss bytes=%<st ms=%tr',
        f'http_port {HEALTH_PORT} name=health',
        'acl health myportname health',
        'http_access deny health',
        '# Requests only: the kubelet\'s probes connect every few seconds and send none.',
        'acl request has request',
        'access_log stdio:/dev/stdout logformat=egress request',
        'cache_log stdio:/dev/stderr',
    ]
    by_port: dict[int, dict[Instance, list[str]]] = {}
    for each, names in allowed.items():
        if each in assigned:
            for name in names:
                if name not in covered(names):
                    host, _, port = name.rpartition(':')
                    by_port.setdefault(int(port), {}).setdefault(each, []).append(host.removeprefix('*'))
    for port in sorted(by_port):
        lines.append(f'acl port-{port} port {port}')
    for each, port in sorted(assigned.items(), key=lambda item: item[1]):
        lines += [f'http_port {port} name={each.namespace}', f'acl {each.namespace} myportname {each.namespace}',
                  f'note instance {each.namespace} {each.namespace}']
        for target in sorted(by_port):
            if each in by_port[target]:
                hosts = sorted(by_port[target][each])
                lines += [f'acl {each.namespace}-{target} dstdomain -n {" ".join(hosts)}',
                          f'note granted yes {each.namespace} {each.namespace}-{target} port-{target}']
    lines += ['# Every host for now: refusing what no grant covers comes later.', 'http_access allow all']
    return '\n'.join(lines) + '\n'


class _Literal(yaml.SafeDumper):
    """Writes a string of several lines as a block (``|``), so the generated file reads as Squid's own."""


_Literal.add_representer(str, lambda dumper, text: dumper.represent_scalar(
    'tag:yaml.org,2002:str', text, style='|' if '\n' in text else None))


def dump(document: dict) -> str:
    return yaml.dump(document, Dumper=_Literal, sort_keys=False, default_flow_style=False)


def proxy_documents(assigned: dict[Instance, int], allowed: dict[Instance, list[str]] | None = None) -> list[dict]:
    """The values the proxy's HelmRelease reads (configuration and Service ports), and its NetworkPolicy."""
    values = {'configMaps': {'config': {'data': {'squid.conf': squid_conf(assigned, allowed)}}},
              'service': {'main': {'ports': {f'p{port}': {'port': port} for port in sorted(assigned.values())}}}}
    configmap = {'apiVersion': 'v1', 'kind': 'ConfigMap',
                 'metadata': {'name': 'egress-proxy', 'namespace': NAMESPACE,
                              'labels': {'reconcile.fluxcd.io/watch': 'Enabled'}},
                 'data': {'values.yaml': dump(values)}}
    policy = {'apiVersion': 'networking.k8s.io/v1', 'kind': 'NetworkPolicy',
              'metadata': {'name': 'egress-port-per-instance', 'namespace': NAMESPACE},
              'spec': {'podSelector': {'matchLabels': {'app.kubernetes.io/instance': NAME}},
                       'policyTypes': ['Ingress'],
                       'ingress': [{'from': [{'namespaceSelector': {'matchLabels': {
                                       'kubernetes.io/metadata.name': each.namespace}}}],
                                    'ports': [{'port': port}]}
                                   for each, port in sorted(assigned.items(), key=lambda item: item[1])]}}
    return [configmap, policy]


def proxy_text(documents: list[dict]) -> str:
    """The proxy's generated file."""
    return ('# Generated by make app-sync from the instances under apps/: do not edit.\n'
            '# Each port is kept in its instance\'s HTTPS_PROXY variable.\n' + '---\n'.join(dump(doc) for doc in documents))


def grant_problems(each: Instance, names: list[str]) -> list[str]:
    """Rule egress-grants: each grant well formed, once, and not inside another of the same instance."""
    found = [f'egress-grants: {each.app}/{each.env}: {problem}' for name in names if (problem := grant_problem(name))]
    found += [f'egress-grants: {each.app}/{each.env} grants {name} twice' for name in sorted(set(names)) if names.count(name) > 1]
    found += [f'egress-grants: {each.app}/{each.env} grants {name}, which a wildcard grant already covers'
              for name in sorted(set(covered(names)))]
    return found


def problems(root: Path) -> list[str]:
    """Whether every instance has a port of its own and well-formed grants, and the proxy's file is what
    they generate (rules egress-ports and egress-grants)."""
    from swhurl.apps.model import render, store

    world, errors = store.world(root)
    if errors:
        return [f'egress-ports: {error}' for error in errors]
    platform = store.platform(root)
    if not platform.egress:
        return []
    found = [problem for each, entry in world.items() for problem in grant_problems(each, list(entry.settings.egress))]
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
    text = proxy_text(render.shared(world, platform).egress_proxy)
    path = root / PROXY_FILE
    if not (path.is_file() and path.read_text() == text):
        found.append(f'egress-ports: {PROXY_FILE} is not what the instances declare; make app-sync rewrites it')
    return found
