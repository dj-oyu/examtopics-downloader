import type { FC } from "hono/jsx";
import type { ThreadListRowAll } from "../db";
import { Layout } from "./Layout";

export const Threads: FC<{
  rows: ThreadListRowAll[];
  requestCount: number;
}> = ({ rows, requestCount }) => (
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
                <span class="font-mono text-xs text-gray-500 mr-2">
                  {r.slug}
                </span>
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
