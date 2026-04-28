import type { FC, PropsWithChildren } from "hono/jsx";
import type {
  Choice,
  ExamSummary,
  Question,
  QuestionListRow,
  ThreadListRowAll,
  ThreadWithMessages,
} from "./db";

export const Layout: FC<PropsWithChildren<{ title: string; requestCount?: number }>> = ({
  title,
  requestCount = 0,
  children,
}) => (
  <html lang="ja">
    <head>
      <meta charset="utf-8" />
      <meta name="viewport" content="width=device-width, initial-scale=1" />
      <title>{title} — Exam Studio</title>
      <script src="https://cdn.tailwindcss.com"></script>
    </head>
    <body class="min-h-screen bg-gray-50 text-gray-900">
      <header class="bg-white border-b">
        <nav class="max-w-3xl mx-auto px-4 py-3 flex gap-4 items-center">
          <a href="/" class="font-semibold">Exam Studio</a>
          <a href="/requests" class="text-amber-700 hover:underline ml-auto">
            🙏 解説求む {requestCount > 0 && <span class="ml-1 px-2 py-0.5 text-xs bg-amber-500 text-white rounded-full">{requestCount}</span>}
          </a>
        </nav>
      </header>
      <main class="max-w-3xl mx-auto px-4 py-6">{children}</main>
    </body>
  </html>
);

export const Home: FC<{ exams: ExamSummary[]; requestCount: number }> = ({
  exams,
  requestCount,
}) => (
  <Layout title="試験を選択" requestCount={requestCount}>
    <h1 class="text-2xl font-bold mb-4">試験を選択</h1>
    {exams.length === 0 ? (
      <p class="text-gray-500">試験 DB が見つかりません。リポジトリ直下に *.db を配置してください。</p>
    ) : (
      <ul class="space-y-2">
        {exams.map((e) => (
          <li>
            <a
              href={`/e/${e.slug}/q`}
              class="block p-4 bg-white rounded shadow hover:shadow-md transition"
            >
              <div class="flex justify-between items-baseline">
                <span class="font-semibold">{e.name}</span>
                <span class="text-xs text-gray-400 font-mono">{e.slug}.db</span>
              </div>
              <div class="mt-1 text-sm text-gray-600">
                問題: {e.total} / 和訳済: {e.translated} ({e.total ? Math.round((e.translated / e.total) * 100) : 0}%)
              </div>
              <div class="text-sm text-gray-600">
                解答済: {e.answered} / 正答: {e.correct}
              </div>
              {(e.open_threads > 0 || e.awaiting_agent > 0) && (
                <div class="mt-1 text-xs text-amber-700">
                  💬 open スレッド {e.open_threads}{" "}
                  {e.awaiting_agent > 0 && (
                    <span class="ml-2 px-1 bg-amber-500 text-white rounded">
                      agent 返信待ち {e.awaiting_agent}
                    </span>
                  )}
                </div>
              )}
            </a>
          </li>
        ))}
      </ul>
    )}
  </Layout>
);

export const QuestionList: FC<{
  slug: string;
  exam: string;
  filter: string;
  rows: QuestionListRow[];
  prog: { total: number; answered: number; correct: number };
  requestCount: number;
}> = ({ slug, exam, filter, rows, prog, requestCount }) => (
  <Layout title="問題一覧" requestCount={requestCount}>
    <div class="mb-4 flex justify-between items-baseline">
      <div>
        <h1 class="text-xl font-bold">{exam}</h1>
        <div class="text-sm text-gray-500">
          進捗: {prog.answered}/{prog.total} 回答済 / 正答 {prog.correct}{" "}
          <a href={`/e/${slug}/review`} class="ml-2 text-blue-600 hover:underline">
            復習キュー →
          </a>
        </div>
      </div>
      <div class="text-sm">
        {(["all", "unanswered", "wrong"] as const).map((f) => (
          <a
            href={`/e/${slug}/q?filter=${f}`}
            class={
              "px-2 py-1 rounded " +
              (filter === f ? "bg-blue-600 text-white" : "text-blue-600 hover:underline")
            }
          >
            {f === "all" ? "全て" : f === "unanswered" ? "未回答" : "誤答"}
          </a>
        ))}
      </div>
    </div>
    <ul class="divide-y bg-white rounded shadow">
      {rows.map((q) => (
        <li>
          <a
            href={`/e/${slug}/q/${q.id}`}
            class="flex gap-3 items-baseline px-4 py-3 hover:bg-gray-50"
          >
            <span class="font-mono text-sm w-12 text-gray-500">#{q.question_number}</span>
            <span class="flex-1 text-sm line-clamp-2">
              {q.question_text_ja ?? q.question_text}
            </span>
            <Status code={q.last_correct} />
          </a>
        </li>
      ))}
    </ul>
  </Layout>
);

const Status: FC<{ code: number | null }> = ({ code }) => {
  if (code === null) return <span class="text-xs text-gray-400">未</span>;
  if (code === 1) return <span class="text-xs text-green-600">正</span>;
  return <span class="text-xs text-red-600">誤</span>;
};

export const QuestionView: FC<{
  slug: string;
  q: Question;
  choices: Choice[];
  attempts: { selected: string; is_correct: number; attempted_at: string }[];
  prevId: number | null;
  nextId: number | null;
  result?: { correct: boolean; selected: string } | null;
  thread: ThreadWithMessages | null;
  requestCount: number;
}> = ({ slug, q, choices, attempts, prevId, nextId, result, thread, requestCount }) => {
  const isMulti = q.suggested_answer.length > 1;
  const picked = new Set(result ? result.selected.split("") : []);
  return (
    <Layout title={`Q${q.question_number}`} requestCount={requestCount}>
      <div class="mb-4 flex justify-between items-baseline text-sm">
        <a href={`/e/${slug}/q`} class="text-blue-600 hover:underline">
          ← 一覧
        </a>
        <div class="text-gray-500">
          Topic {q.topic} / Q{q.question_number}{" "}
          {isMulti && <span class="text-orange-600">[複数選択]</span>}
        </div>
        <div class="space-x-2">
          {prevId && (
            <a href={`/e/${slug}/q/${prevId}`} class="text-blue-600 hover:underline">
              ← 前
            </a>
          )}
          {nextId && (
            <a href={`/e/${slug}/q/${nextId}`} class="text-blue-600 hover:underline">
              次 →
            </a>
          )}
        </div>
      </div>
      <article class="bg-white rounded-lg shadow p-6 space-y-4">
        <p class="leading-relaxed">{q.question_text_ja ?? q.question_text}</p>
        {q.question_text_ja && (
          <details class="text-sm">
            <summary class="text-gray-500 cursor-pointer">原文を表示</summary>
            <p class="mt-2 text-gray-700">{q.question_text}</p>
          </details>
        )}

        <form
          method="POST"
          action={`/e/${slug}/q/${q.id}/attempt`}
          class="space-y-1 pt-2"
        >
          {choices.map((c) => (
            <label class="flex gap-3 items-start cursor-pointer hover:bg-gray-50 p-2 rounded">
              <input
                type={isMulti ? "checkbox" : "radio"}
                name="selected"
                value={c.label}
                checked={picked.has(c.label) || undefined}
                disabled={result ? true : undefined}
                class="mt-1"
              />
              <div class="flex-1">
                <div>
                  <span class="font-mono font-bold mr-2">{c.label}.</span>
                  <span>{c.text_ja ?? c.text}</span>
                </div>
                {c.text_ja && (
                  <details class="mt-1">
                    <summary class="text-xs text-gray-400 cursor-pointer">原文</summary>
                    <div class="text-xs text-gray-600 mt-1">{c.text}</div>
                  </details>
                )}
              </div>
            </label>
          ))}
          {!result && (
            <button
              type="submit"
              class="mt-3 px-4 py-2 bg-blue-600 hover:bg-blue-700 text-white rounded"
            >
              解答する
            </button>
          )}
        </form>

        {result && <Result correct={result.correct} q={q} slug={slug} nextId={nextId} />}

        <ThreadPanel slug={slug} q={q} thread={thread} />

        {attempts.length > 0 && (
          <details class="text-sm border-t pt-4">
            <summary class="text-gray-500 cursor-pointer">
              直近の解答履歴 ({attempts.length})
            </summary>
            <ul class="mt-2 space-y-1">
              {attempts.map((a) => (
                <li class="font-mono text-xs">
                  <span class={a.is_correct ? "text-green-600" : "text-red-600"}>
                    {a.is_correct ? "○" : "×"}
                  </span>
                  <span class="ml-2">{a.selected || "(未選択)"}</span>
                  <span class="ml-2 text-gray-500">{a.attempted_at}</span>
                </li>
              ))}
            </ul>
          </details>
        )}
      </article>
    </Layout>
  );
};

const MessageBubble: FC<{
  role: "user" | "agent";
  author: string | null;
  content: string;
  created_at: string;
}> = ({ role, author, content, created_at }) => (
  <li
    class={
      "pl-3 py-2 border-l-4 " +
      (role === "user" ? "border-amber-400 bg-amber-50" : "border-blue-400 bg-blue-50")
    }
  >
    <div class="flex justify-between text-xs text-gray-600 mb-1">
      <span class="font-mono">
        {role === "user" ? "🙋 user" : "🤖 agent"}
        {author && <span class="ml-1 text-gray-500">({author})</span>}
      </span>
      <span>{created_at}</span>
    </div>
    <p class="text-sm whitespace-pre-wrap leading-relaxed">{content}</p>
  </li>
);

const ThreadPanel: FC<{ slug: string; q: Question; thread: ThreadWithMessages | null }> = ({
  slug,
  q,
  thread,
}) => {
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

  return (
    <section class="border-t pt-4 space-y-3">
      <header class="flex items-baseline justify-between">
        <h3 class="font-semibold text-amber-800">
          💬 解説スレッド #{thread.id}
          <span class="ml-2 text-xs px-2 py-0.5 rounded bg-amber-100 text-amber-800">
            {awaiting === "agent" ? "エージェント返信待ち" : "ユーザー返信待ち"}
          </span>
        </h3>
        <span class="text-xs text-gray-500">{thread.created_at}</span>
      </header>
      <ol class="space-y-2">
        {thread.messages.map((m) => (
          <MessageBubble
            role={m.role}
            author={m.author}
            content={m.content}
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
    </section>
  );
};

export const Threads: FC<{ rows: ThreadListRowAll[]; requestCount: number }> = ({
  rows,
  requestCount,
}) => (
  <Layout title="解説求む" requestCount={requestCount}>
    <h1 class="text-xl font-bold mb-1">解説スレッド ({rows.length} open)</h1>
    <p class="text-xs text-gray-500 mb-4">
      全試験 DB 横断で表示。エージェント CLI:{" "}
      <code class="bg-white px-1 rounded border">
        uv run tools/translate.py -d &lt;slug&gt;.db next-thread
      </code>
    </p>
    {rows.length === 0 ? (
      <p class="text-gray-500">オープン中のスレッドはありません。</p>
    ) : (
      <ul class="space-y-2">
        {rows.map((r) => (
          <li class="p-4 bg-white rounded shadow">
            <div class="flex justify-between items-baseline">
              <a
                href={`/e/${r.slug}/q/${r.question_id}`}
                class="text-blue-600 hover:underline"
              >
                <span class="font-mono text-xs text-gray-500 mr-2">{r.slug}</span>
                #{r.question_number}
              </a>
              <span class="text-xs">
                <span
                  class={
                    "px-2 py-0.5 rounded " +
                    (r.last_role === "user"
                      ? "bg-amber-100 text-amber-800"
                      : "bg-blue-100 text-blue-800")
                  }
                >
                  {r.last_role === "user" ? "agent 返信待ち" : "user 返信待ち"}
                </span>
                <span class="ml-2 text-gray-500">{r.last_at}</span>
              </span>
            </div>
            <p class="mt-1 text-sm line-clamp-2">
              {r.question_text_ja ?? r.question_text}
            </p>
            {r.last_content && (
              <p class="mt-2 text-xs text-gray-600 line-clamp-2">
                <span class="font-mono mr-1">
                  {r.last_role === "user" ? "🙋" : "🤖"}
                </span>
                {r.last_content}
              </p>
            )}
            <p class="mt-2 text-xs text-gray-400">
              {r.message_count} メッセージ / thread #{r.id}
            </p>
          </li>
        ))}
      </ul>
    )}
  </Layout>
);

export const Result: FC<{
  correct: boolean;
  q: Question;
  slug: string;
  nextId: number | null;
}> = ({ correct, q, slug, nextId }) => (
  <div
    class={
      "mt-4 p-4 rounded border " +
      (correct ? "bg-green-50 border-green-300" : "bg-red-50 border-red-300")
    }
  >
    <p class="font-bold">
      {correct ? "✓ 正解" : `✗ 不正解 (正答: ${q.suggested_answer})`}
    </p>
    {q.explanation_ja && <p class="mt-2 text-sm leading-relaxed">{q.explanation_ja}</p>}
    {q.comments && (
      <details class="mt-3">
        <summary class="text-xs text-gray-500 cursor-pointer">
          examtopics コメント
        </summary>
        <p class="mt-2 text-xs text-gray-700 leading-relaxed">{q.comments}</p>
      </details>
    )}
    <div class="mt-3 flex gap-3 text-sm">
      {q.url && (
        <a href={q.url} target="_blank" rel="noopener" class="text-blue-600 hover:underline">
          ExamTopics で開く →
        </a>
      )}
      {nextId && (
        <a href={`/e/${slug}/q/${nextId}`} class="ml-auto text-blue-600 hover:underline">
          次の問題へ →
        </a>
      )}
    </div>
  </div>
);
