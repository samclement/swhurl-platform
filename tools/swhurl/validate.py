"""Validate active Git-managed entrypoints without contacting the cluster.

Checks documentation links, shell syntax, SOPS Secret structure, and renders
every active Flux path with its substitutions before validating the result
against Kubernetes and Flux schemas. Every check runs even if an earlier one
fails, so one run reports every problem.
"""
from __future__ import annotations

import re
from pathlib import Path

import yaml

from swhurl import ROOT, platform
from swhurl.report import Report
from swhurl.run import CommandError, Runner

MANIFEST_ROOTS = ('clusters', 'infrastructure', 'platform', 'platform-services', 'tenants')
TOKEN = re.compile(r'(?<!\$)\$\{([^}]+)\}')
YAML_SUFFIXES = {'.yaml', '.yml'}
LINK = re.compile(r'\[[^\]]*\]\(([^)\s]+)\)')


class ValidationError(ValueError):
    pass


def fail(message: str) -> None:
    raise ValidationError(message)


def documents(path: Path, root: Path = ROOT) -> list[dict]:
    try:
        parsed = list(yaml.safe_load_all(path.read_text()))
    except yaml.YAMLError as exc:
        fail(f'{path.relative_to(root)}: invalid YAML: {exc}')
    if not parsed or any(not isinstance(item, dict) for item in parsed):
        fail(f'{path.relative_to(root)}: expected Kubernetes YAML documents')
    return parsed


def output(runner: Runner, args: list[str], *, input_text: str | None = None) -> str:
    try:
        return runner.output(args, input=input_text)
    except CommandError as exc:
        fail(str(exc))


def active_paths(root: Path = ROOT) -> dict[Path, dict]:
    """Every local path a Flux unit renders, mapped to that unit's spec."""
    paths: dict[Path, dict] = {root / name: {} for name in platform.BOOTSTRAP_PATHS}
    for definition, item in platform.flux_unit_documents(root):
        spec = item.get('spec') or {}
        source_path = spec.get('path')
        if not isinstance(source_path, str) or not source_path.startswith('./'):
            fail(f'{definition.relative_to(root)}: Flux Kustomization needs a local spec.path')
        path = (root / source_path).resolve()
        if not path.is_relative_to(root):
            fail(f'{definition.relative_to(root)}: spec.path escapes the repository')
        if path in paths and paths[path]:
            fail(f'{definition.relative_to(root)}: duplicate Flux render path {source_path}')
        paths[path] = spec
    for path in paths:
        if not path.is_dir() or not any((path / n).is_file() for n in ('kustomization.yaml', 'kustomization.yml')):
            fail(f'missing active Kustomize path: {path.relative_to(root)}')
    return paths


def platform_settings(root: Path = ROOT) -> dict[str, str]:
    path = root / platform.SETTINGS
    item = documents(path, root)[0]
    if item.get('kind') != 'ConfigMap' or item.get('metadata', {}).get('name') != 'platform-settings':
        fail(f'{platform.SETTINGS}: expected platform-settings ConfigMap')
    data = item.get('data')
    if not isinstance(data, dict) or any(not isinstance(value, str) for value in data.values()):
        fail(f'{platform.SETTINGS}: settings data must contain strings')
    return data


def substitutions(spec: dict, settings: dict[str, str]) -> dict[str, str]:
    sources = (spec.get('postBuild') or {}).get('substituteFrom') or []
    if not sources:
        return {}
    if sources != [{'kind': 'ConfigMap', 'name': 'platform-settings', 'optional': False}]:
        fail('validation needs an explicit mapping for new Flux substitution sources')
    return settings


def check_secrets(report: Report, root: Path = ROOT) -> None:
    count = 0
    for root_name in MANIFEST_ROOTS:
        for path in sorted(c for c in (root / root_name).rglob('*') if c.suffix in YAML_SUFFIXES):
            items = documents(path, root)
            label = path.relative_to(root)
            for item in items:
                if item.get('kind') != 'Secret':
                    continue
                count += 1
                if not path.name.endswith(('.sops.yaml', '.sops.yml')):
                    fail(f'{label}: Git-managed Secret must be a .sops.yaml or .sops.yml file')
                sops = item.get('sops')
                if not isinstance(sops, dict) or not sops.get('age') or not sops.get('mac'):
                    fail(f'{label}: missing SOPS age metadata')
                if sops.get('encrypted_regex') != '^(data|stringData)$':
                    fail(f'{label}: unexpected SOPS encrypted_regex')
                payloads = [item[key] for key in ('data', 'stringData') if key in item]
                if not payloads or any(not isinstance(p, dict) or not p for p in payloads):
                    fail(f'{label}: Secret needs nonempty encrypted data or stringData')
                for payload in payloads:
                    if any(not isinstance(v, str) or not v.startswith('ENC[') for v in payload.values()):
                        fail(f'{label}: Secret contains an unencrypted value')
            if path.name.endswith(('.sops.yaml', '.sops.yml')) and all(i.get('kind') != 'Secret' for i in items):
                fail(f'{label}: expected an encrypted Secret')
    report.ok(f'{count} Git-managed Secrets use SOPS age encryption')


def check_shell_syntax(runner: Runner, report: Report) -> None:
    names = output(runner, ['git', 'ls-files', '--cached', '--others', '--exclude-standard', '*.sh'])
    files = sorted(set(names.splitlines()))
    for name in files:
        output(runner, ['bash', '-n', name])
    report.ok(f'{len(files)} shell files pass individual syntax checks')


def validate_render(runner: Runner, report: Report, path: Path, spec: dict, settings: dict[str, str],
                    root: Path = ROOT) -> None:
    label = path.relative_to(root)
    rendered = output(runner, ['kubectl', 'kustomize', str(label)])
    values = substitutions(spec, settings)

    def replace_token(match: re.Match[str]) -> str:
        key = match.group(1)
        if key not in values:
            fail(f'{label}: unresolved Flux substitution ${{{key}}}')
        return values[key]

    substituted = TOKEN.sub(replace_token, rendered)
    for item in yaml.safe_load_all(substituted):
        if not isinstance(item, dict) or not all(key in item for key in ('apiVersion', 'kind', 'metadata')):
            fail(f'{label}: rendered document lacks apiVersion, kind or metadata')
    output(runner, ['flux-schema', 'validate', '--schema-location', 'default', '--schema-location', 'ecosystem',
                    '--skip-json-path', '/sops'], input_text=substituted)
    report.ok(f'rendered and schema-validated {label}')


def anchors(markdown: Path) -> set[str]:
    """GitHub-style heading anchors for a Markdown file."""
    found = set()
    for line in markdown.read_text().splitlines():
        if line.startswith('#'):
            text = line.lstrip('#').strip().lower()
            found.add(re.sub(r'[^\w\- ]', '', text).replace(' ', '-'))
    return found


def check_doc_links(runner: Runner, report: Report, root: Path = ROOT) -> None:
    """Fail on relative Markdown links whose file or heading anchor does not exist."""
    tracked = output(runner, ['git', 'ls-files', '*.md']).split()
    untracked = output(runner, ['git', 'ls-files', '--others', '--exclude-standard', '*.md']).split()
    broken, checked = [], 0
    for name in sorted(set(tracked + untracked)):
        source = root / name
        if not source.exists() or name.startswith('.claude/'):
            continue
        for target in LINK.findall(source.read_text()):
            if re.match(r'^[a-z][a-z0-9+.-]*:', target):
                continue
            path_part, _, anchor = target.partition('#')
            resolved = (source.parent / path_part).resolve() if path_part else source
            checked += 1
            if not resolved.exists():
                broken.append(f'{name}: {target} (missing file)')
            elif anchor and resolved.suffix == '.md' and anchor not in anchors(resolved):
                broken.append(f'{name}: {target} (missing heading)')
    if broken:
        fail('broken documentation links:\n  ' + '\n  '.join(broken))
    report.ok(f'{checked} relative documentation links resolve')


def validate(runner: Runner, report: Report, root: Path = ROOT) -> int:
    """Run every check, recording failures instead of stopping at the first."""
    def attempt(check, *args):
        try:
            return check(*args)
        except ValidationError as exc:
            report.bad(str(exc))
            return None

    attempt(check_doc_links, runner, report, root)
    attempt(check_shell_syntax, runner, report)
    attempt(check_secrets, report, root)
    settings = attempt(platform_settings, root)
    paths = attempt(active_paths, root)
    if settings is not None and paths is not None:
        for path, spec in paths.items():
            attempt(validate_render, runner, report, path, spec, settings, root)
    if report.passed:
        report.line(f'Validation passed for {len(paths)} active render entrypoints.')
    else:
        report.line(f'\nValidation failed: {report.failures} problem(s).')
    return report.exit_code()


def main(argv: list[str] | None = None, runner: Runner | None = None, report: Report | None = None) -> int:
    return validate(runner or Runner(cwd=ROOT), report or Report())
