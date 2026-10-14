"""nkeys: the Ed25519 derivation against RFC 8032's vectors, and NATS's text encoding."""
import base64
import unittest

from swhurl.apps import nkeys

# RFC 8032 section 7.1, tests 1 to 3: (secret key, public key).
VECTORS = (
    ('9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60',
     'd75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a'),
    ('4ccd089b28ff96da9db6c346ec114e0f5b8a319f35aba624da8cf6ed4fb8a6fb',
     '3d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c'),
    ('c5aa8df43f9f837bedb7442f31dcb7b166d38535076f094b85ce3a2e0b4458f7',
     'fc51cd8e6218a1a38da47ed00230f0580816ed13ba3303ac5deb911548908025'),
)

# RFC 8032 section 7.1, tests 1 to 3: the message and its signature by the key above (checked with openssl too).
SIGNATURES = (
    (b'', 'e5564300c360ac729086e2cc806e828a84877f1eb8e5d974d873e06522490155'
             '5fb8821590a33bacc61e39701cf9b46bd25bf5f0595bbe24655141438e7a100b'),
    (b'r', '92a009a9f0d4cab8720e820b5f642540a2b27b5416503f8fb3762223ebdb69da'
             '085ac1e43e15996e458f3613d0f11d8c387b2eaeb4302aeeb00d291612bb0c00'),
    (b'\xaf\x82', '6291d657deec24024827e69c3abe01a30ce548a284743a445e3680d7db5ac3ac'
             '18ff9b538d16f290ae67f760984dc6594a7c15e9716ed28dc027beceea1ec40a'),
)

class NkeysTests(unittest.TestCase):
    def test_public_keys_match_the_rfc_vectors(self):
        for seed, public in VECTORS:
            with self.subTest(seed=seed[:8]):
                self.assertEqual(nkeys.public_key(bytes.fromhex(seed)).hex(), public)
        with self.assertRaises(ValueError):
            nkeys.public_key(b'short')

    def test_signatures_match_the_rfc_vectors(self):
        for (seed, _), (message, signature) in zip(VECTORS, SIGNATURES, strict=True):
            with self.subTest(seed=seed[:8]):
                self.assertEqual(nkeys.sign(bytes.fromhex(seed), message).hex(), signature)

    def test_a_seed_in_text_form_is_read_back_and_anything_else_refused(self):
        seed, _ = nkeys.generate()
        self.assertEqual(nkeys.encode_seed(nkeys.decode_seed(seed)), seed)
        for bad in ('UABC', seed[:20] + ('A' if seed[20] != 'A' else 'B') + seed[21:], 'not base32!'):  # a changed character fails the checksum
            with self.subTest(bad=bad[:6]), self.assertRaises(ValueError):
                nkeys.decode_seed(bad)

    def test_text_form_is_what_nats_reads(self):
        seed, public = (bytes.fromhex(v) for v in VECTORS[0])
        encoded_seed, encoded_public = nkeys.encode_seed(seed), nkeys.encode_public(public)
        self.assertTrue(encoded_seed.startswith('SU') and encoded_public.startswith('U'))
        self.assertEqual((len(encoded_seed), len(encoded_public)), (58, 56))
        raw = base64.b32decode(encoded_public + '=' * (-len(encoded_public) % 8))
        self.assertEqual(raw[1:33], public, 'the key follows the prefix byte')
        self.assertEqual(int.from_bytes(raw[33:], 'little'), nkeys._crc16(raw[:33]), 'then its checksum')
        raw = base64.b32decode(encoded_seed + '=' * (-len(encoded_seed) % 8))
        self.assertEqual(raw[2:34], seed)
        # The check value of CRC-16/XMODEM.
        self.assertEqual(nkeys._crc16(b'123456789'), 0x31C3)

    def test_generated_pairs_belong_together_and_differ(self):
        seed, public = nkeys.generate()
        raw = base64.b32decode(seed + '=' * (-len(seed) % 8))
        self.assertEqual(nkeys.encode_public(nkeys.public_key(raw[2:34])), public)
        self.assertNotEqual(nkeys.generate()[0], seed)


if __name__ == '__main__':
    unittest.main()
