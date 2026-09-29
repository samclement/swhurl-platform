"""check-secrets helpers, offline."""
import base64
import unittest


class SecretsCheckTests(unittest.TestCase):
    def test_secrets_check_flags_double_encoding(self):
        from swhurl import secrets_check as module
        uuid = b'0f8fad5b-d9cb-469f-a165-70867728950e'
        self.assertFalse(module.looks_double_encoded(uuid), 'a plain token is not double-encoded')
        self.assertTrue(module.looks_double_encoded(base64.b64encode(uuid)), 'base64 of a token is')
        self.assertFalse(module.looks_double_encoded(bytes(range(40))), 'binary bytes are not base64 text')
        self.assertNotIn('.sops.yaml', [p.name for p in module.secret_files()])

    def test_ingestion_key_must_be_one_value_in_both_secrets(self):
        from swhurl.secrets_check import INGESTION_FILES, ingestion_key_problem
        clickstack, otel = INGESTION_FILES
        self.assertIsNone(ingestion_key_problem({clickstack: 'a', otel: 'a'}))
        self.assertIn('differs', ingestion_key_problem({clickstack: 'a', otel: 'b'}))
        self.assertIn('exactly', ingestion_key_problem({clickstack: 'a'}))
        self.assertIn('exactly', ingestion_key_problem({}))
        self.assertIn('exactly', ingestion_key_problem({clickstack: 'a', otel: 'a', 'platform/x.sops.yaml': 'a'}))


if __name__ == '__main__':
    unittest.main()
