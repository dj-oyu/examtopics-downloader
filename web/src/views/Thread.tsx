import type { FC } from "hono/jsx";
import { raw } from "hono/html";
import type {
  Citation,
  Question,
  ReasonCode,
  ThreadWithMessages,
} from "../db";
import { parseCitations } from "../db";
import { loadClientScript } from "../client/loader";
import { renderMarkdown } from "./markdown";
import { formatLocalTimestamp } from "./timestamps";

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

const ThinkingBubble: FC<{ tid: string; visible: boolean }> = ({
  tid,
  visible,
}) => (
  <div
    id={`agent-thinking-${tid}`}
    class="mt-2 pl-3 py-2 border-l-4 border-blue-400 bg-blue-50"
    style={visible ? "" : "display:none"}
  >
    <div class="flex justify-between text-xs text-gray-600 mb-1">
      <span class="font-mono">🤖 agent</span>
      <span class="text-gray-400">応答中…</span>
    </div>
    <div class="flex items-center gap-1.5 py-1">
      <span class="w-2 h-2 rounded-full bg-blue-400 animate-bounce"></span>
      <span
        class="w-2 h-2 rounded-full bg-blue-400 animate-bounce"
        style="animation-delay:0.15s"
      ></span>
      <span
        class="w-2 h-2 rounded-full bg-blue-400 animate-bounce"
        style="animation-delay:0.3s"
      ></span>
    </div>
  </div>
);

const MessageBubble: FC<{
  id: string;
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
      data-msg-id={id}
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
        <span>{formatLocalTimestamp(created_at)}</span>
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

const ThreadLiveScript: FC<{ slug: string; tid: string }> = ({ slug, tid }) => {
  const config = JSON.stringify({ slug, tid });
  const js = `${loadClientScript("thread-live")}\ninitThreadLive(${config});`;
  return <script>{raw(js)}</script>;
};

export const ThreadPanel: FC<{
  slug: string;
  q: Question;
  thread: ThreadWithMessages | null;
  explainPending?: boolean;
}> = ({ slug, q, thread, explainPending = false }) => {
  if (!thread) {
    return (
      <details>
        <summary class="text-sm text-amber-700 cursor-pointer hover:underline">
          🙏 解説スレッドを作成する
        </summary>
        <form
          method="post"
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
    <section class="space-y-3">
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
            class={
              "ml-2 text-xs inline-flex items-center gap-1 " +
              (explainPending ? "text-blue-600" : "text-gray-500")
            }
            style={explainPending ? "" : "display:none"}
          >
            {explainPending && (
              <>
                <span class="inline-block w-2 h-2 rounded-full bg-blue-500 animate-pulse"></span>
                <span>🤖 エージェント応答中…</span>
              </>
            )}
          </span>
        </h3>
        <span class="text-xs text-gray-500">
          {formatLocalTimestamp(thread.created_at)}
        </span>
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
      <ThinkingBubble tid={thread.id} visible={explainPending} />
      <form
        id={`reply-form-${thread.id}`}
        method="post"
        action={`/e/${slug}/threads/${thread.id}/reply`}
        class="space-y-2"
      >
        <textarea
          name="content"
          required
          rows={2}
          maxlength={2000}
          disabled={explainPending || undefined}
          class={
            "w-full px-2 py-1 border rounded text-sm " +
            (explainPending ? "bg-gray-100 text-gray-400 cursor-not-allowed" : "")
          }
          placeholder={explainPending ? "エージェント応答中…" : "返信を入力"}
        />
        <button
          type="submit"
          disabled={explainPending || undefined}
          class={
            "px-3 py-1 text-sm rounded " +
            (explainPending
              ? "bg-gray-300 text-gray-500 cursor-not-allowed"
              : "bg-amber-500 hover:bg-amber-600 text-white")
          }
        >
          返信
        </button>
      </form>
      <div class="flex gap-3 text-xs">
        <form method="post" action={`/e/${slug}/threads/${thread.id}/resolve`}>
          <button type="submit" class="text-green-700 hover:underline">
            ✓ 解決済みにする
          </button>
        </form>
        <form method="post" action={`/e/${slug}/threads/${thread.id}/dismiss`}>
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
