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
from collections.abc import Iterable
from datetime import datetime
from pathlib import Path
from typing import Any

from swhurl import ROOT
from swhurl.run import Runner

MAX_RECORDS = 20
MAX_TEXT = 2000
MAX_BUNDLE_BYTES = 64_000
MAX_QUERY_RESPONSE_BYTES = 64_000
MAX_AGENT_OUTPUT_BYTES = 64_000
MAX_QUERY_ROWS = 20
MAX_PATCH_BYTES = 32_000
MAX_FILES = 5
MAX_CHANGED_LINES = 200
MAX_QUERY_WINDOW_HOURS = 24
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


def telemetry_query(signal: str, *, app: str, start: str, end: str) -> tuple[str, dict[str, str]]:
    """Build a fixed ClickHouse query; caller values are always parameters."""
    if signal not in {"errors", "logs", "traces"}:
        raise PolicyError("signal is not allowlisted")
    if not isinstance(app, str) or not app or len(app) > 128:
        raise PolicyError("app must be a bounded non-empty string")
    try:
        start_at = datetime.fromisoformat(start.replace("Z", "+00:00"))
        end_at = datetime.fromisoformat(end.replace("Z", "+00:00"))
    except (AttributeError, TypeError, ValueError) as error:
        raise PolicyError("query window must use ISO-8601 timestamps") from error
    if start_at.tzinfo is None or end_at.tzinfo is None:
        raise PolicyError("query timestamps must include a timezone")
    if end_at <= start_at or (end_at - start_at).total_seconds() > MAX_QUERY_WINDOW_HOURS * 3600:
        raise PolicyError("query window must be positive and at most 24 hours")
    query = {
        "errors": f"SELECT service, exception_type, count() AS value FROM otel_logs WHERE app = {{app:String}} AND Timestamp >= {{start:DateTime64}} AND Timestamp < {{end:DateTime64}} AND SeverityText IN ('ERROR','FATAL') GROUP BY service, exception_type ORDER BY value DESC LIMIT {MAX_QUERY_ROWS}",
        "logs": f"SELECT Timestamp, service, SeverityText, Body FROM otel_logs WHERE app = {{app:String}} AND Timestamp >= {{start:DateTime64}} AND Timestamp < {{end:DateTime64}} ORDER BY Timestamp DESC LIMIT {MAX_QUERY_ROWS}",
        "traces": f"SELECT Timestamp, TraceId, SpanId, ServiceName, SpanName, StatusCode FROM otel_traces WHERE app = {{app:String}} AND Timestamp >= {{start:DateTime64}} AND Timestamp < {{end:DateTime64}} AND StatusCode = 'Error' ORDER BY Timestamp DESC LIMIT {MAX_QUERY_ROWS}",
    }[signal]
    return query, {"app": app, "start": start, "end": end}


def prefilter(finding: dict[str, Any], state: dict[str, Any]) -> dict[str, Any]:
    """Decide deterministically whether a finding may proceed to analysis."""
    fingerprint_value = finding.get("fingerprint")
    if not isinstance(fingerprint_value, str) or not fingerprint_value:
        raise PolicyError("finding needs a fingerprint")
    seen = fingerprint_value in state.get("seen", [])
    if seen and finding.get("inside_cooldown"):
        reason = "repeat-inside-cooldown"
    elif finding.get("missing_expected_signal"):
        reason = "missing-expected-signal"
    elif finding.get("error_count", 0) > finding.get("baseline_count", 0) * finding.get("threshold_ratio", 2):
        reason = "error-rate-change"
    elif fingerprint_value not in state.get("seen", []):
        reason = "new-fingerprint"
    else:
        reason = "quiet-window"
    return {"fire": reason in {"missing-expected-signal", "error-rate-change", "new-fingerprint"},
            "reason": reason, "model_call": reason in {"missing-expected-signal", "error-rate-change", "new-fingerprint"}}


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
    if len(json.dumps(cleaned, ensure_ascii=False, separators=(",", ":")).encode()) > MAX_BUNDLE_BYTES:
        raise PolicyError("redacted evidence bundle exceeds byte limit")
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
        if (not isinstance(result[name], list) or len(result[name]) > MAX_FILES or
                any(not isinstance(item, str) or not item.strip() or len(item) > MAX_TEXT
                    for item in result[name])):
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
        if (not isinstance(path, str) or
                not re.fullmatch(r"(?:src|tests)/[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)*|README\.md", path)):
            raise PolicyError("unsafe patch path")
        if path in seen or not any(path == allowed or path.startswith(allowed) for allowed in ALLOWED_PATHS):
            raise PolicyError("patch path is not allowlisted")
        if any(path.startswith(prefix) for prefix in FORBIDDEN_PARTS):
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
        cases = json.loads((ROOT / "tests/fixtures/incident-review/prefilter.json").read_text())
        decisions = [prefilter(case["finding"], case["state"]) for case in cases["prefilter_cases"]]
        coverage = coverage_report(cases["findings"], cases["alert_rules"])
        report = {"mode": "offline-fixture-dry-run", "fingerprint": bundle["fingerprint"],
                  "evidence": bundle, "diagnosis": diagnosis,
                  "proposed_patch": patch, "prefilter": decisions,
                  "coverage": coverage,
                  "query_limits": {"server_settings": query_result_settings(),
                                   "response_bytes": MAX_QUERY_RESPONSE_BYTES},
                  "external_calls": 0}
        print(json.dumps(report, indent=2, sort_keys=True))
        return 0
    except (OSError, json.JSONDecodeError, KeyError, PolicyError) as error:
        print(f"incident review refused: {error}", file=sys.stderr)
        return 1


def main(argv: list[str] | None = None) -> int:
    return dry_run(argv)
