/// <reference lib="dom" />

// Client-side script for /admin/fetch. Subscribes to the SSE log stream,
// appends each line to the <pre id="admin-fetch-log"> element, and re-enables
// the form submit button when the `done` event arrives. Loaded by the
// AdminFetch view via the same text-import + Bun.Transpiler path used by
// thread-live / question-live; do not export or import.

type AdminFetchLogLine = {
  stream: "stdout" | "stderr" | "system";
  text: string;
  ts: number;
};

type AdminFetchDoneEvent = { exitCode: number; ts: number };

type AdminFetchSseEvent =
  | { type: "line"; line: AdminFetchLogLine }
  | { type: "done"; done: AdminFetchDoneEvent };

function tsToHHMMSS(ms: number): string {
  const d = new Date(ms);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

function streamPrefix(s: AdminFetchLogLine["stream"]): string {
  if (s === "stderr") return "[err]";
  if (s === "system") return "[sys]";
  return "[out]";
}

function initAdminFetch(): void {
  const pre = document.getElementById("admin-fetch-log");
  const status = document.getElementById("admin-fetch-status");
  const submit = document.querySelector<HTMLButtonElement>(
    'button[data-role="admin-fetch-submit"]'
  );
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
      (kind === "error"
        ? "text-red-600"
        : kind === "ok"
          ? "text-green-600"
          : "text-blue-600");
  };

  const es = new EventSource("/admin/fetch/log");
  es.addEventListener("message", (e: MessageEvent<string>) => {
    let ev: AdminFetchSseEvent;
    try {
      ev = JSON.parse(e.data) as AdminFetchSseEvent;
    } catch {
      return;
    }
    if (ev.type === "line") {
      append(`${tsToHHMMSS(ev.line.ts)} ${streamPrefix(ev.line.stream)} ${ev.line.text}`);
    }
  });
  es.addEventListener("done", (e: MessageEvent<string>) => {
    let ev: AdminFetchDoneEvent;
    try {
      ev = JSON.parse(e.data) as AdminFetchDoneEvent;
    } catch {
      return;
    }
    append(`--- exit ${ev.exitCode} ---`);
    setStatus(
      ev.exitCode === 0 ? `✓ 取得完了 (exit 0)` : `⚠ 取得失敗 (exit ${ev.exitCode})`,
      ev.exitCode === 0 ? "ok" : "error"
    );
    if (submit) {
      submit.disabled = false;
      submit.textContent = "取得を開始";
    }
    es.close();
  });
  es.addEventListener("error", () => {
    /* browser auto-reconnects; the server closes the stream on done */
  });
}

initAdminFetch();
