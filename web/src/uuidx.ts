// Bun mirror of internal/uuidx (Go). Produces UUIDv7 byte arrays and
// converts between the 16-byte BLOB form (used as SQLite primary keys)
// and a 26-char Crockford base32 representation (used in URLs and log
// output). The encoding follows the ULID textual format so that strings
// remain lex-sortable in the same order as the underlying time-ordered
// UUIDv7 bytes. Output must match the Go package byte-for-byte; round-trip
// property tests in uuidx.test.ts pin that contract.

const ALPHABET = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";

const decodeMap = (() => {
  const m = new Int8Array(256).fill(-1);
  for (let i = 0; i < ALPHABET.length; i++) {
    const c = ALPHABET.charCodeAt(i);
    m[c] = i;
    // Lowercase aliases for ASCII letters.
    if (c >= 0x41 && c <= 0x5a) m[c + 32] = i;
  }
  // Crockford-compatible aliases for visually similar characters.
  for (const [ch, v] of [
    ["I", 1],
    ["i", 1],
    ["L", 1],
    ["l", 1],
    ["O", 0],
    ["o", 0],
  ] as const) {
    m[(ch as string).charCodeAt(0)] = v;
  }
  return m;
})();

/**
 * Generate a fresh UUIDv7 as 16 raw bytes. The first 48 bits hold the
 * current epoch milliseconds (big-endian); the version (7) and variant
 * (10) fields are placed per RFC 9562; the remaining 74 bits come from
 * a CSPRNG.
 */
export function newUuidV7(): Uint8Array {
  const b = new Uint8Array(16);
  const now = Date.now();
  // 48-bit epoch ms, big-endian into bytes[0..5]. Use Math.floor for the
  // top 16 bits since JS bitwise ops are 32-bit.
  b[0] = Math.floor(now / 0x10000000000) & 0xff;
  b[1] = Math.floor(now / 0x100000000) & 0xff;
  b[2] = (now >>> 24) & 0xff;
  b[3] = (now >>> 16) & 0xff;
  b[4] = (now >>> 8) & 0xff;
  b[5] = now & 0xff;
  crypto.getRandomValues(b.subarray(6, 16));
  // Version 7 in the top nibble of byte 6.
  b[6] = (b[6] & 0x0f) | 0x70;
  // Variant 10xxxxxx in the top two bits of byte 8.
  b[8] = (b[8] & 0x3f) | 0x80;
  return b;
}

/**
 * Format 16 bytes as a 26-char Crockford base32 string. The result is
 * lex-sortable in the same order as the input bytes interpreted as a
 * 128-bit big-endian integer.
 */
export function encode(b: Uint8Array): string {
  if (b.length !== 16) {
    throw new Error(`uuidx: input length must be 16, got ${b.length}`);
  }
  const out = new Uint8Array(26);
  out[0] = ALPHABET.charCodeAt((b[0] & 0xe0) >> 5);
  out[1] = ALPHABET.charCodeAt(b[0] & 0x1f);
  out[2] = ALPHABET.charCodeAt((b[1] & 0xf8) >> 3);
  out[3] = ALPHABET.charCodeAt(((b[1] & 0x07) << 2) | ((b[2] & 0xc0) >> 6));
  out[4] = ALPHABET.charCodeAt((b[2] & 0x3e) >> 1);
  out[5] = ALPHABET.charCodeAt(((b[2] & 0x01) << 4) | ((b[3] & 0xf0) >> 4));
  out[6] = ALPHABET.charCodeAt(((b[3] & 0x0f) << 1) | ((b[4] & 0x80) >> 7));
  out[7] = ALPHABET.charCodeAt((b[4] & 0x7c) >> 2);
  out[8] = ALPHABET.charCodeAt(((b[4] & 0x03) << 3) | ((b[5] & 0xe0) >> 5));
  out[9] = ALPHABET.charCodeAt(b[5] & 0x1f);
  out[10] = ALPHABET.charCodeAt((b[6] & 0xf8) >> 3);
  out[11] = ALPHABET.charCodeAt(((b[6] & 0x07) << 2) | ((b[7] & 0xc0) >> 6));
  out[12] = ALPHABET.charCodeAt((b[7] & 0x3e) >> 1);
  out[13] = ALPHABET.charCodeAt(((b[7] & 0x01) << 4) | ((b[8] & 0xf0) >> 4));
  out[14] = ALPHABET.charCodeAt(((b[8] & 0x0f) << 1) | ((b[9] & 0x80) >> 7));
  out[15] = ALPHABET.charCodeAt((b[9] & 0x7c) >> 2);
  out[16] = ALPHABET.charCodeAt(((b[9] & 0x03) << 3) | ((b[10] & 0xe0) >> 5));
  out[17] = ALPHABET.charCodeAt(b[10] & 0x1f);
  out[18] = ALPHABET.charCodeAt((b[11] & 0xf8) >> 3);
  out[19] = ALPHABET.charCodeAt(((b[11] & 0x07) << 2) | ((b[12] & 0xc0) >> 6));
  out[20] = ALPHABET.charCodeAt((b[12] & 0x3e) >> 1);
  out[21] = ALPHABET.charCodeAt(((b[12] & 0x01) << 4) | ((b[13] & 0xf0) >> 4));
  out[22] = ALPHABET.charCodeAt(((b[13] & 0x0f) << 1) | ((b[14] & 0x80) >> 7));
  out[23] = ALPHABET.charCodeAt((b[14] & 0x7c) >> 2);
  out[24] = ALPHABET.charCodeAt(((b[14] & 0x03) << 3) | ((b[15] & 0xe0) >> 5));
  out[25] = ALPHABET.charCodeAt(b[15] & 0x1f);
  return new TextDecoder("ascii").decode(out);
}

/**
 * Parse a 26-char Crockford base32 string into 16 bytes. Decode is
 * case-insensitive and accepts the canonical Crockford aliases
 * (I/L → 1, O → 0). Throws on length mismatch, invalid characters,
 * or a leading character that would represent more than 128 bits.
 */
export function decode(s: string): Uint8Array {
  if (s.length !== 26) {
    throw new Error(`uuidx: input length must be 26, got ${s.length}`);
  }
  const n = new Uint8Array(26);
  for (let i = 0; i < 26; i++) {
    const v = decodeMap[s.charCodeAt(i)];
    if (v < 0) {
      throw new Error(
        `uuidx: invalid character ${JSON.stringify(s[i])} at position ${i}`
      );
    }
    n[i] = v;
  }
  if (n[0] > 7) {
    throw new Error("uuidx: leading character encodes more than 128 bits");
  }
  const b = new Uint8Array(16);
  // Each Uint8Array assignment truncates to 8 bits, matching Go's byte
  // overflow semantics. We mirror the Go bit-shift sequence verbatim so
  // any divergence shows up immediately in the round-trip property test.
  b[0] = (n[0] << 5) | n[1];
  b[1] = (n[2] << 3) | (n[3] >> 2);
  b[2] = (n[3] << 6) | (n[4] << 1) | (n[5] >> 4);
  b[3] = (n[5] << 4) | (n[6] >> 1);
  b[4] = (n[6] << 7) | (n[7] << 2) | (n[8] >> 3);
  b[5] = (n[8] << 5) | n[9];
  b[6] = (n[10] << 3) | (n[11] >> 2);
  b[7] = (n[11] << 6) | (n[12] << 1) | (n[13] >> 4);
  b[8] = (n[13] << 4) | (n[14] >> 1);
  b[9] = (n[14] << 7) | (n[15] << 2) | (n[16] >> 3);
  b[10] = (n[16] << 5) | n[17];
  b[11] = (n[18] << 3) | (n[19] >> 2);
  b[12] = (n[19] << 6) | (n[20] << 1) | (n[21] >> 4);
  b[13] = (n[21] << 4) | (n[22] >> 1);
  b[14] = (n[22] << 7) | (n[23] << 2) | (n[24] >> 3);
  b[15] = (n[24] << 5) | n[25];
  return b;
}

/** True if two byte arrays have identical length and content. */
export function bytesEqual(a: Uint8Array, b: Uint8Array): boolean {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) if (a[i] !== b[i]) return false;
  return true;
}
