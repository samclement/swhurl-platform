"""Offline tests for the AI incident review evidence and policy prototype."""
from __future__ import annotations

import json
import unittest
from pathlib import Path

from swhurl.incident_review import PolicyError, evidence_bundle, fingerprint, redact, validate_diagnosis, validate_patch

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
        self.assertEqual(patch["changed_lines"], 2)

    def test_redacts_sensitive_fields_tokens_and_bounds_records(self):
        cleaned = redact({"password": "do-not-show", "message": "token=abc123 Bearer eyJsecret",
                          "items": list(range(30))})
        rendered = json.dumps(cleaned)
        self.assertNotIn("do-not-show", rendered)
        self.assertNotIn("abc123", rendered)
        self.assertNotIn("eyJsecret", rendered)
        self.assertEqual(len(cleaned["items"]), 20)
        self.assertEqual(self.bundle["logs"][0]["api_key"], "<redacted>")
        self.assertIn("<redacted>", self.bundle["logs"][1]["body"])

    def test_fingerprint_is_stable_and_incident_scoped(self):
        self.assertEqual(fingerprint(self.bundle), self.bundle["fingerprint"])
        changed = dict(self.bundle, incident_type="different")
        self.assertNotEqual(fingerprint(changed), self.bundle["fingerprint"])

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
                           ("src/server.ts", "+password: leaked")):
            with self.subTest(path=path), self.assertRaises(PolicyError):
                candidate = dict(self.patch, files=[{"path": path, "diff": diff}])
                validate_patch(candidate, repository="samclement/hello-ts",
                               base_revision=self.bundle["base_revision"])

    def test_refuses_file_and_line_limit_and_bad_patch_entry(self):
        candidate = dict(self.patch, files=[{"path": f"src/{n}.ts", "diff": "+x\n"} for n in range(6)])
        with self.assertRaisesRegex(PolicyError, "file count"):
            validate_patch(candidate, repository="samclement/hello-ts", base_revision=self.bundle["base_revision"])
        candidate = dict(self.patch, files=[{"path": "src/a.ts", "diff": "+x\n" * 201}])
        with self.assertRaisesRegex(PolicyError, "changed-line"):
            validate_patch(candidate, repository="samclement/hello-ts", base_revision=self.bundle["base_revision"])


if __name__ == "__main__":
    unittest.main()
