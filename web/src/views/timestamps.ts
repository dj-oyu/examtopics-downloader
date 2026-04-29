/**
 * Format a SQLite "YYYY-MM-DD HH:MM:SS" UTC timestamp as the equivalent
 * local-time "YYYY-MM-DD HH:MM:SS" string.
 *
 * SQLite's `CURRENT_TIMESTAMP` always returns UTC. JavaScript's `Date`
 * parser treats the bare "YYYY-MM-DD HH:MM:SS" format as local time, so we
 * splice in a `T` and append `Z` to force ISO 8601 UTC parsing, then read
 * back the components in the server's (= for a local dev tool, the user's)
 * timezone.
 */
export function formatLocalTimestamp(sqliteUtc: string | null): string {
  if (!sqliteUtc) return "";
  const d = new Date(sqliteUtc.replace(" ", "T") + "Z");
  if (Number.isNaN(d.getTime())) return sqliteUtc;
  const pad = (n: number): string => String(n).padStart(2, "0");
  return (
    `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ` +
    `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
  );
}
