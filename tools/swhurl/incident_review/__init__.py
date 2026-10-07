"""Deterministic policy for AI incident review: redaction, bounds, validation and patch rules.

Nothing here talks to telemetry, a model, ntfy or GitHub. The steps that do are
``collect`` and ``decide``; ``dryrun`` runs them over checked-in fixtures
(docs/plan.md section 14).
"""
from __future__ import annotations

import hashlib
import json
import re
from collections.abc import Iterable
from pathlib import Path
from typing import Any

from swhurl.run import Runner

from . import allowlist
from .errors import PolicyError as PolicyError

MAX_RECORDS = 20
MAX_TEXT = 2000
MAX_BUNDLE_BYTES = 64_000
MAX_QUERY_RESPONSE_BYTES = 64_000
MAX_AGENT_OUTPUT_BYTES = 64_000
MAX_QUERY_ROWS = 20
MAX_PATCH_BYTES = 32_000
MAX_FILES = 5
MAX_CHANGED_LINES = 200
SAFE_PATH = re.compile(r"[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)*")
SECRET_FIELD = re.compile(r"(?i)(password|secret|token|api[_-]?key|authorization|cookie)")
SECRET_VALUE = re.compile(
    r"(?i)bearer\s+[A-Za-z0-9._~+/-]+=*|\b(?:gh[pousr]_[A-Za-z0-9]{20,}|sk-[A-Za-z0-9_-]{20,})\b|"
    r"\b(?:password|secret|token|api[_-]?key)(\s*[=:]\s*)[^\s,;]+"
)


def query_result_settings() -> dict[str, int]:
    """Return ClickHouse's best-effort result limits; stream reads enforce the hard byte cap."""
    return {"max_result_bytes": MAX_QUERY_RESPONSE_BYTES, "max_result_rows": MAX_QUERY_ROWS}


def read_bounded_response(chunks: Iterable[bytes]) -> bytes:
    """Collect a query response stream, refusing oversized or malformed chunks."""
    body = bytearray()
    for chunk in chunks:
        if not isinstance(chunk, bytes):
            raise PolicyError("query response stream must yield bytes")
        if len(body) + len(chunk) > MAX_QUERY_RESPONSE_BYTES:
            raise PolicyError("query response exceeds byte limit")
        body.extend(chunk)
    return bytes(body)


def decode_codex_output(returncode: int, last_message: str) -> dict[str, Any]:
    """Decode Codex CLI's schema-constrained --output-last-message artifact."""
    if not isinstance(returncode, int) or isinstance(returncode, bool) or returncode != 0:
        raise PolicyError("Codex CLI exited unsuccessfully")
    if not isinstance(last_message, str) or not last_message.strip():
        raise PolicyError("Codex CLI returned no final message")
    if len(last_message.encode()) > MAX_AGENT_OUTPUT_BYTES:
        raise PolicyError("Codex CLI final message exceeds byte limit")
    try:
        response = json.loads(last_message)
    except json.JSONDecodeError as error:
        raise PolicyError("Codex CLI final message is not valid JSON") from error
    if not isinstance(response, dict):
        raise PolicyError("Codex CLI final message must be a JSON object")
    return response


FIRING = ("missing-expected-signal", "error-rate-change", "new-fingerprint")


def prefilter(finding: dict[str, Any], state: dict[str, Any], defaults: dict[str, Any], now: float) -> dict[str, Any]:
    """Decide deterministically, from state alone, whether a finding may proceed to analysis.

    The first matching rule is the recorded reason. ``missing-telemetry`` counts healthy
    points, so only its absence can fire; every other signal counts failures.
    """
    fingerprint_value, count = finding.get("fingerprint"), finding.get("count")
    if not isinstance(fingerprint_value, str) or not fingerprint_value:
        raise PolicyError("finding needs a fingerprint")
    if isinstance(count, bool) or not isinstance(count, int) or count < 0:
        raise PolicyError("finding needs a non-negative count")
    known = state["incidents"].get(fingerprint_value)
    mean = finding.get("baseline_mean", 0)
    absence = finding.get("signal") == "missing-telemetry"
    if known and now < known["cooldown_until"]:
        reason = "repeat-inside-cooldown"
    elif absence:
        reason = "missing-expected-signal" if count == 0 and mean > 0 else "quiet-window"
    elif count > defaults["rate_ratio"] * mean and count >= defaults["rate_min_count"]:
        reason = "error-rate-change"
    elif not known and count > 0:
        reason = "new-fingerprint"
    else:
        reason = "quiet-window"
    return {"fire": reason in FIRING, "reason": reason}


def coverage_report(findings: list[dict[str, Any]], alert_rules: list[dict[str, str]]) -> list[dict[str, Any]]:
    """Mark sweep findings as covered only when an app and signal rule matches."""
    rules = {(rule["app"], rule["signal"]) for rule in alert_rules}
    return [{"fingerprint": item["fingerprint"], "app": item["app"], "signal": item["signal"],
             "coverage": "covered" if (item["app"], item["signal"]) in rules else "alert-gap"}
            for item in findings]


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


BUNDLE_FIELDS = {"version", "repository", "app", "env", "signal", "key", "window", "image",
                 "logs", "metrics", "traces", "links"}


def evidence_bundle(raw: dict[str, Any]) -> dict[str, Any]:
    """Validate, bound and redact a version 1 bundle; ``base_revision`` joins it in Stage 3."""
    if not isinstance(raw, dict) or not BUNDLE_FIELDS <= raw.keys() <= BUNDLE_FIELDS | {"base_revision", "fingerprint"}:
        raise PolicyError("evidence bundle is missing required fields")
    if raw["version"] != 1 or isinstance(raw["version"], bool):
        raise PolicyError("evidence bundle version must be 1")
    app = allowlist.current().repository(raw["repository"])
    if raw["app"] != app.app or raw["env"] not in app.environments or raw["signal"] not in app.signals:
        raise PolicyError("evidence bundle app, environment or signal is not allowlisted")
    if not isinstance(raw["key"], str) or not raw["key"] or len(raw["key"]) > 300:
        raise PolicyError("evidence bundle key must be a bounded non-empty string")
    for name, fields in (("window", {"start", "end"}), ("image", {"tag", "digest"})):
        if (not isinstance(raw[name], dict) or set(raw[name]) != fields
                or not all(isinstance(v, str) and len(v) <= 200 for v in raw[name].values())):
            raise PolicyError(f"evidence bundle {name} is malformed")
    if "base_revision" in raw and (not isinstance(raw["base_revision"], str)
                                   or not re.fullmatch(r"[0-9a-f]{40}", raw["base_revision"])):
        raise PolicyError("evidence base_revision must be a full commit SHA")
    if not all(isinstance(raw[k], list) for k in ("logs", "metrics", "traces", "links")):
        raise PolicyError("evidence records must be lists")
    bounded = {k: v for k, v in raw.items() if k != "fingerprint"}
    for key in ("logs", "metrics", "traces", "links"):
        bounded[key] = raw[key][:MAX_RECORDS]
    cleaned = redact(bounded)
    cleaned["fingerprint"] = fingerprint(cleaned)
    if raw.get("fingerprint", cleaned["fingerprint"]) != cleaned["fingerprint"]:
        raise PolicyError("evidence bundle fingerprint does not match its identity")
    if len(json.dumps(cleaned, ensure_ascii=False, separators=(",", ":")).encode()) > MAX_BUNDLE_BYTES:
        raise PolicyError("redacted evidence bundle exceeds byte limit")
    return cleaned


def fingerprint(identity: dict[str, Any]) -> str:
    """Stable for one failure of one app environment; the time window is never part of it."""
    fields = {key: identity[key] for key in ("repository", "app", "env", "signal", "key")}
    return hashlib.sha256(json.dumps(fields, sort_keys=True, separators=(",", ":")).encode()).hexdigest()[:24]


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
        if (not isinstance(result[name], list) or len(result[name]) > MAX_FILES or
                any(not isinstance(item, str) or not item.strip() or len(item) > MAX_TEXT
                    for item in result[name])):
            raise PolicyError(f"{name} must be a bounded list")
    return redact(result)


def validate_patch(patch: dict[str, Any], *, repository: str, base_revision: str) -> dict[str, Any]:
    allowed_paths = allowlist.current().repository(repository).patch_paths
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
        if not isinstance(path, str) or not SAFE_PATH.fullmatch(path) or ".." in path.split("/"):
            raise PolicyError("unsafe patch path")
        if path in seen or not any(path == allowed or (allowed.endswith("/") and path.startswith(allowed))
                                   for allowed in allowed_paths):
            raise PolicyError("patch path is not allowlisted")
        if any(path.startswith(prefix) for prefix in allowlist.FORBIDDEN_PARTS):
            raise PolicyError("patch touches forbidden paths")
        if path.endswith((".yaml", ".yml", ".json", ".lock")) or Path(path).is_symlink():
            raise PolicyError("manifest, lockfile or symlink edits are forbidden")
        if not isinstance(diff, str) or "\x00" in diff or len(diff.encode()) > MAX_PATCH_BYTES:
            raise PolicyError("patch is binary or exceeds size policy")
        if not diff.startswith(f"--- a/{path}\n+++ b/{path}\n"):
            raise PolicyError("patch headers do not match the declared path")
        if any(line.startswith(("diff --git ", "--- a/", "+++ b/")) for line in diff.splitlines()[2:]):
            raise PolicyError("patch entry contains multiple file headers")
        if re.search(r"(?m)^\s*(?:\+|-)\s*.*(?:password|secret|token|api[_-]?key)\s*[:=]", diff, re.I):
            raise PolicyError("patch contains a secret-like assignment")
        changed_lines += sum(1 for line in diff.splitlines() if line.startswith(("+", "-")) and
                             not line.startswith(("+++", "---")))
        seen.add(path)
    if changed_lines > MAX_CHANGED_LINES:
        raise PolicyError("patch exceeds changed-line limit")
    return {"repository": repository, "base_revision": base_revision,
            "files": files, "changed_lines": changed_lines}


def check_patch_applies(patch: dict[str, Any], checkout: Path, *, runner: Runner | None = None) -> None:
    """Prove a validated patch applies to a clean checkout at its exact base."""
    runner = runner or Runner()
    dirty = runner.output(["git", "status", "--porcelain"], cwd=checkout)
    if dirty.strip():
        raise PolicyError("patch checkout must be clean")
    head = runner.output(["git", "rev-parse", "HEAD"], cwd=checkout).strip()
    if head != patch["base_revision"]:
        raise PolicyError("patch checkout is not at the exact recorded base revision")
    root = checkout.resolve()
    for item in patch["files"]:
        path = root / item["path"]
        parents = path.parents
        if any(parent.is_symlink() for parent in parents if parent == root or root in parent.parents):
            raise PolicyError("patch checkout contains a symlink on an allowed path")
        if path.is_symlink() or root not in path.resolve().parents:
            raise PolicyError("patch target escapes the checkout")
    combined = "".join(item["diff"] for item in patch["files"])
    result = runner.run(["git", "apply", "--check", "-"], input=combined, cwd=checkout, check=False)
    if result.returncode:
        raise PolicyError("candidate patch does not apply cleanly to the recorded base")
