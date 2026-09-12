import {
  closeThread,
  discoverExams,
  getOpenThreadIdForQuestion,
  getThreadAgentSessionId,
  getThreadLastRole,
  getThreadStatus,
  listOpenThreads,
} from "./db";
import { join } from "node:path";
import { loadConfig } from "./config";
import { logEvent, summarize, LOG_PATH } from "./agent_log";

console.log(`[agent] log file: ${LOG_PATH}`);

// SPAWN_CWD is where agent.ts's child processes run. Using process.cwd()
// keeps the value real in both `bun run` and `bun build --compile` modes —
// the previous import.meta.dir-based PROJECT_ROOT became a virtual path
// inside compiled binaries.
const SPAWN_CWD = process.cwd();
// Web no longer spawns claude directly for explain or retranslate. Both
// flows now go through the Go binary, which owns the read-row → spawn
// adapter → validate → write-back loop. Config-driven so a release zip
// that places the binary next to the web server still works with a
// relative hint.
const EXAMTOPICSDL_BIN = process.env.EXAMTOPICSDL_BIN ?? "examtopicsdl";

// translateOverrideArgs lets the web side bolt -client / -model onto the
// spawned `examtopicsdl translate ...` invocation when an env var pins
// the choice; otherwise the Go binary falls back to its own resolution
// (config.json's tools.translate.{client,model} → built-in defaults).
// Keeping this as overrides — rather than hard-coding "claude" — means
// edits to config.json take effect without a web restart.
function translateOverrideArgs(): string[] {
  const out: string[] = [];
  const c = process.env.EXAMTOPICS_TRANSLATE_CLIENT;
  if (c) out.push("-client", c);
  const m = process.env.EXAMTOPICS_TRANSLATE_MODEL;
  if (m) out.push("-model", m);
  return out;
}

type ExplainJob = { kind: "explain"; slug: string; tid: string };
type RetransJob = { kind: "retranslate"; slug: string; qid: number };
type Job = ExplainJob | RetransJob;
type CloseKind = "resolved" | "dismissed";

const inFlight = new Set<string>();
const stack: Job[] = [];
const pendingClose = new Map<string, CloseKind>();
const subscribers = new Map<string, Set<(event: SseEvent) => void>>();
let running = false;

export type SseEvent =
  | { type: "spawning" }
  | { type: "agent-message" }
  | { type: "error"; message: string }
  | { type: "closed"; kind: CloseKind };

export type QuestionSseEvent =
  | { type: "translation-updated" }
  | { type: "translation-failed"; message: string };

const questionSubscribers = new Map<
  string,
  Set<(event: QuestionSseEvent) => void>
>();

const explainKey = (slug: string, tid: string) => `explain:${slug}:${tid}`;
const retransKey = (slug: string, qid: number) =>
  `retrans:${slug}:${qid}`;
const jobKey = (j: Job): string =>
  j.kind === "explain"
    ? explainKey(j.slug, j.tid)
    : retransKey(j.slug, j.qid);


/**
 * Scan every exam DB for open threads whose last message is from the user
 * and enqueue them. Intended to be called once at server boot so threads
 * left awaiting agent reply across a restart get picked up.
 */
export function recoverAwaitingThreads(): void {
  let found = 0;
  let enqueued = 0;
  try {
    for (const exam of discoverExams()) {
      const rows = listOpenThreads(exam.slug, "agent");
      for (const r of rows) {
        found++;
        const before = stack.length;
        enqueueExplain(exam.slug, r.id);
        if (stack.length > before) enqueued++;
      }
    }
  } catch (e) {
    console.error("[agent] recoverAwaitingThreads failed:", e);
    logEvent("error_notified", {
      slug: "*",
      tid: -1,
      message: e instanceof Error ? e.message : String(e),
    });
  }
  console.log(
    `[agent] boot recovery: found=${found} enqueued=${enqueued}`
  );
}

export function enqueueExplain(slug: string, tid: string): void {
  const key = explainKey(slug, tid);
  if (inFlight.has(key)) {
    logEvent("enqueue_skipped", { slug, tid, kind: "explain", reason: "in_flight" });
    return;
  }
  if (stack.some((j) => jobKey(j) === key)) {
    logEvent("enqueue_skipped", { slug, tid, kind: "explain", reason: "already_queued" });
    return;
  }
  const status = getThreadStatus(slug, tid);
  if (status !== "open") {
    logEvent("enqueue_skipped", {
      slug,
      tid,
      kind: "explain",
      reason: "thread_not_open",
      status,
    });
    return;
  }
  const lastRole = getThreadLastRole(slug, tid);
  if (lastRole !== "user") {
    logEvent("enqueue_skipped", {
      slug,
      tid,
      kind: "explain",
      reason: "last_role_not_user",
      last_role: lastRole,
    });
    return;
  }
  stack.push({ kind: "explain", slug, tid });
  logEvent("enqueue", { slug, tid, kind: "explain", queue_depth: stack.length });
  void drainQueue();
}

export function enqueueRetranslate(slug: string, qid: number): void {
  const key = retransKey(slug, qid);
  if (inFlight.has(key)) {
    logEvent("enqueue_skipped", { slug, qid, kind: "retranslate", reason: "in_flight" });
    return;
  }
  if (stack.some((j) => jobKey(j) === key)) {
    logEvent("enqueue_skipped", { slug, qid, kind: "retranslate", reason: "already_queued" });
    return;
  }
  stack.push({ kind: "retranslate", slug, qid });
  logEvent("enqueue", { slug, qid, kind: "retranslate", queue_depth: stack.length });
  void drainQueue();
}

export function isRetranslatePending(slug: string, qid: number): boolean {
  const key = retransKey(slug, qid);
  if (inFlight.has(key)) return true;
  return stack.some((j) => jobKey(j) === key);
}

export function isExplainPending(slug: string, tid: string): boolean {
  const key = explainKey(slug, tid);
  if (inFlight.has(key)) return true;
  return stack.some((j) => jobKey(j) === key);
}

/**
 * Returns true if the close was applied immediately, false if buffered until
 * the in-flight agent run finishes.
 */
export function requestClose(
  slug: string,
  tid: string,
  kind: CloseKind
): boolean {
  const key = explainKey(slug, tid);
  if (inFlight.has(key)) {
    pendingClose.set(key, kind);
    logEvent("close_deferred", { slug, tid, kind });
    return false;
  }
  closeThread(slug, tid, kind);
  notify(slug, tid, { type: "closed", kind });
  logEvent("close_immediate", { slug, tid, kind });
  return true;
}

export function subscribe(
  slug: string,
  tid: string,
  fn: (event: SseEvent) => void
): () => void {
  const subKey = `${slug}:${tid}`;
  let set = subscribers.get(subKey);
  if (!set) {
    set = new Set();
    subscribers.set(subKey, set);
  }
  set.add(fn);
  logEvent("subscriber_added", {
    slug,
    tid,
    subscriber_count: set.size,
  });
  return () => {
    const s = subscribers.get(subKey);
    if (!s) return;
    s.delete(fn);
    const remaining = s.size;
    if (s.size === 0) subscribers.delete(subKey);
    logEvent("subscriber_removed", {
      slug,
      tid,
      subscriber_count: remaining,
    });
  };
}

function notify(slug: string, tid: string, event: SseEvent): void {
  const set = subscribers.get(`${slug}:${tid}`);
  if (!set) return;
  for (const fn of set) {
    try {
      fn(event);
    } catch (e) {
      console.error("[agent] subscriber threw:", e);
    }
  }
}

export function subscribeQuestion(
  slug: string,
  qid: number,
  fn: (event: QuestionSseEvent) => void
): () => void {
  const key = `q:${slug}:${qid}`;
  let set = questionSubscribers.get(key);
  if (!set) {
    set = new Set();
    questionSubscribers.set(key, set);
  }
  set.add(fn);
  logEvent("q_subscriber_added", { slug, qid, subscriber_count: set.size });
  return () => {
    const s = questionSubscribers.get(key);
    if (!s) return;
    s.delete(fn);
    const remaining = s.size;
    if (s.size === 0) questionSubscribers.delete(key);
    logEvent("q_subscriber_removed", {
      slug,
      qid,
      subscriber_count: remaining,
    });
  };
}

function notifyQuestion(
  slug: string,
  qid: number,
  event: QuestionSseEvent
): void {
  const set = questionSubscribers.get(`q:${slug}:${qid}`);
  if (!set) return;
  for (const fn of set) {
    try {
      fn(event);
    } catch (e) {
      console.error("[agent] question subscriber threw:", e);
    }
  }
}

async function drainQueue(): Promise<void> {
  if (running) return;
  running = true;
  logEvent("drain_start", { queue_depth: stack.length });
  try {
    while (stack.length > 0) {
      const job = stack.pop()!;
      const key = jobKey(job);
      if (inFlight.has(key)) continue;

      if (job.kind === "explain") {
        if (getThreadStatus(job.slug, job.tid) !== "open") continue;
        if (getThreadLastRole(job.slug, job.tid) !== "user") continue;
      }

      inFlight.add(key);
      const startedAt = Date.now();

      if (job.kind === "explain") {
        logEvent("job_start", {
          slug: job.slug,
          tid: job.tid,
          kind: "explain",
          prior_session_id: getThreadAgentSessionId(job.slug, job.tid),
          queue_depth: stack.length,
        });
        notify(job.slug, job.tid, { type: "spawning" });
      } else {
        logEvent("job_start", {
          slug: job.slug,
          qid: job.qid,
          kind: "retranslate",
          queue_depth: stack.length,
        });
      }

      let outcome: "ok" | "error" = "ok";
      let errorMsg: string | null = null;
      try {
        if (job.kind === "explain") {
          await runExplain(job.slug, job.tid);
          notify(job.slug, job.tid, { type: "agent-message" });
          logEvent("agent_message_notified", {
            slug: job.slug,
            tid: job.tid,
          });
        } else {
          await runRetranslate(job.slug, job.qid);
        }
      } catch (e) {
        outcome = "error";
        errorMsg = e instanceof Error ? e.message : String(e);
        if (job.kind === "explain") {
          console.error(
            `[agent] thread ${job.slug}#${job.tid} failed:`,
            errorMsg
          );
          notify(job.slug, job.tid, { type: "error", message: errorMsg });
          logEvent("error_notified", {
            slug: job.slug,
            tid: job.tid,
            kind: "explain",
            message: errorMsg,
          });
        } else {
          console.error(
            `[agent] retranslate ${job.slug}#${job.qid} failed:`,
            errorMsg
          );
          logEvent("error_notified", {
            slug: job.slug,
            qid: job.qid,
            kind: "retranslate",
            message: errorMsg,
          });
        }
      } finally {
        inFlight.delete(key);
        let reenqueued = false;

        if (job.kind === "explain") {
          const close = pendingClose.get(key);
          if (close) {
            closeThread(job.slug, job.tid, close);
            pendingClose.delete(key);
            notify(job.slug, job.tid, { type: "closed", kind: close });
            logEvent("close_applied_after_run", {
              slug: job.slug,
              tid: job.tid,
              kind: close,
            });
          } else if (
            getThreadStatus(job.slug, job.tid) === "open" &&
            getThreadLastRole(job.slug, job.tid) === "user"
          ) {
            stack.push({ kind: "explain", slug: job.slug, tid: job.tid });
            reenqueued = true;
            logEvent("reenqueue_tail_user", {
              slug: job.slug,
              tid: job.tid,
              queue_depth: stack.length,
            });
          }
          logEvent("job_end", {
            slug: job.slug,
            tid: job.tid,
            kind: "explain",
            outcome,
            error: errorMsg,
            duration_ms: Date.now() - startedAt,
            reenqueued,
          });
        } else {
          if (outcome === "ok") {
            notifyQuestion(job.slug, job.qid, { type: "translation-updated" });
            logEvent("translation_updated_notified", {
              slug: job.slug,
              qid: job.qid,
            });
            // After a successful retranslate, if there's an open thread on
            // this question awaiting an agent reply, re-enqueue the explain
            // job so the autoresponder can revisit with the corrected
            // translation.
            const tid = getOpenThreadIdForQuestion(job.slug, job.qid);
            if (
              tid !== null &&
              getThreadLastRole(job.slug, tid) === "user"
            ) {
              const explKey = explainKey(job.slug, tid);
              if (
                !inFlight.has(explKey) &&
                !stack.some((s) => jobKey(s) === explKey)
              ) {
                stack.push({ kind: "explain", slug: job.slug, tid });
                reenqueued = true;
                logEvent("retrans_reenqueue_explain", {
                  slug: job.slug,
                  qid: job.qid,
                  tid,
                  queue_depth: stack.length,
                });
              }
            }
          } else {
            notifyQuestion(job.slug, job.qid, {
              type: "translation-failed",
              message: errorMsg ?? "unknown error",
            });
            logEvent("translation_failed_notified", {
              slug: job.slug,
              qid: job.qid,
              message: errorMsg,
            });
          }
          logEvent("job_end", {
            slug: job.slug,
            qid: job.qid,
            kind: "retranslate",
            outcome,
            error: errorMsg,
            duration_ms: Date.now() - startedAt,
            reenqueued,
          });
        }
      }
    }
  } finally {
    running = false;
    logEvent("drain_end", { queue_depth: stack.length });
  }
}

async function runExplain(slug: string, tid: string): Promise<void> {
  // The Go binary owns the entire explain flow now: read thread, spawn
  // the configured LLM adapter (resume on stored session_id, fall back
  // to fresh on first turn), validate the reply, write the agent
  // message + translation_fix + session_id back. agent.ts only needs
  // to trigger the spawn and surface non-zero exits as job failures.
  const dbPath = join(loadConfig().dataDir, `${slug}.db`);
  const args = [
    "translate",
    "explain",
    "-db",
    dbPath,
    "-tid",
    tid,
    ...translateOverrideArgs(),
  ];
  logEvent("spawn_initial", {
    slug,
    tid,
    kind: "explain",
    bin: EXAMTOPICSDL_BIN,
    args,
  });
  const result = await spawnBinary({
    bin: EXAMTOPICSDL_BIN,
    cwd: SPAWN_CWD,
    args,
  });
  logEvent("spawn_result", {
    slug,
    tid,
    kind: "explain",
    bin: EXAMTOPICSDL_BIN,
    exit_code: result.exitCode,
    stdout: summarize(result.stdout),
    stderr: summarize(result.stderr),
  });
  if (result.exitCode !== 0) {
    throw new Error(
      `examtopicsdl translate explain exit ${result.exitCode}: ${result.stderr.slice(-500)}`
    );
  }
}

async function runRetranslate(slug: string, qid: number): Promise<void> {
  // Web no longer spawns the LLM directly. examtopicsdl owns the
  // read row → adapter spawn → write back loop, so the JSON contract
  // is enforced by Go tests and we don't need to babysit prompt
  // engineering from the web side. The chosen client + model come
  // from config.json's tools.translate; env overrides only thread
  // through when explicitly set (see translateOverrideArgs).
  const dbPath = join(loadConfig().dataDir, `${slug}.db`);
  const args = [
    "translate",
    "retranslate",
    "-db",
    dbPath,
    "-qid",
    String(qid),
    ...translateOverrideArgs(),
  ];
  logEvent("spawn_initial", {
    slug,
    qid,
    kind: "retranslate",
    bin: EXAMTOPICSDL_BIN,
    args,
  });
  const result = await spawnBinary({
    bin: EXAMTOPICSDL_BIN,
    cwd: SPAWN_CWD,
    args,
  });
  logEvent("spawn_result", {
    slug,
    qid,
    kind: "retranslate",
    bin: EXAMTOPICSDL_BIN,
    exit_code: result.exitCode,
    stdout: summarize(result.stdout),
    stderr: summarize(result.stderr),
  });
  if (result.exitCode !== 0) {
    throw new Error(
      `examtopicsdl translate exit ${result.exitCode}: ${result.stderr.slice(-500)}`
    );
  }
}

type SpawnResult = { exitCode: number; stdout: string; stderr: string };

async function spawnBinary(opts: {
  bin: string;
  cwd: string;
  args: string[];
}): Promise<SpawnResult> {
  const proc = Bun.spawn([opts.bin, ...opts.args], {
    cwd: opts.cwd,
    stdout: "pipe",
    stderr: "pipe",
    env: { ...process.env },
  });
  const [stdout, stderr, exitCode] = await Promise.all([
    new Response(proc.stdout).text(),
    new Response(proc.stderr).text(),
    proc.exited,
  ]);
  return { exitCode, stdout, stderr };
}
