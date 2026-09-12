/// <reference lib="dom" />
/// <reference lib="dom.iterable" />

// Client-side script for the explanation thread panel.
// Loaded by views/Thread.tsx which transpiles this file via Bun.Transpiler
// and injects an `initThreadLive(config)` call after the body. Do not export
// or import — the transpiled output is inlined into a single <script> block.

type Role = "user" | "agent";
type ReasonCode = "comprehension" | "spec" | "ambiguous" | "translation";
type Citation = { url: string; title?: string };

type Message = {
  id: string;
  role: Role;
  author: string | null;
  content: string;
  content_html: string | null;
  reason_code: ReasonCode | null;
  citations: Citation[];
  created_at: string;
};

type MessagesPayload = { id: string; status: string; messages: Message[] };

type ThreadEvent =
  | { type: "spawning" }
  | { type: "agent-message" }
  | { type: "error"; message: string }
  | { type: "closed"; kind: "resolved" | "dismissed" };

const ROLE_CLASS: Record<Role, string> = {
  user: "border-amber-400 bg-amber-50",
  agent: "border-blue-400 bg-blue-50",
};

const REASON_META: Record<ReasonCode, { label: string; cls: string }> = {
  comprehension: { label: "読み違い", cls: "bg-gray-100 text-gray-700" },
  spec: { label: "仕様", cls: "bg-blue-100 text-blue-800" },
  ambiguous: { label: "曖昧", cls: "bg-amber-100 text-amber-800" },
  translation: { label: "訳語修正", cls: "bg-purple-100 text-purple-800" },
};

const citationHost = (url: string): string => {
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
};

const el = <T extends HTMLElement>(
  tag: keyof HTMLElementTagNameMap,
  props: Partial<T> & { className?: string },
  ...children: (Node | string)[]
): T => {
  const node = Object.assign(document.createElement(tag), props) as unknown as T;
  if (children.length) node.append(...children);
  return node;
};

const renderCitations = (items: Citation[]): HTMLDetailsElement => {
  const summary = el<HTMLElement>("summary", {
    className: "cursor-pointer text-gray-600 hover:text-gray-800",
    textContent: `📚 出典 (${items.length})`,
  });
  const ul = el<HTMLUListElement>("ul", {
    className: "mt-1 space-y-1 pl-4",
  });
  for (const c of items) {
    const a = el<HTMLAnchorElement>("a", {
      href: c.url,
      target: "_blank",
      rel: "noopener",
      className: "text-blue-600 hover:underline break-all",
      textContent: c.title ?? citationHost(c.url),
    });
    const li = el<HTMLLIElement>("li", { className: "leading-snug" }, a);
    if (c.title) {
      li.append(
        el<HTMLSpanElement>("span", {
          className: "ml-1 text-gray-400 text-[10px]",
          textContent: `(${citationHost(c.url)})`,
        })
      );
    }
    ul.append(li);
  }
  const details = el<HTMLDetailsElement>(
    "details",
    { className: "mt-2 text-xs" },
    summary,
    ul
  );
  details.open = true;
  return details;
};

const renderMessage = (m: Message): HTMLLIElement => {
  const left = el<HTMLSpanElement>("span", {
    className: "font-mono",
    textContent: `${m.role === "user" ? "🙋 user" : "🤖 agent"}${
      m.author ? ` (${m.author})` : ""
    }`,
  });
  if (m.role === "agent" && m.reason_code) {
    const meta = REASON_META[m.reason_code];
    if (meta) {
      left.append(
        el<HTMLSpanElement>("span", {
          className: `inline-block ml-2 px-2 py-0.5 text-xs rounded font-mono ${meta.cls}`,
          textContent: meta.label,
        })
      );
    }
  }
  const head = el<HTMLDivElement>(
    "div",
    { className: "flex justify-between text-xs text-gray-600 mb-1" },
    left,
    el<HTMLSpanElement>("span", { textContent: m.created_at })
  );

  const body =
    m.role === "agent" && m.content_html
      ? el<HTMLDivElement>("div", {
          className: "prose prose-sm max-w-none leading-relaxed",
          innerHTML: m.content_html,
        })
      : el<HTMLParagraphElement>("p", {
          className: "text-sm whitespace-pre-wrap leading-relaxed",
          textContent: m.content,
        });

  const li = el<HTMLLIElement>(
    "li",
    { className: `pl-3 py-2 border-l-4 ${ROLE_CLASS[m.role]}` },
    head,
    body
  );
  li.dataset.msgId = m.id;
  if (m.role === "agent" && m.citations.length > 0) {
    li.append(renderCitations(m.citations));
  }
  return li;
};

const REPLY_BUTTON_BUSY =
  "px-3 py-1 text-sm rounded bg-gray-300 text-gray-500 cursor-not-allowed";
const REPLY_BUTTON_IDLE =
  "px-3 py-1 text-sm rounded bg-amber-500 hover:bg-amber-600 text-white";
const REPLY_TEXTAREA_BUSY =
  "w-full px-2 py-1 border rounded text-sm bg-gray-100 text-gray-400 cursor-not-allowed";
const REPLY_TEXTAREA_IDLE = "w-full px-2 py-1 border rounded text-sm ";

function initThreadLive({ slug, tid }: { slug: string; tid: string }): void {
  const ol = document.getElementById(`messages-${tid}`);
  const status = document.getElementById(`agent-status-${tid}`);
  const thinking = document.getElementById(`agent-thinking-${tid}`);
  const replyForm = document.getElementById(`reply-form-${tid}`);
  if (!ol) return;

  const replyTextarea =
    replyForm?.querySelector<HTMLTextAreaElement>('textarea[name="content"]') ??
    null;
  const replyButton =
    replyForm?.querySelector<HTMLButtonElement>('button[type="submit"]') ??
    null;

  const setAgentBusy = (busy: boolean): void => {
    if (thinking) thinking.style.display = busy ? "" : "none";
    if (replyTextarea) {
      replyTextarea.disabled = busy;
      replyTextarea.className = busy ? REPLY_TEXTAREA_BUSY : REPLY_TEXTAREA_IDLE;
      replyTextarea.placeholder = busy ? "エージェント応答中…" : "返信を入力";
    }
    if (replyButton) {
      replyButton.disabled = busy;
      replyButton.className = busy ? REPLY_BUTTON_BUSY : REPLY_BUTTON_IDLE;
    }
  };

  const setStatus = (text: string, kind: "info" | "error" = "info"): void => {
    if (!status) return;
    if (!text) {
      status.style.display = "none";
      status.textContent = "";
      return;
    }
    status.style.display = "inline-flex";
    status.className = `ml-2 text-xs inline-flex items-center gap-1 ${
      kind === "error" ? "text-red-600" : "text-blue-600"
    }`;
    const dotCls =
      kind === "error"
        ? "inline-block w-2 h-2 rounded-full bg-red-500"
        : "inline-block w-2 h-2 rounded-full bg-blue-500 animate-pulse";
    status.replaceChildren(
      el<HTMLSpanElement>("span", { className: dotCls }),
      el<HTMLSpanElement>("span", { textContent: text })
    );
  };

  const refresh = async (): Promise<void> => {
    try {
      const res = await fetch(`/e/${slug}/threads/${tid}/messages.json`);
      if (!res.ok) throw new Error(`status ${res.status}`);
      const data = (await res.json()) as MessagesPayload;
      const seen = new Set(
        Array.from(ol.children, (n) =>
          n instanceof HTMLElement ? n.dataset.msgId ?? "" : ""
        )
      );
      for (const m of data.messages) {
        if (!seen.has(m.id)) ol.append(renderMessage(m));
      }
      if (data.status !== "open") location.reload();
    } catch (e) {
      console.error("[thread-live] refresh failed", e);
    }
  };

  const es = new EventSource(`/e/${slug}/threads/${tid}/events`);
  es.addEventListener("message", (e: MessageEvent<string>) => {
    let ev: ThreadEvent;
    try {
      ev = JSON.parse(e.data) as ThreadEvent;
    } catch {
      return;
    }
    switch (ev.type) {
      case "spawning":
        setStatus("🤖 エージェント応答中…", "info");
        setAgentBusy(true);
        break;
      case "agent-message":
        setStatus("");
        setAgentBusy(false);
        void refresh();
        break;
      case "error":
        setStatus(`⚠ ${ev.message || "agent error"}`, "error");
        setAgentBusy(false);
        break;
      case "closed":
        setAgentBusy(false);
        void refresh();
        break;
    }
  });
  es.addEventListener("error", () => {
    /* browser auto-reconnects */
  });

  if (replyForm instanceof HTMLFormElement && replyTextarea) {
    replyForm.addEventListener("submit", async (e) => {
      e.preventDefault();
      const content = replyTextarea.value.trim();
      if (!content) return;

      // CRITICAL: build FormData BEFORE setAgentBusy(true) flips the
      // textarea to disabled. Per the HTML spec, disabled form controls are
      // excluded from FormData, so doing this in the wrong order ships an
      // empty `content` field and the server silently drops the message.
      const body = new FormData(replyForm);

      console.log("[thread-live] sending reply", {
        action: replyForm.action,
        length: content.length,
      });

      setAgentBusy(true);
      try {
        const res = await fetch(replyForm.action, { method: "POST", body });
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        await res.text();
        console.log("[thread-live] reply accepted by server");
        replyTextarea.value = "";
        await refresh();
      } catch (err) {
        const msg = err instanceof Error ? err.message : "send failed";
        console.error("[thread-live] reply submit failed", err);
        setStatus(`⚠ 送信失敗: ${msg}`, "error");
        setAgentBusy(false);
      }
    });
  }
}
