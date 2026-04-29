import type { FC } from "hono/jsx";
import type { QuestionListRow } from "../db";
import { Layout } from "./Layout";

const Status: FC<{ code: number | null }> = ({ code }) => {
  if (code === null) return <span class="text-xs text-gray-400">未</span>;
  if (code === 1) return <span class="text-xs text-green-600">正</span>;
  return <span class="text-xs text-red-600">誤</span>;
};

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
          <a
            href={`/e/${slug}/review`}
            class="ml-2 text-blue-600 hover:underline"
          >
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
              (filter === f
                ? "bg-blue-600 text-white"
                : "text-blue-600 hover:underline")
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
            <span class="font-mono text-sm w-12 text-gray-500">
              #{q.question_number}
            </span>
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
