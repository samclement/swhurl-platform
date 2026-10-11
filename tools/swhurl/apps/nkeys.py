"""NATS nkeys for apps: an Ed25519 key pair in NATS's text encoding.

An app proves who it is to NATS by signing a challenge with its seed (a secret, in the app's
encrypted Secret); the server's configuration lists only public keys, so it holds no secret and
can live in Git, and a new app's key needs nothing decrypted (the console can only encrypt).

Pure Python on purpose: the console image has no crypto library or openssl binary, and a key is
derived once per app. The curve arithmetic is RFC 8032's reference; tests pin its test vectors.
"""
from __future__ import annotations

import base64
import hashlib
import secrets

_P = 2**255 - 19
_D = -121665 * pow(121666, _P - 2, _P) % _P
_SQRT_M1 = pow(2, (_P - 1) // 4, _P)
USER, SEED = 20 << 3, 18 << 3  # nkeys prefix bytes: encoded keys start with "U" and "SU"


def _recover_x(y: int) -> int:
    x2 = (y * y - 1) * pow(_D * y * y + 1, _P - 2, _P)
    x = pow(x2, (_P + 3) // 8, _P)
    if (x * x - x2) % _P:
        x = x * _SQRT_M1 % _P
    return _P - x if x & 1 else x  # the base point has an even x


_BY = 4 * pow(5, _P - 2, _P) % _P
_BX = _recover_x(_BY)
_BASE = (_BX, _BY, 1, _BX * _BY % _P)  # extended coordinates (X, Y, Z, T)


def _add(a: tuple, b: tuple) -> tuple:
    first, second = (a[1] - a[0]) * (b[1] - b[0]) % _P, (a[1] + a[0]) * (b[1] + b[0]) % _P
    third, fourth = 2 * a[3] * b[3] * _D % _P, 2 * a[2] * b[2] % _P
    e, f, g, h = second - first, fourth - third, fourth + third, second + first
    return e * f % _P, g * h % _P, f * g % _P, e * h % _P


def public_key(seed: bytes) -> bytes:
    """The 32-byte Ed25519 public key of a 32-byte seed."""
    if len(seed) != 32:
        raise ValueError('an Ed25519 seed is 32 bytes')
    scalar = int.from_bytes(hashlib.sha512(seed).digest()[:32], 'little')
    scalar = (scalar & ((1 << 254) - 8)) | (1 << 254)
    point, addend = (0, 1, 1, 0), _BASE
    while scalar:
        if scalar & 1:
            point = _add(point, addend)
        addend = _add(addend, addend)
        scalar >>= 1
    inverse = pow(point[2], _P - 2, _P)
    x, y = point[0] * inverse % _P, point[1] * inverse % _P
    return (y | ((x & 1) << 255)).to_bytes(32, 'little')


def _crc16(data: bytes) -> int:
    crc = 0  # CRC-16/XMODEM, as the nkeys library uses
    for byte in data:
        crc ^= byte << 8
        for _ in range(8):
            crc = ((crc << 1) ^ 0x1021 if crc & 0x8000 else crc << 1) & 0xFFFF
    return crc


def _encode(raw: bytes) -> str:
    return base64.b32encode(raw + _crc16(raw).to_bytes(2, 'little')).decode().rstrip('=')


def encode_public(key: bytes) -> str:
    """A user's public nkey ("U..."), what the NATS server lists."""
    return _encode(bytes([USER]) + key)


def encode_seed(seed: bytes) -> str:
    """A user's seed ("SU..."), what the client keeps secret."""
    return _encode(bytes([SEED | USER >> 5, (USER & 31) << 3]) + seed)


def generate() -> tuple[str, str]:
    """A new user key pair as ``(seed, public key)`` in nkeys text form. The seed is a secret: never print it."""
    seed = secrets.token_bytes(32)
    return encode_seed(seed), encode_public(public_key(seed))
