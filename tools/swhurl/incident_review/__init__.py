"""Offline evidence and candidate patch policy for AI incident review.

This prototype intentionally has no telemetry, model, notification or GitHub
adapter. Its dry run reads only checked-in fixtures.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from pathlib import Path
from typing import Any

from swhurl import ROOT

MAX_RECORDS = 20
MAX_TEXT = 2000
MAX_PATCH_BYTES = 32_000
MAX_FILES = 5
MAX_CHANGED_LINES = 200
ALLOWED_REPOSITORIES = {"samclement/hello-ts"}
ALLOWED_PATHS = ("src/", "tests/", "README.md")
FORBIDDEN_PARTS = {".github/workflows/", "apps/", "clusters/", "platform/", "infra/"}
SECRET_FIELD = re.compile(r"(?i)(password|secret|token|api[_-]?key|authorization|cookie)")
SECRET_VALUE = re.compile(
    r"(?i)bearer\s+[A-Za-z0-9._~+/-]+=*|\b(?:gh[pousr]_[A-Za-z0-9]{20,}|sk-[A-Za-z0-9_-]{20,})\b|"
    r"\b(?:password|secret|token|api[_-]?key)(\s*[=:]\s*)[^\s,;]+"
)


class PolicyError(ValueError):
    """Input is malformed or violates the static review policy."""


def redact(value: Any, *, key: str = "") -> Any:
    """Redact secret-shaped fields and values recursively, bounding strings."""
    if SECRET_FIELD.search(key):
        return "<redacted>"
    if isinstance(value, dict):
        return {str(k): redact(v, key=str(k)) for k, v in value.items()}
    if isinstance(value, list):
        return [redact(item) for item in value[:MAX_RECORDS]]
    if isinstance(value, str):
        text = value[:MAX_TEXT]
        return SECRET_VALUE.sub(lambda match: (match.group(1) or "") + "<redacted>", text)
    return value


def evidence_bundle(raw: dict[str, Any]) -> dict[str, Any]:
    required = {"repository", "app", "base_revision", "incident_type", "window_start", "window_end",
                "logs", "metrics", "traces"}
    if not isinstance(raw, dict) or not required <= raw.keys():
        raise PolicyError("evidence bundle is missing required fields")
    if raw["repository"] not in ALLOWED_REPOSITORIES:
        raise PolicyError("repository is not allowlisted")
    if not isinstance(raw["base_revision"], str) or not re.fullmatch(r"[0-9a-f]{40}", raw["base_revision"]):
        raise PolicyError("evidence base_revision must be a full commit SHA")
    if not all(isinstance(raw[k], list) for k in ("logs", "metrics", "traces")):
        raise PolicyError("evidence records must be lists")
    bounded = dict(raw)
    for key in ("logs", "metrics", "traces"):
        bounded[key] = raw[key][:MAX_RECORDS]
    cleaned = redact(bounded)
    cleaned["fingerprint"] = fingerprint(cleaned)
    return cleaned


def fingerprint(bundle: dict[str, Any]) -> str:
    identity = {key: bundle.get(key) for key in
                ("repository", "app", "incident_type", "window_start", "window_end")}
    return hashlib.sha256(json.dumps(identity, sort_keys=True, separators=(",", ":")).encode()).hexdigest()[:24]


def validate_diagnosis(result: Any, bundle: dict[str, Any]) -> dict[str, Any]:
    fields = {"summary", "likely_cause", "confidence", "evidence_refs", "unresolved_questions",
              "proposed_files", "proposed_tests", "recommend_no_change"}
    if not isinstance(result, dict) or set(result) != fields:
        raise PolicyError("diagnosis schema mismatch")
    if not isinstance(result["confidence"], (int, float)) or not 0 <= result["confidence"] <= 1:
        raise PolicyError("confidence must be between 0 and 1")
    refs = result["evidence_refs"]
    valid_refs = {f"{kind}[{i}]" for kind in ("logs", "metrics", "traces")
                  for i in range(len(bundle[kind]))}
    if not isinstance(refs, list) or not refs or not set(refs) <= valid_refs:
        raise PolicyError("diagnosis cites missing evidence")
    for name in ("summary", "likely_cause"):
        if not isinstance(result[name], str) or not result[name].strip():
            raise PolicyError(f"{name} must be a non-empty string")
    if not isinstance(result["recommend_no_change"], bool):
        raise PolicyError("recommend_no_change must be boolean")
    for name in ("unresolved_questions", "proposed_files", "proposed_tests"):
        if not isinstance(result[name], list) or len(result[name]) > MAX_FILES:
            raise PolicyError(f"{name} must be a bounded list")
    return redact(result)


def validate_patch(patch: dict[str, Any], *, repository: str, base_revision: str) -> dict[str, Any]:
    if repository not in ALLOWED_REPOSITORIES:
        raise PolicyError("repository is not allowlisted")
    if not isinstance(patch, dict) or patch.get("repository") != repository:
        raise PolicyError("patch repository mismatch")
    if patch.get("base_revision") != base_revision or not re.fullmatch(r"[0-9a-f]{40}", base_revision):
        raise PolicyError("patch does not target the exact recorded base revision")
    files = patch.get("files")
    if not isinstance(files, list) or not files or len(files) > MAX_FILES:
        raise PolicyError("patch file count is outside policy")
    changed_lines = 0
    seen: set[str] = set()
    for item in files:
        if not isinstance(item, dict) or set(item) != {"path", "diff"}:
            raise PolicyError("patch file entry schema mismatch")
        path, diff = item["path"], item["diff"]
        if not isinstance(path, str) or path.startswith("/") or ".." in Path(path).parts:
            raise PolicyError("unsafe patch path")
        if path in seen or not any(path == allowed or path.startswith(allowed) for allowed in ALLOWED_PATHS):
            raise PolicyError("patch path is not allowlisted")
        if any(path.startswith(prefix) for prefix in FORBIDDEN_PARTS):
            raise PolicyError("patch touches forbidden paths")
        if path.endswith((".yaml", ".yml", ".json", ".lock")) or Path(path).is_symlink():
            raise PolicyError("manifest, lockfile or symlink edits are forbidden")
        if not isinstance(diff, str) or "\x00" in diff or len(diff.encode()) > MAX_PATCH_BYTES:
            raise PolicyError("patch is binary or exceeds size policy")
        if re.search(r"(?m)^\s*(?:\+|-)\s*.*(?:password|secret|token|api[_-]?key)\s*[:=]", diff, re.I):
            raise PolicyError("patch contains a secret-like assignment")
        changed_lines += sum(1 for line in diff.splitlines() if line.startswith(("+", "-")) and
                             not line.startswith(("+++", "---")))
        seen.add(path)
    if changed_lines > MAX_CHANGED_LINES:
        raise PolicyError("patch exceeds changed-line limit")
    return {"repository": repository, "base_revision": base_revision,
            "files": files, "changed_lines": changed_lines}


def dry_run(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Offline incident review fixture dry run")
    parser.add_argument("--fixture", default=str(ROOT / "tests/fixtures/incident-review/candidate.json"))
    args = parser.parse_args(argv)
    try:
        raw = json.loads(Path(args.fixture).read_text())
        bundle = evidence_bundle(raw["evidence"])
        diagnosis = validate_diagnosis(raw["diagnosis"], bundle)
        patch = validate_patch(raw["patch"], repository=bundle["repository"],
                               base_revision=bundle["base_revision"])
        report = {"mode": "offline-fixture-dry-run", "fingerprint": bundle["fingerprint"],
                  "evidence": bundle, "diagnosis": diagnosis,
                  "proposed_patch": patch, "external_calls": 0}
        print(json.dumps(report, indent=2, sort_keys=True))
        return 0
    except (OSError, json.JSONDecodeError, KeyError, PolicyError) as error:
        print(f"incident review refused: {error}", file=sys.stderr)
        return 1


def main(argv: list[str] | None = None) -> int:
    return dry_run(argv)
