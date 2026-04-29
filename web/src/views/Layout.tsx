import type { FC, PropsWithChildren } from "hono/jsx";
import type { ExamSummary } from "../db";

export const Layout: FC<
  PropsWithChildren<{ title: string; requestCount?: number }>
> = ({ title, requestCount = 0, children }) => (
  <html lang="ja">
    <head>
      <meta charset="utf-8" />
      <meta name="viewport" content="width=device-width, initial-scale=1" />
      <title>{title} — Exam Studio</title>
      <script src="https://cdn.tailwindcss.com?plugins=typography"></script>
    </head>
    <body class="min-h-screen bg-gray-50 text-gray-900">
      <header class="bg-white border-b">
        <nav class="max-w-3xl mx-auto px-4 py-3 flex gap-4 items-center">
          <a href="/" class="font-semibold">
            Exam Studio
          </a>
          <a href="/requests" class="text-amber-700 hover:underline ml-auto">
            🙏 解説求む{" "}
            {requestCount > 0 && (
              <span class="ml-1 px-2 py-0.5 text-xs bg-amber-500 text-white rounded-full">
                {requestCount}
              </span>
            )}
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
      <p class="text-gray-500">
        試験 DB が見つかりません。リポジトリ直下に *.db を配置してください。
      </p>
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
                問題: {e.total} / 和訳済: {e.translated} (
                {e.total ? Math.round((e.translated / e.total) * 100) : 0}%)
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
