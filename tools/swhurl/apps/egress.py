"""Outbound traffic: every app instance reaches outside the cluster through the egress proxy
(platform/egress, docs/services.md#egress-proxy), on a port of its own, so the proxy knows which
instance is asking without trusting anything the app sends.

The port is kept in the instance's own HTTPS_PROXY variable, so regenerating never moves it: make
app-sync (derive.sync_env) gives an instance without one the lowest free port, and writes
platform/egress/proxy.yaml from every instance on disk: the proxy's configuration and Service ports
(values for its HelmRelease) and the NetworkPolicy that admits each port only from its instance's
namespace. For now the proxy allows every host and logs each request with the instance's name; grants
come later (docs/plan.md section 21, stage 5).

Plain data, and only modules the offline policy check may import (no ruamel.yaml).
"""
from __future__ import annotations

import re
from pathlib import Path

import yaml

from swhurl.apps import helmrelease
from swhurl.apps.contract import OTLP_HOST_IP, otlp_variables
from swhurl.apps.instance import ENVIRONMENTS, INSTANCE_ROOTS, Instance, instances

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


def port_of(env: dict) -> int | None:
    """The instance's proxy port, from its HTTPS_PROXY variable."""
    found = PORT.match(str(env.get(HTTPS_VARIABLE, '')))
    return int(found[1]) if found else None


def ports(root: Path) -> dict[Instance, int]:
    """Every instance's proxy port as it is on disk, staging's before production's."""
    found = instances(root / INSTANCE_ROOTS[0])
    taken = {}
    for each in (each for env in ENVIRONMENTS for each in found if each.env == env):
        port = port_of(helmrelease.variables(yaml.safe_load((each.folder(root) / 'helmrelease.yaml').read_text())))
        if port is not None:
            taken[each] = port
    return taken


def free_port(taken: set[int]) -> int:
    port = FIRST_PORT
    while port in taken:
        port += 1
    return port


def squid_conf(assigned: dict[Instance, int]) -> str:
    lines = [
        '# Squid, one port per app instance; every request is logged with the instance that sent it.',
        'pid_filename none',
        'pinger_enable off',
        'cache deny all',
        'via off',
        'forwarded_for delete',
        'shutdown_lifetime 3 seconds',
        'logformat egress instance=%{instance}note client=%>a method=%rm host=%>rd:%>rP status=%>Hs/%Ss '
        'bytes=%<st ms=%tr',
        'access_log stdio:/dev/stdout logformat=egress',
        'cache_log stdio:/dev/stderr',
        f'http_port {HEALTH_PORT} name=health',
        'acl health myportname health',
        'http_access deny health',
    ]
    for each, port in sorted(assigned.items(), key=lambda item: item[1]):
        lines += [f'http_port {port} name={each.namespace}', f'acl {each.namespace} myportname {each.namespace}',
                  f'note instance {each.namespace} {each.namespace}']
    lines += ['# Every host for now: grants come with make app-egress.', 'http_access allow all']
    return '\n'.join(lines) + '\n'


class _Literal(yaml.SafeDumper):
    """Writes a string of several lines as a block (``|``), so the generated file reads as Squid's own."""


_Literal.add_representer(str, lambda dumper, text: dumper.represent_scalar(
    'tag:yaml.org,2002:str', text, style='|' if '\n' in text else None))


def dump(document: dict) -> str:
    return yaml.dump(document, Dumper=_Literal, sort_keys=False, default_flow_style=False)


def proxy_documents(assigned: dict[Instance, int]) -> list[dict]:
    """The values the proxy's HelmRelease reads (configuration and Service ports), and its NetworkPolicy."""
    values = {'configMaps': {'config': {'data': {'squid.conf': squid_conf(assigned)}}},
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


def proxy_file(root: Path) -> tuple[Path, str] | None:
    """The proxy's generated file for every instance under ``root`` as it is on disk, with its path;
    None where this root has no proxy (the scratch roots of tests and template checks)."""
    if not (root / PROXY_FILE).parent.is_dir():
        return None
    body = '---\n'.join(dump(doc) for doc in proxy_documents(ports(root)))
    return root / PROXY_FILE, ('# Generated by make app-sync from the instances under apps/: do not edit.\n'
                               '# Each port is kept in its instance\'s HTTPS_PROXY variable.\n' + body)


def problems(root: Path) -> list[str]:
    """Whether every instance has a port of its own and the proxy's file is what they generate (rule egress-ports)."""
    wanted = proxy_file(root)
    if not wanted:
        return []
    found = []
    owners: dict[int, list[Instance]] = {}
    for each, port in ports(root).items():
        owners.setdefault(port, []).append(each)
    for each in instances(root / INSTANCE_ROOTS[0]):
        if not any(each in held for held in owners.values()):
            found.append(f'egress-ports: {each.app}/{each.env} has no proxy port; make app-sync gives it one')
    for port, held in sorted(owners.items()):
        if len(held) > 1:
            found.append(f'egress-ports: port {port} is in {", ".join(f"{e.app}/{e.env}" for e in held)}; '
                         f'remove {HTTPS_VARIABLE} from all but one and run make app-sync')
    path, text = wanted
    if not (path.is_file() and path.read_text() == text):
        found.append(f'egress-ports: {PROXY_FILE} is not what the instances declare; make app-sync rewrites it')
    return found
