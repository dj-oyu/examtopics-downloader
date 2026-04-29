import { mkdirSync, appendFileSync } from "node:fs";
import { resolve } from "node:path";

const PROJECT_ROOT = resolve(import.meta.dir, "../..");
const LOG_DIR = process.env.AGENT_LOG_DIR ?? resolve(PROJECT_ROOT, "logs");
const LOG_FILE =
  process.env.AGENT_LOG_FILE ?? resolve(LOG_DIR, "agent.jsonl");
const ENABLED = (process.env.AGENT_LOG ?? "1") !== "0";
const ECHO_TO_CONSOLE = (process.env.AGENT_LOG_CONSOLE ?? "1") !== "0";

let dirReady = false;

function ensureDir(): void {
  if (dirReady) return;
  try {
    mkdirSync(LOG_DIR, { recursive: true });
    dirReady = true;
  } catch (e) {
    // If we can't create the dir, fall back silently to console-only.
    console.error("[agent_log] mkdir failed:", e);
  }
}

export type LogEvent =
  | "enqueue"
  | "enqueue_skipped"
  | "drain_start"
  | "drain_end"
  | "job_start"
  | "job_end"
  | "spawn_initial"
  | "spawn_resume"
  | "spawn_result"
  | "session_id_extracted"
  | "session_id_stored"
  | "resume_failed_fallback"
  | "agent_message_notified"
  | "error_notified"
  | "close_immediate"
  | "close_deferred"
  | "close_applied_after_run"
  | "reenqueue_tail_user"
  | "retrans_reenqueue_explain"
  | "translation_updated_notified"
  | "translation_failed_notified"
  | "subscriber_added"
  | "subscriber_removed"
  | "q_subscriber_added"
  | "q_subscriber_removed";

export type LogPayload = Record<string, unknown>;

export function logEvent(event: LogEvent, payload: LogPayload = {}): void {
  if (!ENABLED) return;
  const entry = {
    ts: new Date().toISOString(),
    event,
    ...payload,
  };
  const line = JSON.stringify(entry);
  if (ECHO_TO_CONSOLE) {
    console.log(`[agent] ${event}`, payload);
  }
  ensureDir();
  try {
    appendFileSync(LOG_FILE, line + "\n", { encoding: "utf-8" });
  } catch (e) {
    if (ECHO_TO_CONSOLE) console.error("[agent_log] write failed:", e);
  }
}

/** Truncate a string for safe logging. Default keeps head + tail markers. */
export function summarize(
  text: string | undefined | null,
  maxLen = 4000
): { length: number; head?: string; tail?: string; full?: string } {
  if (text == null) return { length: 0 };
  if (text.length <= maxLen) return { length: text.length, full: text };
  const half = Math.floor(maxLen / 2);
  return {
    length: text.length,
    head: text.slice(0, half),
    tail: text.slice(text.length - half),
  };
}

export const LOG_PATH = LOG_FILE;
