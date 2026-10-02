"""``check-otel``: validate each OTel collector's rendered config with the collector it will run.

For every HelmRelease in ``platform/otel``: render the chart with our values
(as Flux would), take the collector config and the image tag from the output,
and run that exact ``otelcol-k8s`` release's ``validate`` on it. A config the
collector would reject (for example a pipeline naming a component the chart
no longer defines) fails the check. Component names the collector knows only
as deprecated aliases (``kubeletstats`` for ``kubelet_stats``) are warnings.

The binary is downloaded once per version from the collector's GitHub
releases, checked against its published SHA-256 and cached. Pod-only inputs
are stubbed for validation: the host filesystem mount (``root_path``) points
at an empty directory, a receiver authenticating with the pod's service
account (``auth_type: serviceAccount``, whose token and CA exist only in the
pod) validates with ``auth_type: none``, and each environment variable the pod declares is set
to its literal value, or a placeholder where the cluster fills it in (``valueFrom``).
"""
from __future__ import annotations

import hashlib
import os
import tempfile
from pathlib import Path

import yaml

from swhurl import ROOT, logs
from swhurl.apps import policy
from swhurl.report import Report
from swhurl.run import CommandError, Runner

COLLECTOR = 'otelcol-k8s'
RELEASES = 'https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download'
CACHE = Path(os.environ.get('XDG_CACHE_HOME', Path.home() / '.cache')) / 'swhurl-platform/otelcol'
OTEL = Path('platform/otel')
KINDS = ('receivers', 'processors', 'exporters', 'extensions', 'connectors')
PLACEHOLDER = '127.0.0.1'  # for valueFrom (pod IP, node name, Secrets): valid as an IP, harmless as a name


class OtelError(Exception):
    pass


def collector(runner: Runner, version: str, cache: Path = CACHE) -> Path:
    """The ``otelcol-k8s`` binary for ``version``, downloaded and checksum-verified once."""
    binary = cache / version / COLLECTOR
    if binary.is_file():
        return binary
    binary.parent.mkdir(parents=True, exist_ok=True)
    name = f'{COLLECTOR}_{version}_linux_amd64.tar.gz'
    url = f'{RELEASES}/v{version}/{name}'
    archive = binary.parent / name
    try:
        expected = runner.output(['curl', '--fail', '--silent', '--show-error', '--location', f'{url}.sha256']).split()[0]
        runner.run(['curl', '--fail', '--silent', '--show-error', '--location', '--output', str(archive), url])
        actual = hashlib.sha256(archive.read_bytes()).hexdigest()
        if actual != expected:
            raise OtelError(f'{name}: SHA-256 {actual} does not match the published {expected}')
        runner.run(['tar', '-xzf', str(archive), '-C', str(binary.parent), COLLECTOR])
    finally:
        archive.unlink(missing_ok=True)
    return binary


def render(runner: Runner, release: dict) -> tuple[dict, dict[str, str], str]:
    """``(collector config, the pod's environment for validation, image tag)`` for one HelmRelease."""
    spec = release['spec']['chart']['spec']
    chart = policy.chart_dir(spec['chart'], spec['version'], policy.helm_repositories()[spec['sourceRef']['name']], runner)
    with tempfile.NamedTemporaryFile('w', suffix='.yaml') as values:
        yaml.safe_dump(release['spec'].get('values', {}), values)
        values.flush()
        out = runner.output(['helm', 'template', release['metadata']['name'], str(chart),
                             '-n', release['metadata']['namespace'], '-f', values.name])
    docs = [d for d in yaml.safe_load_all(out) if d]
    config_map = next((d for d in docs if d['kind'] == 'ConfigMap' and 'relay' in (d.get('data') or {})), None)
    workload = next((d for d in docs if d['kind'] in ('DaemonSet', 'Deployment', 'StatefulSet')), None)
    if config_map is None or workload is None:
        raise OtelError('the chart rendered no collector ConfigMap (relay) or workload')
    container = workload['spec']['template']['spec']['containers'][0]
    env = {e['name']: str(e['value']) if 'value' in e else PLACEHOLDER for e in container.get('env') or []}
    return (yaml.safe_load(config_map['data']['relay']), env,
            container['image'].rpartition(':')[2])


def canonical_names(runner: Runner, binary: Path) -> dict[str, set[str]]:
    listed = yaml.safe_load(runner.output([str(binary), 'components']))
    return {kind: {c['name'] for c in listed.get(kind) or []} for kind in KINDS}


def deprecated_names(config: dict, known: dict[str, set[str]]) -> list[str]:
    """Configured component types the collector does not list: it accepts them only as aliases."""
    found = []
    for kind in KINDS:
        for component in config.get(kind) or {}:
            kind_name = component.split('/', 1)[0]
            if kind_name not in known[kind]:
                hint = next((n for n in sorted(known[kind]) if n.replace('_', '') == kind_name.replace('_', '')), None)
                found.append(f'{kind} {kind_name}' + (f' (now {hint})' if hint else ''))
    return found


def validate(runner: Runner, binary: Path, config: dict, env: dict[str, str],
             extra_args: list[str] | None = None) -> str:
    """The collector's own verdict; '' if valid, otherwise its error message."""
    with tempfile.TemporaryDirectory() as tmp:
        for receiver in (config.get('receivers') or {}).values():
            if not isinstance(receiver, dict):
                continue
            if receiver.get('root_path'):
                receiver['root_path'] = tmp  # the pod mounts the host here; any existing directory validates
            if receiver.get('auth_type') == 'serviceAccount':
                receiver['auth_type'] = 'none'  # the service-account token and CA exist only in the pod
        path = Path(tmp) / 'config.yaml'
        path.write_text(yaml.safe_dump(config))
        gates = [arg for arg in extra_args or [] if arg.startswith('--feature-gates=')]
        result = runner.run([str(binary), 'validate', *gates, f'--config=file:{path}'], check=False,
                            env=env)
    return '' if result.returncode == 0 else (result.stderr or result.stdout).strip()


def check(runner: Runner, report: Report, root: Path = ROOT) -> int:
    for path in sorted((root / OTEL).glob('helmrelease-*.yaml')):
        release = yaml.safe_load(path.read_text())
        name = release['metadata']['name']
        report.section(f'OTel collector {name}')
        try:
            config, env, version = render(runner, release)
            binary = collector(runner, version)
            extra_args = release['spec'].get('values', {}).get('command', {}).get('extraArgs', [])
            error = validate(runner, binary, config, env, extra_args)
            if error:
                report.bad(f'{COLLECTOR} {version} rejects the rendered config: {error}')
                continue
            report.ok(f'{COLLECTOR} {version} accepts the rendered config ({path.relative_to(root)})')
            if any(name.startswith('transform/') for name in config.get('processors', {})):
                logs.check_fixtures(runner, binary, config, extra_args, report)
            for deprecated in deprecated_names(config, canonical_names(runner, binary)):
                report.warn(f'deprecated component name: {deprecated}')
        except (CommandError, OtelError, KeyError, StopIteration) as error:
            report.bad(f'could not validate {path.relative_to(root)}: {error}')
    return report.exit_code()


def main(argv: list[str] | None = None, runner: Runner | None = None, report: Report | None = None) -> int:
    return check(runner or Runner(), report or Report())
