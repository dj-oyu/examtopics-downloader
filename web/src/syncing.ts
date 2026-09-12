// Admin-sync support module for the /admin/sync UI.
//
// The multi-host sync subcommands live in the Go CLI (`sync translations` /
// `sync snapshot`); rather than reimplement the merge in TypeScript, this module
// spawns the resolved `examtopicsdl` binary and streams its output, the same way
// admin.ts drives `fetch`. Single job in flight, line ring buffer, SSE fan-out.
//
// The merge result is parsed back out of the CLI's own stdout so the page can
// render the conflict list (field + question url) as links instead of asking the
// user to read the log.

import { existsSync, mkdirSync, readdirSync, statSync } from "node:fs";
import { basename, extname, join, resolve, sep } from "node:path";
import { spawnSync, type Subprocess } from "bun";

import { resolveDownloaderBin } from "./admin";
import { loadConfig } from "./config";
import { isValidSlug } from "./admin";

export const SYNC_LOG_RING = 400;

export type SyncStream = "stdout" | "stderr" | "system";

export type SyncLogLine = { stream: SyncStream; text: string; ts: number };

export type SyncConflict = { field: string; url: string };

export type TranslationsResult = {
  filledQuestions: number;
  filledChoices: number;
  /** Conflicts the CLI listed explicitly (it truncates after 20). */
  conflicts: SyncConflict[];
  /** Total conflicts reported, which can exceed conflicts.length. */
  conflictCount: number;
  overwritten: number;
  preferPeer: boolean;
  dryRun: boolean;
};

export type SyncDoneEvent = {
  exitCode: number;
  ts: number;
  result: TranslationsResult | null;
  snapshotPath: string | null;
};

export type SyncEvent =
  | { type: "line"; line: SyncLogLine }
  | { type: "done"; done: SyncDoneEvent };

export type SyncJobKind = "translations" | "snapshot";

export type SyncJob = {
  kind: SyncJobKind;
  slug: string;
  peerPath: string | null;
  dryRun: boolean;
  preferPeer: boolean;
  startedAt: number;
  bin: string;
  args: string[];
  proc: Subprocess<"ignore", "pipe", "pipe">;
  log: SyncLogLine[];
  done: SyncDoneEvent | null;
};

// --- pure helpers (unit-tested without a child process) --------------------

export function emptyTranslationsResult(
  preferPeer: boolean,
  dryRun: boolean
): TranslationsResult {
  return {
    filledQuestions: 0,
    filledChoices: 0,
    conflicts: [],
    conflictCount: 0,
    overwritten: 0,
    preferPeer,
    dryRun,
  };
}

const FIELD_RE = /^\s{4}(\S+)\s+(https?:\/\/\S+)\s*$/;

/**
 * Parse the CLI's summary block out of its stdout. Mirrors the output of
 * `examtopicsdl sync translations`:
 *
 *   merged translations: <dst> <- <src>
 *     filled question fields: 306
 *     filled choice fields:   602
 *     conflicts: 2 (kept this DB's wording; overwritten: 0)
 *       explanation_ja  https://…
 *     (re-run with --prefer peer …)
 */
export function parseTranslationsOutput(
  text: string,
  preferPeer = false,
  dryRun = false
): TranslationsResult {
  const out = emptyTranslationsResult(preferPeer, dryRun);
  for (const line of text.split(/\r?\n/)) {
    let m = line.match(/^\s*filled question fields:\s+(\d+)/);
    if (m) {
      out.filledQuestions = Number(m[1]);
      continue;
    }
    m = line.match(/^\s*filled choice fields:\s+(\d+)/);
    if (m) {
      out.filledChoices = Number(m[1]);
      continue;
    }
    m = line.match(/^\s*conflicts:\s+(\d+)/);
    if (m) {
      out.conflictCount = Number(m[1]);
      const ow = line.match(/overwritten:\s+(\d+)/);
      if (ow) out.overwritten = Number(ow[1]);
      continue;
    }
    m = line.match(FIELD_RE);
    if (m && m[1] !== "snapshot") {
      out.conflicts.push({ field: m[1], url: m[2] });
    }
  }
  return out;
}

/** Parse `snapshot written: <src> -> <out>`. */
export function parseSnapshotOutput(text: string): string | null {
  const m = text.match(/^\s*snapshot written:\s+\S+\s+->\s+(.+?)\s*$/m);
  return m ? m[1] : null;
}

export type PeerCandidate = {
  path: string;
  name: string;
  dir: string;
  size: number;
  mtimeMs: number;
  /** True when the file is itself an exam DB (slug.db) rather than a snapshot. */
  isExamDb: boolean;
};

/**
 * Files the user can pull translations from: every *.db in the data dir plus
 * everything in `<dataDir>/incoming`, which is where a snapshot copied from the
 * other machine usually lands (a Syncthing folder, a USB copy, scp).
 */
export function listPeerCandidates(dataDir?: string): PeerCandidate[] {
  const dir = dataDir ?? loadConfig().dataDir;
  const out: PeerCandidate[] = [];
  const seen = new Set<string>();
  const scan = (d: string) => {
    let names: string[];
    try {
      names = readdirSync(d);
    } catch {
      return;
    }
    for (const name of names) {
      if (extname(name).toLowerCase() !== ".db") continue;
      const p = join(d, name);
      if (seen.has(p)) continue;
      seen.add(p);
      let st;
      try {
        st = statSync(p);
      } catch {
        continue;
      }
      if (!st.isFile()) continue;
      const slug = basename(name, extname(name));
      out.push({
        path: p,
        name,
        dir: d,
        size: st.size,
        mtimeMs: st.mtimeMs,
        isExamDb: isValidSlug(slug),
      });
    }
  };
  scan(dir);
  scan(join(dir, "incoming"));
  out.sort((a, b) => b.mtimeMs - a.mtimeMs);
  return out;
}

/**
 * Resolve a snapshot name for download, refusing anything that escapes
 * `<dataDir>/outgoing`. Names come in over the query string, so this is the
 * guard against `../../etc/passwd`.
 */
export function resolveSnapshotDownload(
  name: string,
  dataDir?: string
): string | null {
  const dir = dataDir ?? loadConfig().dataDir;
  if (!name || name.includes("/") || name.includes("\\") || name.includes("..")) {
    return null;
  }
  const outgoing = join(dir, "outgoing");
  const p = resolve(outgoing, name);
  if (!p.startsWith(outgoing + sep)) return null;
  return existsSync(p) ? p : null;
}

export function outgoingDir(dataDir?: string): string {
  return join(dataDir ?? loadConfig().dataDir, "outgoing");
}

// --- job runner ------------------------------------------------------------

let currentJob: SyncJob | null = null;
const subscribers = new Set<(event: SyncEvent) => void>();

export function currentSyncJob(): SyncJob | null {
  return currentJob;
}

export function isSyncJobInFlight(): boolean {
  return currentJob !== null && currentJob.done === null;
}

export function subscribeSync(fn: (event: SyncEvent) => void): () => void {
  subscribers.add(fn);
  // Replay so a client that connects mid-job still sees the output.
  if (currentJob) {
    for (const line of currentJob.log) fn({ type: "line", line });
    if (currentJob.done) fn({ type: "done", done: currentJob.done });
  }
  return () => subscribers.delete(fn);
}

function emit(event: SyncEvent): void {
  for (const fn of subscribers) {
    try {
      fn(event);
    } catch {
      // A subscriber throwing must not break the job.
    }
  }
}

function appendLine(stream: SyncStream, text: string): void {
  if (!currentJob) return;
  const line: SyncLogLine = { stream, text, ts: Date.now() };
  currentJob.log.push(line);
  if (currentJob.log.length > SYNC_LOG_RING) currentJob.log.shift();
  emit({ type: "line", line });
}

async function pumpLines(
  stream: ReadableStream<Uint8Array>,
  onLine: (line: string) => void
): Promise<void> {
  const reader = stream.getReader();
  const decoder = new TextDecoder();
  let buf = "";
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });
    let idx: number;
    while ((idx = buf.indexOf("\n")) >= 0) {
      onLine(buf.slice(0, idx).replace(/\r$/, ""));
      buf = buf.slice(idx + 1);
    }
  }
  if (buf) onLine(buf.replace(/\r$/, ""));
}

export type StartSyncOptions = {
  slug: string;
  peerPath?: string;
  preferPeer?: boolean;
  dryRun?: boolean;
  spawnImpl?: typeof Bun.spawn;
};

export type StartSyncResult =
  | { ok: true }
  | { ok: false; status: number; message: string };

function startJob(
  kind: SyncJobKind,
  opts: StartSyncOptions,
  build: (dataDir: string) => string[] | { error: string }
): StartSyncResult {
  if (isSyncJobInFlight()) {
    return { ok: false, status: 409, message: "another sync job is in flight" };
  }
  if (!isValidSlug(opts.slug)) {
    return { ok: false, status: 400, message: "invalid slug" };
  }
  // Validate the request before reaching for the binary: a missing peer file is
  // the user's problem to fix, and reporting it beats reporting PATH.
  const dataDir = loadConfig().dataDir;
  const built = build(dataDir);
  if (!Array.isArray(built)) {
    return { ok: false, status: 400, message: built.error };
  }
  const args = built;
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

  currentJob = {
    kind,
    slug: opts.slug,
    peerPath: opts.peerPath ?? null,
    dryRun: opts.dryRun === true,
    preferPeer: opts.preferPeer === true,
    startedAt: Date.now(),
    bin,
    args,
    proc,
    log: [],
    done: null,
  };
  appendLine("system", `spawned ${bin} ${args.join(" ")} (cwd=${dataDir})`);

  const stdoutChunks: string[] = [];
  const stdoutPump = pumpLines(proc.stdout as unknown as ReadableStream<Uint8Array>, (l) => {
    stdoutChunks.push(l);
    appendLine("stdout", l);
  }).catch((e) => appendLine("system", `stdout pump failed: ${e}`));
  const stderrPump = pumpLines(proc.stderr as unknown as ReadableStream<Uint8Array>, (l) =>
    appendLine("stderr", l)
  ).catch((e) => appendLine("system", `stderr pump failed: ${e}`));

  void (async () => {
    const exitCode = await proc.exited;
    await Promise.all([stdoutPump, stderrPump]);
    const text = stdoutChunks.join("\n");
    const result =
      kind === "translations"
        ? parseTranslationsOutput(text, currentJob?.preferPeer, currentJob?.dryRun)
        : null;
    const snapshotPath = kind === "snapshot" ? parseSnapshotOutput(text) : null;
    const done: SyncDoneEvent = { exitCode, ts: Date.now(), result, snapshotPath };
    if (currentJob) currentJob.done = done;
    appendLine("system", `exit ${exitCode}`);
    emit({ type: "done", done });
  })();

  return { ok: true };
}

/** `examtopicsdl sync translations` — pull the peer's translated wording in. */
export function startTranslationsSync(opts: StartSyncOptions): StartSyncResult {
  return startJob("translations", opts, (dataDir) => {
    if (!opts.peerPath) return { error: "peer snapshot path is required" };
    const peer = resolve(opts.peerPath);
    if (!existsSync(peer)) return { error: `peer DB not found: ${peer}` };
    const dst = join(dataDir, `${opts.slug}.db`);
    if (resolve(dst) === peer) {
      return { error: "peer DB is this DB — pick the other machine's snapshot" };
    }
    const args = [
      "sync",
      "translations",
      "-d",
      dst,
      "--from",
      peer,
      "--allow-wal",
    ];
    if (opts.dryRun) args.push("--dry-run");
    if (opts.preferPeer) args.push("--prefer", "peer");
    return args;
  });
}

/** `examtopicsdl sync snapshot` into `<dataDir>/outgoing`, ready to copy out. */
export function startSnapshot(opts: { slug: string; spawnImpl?: typeof Bun.spawn }): StartSyncResult {
  return startJob("snapshot", opts, (dataDir) => {
    const dst = join(dataDir, `${opts.slug}.db`);
    if (!existsSync(dst)) return { error: `exam DB not found: ${dst}` };
    const out = join(
      outgoingDir(dataDir),
      `${opts.slug}-${new Date().toISOString().replace(/[:.]/g, "-")}.db`
    );
    try {
      mkdirSync(outgoingDir(dataDir), { recursive: true });
    } catch (e) {
      return { error: `cannot create ${outgoingDir(dataDir)}: ${e}` };
    }
    return ["sync", "snapshot", "-d", dst, "-o", out];
  });
}

// --- test hooks ------------------------------------------------------------

export function resetSyncStateForTests(): void {
  currentJob = null;
  subscribers.clear();
}

export function installFakeSyncJobForTests(
  job: Partial<SyncJob> & { kind: SyncJobKind; slug: string }
): void {
  currentJob = {
    peerPath: null,
    dryRun: false,
    preferPeer: false,
    startedAt: Date.now(),
    bin: "/fake/examtopicsdl",
    args: [],
    proc: null as unknown as Subprocess<"ignore", "pipe", "pipe">,
    log: [],
    done: null,
    ...job,
  };
}

/** Used by the CLI resolver test hook in admin.ts. */
export const _spawnSync = spawnSync;
