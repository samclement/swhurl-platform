"""check-secrets helpers, offline."""
import base64
import io
import os
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path
from unittest import mock


class SecretsCheckTests(unittest.TestCase):
    def test_notification_destinations_match_without_reporting_values(self):
        from swhurl.secrets_check import notification_destination_problem
        copies = {'source-platform': 'a', 'checker-platform': 'a'}
        self.assertIsNone(notification_destination_problem(copies))
        copies['checker-platform'] = 'private-fingerprint'
        error = notification_destination_problem(copies)
        self.assertIn('differs', error)
        self.assertNotIn('private-fingerprint', error)
        self.assertIn('missing', notification_destination_problem({}))
    def test_secrets_check_flags_double_encoding(self):
        from swhurl import secrets_check as module
        uuid = b'0f8fad5b-d9cb-469f-a165-70867728950e'
        self.assertFalse(module.looks_double_encoded(uuid), 'a plain token is not double-encoded')
        self.assertTrue(module.looks_double_encoded(base64.b64encode(uuid)), 'base64 of a token is')
        self.assertFalse(module.looks_double_encoded(bytes(range(40))), 'binary bytes are not base64 text')
        self.assertNotIn('.sops.yaml', [p.name for p in module.secret_files()])

    def test_a_shared_token_must_be_one_value_in_both_secrets(self):
        from swhurl.secrets_check import SHARED, shared_problem
        for what in ('the image webhook token', 'the GitHub dispatch token'):
            first, console = (f for f, _ in SHARED[what])
            self.assertIsNone(shared_problem(what, {first: 'a', console: 'a'}))
            for copies in ({first: 'a', console: 'b'}, {first: 'a'}, {}):
                self.assertIn(f'{what} must be set and identical', shared_problem(what, copies))

    def test_ingestion_key_must_be_one_value_in_both_secrets(self):
        from swhurl.secrets_check import INGESTION_FILES, ingestion_key_problem
        clickstack, otel = INGESTION_FILES
        self.assertIsNone(ingestion_key_problem({clickstack: 'a', otel: 'a'}))
        self.assertIn('differs', ingestion_key_problem({clickstack: 'a', otel: 'b'}))
        self.assertIn('exactly', ingestion_key_problem({clickstack: 'a'}))
        self.assertIn('exactly', ingestion_key_problem({}))
        self.assertIn('exactly', ingestion_key_problem({clickstack: 'a', otel: 'a', 'platform/x.sops.yaml': 'a'}))

    def test_a_file_with_two_documents_is_checked_document_by_document(self):
        from swhurl import secrets_check as module
        documents = [
            {'metadata': {'namespace': 'flux-system', 'name': 'backup-writer'},
             'stringData': {'BACKUP_AWS_ACCESS_KEY_ID': 'one', 'BACKUP_AWS_SECRET_ACCESS_KEY': 'two'}},
            {'metadata': {'namespace': 'observability', 'name': 'backup-writer'},
             'stringData': {'AWS_ACCESS_KEY_ID': 'three', 'AWS_SECRET_ACCESS_KEY': ''}},
        ]
        findings = module.Findings()
        output = io.StringIO()
        with redirect_stdout(output):
            module.check_documents(Path('platform/backup/writer.sops.yaml'), documents, fixture=False,
                                   findings=findings)
        lines = output.getvalue().splitlines()
        self.assertEqual(sum(line.startswith('[OK] platform/backup/writer.sops.yaml:') for line in lines), 2,
                         'one line per document, not per file')
        self.assertIn('observability/backup-writer', output.getvalue())
        self.assertIn('[ERROR] platform/backup/writer.sops.yaml: stringData.AWS_SECRET_ACCESS_KEY: empty',
                      output.getvalue(), 'the second document is checked too')
        self.assertEqual(findings.errors, 1)

    def test_the_check_reads_every_document_of_a_decrypted_file(self):
        """The backup writer's file holds two Secrets; a check that read one document refused the file."""
        import yaml

        from swhurl import secrets_check as module
        from swhurl.run import FakeRunner
        relative = 'platform/backup/writer.sops.yaml'
        documents = [
            {'apiVersion': 'v1', 'kind': 'Secret', 'metadata': {'name': 'backup-writer', 'namespace': 'flux-system'},
             'stringData': {'BACKUP_AWS_ACCESS_KEY_ID': 'one', 'BACKUP_AWS_SECRET_ACCESS_KEY': 'two'}},
            {'apiVersion': 'v1', 'kind': 'Secret', 'metadata': {'name': 'backup-writer', 'namespace': 'observability'},
             'stringData': {'AWS_ACCESS_KEY_ID': 'one', 'AWS_SECRET_ACCESS_KEY': 'two'}},
        ]
        runner = FakeRunner().on('git', 'ls-files', stdout=relative + '\n').on(
            'sops', 'decrypt', str(module.ROOT / relative), stdout=yaml.safe_dump_all(documents))
        output = io.StringIO()
        with tempfile.TemporaryDirectory() as tmp:
            key = Path(tmp) / 'age.key'
            key.write_text('')
            with mock.patch.dict(os.environ, {'SOPS_AGE_KEY_FILE': str(key)}), redirect_stdout(output):
                status = module.main([], runner)
        text = output.getvalue()
        self.assertIn(f'[OK] {relative}: flux-system/backup-writer', text)
        self.assertIn(f'[OK] {relative}: observability/backup-writer', text)
        self.assertNotIn('cannot read', text)
        # The fake lists this one file, so the other checks report their files missing; that is the only failure.
        self.assertIn('must be in exactly', text)
        self.assertEqual(status, 1)


if __name__ == '__main__':
    unittest.main()
