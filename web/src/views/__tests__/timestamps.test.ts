import { describe, expect, test } from "bun:test";
import { formatLocalTimestamp } from "../timestamps";

describe("formatLocalTimestamp", () => {
  test("converts SQLite UTC string to local-time YYYY-MM-DD HH:MM:SS", () => {
    // SQLite emits "2026-04-29 19:23:10" in UTC. Cross-check by parsing the
    // same instant ourselves and asserting the output matches what the
    // current TZ would render.
    const sqlite = "2026-04-29 19:23:10";
    const expected = (() => {
      const d = new Date("2026-04-29T19:23:10Z");
      const pad = (n: number) => String(n).padStart(2, "0");
      return (
        `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ` +
        `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
      );
    })();
    expect(formatLocalTimestamp(sqlite)).toBe(expected);
  });

  test("UTC timestamp is shifted by the local TZ offset (no longer raw UTC)", () => {
    const sqlite = "2026-04-29 19:23:10";
    const out = formatLocalTimestamp(sqlite);
    const offsetMin = new Date("2026-04-29T19:23:10Z").getTimezoneOffset();
    if (offsetMin === 0) {
      // Server is on UTC — output should equal the input
      expect(out).toBe(sqlite);
    } else {
      // Output should differ from the raw UTC string
      expect(out).not.toBe(sqlite);
    }
  });

  test("null and empty inputs return empty string", () => {
    expect(formatLocalTimestamp(null)).toBe("");
    expect(formatLocalTimestamp("")).toBe("");
  });

  test("unparseable input falls back to the raw value", () => {
    expect(formatLocalTimestamp("not a date")).toBe("not a date");
  });
});
