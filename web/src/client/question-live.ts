/// <reference lib="dom" />

// Client-side script for the per-question SSE channel. Loaded by
// views/Retranslate.tsx via Bun.Transpiler and inlined into a single <script>
// block alongside an `initQuestionLive(config)` invocation.

type QuestionEvent =
  | { type: "translation-updated" }
  | { type: "translation-failed"; message: string };

type IndicatorKind = "running" | "ok" | "error";

const INDICATOR_TEXT_CLS: Record<IndicatorKind, string> = {
  running: "text-orange-600",
  ok: "text-green-600",
  error: "text-red-600",
};

const INDICATOR_DOT_CLS: Record<IndicatorKind, string> = {
  running: "inline-block w-2 h-2 rounded-full bg-orange-500 animate-pulse",
  ok: "inline-block w-2 h-2 rounded-full bg-green-500",
  error: "inline-block w-2 h-2 rounded-full bg-red-500",
};

function initQuestionLive({ slug, qid }: { slug: string; qid: number }): void {
  const indicator = document.getElementById(`retrans-indicator-${qid}`);

  const setIndicator = (text: string, kind: IndicatorKind = "running"): void => {
    if (!indicator) return;
    indicator.style.display = text ? "inline-flex" : "none";
    indicator.className = `text-xs inline-flex items-center gap-1 ${INDICATOR_TEXT_CLS[kind]}`;
    const dot = indicator.querySelector<HTMLSpanElement>(
      "span:not([data-role])"
    );
    const msg = indicator.querySelector<HTMLSpanElement>('[data-role="msg"]');
    if (dot) dot.className = INDICATOR_DOT_CLS[kind];
    if (msg) msg.textContent = text;
  };

  const es = new EventSource(`/e/${slug}/q/${qid}/events`);
  es.addEventListener("message", (e: MessageEvent<string>) => {
    let ev: QuestionEvent;
    try {
      ev = JSON.parse(e.data) as QuestionEvent;
    } catch {
      return;
    }
    switch (ev.type) {
      case "translation-updated":
        setIndicator("✓ 翻訳完了 — リロード中…", "ok");
        setTimeout(() => location.reload(), 600);
        break;
      case "translation-failed":
        setIndicator(`⚠ 翻訳失敗: ${ev.message || "unknown"}`, "error");
        break;
    }
  });
  es.addEventListener("error", () => {
    /* browser auto-reconnects */
  });
}
