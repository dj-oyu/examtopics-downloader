package uuidx

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestEncodeDecodeRoundtripFixed(t *testing.T) {
	cases := []struct {
		name string
		in   [16]byte
		want string
	}{
		{"all-zero", [16]byte{}, strings.Repeat("0", 26)},
		{"all-FF", fillByte(0xFF), "7ZZZZZZZZZZZZZZZZZZZZZZZZZ"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Encode(c.in)
			if got != c.want {
				t.Fatalf("Encode(%x) = %q, want %q", c.in, got, c.want)
			}
			back, err := Decode(got)
			if err != nil {
				t.Fatalf("Decode(%q) error: %v", got, err)
			}
			if back != c.in {
				t.Fatalf("Decode(Encode(x)) = %x, want %x", back, c.in)
			}
		})
	}
}

// TestEncodeDecodeRoundtripRandom exercises the round-trip property over
// 10000 random byte arrays. Required by §7 of the portable-builds plan.
func TestEncodeDecodeRoundtripRandom(t *testing.T) {
	const n = 10000
	for range n {
		raw, err := randomBytes(16)
		if err != nil {
			t.Fatalf("rand: %v", err)
		}
		var in [16]byte
		copy(in[:], raw)
		s := Encode(in)
		if len(s) != 26 {
			t.Fatalf("encoded length = %d, want 26 (input %x)", len(s), in)
		}
		out, err := Decode(s)
		if err != nil {
			t.Fatalf("Decode(%q) error: %v", s, err)
		}
		if out != in {
			t.Fatalf("roundtrip mismatch: in=%x out=%x via %q", in, out, s)
		}
	}
}

func TestEncodeAlways26Chars(t *testing.T) {
	for i := range 256 {
		var in [16]byte
		in[0] = byte(i) // sweep top byte
		s := Encode(in)
		if len(s) != 26 {
			t.Fatalf("len=%d for top byte %02x", len(s), i)
		}
	}
}

func TestDecodeRejectsBadLength(t *testing.T) {
	for _, s := range []string{"", "0", strings.Repeat("0", 25), strings.Repeat("0", 27)} {
		if _, err := Decode(s); err == nil {
			t.Fatalf("Decode(%q) expected error, got nil", s)
		}
	}
}

func TestDecodeRejectsInvalidChar(t *testing.T) {
	// 'U' is excluded from Crockford base32.
	bad := strings.Repeat("0", 25) + "U"
	if _, err := Decode(bad); err == nil {
		t.Fatalf("Decode(%q) expected error, got nil", bad)
	}
}

func TestDecodeAcceptsCrockfordAliases(t *testing.T) {
	// I/L are aliases for 1, O is alias for 0.
	canonical := "10000000000000000000000010"
	for _, alias := range []string{
		"I0000000000000000000000010", // I → 1
		"L0000000000000000000000010", // L → 1
		"i0000000000000000000000010", // lowercase
		"10000O00000000000000000010", // O → 0 (no-op since already 0, but exercises map)
	} {
		want, _ := Decode(canonical)
		got, err := Decode(alias)
		if err != nil {
			t.Fatalf("Decode(%q) error: %v", alias, err)
		}
		if got != want {
			t.Fatalf("alias %q decoded to %x, want %x", alias, got, want)
		}
	}
}

func TestDecodeRejectsOversizedFirstChar(t *testing.T) {
	// First char must be 0-7; any value >7 implies more than 128 bits.
	for _, c := range []byte{'8', '9', 'A', 'Z'} {
		s := string(c) + strings.Repeat("0", 25)
		if _, err := Decode(s); err == nil {
			t.Fatalf("Decode(%q) expected error, got nil", s)
		}
	}
}

func TestNewProducesUUIDv7(t *testing.T) {
	b, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	u, err := uuid.FromBytes(b[:])
	if err != nil {
		t.Fatalf("FromBytes: %v", err)
	}
	if u.Version() != 7 {
		t.Fatalf("version = %d, want 7", u.Version())
	}
}

// TestEncodePreservesLexOrder confirms that two byte arrays compared as
// big-endian 128-bit integers retain their order after encoding. UUIDv7's
// time-prefix relies on this for natural chronological listing in URLs.
func TestEncodePreservesLexOrder(t *testing.T) {
	pairs := []struct {
		lo, hi [16]byte
	}{
		{[16]byte{}, fillByte(0xFF)},
		{[16]byte{0x00, 0x00, 0x00, 0x01}, [16]byte{0x00, 0x00, 0x00, 0x02}},
		{[16]byte{0x7F, 0xFF, 0xFF, 0xFF}, [16]byte{0x80, 0x00, 0x00, 0x00}},
	}
	for _, p := range pairs {
		l, h := Encode(p.lo), Encode(p.hi)
		if !(l < h) {
			t.Fatalf("lex order violated: %q !< %q", l, h)
		}
	}
}

func fillByte(v byte) [16]byte {
	var b [16]byte
	for i := range b {
		b[i] = v
	}
	return b
}
