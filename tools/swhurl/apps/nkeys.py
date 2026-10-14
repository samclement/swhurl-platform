"""NATS nkeys: an Ed25519 key pair in NATS's text encoding, and the signature that proves it.

An app proves who it is to NATS by signing a challenge with its seed (a secret, in the app's
encrypted Secret); the server's configuration lists only public keys, so it holds no secret and
can live in Git, and a new app's key needs nothing decrypted. The console also signs in itself, to
write feature flag overrides (console/flagstore.py), so it signs NATS's challenge here too.

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
_L = 2**252 + 27742317777372353535851937790883648493  # the order of the base point
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


def _multiply(scalar: int, point: tuple = _BASE) -> tuple:
    found, addend = (0, 1, 1, 0), point
    while scalar:
        if scalar & 1:
            found = _add(found, addend)
        addend = _add(addend, addend)
        scalar >>= 1
    return found


def _compress(point: tuple) -> bytes:
    inverse = pow(point[2], _P - 2, _P)
    x, y = point[0] * inverse % _P, point[1] * inverse % _P
    return (y | ((x & 1) << 255)).to_bytes(32, 'little')


def _expand(seed: bytes) -> tuple[int, bytes]:
    if len(seed) != 32:
        raise ValueError('an Ed25519 seed is 32 bytes')
    digest = hashlib.sha512(seed).digest()
    scalar = int.from_bytes(digest[:32], 'little')
    return (scalar & ((1 << 254) - 8)) | (1 << 254), digest[32:]


def public_key(seed: bytes) -> bytes:
    """The 32-byte Ed25519 public key of a 32-byte seed."""
    return _compress(_multiply(_expand(seed)[0]))


def sign(seed: bytes, message: bytes) -> bytes:
    """The 64-byte Ed25519 signature of ``message`` (RFC 8032 section 5.1.6)."""
    scalar, prefix = _expand(seed)
    public = _compress(_multiply(scalar))
    r = int.from_bytes(hashlib.sha512(prefix + message).digest(), 'little') % _L
    nonce = _compress(_multiply(r))
    k = int.from_bytes(hashlib.sha512(nonce + public + message).digest(), 'little') % _L
    return nonce + ((r + k * scalar) % _L).to_bytes(32, 'little')


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


def decode_seed(text: str) -> bytes:
    """The 32-byte seed of a user's seed in text form ("SU..."); ValueError if it is not one."""
    try:
        raw = base64.b32decode(text.strip() + '=' * (-len(text.strip()) % 8))
    except ValueError:
        raise ValueError('not an nkey seed') from None
    if len(raw) != 36 or raw[:2] != bytes([SEED | USER >> 5, (USER & 31) << 3]) \
            or int.from_bytes(raw[34:], 'little') != _crc16(raw[:34]):
        raise ValueError('not a user nkey seed')
    return raw[2:34]


def generate() -> tuple[str, str]:
    """A new user key pair as ``(seed, public key)`` in nkeys text form. The seed is a secret: never print it."""
    seed = secrets.token_bytes(32)
    return encode_seed(seed), encode_public(public_key(seed))
