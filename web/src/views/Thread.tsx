import type { FC } from "hono/jsx";
import { raw } from "hono/html";
import type {
  Citation,
  Question,
  ReasonCode,
  ThreadWithMessages,
} from "../db";
import { parseCitations } from "../db";
import { renderMarkdown } from "./markdown";

const REASON_LABEL: Record<ReasonCode, { label: string; color: string }> = {
  comprehension: { label: "読み違い", color: "bg-gray-100 text-gray-700" },
  spec: { label: "仕様", color: "bg-blue-100 text-blue-800" },
  ambiguous: { label: "曖昧", color: "bg-amber-100 text-amber-800" },
  translation: { label: "訳語修正", color: "bg-purple-100 text-purple-800" },
};

const ReasonBadge: FC<{ code: ReasonCode }> = ({ code }) => {
  const meta = REASON_LABEL[code];
  return (
    <span
      class={
        "inline-block ml-2 px-2 py-0.5 text-xs rounded font-mono " + meta.color
      }
    >
      {meta.label}
    </span>
  );
};

function citationHost(url: string): string {
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
}

const CitationList: FC<{ items: Citation[] }> = ({ items }) => (
  <details class="mt-2 text-xs" open>
    <summary class="cursor-pointer text-gray-600 hover:text-gray-800">
      📚 出典 ({items.length})
    </summary>
    <ul class="mt-1 space-y-1 pl-4">
      {items.map((c) => (
        <li class="leading-snug">
          <a
            href={c.url}
            target="_blank"
            rel="noopener"
            class="text-blue-600 hover:underline break-all"
          >
            {c.title ?? citationHost(c.url)}
          </a>
          {c.title && (
            <span class="ml-1 text-gray-400 text-[10px]">
              ({citationHost(c.url)})
            </span>
          )}
        </li>
      ))}
    </ul>
  </details>
);

const MessageBubble: FC<{
  id: number;
  role: "user" | "agent";
  author: string | null;
  content: string;
  reason_code: ReasonCode | null;
  citations: string | null;
  created_at: string;
}> = ({ id, role, author, content, reason_code, citations, created_at }) => {
  const cites = role === "agent" ? parseCitations(citations) : [];
  return (
    <li
      data-msg-id={String(id)}
      class={
        "pl-3 py-2 border-l-4 " +
        (role === "user"
          ? "border-amber-400 bg-amber-50"
          : "border-blue-400 bg-blue-50")
      }
    >
      <div class="flex justify-between text-xs text-gray-600 mb-1">
        <span class="font-mono">
          {role === "user" ? "🙋 user" : "🤖 agent"}
          {author && <span class="ml-1 text-gray-500">({author})</span>}
          {role === "agent" && reason_code && <ReasonBadge code={reason_code} />}
        </span>
        <span>{created_at}</span>
      </div>
      {role === "agent" ? (
        <div class="prose prose-sm max-w-none leading-relaxed">
          {raw(renderMarkdown(content))}
        </div>
      ) : (
        <p class="text-sm whitespace-pre-wrap leading-relaxed">{content}</p>
      )}
      {cites.length > 0 && <CitationList items={cites} />}
    </li>
  );
};

const ThreadLiveScript: FC<{ slug: string; tid: number }> = ({ slug, tid }) => {
  const slugLit = JSON.stringify(slug);
  const tidLit = String(tid);
  const js = `
(function(){
  var slug = ${slugLit};
  var tid = ${tidLit};
  var ol = document.getElementById("messages-" + tid);
  var status = document.getElementById("agent-status-" + tid);
  if (!ol) return;
  var ROLE_CLASS = {
    user: "border-amber-400 bg-amber-50",
    agent: "border-blue-400 bg-blue-50"
  };
  function setStatus(text, kind){
    if (!status) return;
    if (!text) {
      status.style.display = "none";
      status.textContent = "";
      return;
    }
    status.style.display = "inline-flex";
    status.className = "ml-2 text-xs inline-flex items-center gap-1 " +
      (kind === "error" ? "text-red-600" : "text-blue-600");
    var dotClass = kind === "error"
      ? "inline-block w-2 h-2 rounded-full bg-red-500"
      : "inline-block w-2 h-2 rounded-full bg-blue-500 animate-pulse";
    status.innerHTML = '<span class="' + dotClass + '"></span><span></span>';
    status.lastChild.textContent = text;
  }
  var REASON_META = {
    comprehension: { label: "読み違い", cls: "bg-gray-100 text-gray-700" },
    spec:          { label: "仕様",     cls: "bg-blue-100 text-blue-800" },
    ambiguous:     { label: "曖昧",     cls: "bg-amber-100 text-amber-800" },
    translation:   { label: "訳語修正", cls: "bg-purple-100 text-purple-800" }
  };
  function citationHost(url){
    try { return new URL(url).host; } catch (_) { return url; }
  }
  function renderCitations(items){
    var details = document.createElement("details");
    details.className = "mt-2 text-xs"; details.open = true;
    var summary = document.createElement("summary");
    summary.className = "cursor-pointer text-gray-600 hover:text-gray-800";
    summary.textContent = "\u{1F4DA} 出典 (" + items.length + ")";
    details.appendChild(summary);
    var ul = document.createElement("ul");
    ul.className = "mt-1 space-y-1 pl-4";
    items.forEach(function(c){
      var li = document.createElement("li");
      li.className = "leading-snug";
      var a = document.createElement("a");
      a.href = c.url; a.target = "_blank"; a.rel = "noopener";
      a.className = "text-blue-600 hover:underline break-all";
      a.textContent = c.title || citationHost(c.url);
      li.appendChild(a);
      if (c.title) {
        var host = document.createElement("span");
        host.className = "ml-1 text-gray-400 text-[10px]";
        host.textContent = "(" + citationHost(c.url) + ")";
        li.appendChild(host);
      }
      ul.appendChild(li);
    });
    details.appendChild(ul);
    return details;
  }
  function renderMessage(m){
    var li = document.createElement("li");
    li.className = "pl-3 py-2 border-l-4 " + (ROLE_CLASS[m.role] || "");
    li.dataset.msgId = String(m.id);
    var head = document.createElement("div");
    head.className = "flex justify-between text-xs text-gray-600 mb-1";
    var left = document.createElement("span");
    left.className = "font-mono";
    left.textContent = (m.role === "user" ? "\u{1F64B} user" : "\u{1F916} agent") + (m.author ? " (" + m.author + ")" : "");
    if (m.role === "agent" && m.reason_code && REASON_META[m.reason_code]) {
      var rb = document.createElement("span");
      var meta = REASON_META[m.reason_code];
      rb.className = "inline-block ml-2 px-2 py-0.5 text-xs rounded font-mono " + meta.cls;
      rb.textContent = meta.label;
      left.appendChild(rb);
    }
    var right = document.createElement("span");
    right.textContent = m.created_at;
    head.appendChild(left); head.appendChild(right);
    var body;
    if (m.role === "agent" && m.content_html) {
      body = document.createElement("div");
      body.className = "prose prose-sm max-w-none leading-relaxed";
      body.innerHTML = m.content_html;
    } else {
      body = document.createElement("p");
      body.className = "text-sm whitespace-pre-wrap leading-relaxed";
      body.textContent = m.content;
    }
    li.appendChild(head); li.appendChild(body);
    if (m.role === "agent" && Array.isArray(m.citations) && m.citations.length > 0) {
      li.appendChild(renderCitations(m.citations));
    }
    return li;
  }
  function refresh(){
    return fetch("/e/" + slug + "/threads/" + tid + "/messages.json")
      .then(function(r){ if (!r.ok) throw new Error("status " + r.status); return r.json(); })
      .then(function(data){
        var seen = {};
        Array.prototype.forEach.call(ol.children, function(li){
          if (li.dataset && li.dataset.msgId) seen[li.dataset.msgId] = true;
        });
        data.messages.forEach(function(m){
          if (!seen[String(m.id)]) ol.appendChild(renderMessage(m));
        });
        if (data.status !== "open") { location.reload(); }
      })
      .catch(function(e){ console.error("[thread-live] refresh failed", e); });
  }
  var es = new EventSource("/e/" + slug + "/threads/" + tid + "/events");
  es.onmessage = function(e){
    var ev; try { ev = JSON.parse(e.data); } catch (_) { return; }
    if (ev.type === "spawning") setStatus("\u{1F916} エージェント応答中…", "info");
    else if (ev.type === "agent-message") { setStatus("", "info"); refresh(); }
    else if (ev.type === "error") setStatus("⚠ " + (ev.message || "agent error"), "error");
    else if (ev.type === "closed") refresh();
  };
  es.onerror = function(){};
})();`;
  return <script>{raw(js)}</script>;
};

export const ThreadPanel: FC<{
  slug: string;
  q: Question;
  thread: ThreadWithMessages | null;
}> = ({ slug, q, thread }) => {
  if (!thread) {
    return (
      <details class="border-t pt-4">
        <summary class="text-sm text-amber-700 cursor-pointer hover:underline">
          🙏 解説スレッドを作成する
        </summary>
        <form
          method="POST"
          action={`/e/${slug}/q/${q.id}/threads`}
          class="mt-2 space-y-2"
        >
          <textarea
            name="content"
            required
            rows={3}
            maxlength={2000}
            class="w-full px-2 py-1 border rounded text-sm"
            placeholder="どこが分かりづらいか、追加で聞きたい点など"
          />
          <button
            type="submit"
            class="px-3 py-1 text-sm bg-amber-500 hover:bg-amber-600 text-white rounded"
          >
            スレッドを作成
          </button>
        </form>
      </details>
    );
  }

  const lastRole = thread.messages[thread.messages.length - 1]?.role;
  const awaiting = lastRole === "user" ? "agent" : "user";
  const olId = `messages-${thread.id}`;
  const statusId = `agent-status-${thread.id}`;

  return (
    <section class="border-t pt-4 space-y-3">
      <header class="flex items-baseline justify-between">
        <h3 class="font-semibold text-amber-800">
          💬 解説スレッド #{thread.id}
          <span class="ml-2 text-xs px-2 py-0.5 rounded bg-amber-100 text-amber-800">
            {awaiting === "agent"
              ? "エージェント返信待ち"
              : "ユーザー返信待ち"}
          </span>
          <span
            id={statusId}
            class="ml-2 text-xs text-gray-500"
            style="display:none"
          ></span>
        </h3>
        <span class="text-xs text-gray-500">{thread.created_at}</span>
      </header>
      <ol id={olId} class="space-y-2">
        {thread.messages.map((m) => (
          <MessageBubble
            id={m.id}
            role={m.role}
            author={m.author}
            content={m.content}
            reason_code={m.reason_code}
            citations={m.citations}
            created_at={m.created_at}
          />
        ))}
      </ol>
      <form
        method="POST"
        action={`/e/${slug}/threads/${thread.id}/reply`}
        class="space-y-2"
      >
        <textarea
          name="content"
          required
          rows={2}
          maxlength={2000}
          class="w-full px-2 py-1 border rounded text-sm"
          placeholder="返信を入力"
        />
        <button
          type="submit"
          class="px-3 py-1 text-sm bg-amber-500 hover:bg-amber-600 text-white rounded"
        >
          返信
        </button>
      </form>
      <div class="flex gap-3 text-xs">
        <form method="POST" action={`/e/${slug}/threads/${thread.id}/resolve`}>
          <button type="submit" class="text-green-700 hover:underline">
            ✓ 解決済みにする
          </button>
        </form>
        <form method="POST" action={`/e/${slug}/threads/${thread.id}/dismiss`}>
          <button type="submit" class="text-gray-500 hover:underline">
            破棄
          </button>
        </form>
        <span class="ml-auto text-gray-400 font-mono">
          CLI: <code>uv run tools/translate.py -d {slug}.db next-thread</code>
        </span>
      </div>
      <ThreadLiveScript slug={slug} tid={thread.id} />
    </section>
  );
};
