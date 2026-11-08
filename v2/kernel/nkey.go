package kernel

import (
	"crypto/ed25519"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"slices"
	"strings"
)

// NATS nkeys are Ed25519 pairs in a text form: the public key is what the server lists, and the seed — the
// private half — is what a client signs in with. The encoding is a one-byte prefix, the 32-byte key, and a
// CRC-16 checksum, in base32 without padding. v2 derives the pair from a seed the caller supplies, so the
// derivation is a pure function: the randomness belongs to whoever mints, not to the model.
const (
	nkeyUser     = 20 << 3 // the user public key's prefix: "U…"
	nkeyUserSeed = 18 << 3 // the seed's prefix: "SU…"
	nkeyBase32   = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
)

// UserKey is one user pair in nkeys text form. Seed is a secret and is never printed by v2; Public is what a
// record states and what the server's user list holds.
type UserKey struct {
	Seed   string
	Public string
}

// NewUserKey is the pair a 32-byte seed states. The same seed states the same pair, so a test can pin the
// encoding against a published vector.
func NewUserKey(seed [32]byte) UserKey {
	private := ed25519.NewKeyFromSeed(seed[:])
	public := private.Public().(ed25519.PublicKey)
	return UserKey{
		Seed: encodeNKey([]byte{byte(nkeyUserSeed | nkeyUser>>5), byte((nkeyUser & 31) << 3)}, seed[:]),
		// The user's prefix byte is first, then the 32-byte Ed25519 public key.
		Public: encodeNKey([]byte{byte(nkeyUser)}, public),
	}
}

// DecodeUserSeed is the 32-byte seed a user seed in text form ("SU…") states: the encoding NewUserKey
// writes, read back. A value that is not one is refused without saying what it held, because it is a secret.
func DecodeUserSeed(text string) ([32]byte, error) {
	var seed [32]byte
	raw, err := base32.NewEncoding(nkeyBase32).WithPadding(base32.NoPadding).DecodeString(strings.TrimSpace(text))
	prefix := []byte{byte(nkeyUserSeed | nkeyUser>>5), byte((nkeyUser & 31) << 3)}
	if err != nil || len(raw) != 34+2 || !slices.Equal(raw[:2], prefix) ||
		binary.LittleEndian.Uint16(raw[34:]) != crc16(raw[:34]) {
		return seed, errors.New("not a user nkey seed")
	}
	copy(seed[:], raw[2:34])
	return seed, nil
}

// Sign is the signature a seed gives one message: what a client proves to the server that answers with a nonce.
// The seed is a secret and is never part of an error.
func Sign(seed [32]byte, message []byte) []byte {
	return ed25519.Sign(ed25519.NewKeyFromSeed(seed[:]), message)
}

// encodeNKey is a prefix, the key and its CRC-16/XMODEM checksum, in base32 without padding.
func encodeNKey(prefix, key []byte) string {
	raw := make([]byte, 0, len(prefix)+len(key)+2)
	raw = append(raw, prefix...)
	raw = append(raw, key...)
	raw = binary.LittleEndian.AppendUint16(raw, crc16(raw))
	return base32.NewEncoding(nkeyBase32).WithPadding(base32.NoPadding).EncodeToString(raw)
}

// crc16 is CRC-16/XMODEM, which is what the nkeys libraries use: polynomial 0x1021, zero seed.
func crc16(data []byte) uint16 {
	crc := uint16(0)
	for _, value := range data {
		crc ^= uint16(value) << 8
		for range 8 {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}
