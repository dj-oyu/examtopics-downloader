import {
  closeThread,
  discoverExams,
  getLatestUserContent,
  getOpenThreadIdForQuestion,
  getThreadAgentSessionId,
  getThreadLastRole,
  getThreadStatus,
  listOpenThreads,
  setThreadAgentSessionId,
} from "./db";
import { join } from "node:path";
import { loadConfig } from "./config";
import { logEvent, summarize, LOG_PATH } from "./agent_log";
// AGENTS.md is the canonical source of agent prompt rules. Embedding it via
// a text import frees the compiled Bun binary from runtime filesystem
// lookups (§2 notice 7 of docs/plans/portable-builds.md): the bytes ship
// inside the binary instead of being resolved relative to import.meta.dir,
// which becomes a virtual path after `bun build --compile`.
import agentsMarkdown from "../../AGENTS.md" with { type: "text" };

console.log(`[agent] log file: ${LOG_PATH}`);

// SPAWN_CWD is where agent.ts's child processes (claude CLI today) run.
// Using process.cwd() keeps the value real in both `bun run` and
// `bun build --compile` modes — the previous import.meta.dir-based
// PROJECT_ROOT became a virtual path inside compiled binaries. Task 4-C
// will route this through loadConfig().dataDir for fully explicit
// placement.
const SPAWN_CWD = process.cwd();
const CLAUDE_BIN = process.env.CLAUDE_BIN ?? "claude";
// Web retranslate now spawns the Go CLI, which in turn spawns the chosen
// LLM adapter (claude by default). Config-driven so a release zip that
// places the binary next to the web server still works with a relative
// hint.
const EXAMTOPICSDL_BIN = process.env.EXAMTOPICSDL_BIN ?? "examtopicsdl";
const TRANSLATE_CLIENT = process.env.EXAMTOPICS_TRANSLATE_CLIENT ?? "claude";
const EXPLAIN_ALLOWED_TOOLS =
  "Bash,mcp__claude_ai_AWS_Knowledge_MCP_Server__aws___search_documentation," +
  "mcp__claude_ai_AWS_Knowledge_MCP_Server__aws___read_documentation," +
  "mcp__claude_ai_AWS_Knowledge_MCP_Server__aws___recommend";

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

// rulesExcerpt is computed once at module load — the AGENTS.md content is
// known statically at build time so there's no need for the lazy/cached
// pattern the previous filesystem reader used.
const rulesExcerpt = (() => {
  const m = agentsMarkdown.match(
    /<!-- AGENT_REPLY_PROMPT_START -->([\s\S]*?)<!-- AGENT_REPLY_PROMPT_END -->/
  );
  return m ? m[1].trim() : "";
})();

function loadRulesExcerpt(): string {
  return rulesExcerpt;
}

function buildExplainInitialPrompt(slug: string, tid: string): string {
  const rules = loadRulesExcerpt();
  return `You are the explanation agent for an AWS exam study tool. The user asked a question on a specific exam item; you must reply with a structured agent message.

Authoritative rules (do NOT diverge):

${rules}

# Your standing instructions for THIS thread (apply on every turn, including when the user follows up later)

1. Always run \`uv run tools/translate.py -d ${slug}.db show-thread ${tid}\` first to see the latest payload (question, choices, comments, full message history). The DB is the source of truth — do not rely on memory alone.
2. Compose a Japanese reply for the latest user message. Choose exactly one \`reason_code\` per the rules above (priority order: translation > comprehension > spec > ambiguous).
3. For \`reason_code='spec'\` or \`'ambiguous'\`, fetch citations via the AWS Documentation MCP tools (\`aws___search_documentation\`, \`aws___read_documentation\`). At least one citation URL must contain \`docs.aws.amazon.com\`.
4. Write the reply by piping JSON to \`uv run tools/translate.py -d ${slug}.db reply ${tid}\`. Use \`author: "claude-code"\`. Do NOT set \`resolve: true\` automatically — leave the thread open so the user can ask follow-ups. Only set \`resolve\` if the user explicitly indicates the thread is done.
5. After reply succeeds, stop. Do not loop.

When the user follows up in this same thread (you'll be resumed via --resume on the same session), repeat steps 1–5 for the new latest user message.`;
}

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
  const sessionId = getThreadAgentSessionId(slug, tid);

  if (sessionId) {
    const userContent = getLatestUserContent(slug, tid);
    if (!userContent) throw new Error("no latest user message to resume on");
    logEvent("spawn_resume", {
      slug,
      tid,
      kind: "explain",
      session_id: sessionId,
      user_content: summarize(userContent),
    });
    const ok = await spawnClaude({
      cwd: SPAWN_CWD,
      args: [
        "--resume",
        sessionId,
        "-p",
        userContent,
        "--output-format",
        "json",
        "--allowed-tools",
        EXPLAIN_ALLOWED_TOOLS,
      ],
    });
    logEvent("spawn_result", {
      slug,
      tid,
      kind: "explain",
      mode: "resume",
      session_id: sessionId,
      exit_code: ok.exitCode,
      stdout: summarize(ok.stdout),
      stderr: summarize(ok.stderr),
    });
    if (ok.exitCode === 0) {
      maybeUpdateSessionId(slug, tid, ok.stdout, sessionId);
      return;
    }
    console.warn(
      `[agent] --resume failed for ${slug}#${tid} (exit ${ok.exitCode}); falling back to fresh session`
    );
    logEvent("resume_failed_fallback", {
      slug,
      tid,
      session_id: sessionId,
      exit_code: ok.exitCode,
    });
  }

  const initialPrompt = buildExplainInitialPrompt(slug, tid);
  logEvent("spawn_initial", {
    slug,
    tid,
    kind: "explain",
    prompt: summarize(initialPrompt),
  });
  const fresh = await spawnClaude({
    cwd: SPAWN_CWD,
    args: [
      "-p",
      initialPrompt,
      "--output-format",
      "json",
      "--allowed-tools",
      EXPLAIN_ALLOWED_TOOLS,
    ],
  });
  logEvent("spawn_result", {
    slug,
    tid,
    kind: "explain",
    mode: "initial",
    exit_code: fresh.exitCode,
    stdout: summarize(fresh.stdout),
    stderr: summarize(fresh.stderr),
  });
  if (fresh.exitCode !== 0) {
    throw new Error(
      `claude exited ${fresh.exitCode}: ${fresh.stderr.slice(-500)}`
    );
  }
  maybeUpdateSessionId(slug, tid, fresh.stdout, null);
}

async function runRetranslate(slug: string, qid: number): Promise<void> {
  // Web no longer spawns the LLM directly. examtopicsdl owns the
  // read row → adapter spawn → write back loop, so the JSON contract
  // is enforced by Go tests and we don't need to babysit prompt
  // engineering from the web side. The chosen adapter is fed to the
  // CLI via -client; default mirrors the historical Claude path.
  const dbPath = join(loadConfig().dataDir, `${slug}.db`);
  const args = [
    "translate",
    "retranslate",
    "-db",
    dbPath,
    "-qid",
    String(qid),
    "-client",
    TRANSLATE_CLIENT,
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
    client: TRANSLATE_CLIENT,
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

function maybeUpdateSessionId(
  slug: string,
  tid: string,
  stdout: string,
  prev: string | null
): void {
  const id = extractSessionId(stdout);
  logEvent("session_id_extracted", {
    slug,
    tid,
    extracted: id,
    previous: prev,
  });
  if (id && id !== prev) {
    setThreadAgentSessionId(slug, tid, id);
    logEvent("session_id_stored", { slug, tid, session_id: id });
  }
}

function extractSessionId(stdout: string): string | null {
  // claude -p --output-format json prints a single JSON object.
  // Be permissive: try whole-stdout JSON first, then last line.
  const tryParse = (s: string): string | null => {
    try {
      const o = JSON.parse(s);
      if (o && typeof o.session_id === "string") return o.session_id;
    } catch {}
    return null;
  };
  const whole = tryParse(stdout.trim());
  if (whole) return whole;
  const lines = stdout.trim().split(/\r?\n/);
  for (let i = lines.length - 1; i >= 0; i--) {
    const id = tryParse(lines[i]);
    if (id) return id;
  }
  return null;
}

type SpawnResult = { exitCode: number; stdout: string; stderr: string };

async function spawnClaude(opts: {
  cwd: string;
  args: string[];
}): Promise<SpawnResult> {
  return spawnBinary({ bin: CLAUDE_BIN, cwd: opts.cwd, args: opts.args });
}

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
