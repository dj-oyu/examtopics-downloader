import { describe, expect, test } from "bun:test";
import { bytesEqual, decode, encode, newUuidV7 } from "./uuidx";

function fillByte(v: number): Uint8Array {
  const b = new Uint8Array(16);
  b.fill(v);
  return b;
}

describe("encode/decode fixed cases", () => {
  test("all-zero round-trips through 26 zero chars", () => {
    const z = new Uint8Array(16);
    expect(encode(z)).toBe("0".repeat(26));
    expect(bytesEqual(decode("0".repeat(26)), z)).toBe(true);
  });

  test("all-FF round-trips through 7ZZ...ZZ", () => {
    const f = fillByte(0xff);
    expect(encode(f)).toBe("7ZZZZZZZZZZZZZZZZZZZZZZZZZ");
    expect(bytesEqual(decode("7ZZZZZZZZZZZZZZZZZZZZZZZZZ"), f)).toBe(true);
  });
});

describe("encode/decode round-trip property", () => {
  // Mirrors Go's TestEncodeDecodeRoundtripRandom — 10000 random byte
  // arrays must survive encode→decode with byte-for-byte identity.
  // Required by §7 of docs/plans/portable-builds.md.
  test("10000 random round-trips", () => {
    const buf = new Uint8Array(16);
    for (let i = 0; i < 10000; i++) {
      crypto.getRandomValues(buf);
      const s = encode(buf);
      expect(s.length).toBe(26);
      const back = decode(s);
      if (!bytesEqual(back, buf)) {
        throw new Error(
          `roundtrip mismatch via ${s}: in=${[...buf]} out=${[...back]}`
        );
      }
    }
  });
});

describe("encode invariants", () => {
  test("always produces 26 chars across the top byte sweep", () => {
    const b = new Uint8Array(16);
    for (let i = 0; i < 256; i++) {
      b[0] = i;
      expect(encode(b).length).toBe(26);
    }
  });

  test("preserves lex order", () => {
    const pairs: [Uint8Array, Uint8Array][] = [
      [new Uint8Array(16), fillByte(0xff)],
      [
        Uint8Array.from([0, 0, 0, 1, ...new Array(12).fill(0)]),
        Uint8Array.from([0, 0, 0, 2, ...new Array(12).fill(0)]),
      ],
      [
        Uint8Array.from([0x7f, 0xff, 0xff, 0xff, ...new Array(12).fill(0)]),
        Uint8Array.from([0x80, 0x00, 0x00, 0x00, ...new Array(12).fill(0)]),
      ],
    ];
    for (const [lo, hi] of pairs) {
      const l = encode(lo);
      const h = encode(hi);
      expect(l < h).toBe(true);
    }
  });
});

describe("decode error handling", () => {
  test("rejects bad length", () => {
    for (const s of ["", "0", "0".repeat(25), "0".repeat(27)]) {
      expect(() => decode(s)).toThrow();
    }
  });

  test("rejects invalid characters", () => {
    // U is excluded from Crockford base32.
    expect(() => decode("0".repeat(25) + "U")).toThrow();
  });

  test("rejects an oversized first character", () => {
    // First char > 7 implies more than 128 bits.
    for (const c of ["8", "9", "A", "Z"]) {
      expect(() => decode(c + "0".repeat(25))).toThrow();
    }
  });

  test("accepts Crockford aliases I/L → 1 and O → 0", () => {
    const canonical = "10000000000000000000000010";
    const want = decode(canonical);
    for (const alias of [
      "I0000000000000000000000010",
      "L0000000000000000000000010",
      "i0000000000000000000000010",
      "10000O00000000000000000010",
    ]) {
      const got = decode(alias);
      expect(bytesEqual(got, want)).toBe(true);
    }
  });
});

describe("newUuidV7", () => {
  test("returns 16 bytes with version=7 and variant=10", () => {
    const b = newUuidV7();
    expect(b.length).toBe(16);
    expect((b[6] & 0xf0) >> 4).toBe(7);
    expect((b[8] & 0xc0) >> 6).toBe(0b10);
  });

  test("two consecutive ids differ", () => {
    const a = newUuidV7();
    const b = newUuidV7();
    expect(bytesEqual(a, b)).toBe(false);
  });

  test("encoded form survives a round-trip", () => {
    for (let i = 0; i < 100; i++) {
      const b = newUuidV7();
      const s = encode(b);
      expect(s.length).toBe(26);
      expect(bytesEqual(decode(s), b)).toBe(true);
    }
  });

  test("time prefix tracks Date.now() within 1 second", () => {
    const before = Date.now();
    const b = newUuidV7();
    const after = Date.now();
    // First 48 bits = epoch ms, big-endian.
    const ms =
      b[0] * 0x10000000000 +
      b[1] * 0x100000000 +
      b[2] * 0x1000000 +
      b[3] * 0x10000 +
      b[4] * 0x100 +
      b[5];
    expect(ms).toBeGreaterThanOrEqual(before);
    expect(ms).toBeLessThanOrEqual(after);
  });
});
