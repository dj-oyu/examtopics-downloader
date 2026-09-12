/// <reference lib="dom" />

// Client-side script for /admin/sync. Subscribes to the SSE log stream, appends
// each line to the <pre id="admin-sync-log"> element, and reloads the page when
// the job finishes so the server-rendered result panel (filled counts plus the
// conflict list with question links) appears. Loaded by the AdminSync view via
// the same text-import + Bun.Transpiler path used by admin-fetch; do not export.

type SyncLogLine = {
  stream: "stdout" | "stderr" | "system";
  text: string;
  ts: number;
};

type SyncDoneEvent = { exitCode: number; ts: number };

type SyncSseEvent =
  | { type: "line"; line: SyncLogLine }
  | { type: "done"; done: SyncDoneEvent };

function adminSyncTime(ms: number): string {
  const d = new Date(ms);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

function adminSyncPrefix(s: SyncLogLine["stream"]): string {
  if (s === "stderr") return "[err]";
  if (s === "system") return "[sys]";
  return "[out]";
}

function initAdminSync(): void {
  const pre = document.getElementById("admin-sync-log");
  const status = document.getElementById("admin-sync-status");
  if (!pre) return;

  const append = (text: string) => {
    pre.appendChild(document.createTextNode(text + "\n"));
    pre.scrollTop = pre.scrollHeight;
  };

  const setStatus = (text: string, kind: "info" | "ok" | "error") => {
    if (!status) return;
    status.textContent = text;
    status.className =
      "ml-2 text-xs " +
      (kind === "ok"
        ? "text-green-600"
        : kind === "error"
          ? "text-red-600"
          : "text-blue-600");
  };

  // The job may already be finished when the page loads (e.g. after the 303
  // redirect); in that case the server rendered the result, so only stream when
  // a job is actually in flight.
  const running = pre.dataset.running === "true";
  if (!running) return;

  const es = new EventSource("/admin/sync/log");
  es.onmessage = (ev) => {
    let msg: SyncSseEvent;
    try {
      msg = JSON.parse(ev.data);
    } catch {
      return;
    }
    if (msg.type === "line") {
      append(
        `${adminSyncTime(msg.line.ts)} ${adminSyncPrefix(msg.line.stream)} ${msg.line.text}`
      );
      return;
    }
    setStatus(
      msg.done.exitCode === 0 ? "完了" : `失敗 (exit ${msg.done.exitCode})`,
      msg.done.exitCode === 0 ? "ok" : "error"
    );
    es.close();
    // Reload so the parsed result (conflict list, counts) is server-rendered.
    window.setTimeout(() => window.location.reload(), 300);
  };
  es.onerror = () => {
    setStatus("SSE 切断", "error");
  };
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", initAdminSync);
} else {
  initAdminSync();
}
