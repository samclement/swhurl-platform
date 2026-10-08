"""Offline tests for the AI incident review evidence and policy prototype."""
from __future__ import annotations

import json
import unittest
from pathlib import Path

from swhurl.incident_review import (
    PolicyError,
    allowlist,
    check_patch_applies,
    coverage_report,
    decode_codex_output,
    describe_finding,
    evidence_bundle,
    fingerprint,
    prefilter,
    read_bounded_response,
    redact,
    validate_diagnosis,
    validate_patch,
)
from swhurl.incident_review.adapters import (
    DraftPullRequest,
    open_draft_pull_request,
    request_diagnosis,
    request_patch,
)
from swhurl.run import FakeRunner

FIXTURE = Path(__file__).parent / "fixtures/incident-review/candidate.json"


class IncidentReviewTests(unittest.TestCase):
    def setUp(self):
        self.raw = json.loads(FIXTURE.read_text())
        self.bundle = evidence_bundle(self.raw["evidence"])
        self.patch = self.raw["patch"]

    def test_fixture_candidate_passes(self):
        diagnosis = validate_diagnosis(self.raw["diagnosis"], self.bundle)
        patch = validate_patch(self.patch, repository=self.bundle["repository"],
                               base_revision=self.bundle["base_revision"])
        self.assertEqual(diagnosis["confidence"], 0.86)
        self.assertEqual(patch["changed_lines"], 5)
        schema = json.loads((Path(__file__).parents[1] / "tools/swhurl/incident_review/diagnosis.schema.json").read_text())
        self.assertEqual(set(schema["required"]), set(diagnosis))

    def test_redacts_sensitive_fields_tokens_and_bounds_records(self):
        cleaned = redact({"password": "do-not-show", "message": "token=abc123 Bearer eyJsecret",
                          "items": list(range(30))})
        rendered = json.dumps(cleaned)
        self.assertNotIn("do-not-show", rendered)
        self.assertNotIn("abc123", rendered)
        self.assertNotIn("eyJsecret", rendered)
        self.assertEqual(len(cleaned["items"]), 20)
        self.assertEqual(self.bundle["logs"][0]["api_key"], "<redacted>")
        self.assertIn("<redacted>", self.bundle["logs"][1]["message"])

    def test_evidence_bundle_fails_closed_above_byte_limit(self):
        evidence = dict(self.raw["evidence"], logs=[{"body": "x" * 2000, "detail": "y" * 2000}
                                                    for _ in range(20)])
        with self.assertRaisesRegex(PolicyError, "byte limit"):
            evidence_bundle(evidence)

    def test_fingerprint_is_stable_and_incident_scoped(self):
        self.assertEqual(fingerprint(self.bundle), self.bundle["fingerprint"])
        later = dict(self.raw["evidence"], window={"start": "2026-10-07T03:00:00+00:00",
                                                   "end": "2026-10-07T04:00:00+00:00"})
        self.assertEqual(evidence_bundle(later)["fingerprint"], self.bundle["fingerprint"])
        for field, value in (("signal", "error-spans"), ("key", "hello-ts TypeError"), ("env", "prod")):
            with self.subTest(field=field):
                self.assertNotEqual(fingerprint(dict(self.bundle, **{field: value})), self.bundle["fingerprint"])

    def test_bundle_refuses_unknown_scope_version_and_mismatched_fingerprint(self):
        for change, pattern in (({"version": 2}, "version"), ({"env": "prod"}, "not allowlisted"),
                                ({"signal": "all-logs"}, "not allowlisted"), ({"app": "other"}, "not allowlisted"),
                                ({"repository": "attacker/repo"}, "not allowlisted"), ({"key": ""}, "key"),
                                ({"window": {"start": "x"}}, "window"), ({"fingerprint": "0" * 24}, "fingerprint"),
                                ({"base_revision": "abc"}, "full commit SHA"), ({"extra": 1}, "required fields")):
            with self.subTest(change=change), self.assertRaisesRegex(PolicyError, pattern):
                evidence_bundle(dict(self.raw["evidence"], **change))
        without_base = {k: v for k, v in self.raw["evidence"].items() if k != "base_revision"}
        self.assertNotIn("base_revision", evidence_bundle(without_base))

    def test_refuses_missing_or_out_of_bundle_evidence(self):
        result = dict(self.raw["diagnosis"], evidence_refs=["logs[99]"])
        with self.assertRaisesRegex(PolicyError, "missing evidence"):
            validate_diagnosis(result, self.bundle)
        with self.assertRaisesRegex(PolicyError, "schema"):
            validate_diagnosis({"summary": "untrusted"}, self.bundle)

    def test_refuses_unallowlisted_repository_and_wrong_base(self):
        with self.assertRaisesRegex(PolicyError, "allowlisted"):
            validate_patch(dict(self.patch, repository="attacker/repo"), repository="attacker/repo",
                           base_revision=self.bundle["base_revision"])
        with self.assertRaisesRegex(PolicyError, "exact recorded"):
            validate_patch(self.patch, repository="samclement/hello-ts", base_revision="f" * 40)

    def test_refuses_forbidden_paths_traversal_symlink_and_secret_assignments(self):
        for path, diff in ((".github/workflows/ci.yml", "+safe"), ("../escape.ts", "+safe"),
                           ("src/server.ts", "+password: leaked"),
                           ("src/server.ts", "--- a/src/server.ts\n+++ b/src/server.ts\n"
                                               "@@ -1 +1 @@\n-safe\n+safe\n"
                                               "--- a/.github/workflows/ci.yml\n+++ b/.github/workflows/ci.yml\n")):
            with self.subTest(path=path), self.assertRaises(PolicyError):
                candidate = dict(self.patch, files=[{"path": path, "diff": diff}])
                validate_patch(candidate, repository="samclement/hello-ts",
                               base_revision=self.bundle["base_revision"])

    def test_refuses_file_and_line_limit_and_bad_patch_entry(self):
        candidate = dict(self.patch, files=[{"path": f"src/{n}.ts", "diff": "+x\n"} for n in range(6)])
        with self.assertRaisesRegex(PolicyError, "file count"):
            validate_patch(candidate, repository="samclement/hello-ts", base_revision=self.bundle["base_revision"])
        candidate = dict(self.patch, files=[{"path": "src/a.ts",
                                             "diff": "--- a/src/a.ts\n+++ b/src/a.ts\n" + "+x\n" * 201}])
        with self.assertRaisesRegex(PolicyError, "changed-line"):
            validate_patch(candidate, repository="samclement/hello-ts", base_revision=self.bundle["base_revision"])

    def test_patch_apply_check_requires_clean_exact_base(self):
        patch = validate_patch(self.patch, repository=self.bundle["repository"],
                               base_revision=self.bundle["base_revision"])
        clean = FakeRunner().on("git", "status", "--porcelain").on(
            "git", "rev-parse", "HEAD", stdout=self.bundle["base_revision"] + "\n").on(
            "git", "apply", "--check", "-")
        check_patch_applies(patch, Path("/fixture"), runner=clean)
        self.assertEqual(len(clean.calls), 3)

        dirty = FakeRunner().on("git", "status", "--porcelain", stdout=" M src/server.ts\n")
        with self.assertRaisesRegex(PolicyError, "must be clean"):
            check_patch_applies(patch, Path("/fixture"), runner=dirty)
        wrong_base = FakeRunner().on("git", "status", "--porcelain").on(
            "git", "rev-parse", "HEAD", stdout="f" * 40)
        with self.assertRaisesRegex(PolicyError, "exact recorded base"):
            check_patch_applies(patch, Path("/fixture"), runner=wrong_base)
        rejected = FakeRunner().on("git", "status", "--porcelain").on(
            "git", "rev-parse", "HEAD", stdout=self.bundle["base_revision"]).on(
            "git", "apply", "--check", "-", returncode=1)
        with self.assertRaisesRegex(PolicyError, "does not apply"):
            check_patch_applies(patch, Path("/fixture"), runner=rejected)

    def test_query_response_stream_is_strictly_byte_bounded(self):
        self.assertEqual(read_bounded_response([b"first", b"second"]), b"firstsecond")
        self.assertEqual(len(read_bounded_response([b"x" * 64_000])), 64_000)
        with self.assertRaisesRegex(PolicyError, "byte limit"):
            read_bounded_response([b"x" * 63_999, b"yz"])
        with self.assertRaisesRegex(PolicyError, "yield bytes"):
            read_bounded_response(["text"])

    def test_codex_cli_output_envelope_fails_closed(self):
        fixture = json.loads((FIXTURE.parent / "codex-output.json").read_text())
        for case in fixture["refusals"]:
            with self.subTest(case=case), self.assertRaises(PolicyError):
                decode_codex_output(case["returncode"], case["last_message"])
        diagnosis = decode_codex_output(0, json.dumps(self.raw["diagnosis"]))
        self.assertEqual(validate_diagnosis(diagnosis, self.bundle)["confidence"], 0.86)
        with self.assertRaisesRegex(PolicyError, "byte limit"):
            decode_codex_output(0, json.dumps({"output": "x" * 64_000}))

    def test_malformed_and_missing_evidence_responses_are_refused(self):
        fixture = json.loads((FIXTURE.parent / "refusals.json").read_text())
        for response in fixture["diagnoses"]:
            with self.subTest(response=response), self.assertRaises(PolicyError):
                validate_diagnosis(response, self.bundle)

    def test_prefilter_firing_and_suppression_cases(self):
        fixture = json.loads((FIXTURE.parent / "prefilter.json").read_text())
        defaults = allowlist.current().defaults
        for case in fixture["prefilter_cases"]:
            with self.subTest(case=case["name"]):
                decision = prefilter(case["finding"], case["state"], defaults, fixture["now"])
                self.assertEqual(decision["reason"], case["expect"])
                self.assertEqual(decision["fire"], case["expect"] in ("new-fingerprint", "error-rate-change",
                                                                      "missing-expected-signal"))
        for finding in ({"count": 1}, {"fingerprint": "x", "count": -1}, {"fingerprint": "x", "count": True}):
            with self.subTest(finding=finding), self.assertRaises(PolicyError):
                prefilter(finding, {"incidents": {}}, defaults, fixture["now"])

    def test_findings_are_described_without_ambiguity(self):
        metrics = {"signal": "missing-telemetry", "key": "no-metrics", "count": 1260, "baseline_mean": 1260.0,
                   "reason": "quiet-window"}
        self.assertEqual(describe_finding(metrics),
                         "metrics: 1260 metric points arrived (hourly average 1260.0), nothing unusual")
        self.assertEqual(describe_finding({**metrics, "count": 0, "reason": "missing-expected-signal"}),
                         "metrics: no metric points arrived (hourly average 1260.0), metrics stopped")
        self.assertEqual(describe_finding({"signal": "unexpected-service", "key": "swhurl-app", "count": 3,
                                           "baseline_mean": 0.0, "reason": "new-fingerprint"}),
                         "unexpected service name 'swhurl-app': 3 in the hour (hourly average 0.0), a new failure")

    def test_coverage_report_lists_alert_gaps(self):
        fixture = json.loads((FIXTURE.parent / "prefilter.json").read_text())
        rows = coverage_report(fixture["findings"], fixture["alert_rules"])
        self.assertEqual([row["coverage"] for row in rows], ["covered", "alert-gap"])

    def test_model_adapter_is_injected_and_outputs_are_validated(self):
        class FakeModel:
            def __init__(self):
                self.calls = []

            def diagnose(self, evidence):
                self.calls.append(("diagnose", evidence))
                return self.raw_diagnosis

            def propose_patch(self, evidence, diagnosis):
                self.calls.append(("patch", evidence, diagnosis))
                return self.raw_patch

        model = FakeModel()
        model.raw_diagnosis = self.raw["diagnosis"]
        model.raw_patch = self.patch
        diagnosis = request_diagnosis(model, self.bundle)
        patch = request_patch(model, self.bundle, diagnosis)
        self.assertEqual([call[0] for call in model.calls], ["diagnose", "patch"])
        self.assertEqual(patch["base_revision"], self.bundle["base_revision"])

        model.raw_diagnosis = dict(self.raw["diagnosis"], evidence_refs=["logs[99]"])
        with self.assertRaisesRegex(PolicyError, "missing evidence"):
            request_diagnosis(model, self.bundle)
        model.raw_diagnosis = dict(self.raw["diagnosis"], recommend_no_change=True)
        no_change = request_diagnosis(model, self.bundle)
        calls_before = len(model.calls)
        with self.assertRaisesRegex(PolicyError, "no change"):
            request_patch(model, self.bundle, no_change)
        self.assertEqual(len(model.calls), calls_before)

    def test_github_adapter_receives_only_validated_draft_patch(self):
        class FakeGitHub:
            def __init__(self):
                self.calls = []

            def create_draft_pull_request(self, **kwargs):
                self.calls.append(kwargs)
                return DraftPullRequest(number=42, url="https://github.com/samclement/hello-ts/pull/42")

        adapter = FakeGitHub()
        result = open_draft_pull_request(adapter, self.patch, title="Reject negative repeat counts", body="Evidence-backed fix.")
        self.assertEqual(result.number, 42)
        self.assertEqual(len(adapter.calls), 1)
        self.assertEqual(adapter.calls[0]["repository"], "samclement/hello-ts")
        self.assertEqual(adapter.calls[0]["base_revision"], self.bundle["base_revision"])

        invalid = dict(self.patch, files=[{"path": "../outside", "diff": "bad"}])
        with self.assertRaises(PolicyError):
            open_draft_pull_request(adapter, invalid, title="Unsafe", body="Must not be sent.")
        self.assertEqual(len(adapter.calls), 1)

        with self.assertRaisesRegex(PolicyError, "title"):
            open_draft_pull_request(adapter, self.patch, title=" ", body="Body")
        self.assertEqual(len(adapter.calls), 1)


if __name__ == "__main__":
    unittest.main()
