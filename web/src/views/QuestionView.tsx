import type { FC } from "hono/jsx";
import type {
  AnswerVerdict,
  Question,
  QuestionDetail,
  ThreadWithMessages,
} from "../db";
import { Layout } from "./Layout";
import { ThreadPanel } from "./Thread";
import { QuestionLiveScript, RetranslatePanel } from "./Retranslate";
import { formatLocalTimestamp } from "./timestamps";

export type AttemptResult = {
  correct: boolean;
  selected: string;
  /** Nothing to grade against (verdict `unknown`): no attempt row was written. */
  ungraded?: boolean;
};

/** Every answer that counts as correct, in the learner's words. */
const acceptedLabel = (verdict: AnswerVerdict, q: Question): string =>
  verdict.accepted.length ? verdict.accepted.join(" または ") : q.suggested_answer || "不明";

/**
 * The community vote split. Shown only AFTER the learner answers — before that
 * it would give the answer away.
 */
const CommunityVotes: FC<{ verdict: AnswerVerdict; q: Question }> = ({
  verdict,
  q,
}) => {
  if (verdict.community.length === 0) {
    return (
      <p class="mt-3 border-t pt-3 text-xs text-gray-500">
        コミュニティ投票は記録されていません（正解キーも無いため判定できません）。
      </p>
    );
  }
  const two = verdict.accepted.length === 2;
  return (
    <div class="mt-3 border-t pt-3">
      <p class="text-xs text-gray-500">
        コミュニティ投票（{verdict.total_votes}票）
      </p>
      <ul class="mt-2 space-y-1">
        {verdict.community.map((v) => (
          <li class="flex items-center gap-2 text-xs">
            <span class="font-mono w-8">{v.label}</span>
            <span class="flex-1 h-3 bg-gray-100 rounded">
              <span
                class={
                  "block h-3 rounded " +
                  (verdict.accepted.includes(v.label)
                    ? "bg-blue-500"
                    : "bg-gray-300")
                }
                style={`width: ${Math.max(v.pct, 2)}%`}
              />
            </span>
            <span class="w-24 text-right text-gray-600">
              {v.pct}% ({v.votes}票)
            </span>
          </li>
        ))}
      </ul>
      {verdict.status === "ambiguous" && (
        <p class="mt-2 text-xs text-amber-700">
          この問題はコミュニティでも意見が割れており、
          {two ? "どちらも" : "いずれも"}正解として扱います（正答:{" "}
          {acceptedLabel(verdict, q)}）。
        </p>
      )}
      {verdict.rationale && (
        <p class="mt-1 text-xs text-gray-400">{verdict.rationale}</p>
      )}
    </div>
  );
};

const Result: FC<{
  correct: boolean;
  ungraded: boolean;
  q: Question;
  verdict: AnswerVerdict;
  slug: string;
  nextId: number | null;
}> = ({ correct, ungraded, q, verdict, slug, nextId }) => (
  <div
    class={
      "mt-4 p-4 rounded border " +
      (ungraded
        ? "bg-gray-50 border-gray-300"
        : correct
          ? "bg-green-50 border-green-300"
          : "bg-red-50 border-red-300")
    }
  >
    <p class="font-bold">
      {ungraded
        ? "— 判定不能（正解を特定できない問題のため採点対象外）"
        : correct
          ? "✓ 正解"
          : `✗ 不正解 (正答: ${acceptedLabel(verdict, q)})`}
    </p>
    <CommunityVotes verdict={verdict} q={q} />
    {q.explanation_ja && (
      <p class="mt-2 text-sm leading-relaxed">{q.explanation_ja}</p>
    )}
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
        <a
          href={q.url}
          target="_blank"
          rel="noopener"
          class="text-blue-600 hover:underline"
        >
          ExamTopics で開く →
        </a>
      )}
      {nextId && (
        <a
          href={`/e/${slug}/q/${nextId}`}
          class="ml-auto text-blue-600 hover:underline"
        >
          次の問題へ →
        </a>
      )}
    </div>
  </div>
);

export type QuestionViewProps = QuestionDetail & {
  slug: string;
  result?: AttemptResult | null;
  thread: ThreadWithMessages | null;
  retranslatePending: boolean;
  explainPending?: boolean;
  requestCount: number;
};

export const QuestionView: FC<QuestionViewProps> = ({
  slug,
  q,
  choices,
  attempts,
  verdict,
  prevId,
  nextId,
  result,
  thread,
  retranslatePending,
  explainPending = false,
  requestCount,
}) => {
  const isMulti = q.suggested_answer.length > 1;
  const picked = new Set(result ? result.selected.split("") : []);
  return (
    <Layout title={`Q${q.question_number}`} requestCount={requestCount} wide>
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
            <a
              href={`/e/${slug}/q/${prevId}`}
              class="text-blue-600 hover:underline"
            >
              ← 前
            </a>
          )}
          {nextId && (
            <a
              href={`/e/${slug}/q/${nextId}`}
              class="text-blue-600 hover:underline"
            >
              次 →
            </a>
          )}
        </div>
      </div>

      {/*
        On lg+ split into two columns: question pane stays sticky on the left
        while the chat pane scrolls naturally on the right. On smaller
        viewports the panes stack as two cards.
      */}
      <div class="lg:grid lg:grid-cols-2 lg:gap-6 lg:items-start">
        <article class="bg-white rounded-lg shadow p-6 space-y-4 lg:sticky lg:top-4 lg:max-h-[calc(100vh-2rem)] lg:overflow-y-auto">
          {verdict.status !== "settled" && (
            <p
              class={
                "text-xs px-2 py-1 rounded inline-block " +
                (verdict.status === "ambiguous"
                  ? "bg-amber-100 text-amber-800"
                  : "bg-gray-100 text-gray-600")
              }
            >
              {verdict.status === "ambiguous"
                ? "⚠ あいまいな問題（コミュニティでも意見が割れています）"
                : "⚠ 正解が特定できない問題（図から判断する問題・採点対象外）"}
            </p>
          )}
          <p class="leading-relaxed">{q.question_text_ja ?? q.question_text}</p>
          {q.question_text_ja && (
            <details class="text-sm">
              <summary class="text-gray-500 cursor-pointer">原文を表示</summary>
              <p class="mt-2 text-gray-700">{q.question_text}</p>
            </details>
          )}

          <form
            method="post"
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
                      <summary class="text-xs text-gray-400 cursor-pointer">
                        原文
                      </summary>
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

          {result && (
            <Result
              correct={result.correct}
              ungraded={result.ungraded === true}
              q={q}
              verdict={verdict}
              slug={slug}
              nextId={nextId}
            />
          )}

          <RetranslatePanel
            slug={slug}
            qid={q.id}
            pending={retranslatePending}
          />

          {attempts.length > 0 && (
            <details class="text-sm border-t pt-4">
              <summary class="text-gray-500 cursor-pointer">
                直近の解答履歴 ({attempts.length})
              </summary>
              <ul class="mt-2 space-y-1">
                {attempts.map((a) => (
                  <li class="font-mono text-xs">
                    <span
                      class={a.is_correct ? "text-green-600" : "text-red-600"}
                    >
                      {a.is_correct ? "○" : "×"}
                    </span>
                    <span class="ml-2">{a.selected || "(未選択)"}</span>
                    <span class="ml-2 text-gray-500">
                      {formatLocalTimestamp(a.attempted_at)}
                    </span>
                  </li>
                ))}
              </ul>
            </details>
          )}
        </article>

        <article class="bg-white rounded-lg shadow p-6 mt-4 lg:mt-0">
          <ThreadPanel
            slug={slug}
            q={q}
            thread={thread}
            explainPending={explainPending}
          />
          <QuestionLiveScript slug={slug} qid={q.id} />
        </article>
      </div>
    </Layout>
  );
};
