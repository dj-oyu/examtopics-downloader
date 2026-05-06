// Admin-fetch support module for the /admin/fetch UI.
//
// Concerns:
//  1. Resolving the examtopicsdl binary on a release-zip layout (env →
//     exec dir → cwd → PATH), per §3.4 of docs/plans/portable-builds.md.
//  2. Loading the provider whitelist from `examtopicsdl providers` once
//     at module init so the form's <select> stays in sync with the Go
//     side without duplicating the constants list.
//  3. Driving a single in-flight fetch job: spawn, line-buffered ring
//     of recent stdout/stderr lines, broadcast to SSE subscribers,
//     final exit-code event.
//
// Auth (token compare, request guards) is handled by the routes in
// server.tsx — this module is intentionally transport-agnostic.

import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { spawnSync, type Subprocess } from "bun";
import { timingSafeEqual } from "node:crypto";

import { loadConfig } from "./config";

const IS_WINDOWS = process.platform === "win32";
const DOWNLOADER_BASENAME = IS_WINDOWS ? "examtopicsdl.exe" : "examtopicsdl";

let cachedBin: string | null = null;
let cachedBinError: string | null = null;

/**
 * Resolve the examtopicsdl binary path.
 *
 * Search order, matching docs/plans/portable-builds.md §3.4:
 *   1. EXAMTOPICSDL_BIN env (explicit override)
 *   2. <dir of process.execPath>/examtopicsdl[.exe] — release-zip layout
 *   3. <process.cwd()>/examtopicsdl[.exe]
 *   4. PATH lookup via Bun.which
 *
 * Throws when nothing is found. The error message is also cached on
 * the first call so subsequent callers see a stable failure mode.
 */
export function resolveDownloaderBin(): string {
  if (cachedBin) return cachedBin;
  if (cachedBinError) throw new Error(cachedBinError);

  const tried: string[] = [];

  const envOverride = process.env.EXAMTOPICSDL_BIN;
  if (envOverride) {
    tried.push(`EXAMTOPICSDL_BIN=${envOverride}`);
    if (existsSync(envOverride)) {
      cachedBin = envOverride;
      return cachedBin;
    }
    // Even if the explicit override doesn't exist on disk, try it via
    // Bun.which (it might be a bare command name shadowing PATH).
    const w = Bun.which(envOverride);
    if (w) {
      cachedBin = w;
      return cachedBin;
    }
  }

  const execDir = process.execPath ? dirname(process.execPath) : "";
  if (execDir) {
    const p = join(execDir, DOWNLOADER_BASENAME);
    tried.push(p);
    if (existsSync(p)) {
      cachedBin = p;
      return cachedBin;
    }
  }

  const cwdPath = join(process.cwd(), DOWNLOADER_BASENAME);
  tried.push(cwdPath);
  if (existsSync(cwdPath)) {
    cachedBin = cwdPath;
    return cachedBin;
  }

  const onPath = Bun.which(DOWNLOADER_BASENAME);
  if (onPath) {
    cachedBin = onPath;
    return cachedBin;
  }
  tried.push(`PATH lookup for ${DOWNLOADER_BASENAME}`);

  cachedBinError =
    `examtopicsdl binary not found. Tried (in order): ${tried.join(", ")}. ` +
    `Set EXAMTOPICSDL_BIN, place the binary next to examtopics-web, or add it to PATH.`;
  throw new Error(cachedBinError);
}

/** Reset the binary resolution cache. Test-only helper. */
export function resetDownloaderBinCache(): void {
  cachedBin = null;
  cachedBinError = null;
}

let cachedProviders: Set<string> | null = null;
let cachedProvidersError: string | null = null;

/**
 * Load the provider whitelist by spawning `examtopicsdl providers` and
 * parsing its newline-delimited stdout. Cached on the first call.
 *
 * Returns a Set of canonical provider slugs. Throws with a short error
 * description when the binary can't be located or the spawn failed —
 * the caller (POST /admin/fetch) surfaces this as a 503 so the GET
 * page still renders the form.
 */
export function loadProviders(): Set<string> {
  if (cachedProviders) return cachedProviders;
  if (cachedProvidersError) throw new Error(cachedProvidersError);

  let bin: string;
  try {
    bin = resolveDownloaderBin();
  } catch (e) {
    cachedProvidersError = `examtopicsdl unreachable: ${
      e instanceof Error ? e.message : String(e)
    }`;
    throw new Error(cachedProvidersError);
  }

  let result;
  try {
    result = spawnSync([bin, "providers"], { stdout: "pipe", stderr: "pipe" });
  } catch (e) {
    cachedProvidersError = `examtopicsdl unreachable: spawn failed: ${
      e instanceof Error ? e.message : String(e)
    }`;
    throw new Error(cachedProvidersError);
  }

  if (!result.success) {
    const tail = (result.stderr ?? "").toString().slice(-200).trim();
    cachedProvidersError = `examtopicsdl unreachable: providers exit ${result.exitCode}${
      tail ? `: ${tail}` : ""
    }`;
    throw new Error(cachedProvidersError);
  }

  const out = (result.stdout ?? "").toString();
  const set = new Set<string>();
  for (const raw of out.split(/\r?\n/)) {
    const line = raw.trim();
    if (line) set.add(line);
  }
  if (set.size === 0) {
    cachedProvidersError =
      "examtopicsdl unreachable: providers returned no output";
    throw new Error(cachedProvidersError);
  }
  cachedProviders = set;
  return cachedProviders;
}

/** Try-form of loadProviders that does not throw — returns null on failure. */
export function tryLoadProviders(): Set<string> | null {
  try {
    return loadProviders();
  } catch {
    return null;
  }
}

/** Reset the provider cache. Test-only helper. */
export function resetProvidersCache(): void {
  cachedProviders = null;
  cachedProvidersError = null;
}

// ---------------------------------------------------------------------------
// Admin token + auth helpers
// ---------------------------------------------------------------------------

export const ADMIN_COOKIE_NAME = "examtopics_admin";

export function getAdminToken(): string | null {
  const t = process.env.EXAMTOPICS_ADMIN_TOKEN;
  return t && t.length > 0 ? t : null;
}

/** Constant-time compare; returns false on length mismatch instead of throwing. */
export function tokenMatches(provided: string | null | undefined): boolean {
  const expected = getAdminToken();
  if (!expected) return false;
  if (!provided) return false;
  const a = Buffer.from(provided, "utf-8");
  const b = Buffer.from(expected, "utf-8");
  if (a.length !== b.length) return false;
  return timingSafeEqual(a, b);
}

// ---------------------------------------------------------------------------
// Slug validation (mirrors db.ts)
// ---------------------------------------------------------------------------

export const SLUG_RE = /^[A-Za-z0-9._-]+$/;

export function isValidSlug(slug: string): boolean {
  if (!SLUG_RE.test(slug)) return false;
  if (slug === "." || slug === "..") return false;
  return true;
}

// ---------------------------------------------------------------------------
// Single-job state + ring buffer
// ---------------------------------------------------------------------------

export type FetchLogLine = {
  stream: "stdout" | "stderr" | "system";
  text: string;
  ts: number;
};

export type FetchDoneEvent = { exitCode: number; ts: number };

export type FetchEvent =
  | { type: "line"; line: FetchLogLine }
  | { type: "done"; done: FetchDoneEvent };

export type FetchJob = {
  provider: string;
  slug: string;
  startedAt: number;
  bin: string;
  args: string[];
  proc: Subprocess<"ignore", "pipe", "pipe"> | null;
  // Ring buffer of recent log lines so a freshly-attached SSE subscriber
  // sees historical context, not just whatever streams in after attach.
  log: FetchLogLine[];
  done: FetchDoneEvent | null;
};

const RING_LIMIT = 500;
const subscribers = new Set<(event: FetchEvent) => void>();
let currentJob: FetchJob | null = null;

export function getCurrentJob(): FetchJob | null {
  return currentJob;
}

/** Snapshot for the GET view; immutable copy so callers can't mutate state. */
export function describeCurrentJob():
  | {
      provider: string;
      slug: string;
      startedAt: number;
      running: boolean;
      exitCode: number | null;
    }
  | null {
  if (!currentJob) return null;
  return {
    provider: currentJob.provider,
    slug: currentJob.slug,
    startedAt: currentJob.startedAt,
    running: currentJob.done === null,
    exitCode: currentJob.done?.exitCode ?? null,
  };
}

export function isJobInFlight(): boolean {
  return currentJob !== null && currentJob.done === null;
}

export function subscribeFetch(fn: (event: FetchEvent) => void): () => void {
  subscribers.add(fn);
  // Replay the ring buffer so the subscriber sees historical lines.
  if (currentJob) {
    for (const line of currentJob.log) {
      try {
        fn({ type: "line", line });
      } catch {}
    }
    if (currentJob.done) {
      try {
        fn({ type: "done", done: currentJob.done });
      } catch {}
    }
  }
  return () => {
    subscribers.delete(fn);
  };
}

function broadcast(event: FetchEvent): void {
  for (const fn of subscribers) {
    try {
      fn(event);
    } catch (e) {
      console.error("[admin/fetch] subscriber threw:", e);
    }
  }
}

function appendLine(stream: FetchLogLine["stream"], text: string): void {
  if (!currentJob) return;
  const line: FetchLogLine = { stream, text, ts: Date.now() };
  currentJob.log.push(line);
  if (currentJob.log.length > RING_LIMIT) {
    currentJob.log.splice(0, currentJob.log.length - RING_LIMIT);
  }
  broadcast({ type: "line", line });
}

/**
 * Pump a ReadableStream<Uint8Array> as utf-8 lines, splitting on
 * \r\n / \n / \r and forwarding each line via `onLine`. Trailing
 * partial data is flushed when the stream closes.
 */
async function pumpLines(
  stream: ReadableStream<Uint8Array> | null,
  onLine: (line: string) => void
): Promise<void> {
  if (!stream) return;
  const reader = stream.getReader();
  const decoder = new TextDecoder();
  let buf = "";
  try {
    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      if (value) buf += decoder.decode(value, { stream: true });
      let idx = buf.search(/\r\n|\n|\r/);
      while (idx !== -1) {
        const line = buf.slice(0, idx);
        const matchLen = buf[idx] === "\r" && buf[idx + 1] === "\n" ? 2 : 1;
        buf = buf.slice(idx + matchLen);
        onLine(line);
        idx = buf.search(/\r\n|\n|\r/);
      }
    }
    buf += decoder.decode();
    if (buf.length > 0) onLine(buf);
  } finally {
    try {
      reader.releaseLock();
    } catch {}
  }
}

export type StartFetchOptions = {
  provider: string;
  slug: string;
  /** Override the spawn implementation — wired up for tests. */
  spawnImpl?: typeof Bun.spawn;
};

export type StartFetchResult =
  | { ok: true; job: FetchJob }
  | { ok: false; status: 409 | 503; message: string };

/**
 * Start a fetch job. Caller (POST /admin/fetch) is responsible for
 * having validated `provider` against the whitelist and `slug` via
 * isValidSlug. We re-validate slug here as a defense-in-depth measure.
 */
export function startFetch(
  opts: StartFetchOptions
): StartFetchResult {
  if (isJobInFlight()) {
    return { ok: false, status: 409, message: "another fetch job is in flight" };
  }
  if (!isValidSlug(opts.slug)) {
    return { ok: false, status: 503, message: "invalid slug" };
  }

  let bin: string;
  try {
    bin = resolveDownloaderBin();
  } catch (e) {
    return {
      ok: false,
      status: 503,
      message: e instanceof Error ? e.message : String(e),
    };
  }

  const cfg = loadConfig();
  const dataDir = cfg.dataDir;
  const dbPath = resolve(dataDir, `${opts.slug}.db`);
  const args = [
    "fetch",
    "-p",
    opts.provider,
    "-s",
    opts.slug,
    "-sqlite",
    dbPath,
  ];

  const spawnImpl = opts.spawnImpl ?? Bun.spawn;
  let proc: Subprocess<"ignore", "pipe", "pipe">;
  try {
    proc = spawnImpl([bin, ...args], {
      cwd: dataDir,
      stdin: "ignore",
      stdout: "pipe",
      stderr: "pipe",
      env: { ...process.env },
    }) as Subprocess<"ignore", "pipe", "pipe">;
  } catch (e) {
    return {
      ok: false,
      status: 503,
      message: `spawn failed: ${e instanceof Error ? e.message : String(e)}`,
    };
  }

  const job: FetchJob = {
    provider: opts.provider,
    slug: opts.slug,
    startedAt: Date.now(),
    bin,
    args,
    proc,
    log: [],
    done: null,
  };
  currentJob = job;
  appendLine(
    "system",
    `spawned ${bin} ${args.join(" ")} (cwd=${dataDir})`
  );

  // Pipe both streams concurrently. Errors in one pipe should not leave
  // the other dangling — we still await proc.exited regardless.
  const stdoutPump = pumpLines(proc.stdout as unknown as ReadableStream<Uint8Array>, (l) =>
    appendLine("stdout", l)
  ).catch((e) => {
    appendLine("system", `stdout pump failed: ${e instanceof Error ? e.message : e}`);
  });
  const stderrPump = pumpLines(proc.stderr as unknown as ReadableStream<Uint8Array>, (l) =>
    appendLine("stderr", l)
  ).catch((e) => {
    appendLine("system", `stderr pump failed: ${e instanceof Error ? e.message : e}`);
  });

  // Wait for child exit + drain pumps, then mark the job complete.
  void (async () => {
    let exitCode = -1;
    try {
      exitCode = await proc.exited;
    } catch (e) {
      appendLine("system", `proc.exited threw: ${e instanceof Error ? e.message : e}`);
    }
    try {
      await Promise.all([stdoutPump, stderrPump]);
    } catch {}
    const done: FetchDoneEvent = { exitCode, ts: Date.now() };
    if (currentJob) {
      currentJob.done = done;
      appendLine("system", `exit code ${exitCode}`);
    }
    broadcast({ type: "done", done });
  })();

  return { ok: true, job };
}

/**
 * Test-only reset: clear in-flight state and subscribers. Production
 * code never calls this; the single-job invariant is enough.
 */
export function resetFetchStateForTests(): void {
  if (currentJob?.proc) {
    try {
      currentJob.proc.kill();
    } catch {}
  }
  currentJob = null;
  subscribers.clear();
}

/** Test-only seam: install a synthetic in-flight job. */
export function installFakeJobForTests(job: Partial<FetchJob> & { provider: string; slug: string }): void {
  currentJob = {
    provider: job.provider,
    slug: job.slug,
    startedAt: job.startedAt ?? Date.now(),
    bin: job.bin ?? "fake",
    args: job.args ?? [],
    proc: job.proc ?? null,
    log: job.log ?? [],
    done: job.done ?? null,
  };
}
