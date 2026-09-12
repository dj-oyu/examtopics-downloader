// Package uuidx generates UUIDv7 values and converts them between the
// canonical 16-byte BLOB form (used as SQLite primary keys) and a 26-char
// Crockford base32 representation (used in URLs and log output).
//
// The 16 → 26 encoding matches the ULID textual format: 128 data bits are
// left-padded with two zero bits, then split into 26 5-bit groups, MSB first.
// Crockford alphabet (no I, L, O, U) keeps the strings unambiguous in print
// and case-insensitive on decode. Strings remain lex-sortable in the same
// order as the underlying time-ordered UUIDv7 bytes.
package uuidx

import (
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var decodeMap = buildDecodeMap()

func buildDecodeMap() [256]int8 {
	var m [256]int8
	for i := range m {
		m[i] = -1
	}
	for i := range len(alphabet) {
		c := alphabet[i]
		m[c] = int8(i)
		if c >= 'A' && c <= 'Z' {
			m[c+('a'-'A')] = int8(i)
		}
	}
	// Crockford-compatible aliases for visually similar characters.
	for _, p := range [][2]byte{{'I', 1}, {'i', 1}, {'L', 1}, {'l', 1}, {'O', 0}, {'o', 0}} {
		m[p[0]] = int8(p[1])
	}
	return m
}

// New returns a freshly generated UUIDv7 as 16 raw bytes.
func New() ([16]byte, error) {
	u, err := uuid.NewV7()
	if err != nil {
		return [16]byte{}, err
	}
	return u, nil
}

// MustNew is the panic-on-error variant of New for cases where entropy
// failure is unrecoverable (e.g., DB row insertion).
func MustNew() [16]byte {
	b, err := New()
	if err != nil {
		panic(fmt.Errorf("uuidx: UUIDv7 generation failed: %w", err))
	}
	return b
}

// Encode formats 16 bytes as a 26-char Crockford base32 string.
// The result is lex-sortable in the same order as the input bytes
// when interpreted as a 128-bit big-endian integer.
func Encode(b [16]byte) string {
	var out [26]byte
	out[0] = alphabet[(b[0]&0xE0)>>5]
	out[1] = alphabet[b[0]&0x1F]
	out[2] = alphabet[(b[1]&0xF8)>>3]
	out[3] = alphabet[((b[1]&0x07)<<2)|((b[2]&0xC0)>>6)]
	out[4] = alphabet[(b[2]&0x3E)>>1]
	out[5] = alphabet[((b[2]&0x01)<<4)|((b[3]&0xF0)>>4)]
	out[6] = alphabet[((b[3]&0x0F)<<1)|((b[4]&0x80)>>7)]
	out[7] = alphabet[(b[4]&0x7C)>>2]
	out[8] = alphabet[((b[4]&0x03)<<3)|((b[5]&0xE0)>>5)]
	out[9] = alphabet[b[5]&0x1F]
	out[10] = alphabet[(b[6]&0xF8)>>3]
	out[11] = alphabet[((b[6]&0x07)<<2)|((b[7]&0xC0)>>6)]
	out[12] = alphabet[(b[7]&0x3E)>>1]
	out[13] = alphabet[((b[7]&0x01)<<4)|((b[8]&0xF0)>>4)]
	out[14] = alphabet[((b[8]&0x0F)<<1)|((b[9]&0x80)>>7)]
	out[15] = alphabet[(b[9]&0x7C)>>2]
	out[16] = alphabet[((b[9]&0x03)<<3)|((b[10]&0xE0)>>5)]
	out[17] = alphabet[b[10]&0x1F]
	out[18] = alphabet[(b[11]&0xF8)>>3]
	out[19] = alphabet[((b[11]&0x07)<<2)|((b[12]&0xC0)>>6)]
	out[20] = alphabet[(b[12]&0x3E)>>1]
	out[21] = alphabet[((b[12]&0x01)<<4)|((b[13]&0xF0)>>4)]
	out[22] = alphabet[((b[13]&0x0F)<<1)|((b[14]&0x80)>>7)]
	out[23] = alphabet[(b[14]&0x7C)>>2]
	out[24] = alphabet[((b[14]&0x03)<<3)|((b[15]&0xE0)>>5)]
	out[25] = alphabet[b[15]&0x1F]
	return string(out[:])
}

// Decode parses a 26-char Crockford base32 string into 16 bytes.
// Case-insensitive; Crockford aliases (I,L → 1, O → 0) are accepted.
// Returns an error for length mismatch, invalid chars, or a leading
// character that would encode more than 128 bits of data.
func Decode(s string) ([16]byte, error) {
	var b [16]byte
	if len(s) != 26 {
		return b, fmt.Errorf("uuidx: input length must be 26, got %d", len(s))
	}
	var n [26]byte
	for i := range 26 {
		v := decodeMap[s[i]]
		if v < 0 {
			return b, fmt.Errorf("uuidx: invalid character %q at position %d", s[i], i)
		}
		n[i] = byte(v)
	}
	if n[0] > 7 {
		return b, errors.New("uuidx: leading character encodes more than 128 bits")
	}
	b[0] = n[0]<<5 | n[1]
	b[1] = n[2]<<3 | n[3]>>2
	b[2] = n[3]<<6 | n[4]<<1 | n[5]>>4
	b[3] = n[5]<<4 | n[6]>>1
	b[4] = n[6]<<7 | n[7]<<2 | n[8]>>3
	b[5] = n[8]<<5 | n[9]
	b[6] = n[10]<<3 | n[11]>>2
	b[7] = n[11]<<6 | n[12]<<1 | n[13]>>4
	b[8] = n[13]<<4 | n[14]>>1
	b[9] = n[14]<<7 | n[15]<<2 | n[16]>>3
	b[10] = n[16]<<5 | n[17]
	b[11] = n[18]<<3 | n[19]>>2
	b[12] = n[19]<<6 | n[20]<<1 | n[21]>>4
	b[13] = n[21]<<4 | n[22]>>1
	b[14] = n[22]<<7 | n[23]<<2 | n[24]>>3
	b[15] = n[24]<<5 | n[25]
	return b, nil
}

// randomBytes is a small helper used only by tests; kept here so the test
// file can stay in the uuidx package without re-importing crypto/rand.
func randomBytes(n int) ([]byte, error) {
	out := make([]byte, n)
	if _, err := rand.Read(out); err != nil {
		return nil, err
	}
	return out, nil
}
