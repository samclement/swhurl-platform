"""Validate active Git-managed entrypoints without contacting the cluster."""

from __future__ import annotations

import re
import sys
from pathlib import Path

import yaml

from swhurl import ROOT
from swhurl.run import CommandError, Runner

RUNNER = Runner(cwd=ROOT)
MANIFEST_ROOTS = ("clusters", "infrastructure", "platform-services", "tenants")
BOOTSTRAP_PATHS = ("clusters/home/flux-system", "clusters/home/flux-system/sources")
TOKEN = re.compile(r"(?<!\$)\$\{([^}]+)\}")
YAML_SUFFIXES = {".yaml", ".yml"}


def fail(message: str) -> None:
    raise ValueError(message)


def documents(path: Path) -> list[dict]:
    try:
        parsed = list(yaml.safe_load_all(path.read_text()))
    except yaml.YAMLError as exc:
        fail(f"{path.relative_to(ROOT)}: invalid YAML: {exc}")
    if not parsed or any(not isinstance(item, dict) for item in parsed):
        fail(f"{path.relative_to(ROOT)}: expected Kubernetes YAML documents")
    return parsed


def run(args: list[str], *, input_text: str | None = None) -> str:
    try:
        return RUNNER.output(args, input=input_text)
    except CommandError as exc:
        fail(str(exc))


def active_paths() -> dict[Path, dict]:
    paths: dict[Path, dict] = {ROOT / name: {} for name in BOOTSTRAP_PATHS}
    definitions = [
        ROOT / "clusters/home/flux-system/kustomizations.yaml",
        *sorted(
            path for path in (ROOT / "clusters/home").iterdir()
            if path.suffix in YAML_SUFFIXES
        ),
    ]
    for definition in definitions:
        for item in documents(definition):
            if item.get("apiVersion") != "kustomize.toolkit.fluxcd.io/v1":
                continue
            if item.get("kind") != "Kustomization":
                continue
            spec = item.get("spec") or {}
            source_path = spec.get("path")
            if not isinstance(source_path, str) or not source_path.startswith("./"):
                fail(f"{definition.relative_to(ROOT)}: Flux Kustomization needs a local spec.path")
            path = (ROOT / source_path).resolve()
            if not path.is_relative_to(ROOT):
                fail(f"{definition.relative_to(ROOT)}: spec.path escapes the repository")
            if path in paths and paths[path]:
                fail(f"{definition.relative_to(ROOT)}: duplicate Flux render path {source_path}")
            paths[path] = spec
    for path in paths:
        if not path.is_dir() or not any(
            (path / name).is_file() for name in ("kustomization.yaml", "kustomization.yml")
        ):
            fail(f"missing active Kustomize path: {path.relative_to(ROOT)}")
    return paths


def platform_settings() -> dict[str, str]:
    path = ROOT / "clusters/home/flux-system/sources/configmap-platform-settings.yaml"
    item = documents(path)[0]
    if item.get("kind") != "ConfigMap" or item.get("metadata", {}).get("name") != "platform-settings":
        fail(f"{path.relative_to(ROOT)}: expected platform-settings ConfigMap")
    data = item.get("data")
    if not isinstance(data, dict) or any(not isinstance(value, str) for value in data.values()):
        fail(f"{path.relative_to(ROOT)}: settings data must contain strings")
    return data


def substitutions(spec: dict, settings: dict[str, str]) -> dict[str, str]:
    sources = (spec.get("postBuild") or {}).get("substituteFrom") or []
    if not sources:
        return {}
    if sources != [{"kind": "ConfigMap", "name": "platform-settings", "optional": False}]:
        fail("validation needs an explicit mapping for new Flux substitution sources")
    return settings


def check_secrets() -> None:
    count = 0
    for root_name in MANIFEST_ROOTS:
        for path in sorted(
            candidate for candidate in (ROOT / root_name).rglob("*")
            if candidate.suffix in YAML_SUFFIXES
        ):
            items = documents(path)
            for item in items:
                if item.get("kind") != "Secret":
                    continue
                count += 1
                label = path.relative_to(ROOT)
                if not path.name.endswith((".sops.yaml", ".sops.yml")):
                    fail(f"{label}: Git-managed Secret must be a .sops.yaml or .sops.yml file")
                sops = item.get("sops")
                if not isinstance(sops, dict) or not sops.get("age") or not sops.get("mac"):
                    fail(f"{label}: missing SOPS age metadata")
                if sops.get("encrypted_regex") != "^(data|stringData)$":
                    fail(f"{label}: unexpected SOPS encrypted_regex")
                payloads = [item[key] for key in ("data", "stringData") if key in item]
                if not payloads or any(not isinstance(payload, dict) or not payload for payload in payloads):
                    fail(f"{label}: Secret needs nonempty encrypted data or stringData")
                for payload in payloads:
                    if any(not isinstance(value, str) or not value.startswith("ENC[") for value in payload.values()):
                        fail(f"{label}: Secret contains an unencrypted value")
            if path.name.endswith((".sops.yaml", ".sops.yml")) and all(
                item.get("kind") != "Secret" for item in items
            ):
                fail(f"{path.relative_to(ROOT)}: expected an encrypted Secret")
    print(f"[OK] {count} Git-managed Secrets use SOPS age encryption")


def check_shell_syntax() -> None:
    names = run(["git", "ls-files", "--cached", "--others", "--exclude-standard", "*.sh"])
    files = sorted(set(names.splitlines()))
    for name in files:
        run(["bash", "-n", name])
    print(f"[OK] {len(files)} shell files pass individual syntax checks")


def validate_render(path: Path, spec: dict, settings: dict[str, str]) -> None:
    label = path.relative_to(ROOT)
    rendered = run(["kubectl", "kustomize", str(label)])
    values = substitutions(spec, settings)

    def replace_token(match: re.Match[str]) -> str:
        key = match.group(1)
        if key not in values:
            fail(f"{label}: unresolved Flux substitution ${{{key}}}")
        return values[key]

    substituted = TOKEN.sub(replace_token, rendered)
    for item in yaml.safe_load_all(substituted):
        if not isinstance(item, dict) or not all(key in item for key in ("apiVersion", "kind", "metadata")):
            fail(f"{label}: rendered document lacks apiVersion, kind or metadata")
    run(
        ["flux-schema", "validate", "--schema-location", "default",
         "--schema-location", "ecosystem", "--skip-json-path", "/sops"],
        input_text=substituted,
    )
    print(f"[OK] rendered and schema-validated {label}")


LINK = re.compile(r"\[[^\]]*\]\(([^)\s]+)\)")


def anchors(markdown: Path) -> set[str]:
    """GitHub-style heading anchors for a Markdown file."""
    found = set()
    for line in markdown.read_text().splitlines():
        if line.startswith("#"):
            text = line.lstrip("#").strip().lower()
            text = re.sub(r"[^\w\- ]", "", text).replace(" ", "-")
            found.add(text)
    return found


def check_doc_links() -> None:
    """Fail on relative Markdown links whose file or heading anchor does not exist."""
    tracked = run(["git", "ls-files", "*.md"]).split()
    untracked = run(["git", "ls-files", "--others", "--exclude-standard", "*.md"]).split()
    broken = []
    checked = 0
    for name in sorted(set(tracked + untracked)):
        source = ROOT / name
        if not source.exists() or name.startswith(".claude/"):
            continue
        for target in LINK.findall(source.read_text()):
            if re.match(r"^[a-z][a-z0-9+.-]*:", target):
                continue
            path_part, _, anchor = target.partition("#")
            resolved = (source.parent / path_part).resolve() if path_part else source
            checked += 1
            if not resolved.exists():
                broken.append(f"{name}: {target} (missing file)")
            elif anchor and resolved.suffix == ".md" and anchor not in anchors(resolved):
                broken.append(f"{name}: {target} (missing heading)")
    if broken:
        raise ValueError("broken documentation links:\n  " + "\n  ".join(broken))
    print(f"[OK] {checked} relative documentation links resolve")


def validate() -> None:
    check_doc_links()
    check_shell_syntax()
    check_secrets()
    settings = platform_settings()
    paths = active_paths()
    for path, spec in paths.items():
        validate_render(path, spec, settings)
    print(f"Validation passed for {len(paths)} active render entrypoints.")


def main(argv: list[str] | None = None) -> int:
    try:
        validate()
    except ValueError as exc:
        print(f"[ERROR] {exc}", file=sys.stderr)
        return 1
    return 0
