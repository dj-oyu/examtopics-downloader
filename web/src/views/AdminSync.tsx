import type { FC } from "hono/jsx";
import { raw } from "hono/html";
import { Layout } from "./Layout";
import { loadClientScript } from "../client/loader";
import { formatLocalTimestamp } from "./timestamps";

export type AdminSyncExamOption = {
  slug: string;
  name: string;
  total: number;
  translated: number;
};

export type AdminSyncPeer = {
  path: string;
  name: string;
  dir: string;
  size: number;
  mtimeMs: number;
  isExamDb: boolean;
};

export type AdminSyncConflictView = {
  field: string;
  url: string;
  /** Question id inside the exam DB, when the url is known there. */
  qid: number | null;
};

export type AdminSyncResultView = {
  filledQuestions: number;
  filledChoices: number;
  conflictCount: number;
  overwritten: number;
  preferPeer: boolean;
  dryRun: boolean;
  conflicts: AdminSyncConflictView[];
};

export type AdminSyncJobView = {
  kind: "translations" | "snapshot";
  slug: string;
  peerPath: string | null;
  startedAt: number;
  running: boolean;
  exitCode: number | null;
  result: AdminSyncResultView | null;
  snapshotPath: string | null;
};

export type AdminSyncProps = {
  exams: AdminSyncExamOption[];
  defaultSlug: string;
  peers: AdminSyncPeer[];
  snapshots: { name: string; size: number; mtimeMs: number }[];
  binPath: string | null;
  binResolveError: string | null;
  job: AdminSyncJobView | null;
  notice?: { kind: "info" | "error"; text: string } | null;
  loginEnabled: boolean;
  cookieAuthed: boolean;
  requestCount?: number;
};

const fmtBytes = (n: number): string =>
  n > 1024 * 1024
    ? `${(n / 1024 / 1024).toFixed(1)} MB`
    : `${Math.max(1, Math.round(n / 1024))} KB`;

const fmtWhen = (ms: number): string =>
  formatLocalTimestamp(new Date(ms).toISOString());

export const AdminSync: FC<AdminSyncProps> = ({
  exams,
  defaultSlug,
  peers,
  snapshots,
  binPath,
  binResolveError,
  job,
  notice,
  loginEnabled,
  cookieAuthed,
  requestCount = 0,
}) => {
  const busy = job !== null && job.running;
  const result = job?.result ?? null;

  return (
    <Layout title="管理: 同期" requestCount={requestCount}>
      <h1 class="text-xl font-bold mb-1">ローカル同期 (admin)</h1>
      <p class="text-xs text-gray-500 mb-4">
        別のマシンで進めた和訳を
        <code class="bg-white px-1 rounded border mx-1">sync translations</code>
        で取り込みます。和訳列だけを加法的にマージし（削除なし・設問追加なし）、
        両方が訳していて食い違う箇所は<b>衝突として報告</b>します。
        <a href="/admin/fetch" class="text-blue-600 hover:underline ml-2">
          ← 取得 (admin)
        </a>
      </p>
      <p class="text-xs text-gray-400 mb-3">
        手順: ① 他のマシンで「スナップショットを書き出す」→ ファイルを持ってくる
        （Syncthing / USB / scp など、自分の経路）→ ② このマシンで「取り込む」。
        プレビュー（dry-run）は書き込まずに件数と衝突だけ出します。
      </p>

      {binPath && (
        <p class="text-xs text-gray-400 mb-2 font-mono">bin: {binPath}</p>
      )}
      {binResolveError && (
        <div class="mb-3 p-3 bg-red-50 border border-red-200 rounded text-sm text-red-800">
          <p class="font-semibold mb-1">バイナリ解決エラー</p>
          <p class="font-mono text-xs whitespace-pre-wrap break-all">
            {binResolveError}
          </p>
        </div>
      )}

      {notice && (
        <div
          class={
            "mb-3 p-3 rounded text-sm " +
            (notice.kind === "error"
              ? "bg-red-50 border border-red-200 text-red-800"
              : "bg-blue-50 border border-blue-200 text-blue-800")
          }
        >
          {notice.text}
        </div>
      )}

      {loginEnabled && !cookieAuthed && (
        <details class="mb-3 text-xs">
          <summary class="cursor-pointer text-gray-500">
            🔐 セッションログイン (Bearer ヘッダ無しでフォーム POST したい場合)
          </summary>
          <form method="post" action="/admin/login" class="mt-2 flex gap-2 items-center">
            <label class="flex-1">
              <span class="block text-gray-500 mb-1">admin token</span>
              <input
                type="password"
                name="token"
                required
                class="w-full px-2 py-1 border rounded font-mono"
                placeholder="EXAMTOPICS_ADMIN_TOKEN"
              />
            </label>
            <button
              type="submit"
              class="px-3 py-1 mt-4 text-xs bg-gray-800 hover:bg-gray-900 text-white rounded"
            >
              ログイン
            </button>
          </form>
        </details>
      )}
      {cookieAuthed && (
        <p class="mb-3 text-xs text-green-700">
          🔓 セッション認証済み。{" "}
          <form method="post" action="/admin/logout" class="inline">
            <button type="submit" class="text-blue-600 hover:underline">
              ログアウト
            </button>
          </form>
        </p>
      )}

      {exams.length === 0 ? (
        <div class="mb-4 p-4 bg-gray-50 rounded border text-sm text-gray-600">
          試験 DB が見つかりません。まず取得するか、dataDir に *.db を置いてください。
        </div>
      ) : (
        <div class="grid gap-4 lg:grid-cols-2">
          <form
            method="post"
            action="/admin/sync/translations"
            class="space-y-3 p-4 bg-white rounded shadow"
          >
            <h2 class="text-sm font-semibold">① 和訳を取り込む (このマシンへ)</h2>
            <div>
              <label htmlFor="admin-sync-slug" class="block text-sm mb-1">
                取り込み先の試験
              </label>
              <select
                id="admin-sync-slug"
                name="slug"
                class="w-full px-2 py-1 border rounded font-mono"
                required
              >
                {exams.map((e) => (
                  <option value={e.slug} selected={e.slug === defaultSlug || undefined}>
                    {e.slug} — {e.name} ({e.translated}/{e.total} 和訳済み)
                  </option>
                ))}
              </select>
            </div>
            <div>
              <label htmlFor="admin-sync-peer" class="block text-sm mb-1">
                相手マシンのスナップショット
              </label>
              {peers.length > 0 ? (
                <select
                  id="admin-sync-peer"
                  name="peer"
                  class="w-full px-2 py-1 border rounded font-mono"
                >
                  {peers.map((p) => (
                    <option value={p.path}>
                      {p.name} — {fmtBytes(p.size)} — {fmtWhen(p.mtimeMs)}
                      {p.isExamDb ? "  ⚠ 試験DB" : ""}
                    </option>
                  ))}
                </select>
              ) : (
                <input
                  id="admin-sync-peer"
                  name="peer"
                  type="text"
                  placeholder="/data/incoming/other-machine.db"
                  class="w-full px-2 py-1 border rounded font-mono"
                />
              )}
              <input
                name="peerPath"
                type="text"
                placeholder="パスを直接指定する場合（上の選択より優先）"
                class="w-full mt-1 px-2 py-1 border rounded font-mono text-xs"
              />
              <p class="text-xs text-gray-400 mt-1">
                dataDir 直下と <code>dataDir/incoming</code> の *.db を一覧しています。
                ⚠ は試験DB本体（相手マシンで「書き出す」を実行したスナップショットを選ぶのが安全）。
              </p>
            </div>
            <div class="flex gap-4 items-center text-sm">
              <label class="flex items-center gap-1">
                <input type="radio" name="prefer" value="local" checked />
                衝突はこのマシンの訳を残す
              </label>
              <label class="flex items-center gap-1">
                <input type="radio" name="prefer" value="peer" />
                衝突は相手の訳で置き換える
              </label>
            </div>
            <div class="flex gap-3 items-center">
              <label class="flex items-center gap-1 text-sm">
                <input type="checkbox" name="dryRun" value="1" />
                プレビュー (dry-run)
              </label>
              <button
                type="submit"
                disabled={busy || undefined}
                class={
                  "px-4 py-2 rounded text-white text-sm " +
                  (busy
                    ? "bg-gray-400 cursor-not-allowed"
                    : "bg-blue-600 hover:bg-blue-700")
                }
              >
                {busy ? "実行中…" : "取り込みを実行"}
              </button>
              <span id="admin-sync-status" class="text-xs text-blue-600"></span>
            </div>
          </form>

          <div class="space-y-3 p-4 bg-white rounded shadow">
            <form method="post" action="/admin/sync/snapshot" class="space-y-3">
              <h2 class="text-sm font-semibold">② スナップショットを書き出す (他のマシンへ)</h2>
              <div>
                <label htmlFor="admin-sync-export-slug" class="block text-sm mb-1">
                  対象の試験
                </label>
                <select
                  id="admin-sync-export-slug"
                  name="slug"
                  class="w-full px-2 py-1 border rounded font-mono"
                  required
                >
                  {exams.map((e) => (
                    <option value={e.slug} selected={e.slug === defaultSlug || undefined}>
                      {e.slug} — {e.name}
                    </option>
                  ))}
                </select>
              </div>
              <div class="flex gap-3 items-center">
                <button
                  type="submit"
                  disabled={busy || undefined}
                  class={
                    "px-4 py-2 rounded text-white text-sm " +
                    (busy
                      ? "bg-gray-400 cursor-not-allowed"
                      : "bg-gray-800 hover:bg-gray-900")
                  }
                >
                  {job?.kind === "snapshot" && busy ? "作成中…" : "書き出す"}
                </button>
                <span class="text-xs text-gray-400">
                  VACUUM INTO なので稼働中の DB でも安全
                </span>
              </div>
            </form>

            <div>
              <h3 class="text-xs font-semibold mb-1">
                書き出し済み (dataDir/outgoing)
              </h3>
              {snapshots.length === 0 ? (
                <p class="text-xs text-gray-400">まだありません。</p>
              ) : (
                <ul class="space-y-1">
                  {snapshots.map((s) => (
                    <li class="text-xs font-mono">
                      <a
                        href={`/admin/sync/download?name=${encodeURIComponent(s.name)}`}
                        class="text-blue-600 hover:underline"
                      >
                        {s.name}
                      </a>
                      <span class="text-gray-500 ml-2">
                        {fmtBytes(s.size)} — {fmtWhen(s.mtimeMs)}
                      </span>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          </div>
        </div>
      )}

      {job && (
        <div
          class={
            "mt-4 p-3 rounded border text-sm " +
            (job.running
              ? "bg-blue-50 border-blue-200 text-blue-800"
              : job.exitCode === 0
                ? "bg-green-50 border-green-200 text-green-800"
                : "bg-red-50 border-red-200 text-red-800")
          }
        >
          <p class="font-mono">
            {job.running
              ? "running"
              : job.exitCode === 0
                ? "ok"
                : `failed (exit ${job.exitCode})`}
            : {job.kind} slug=<b>{job.slug}</b>
            {job.peerPath && (
              <span>
                {" "}
                peer=<b class="break-all">{job.peerPath}</b>
              </span>
            )}
            <span class="ml-2 text-xs">{fmtWhen(job.startedAt)}</span>
          </p>
          {job.snapshotPath && (
            <p class="mt-1 font-mono text-xs break-all">
              出力: {job.snapshotPath}
            </p>
          )}
        </div>
      )}

      {result && (
        <div class="mt-3 p-4 bg-white rounded shadow">
          <h2 class="text-sm font-semibold mb-2">
            {result.dryRun ? "プレビュー結果（未書き込み）" : "取り込み結果"}
          </h2>
          <ul class="text-sm space-y-1">
            <li>
              埋めた設問フィールド: <b>{result.filledQuestions}</b> ／ 選択肢:{" "}
              <b>{result.filledChoices}</b>
            </li>
            <li>
              衝突: <b>{result.conflictCount}</b>
              {result.preferPeer ? (
                <span>（相手の訳で置換: {result.overwritten}）</span>
              ) : (
                <span>（このマシンの訳を保持）</span>
              )}
            </li>
          </ul>
          {result.conflicts.length > 0 && (
            <details class="mt-3" open={result.conflicts.length <= 10 || undefined}>
              <summary class="text-xs text-gray-500 cursor-pointer">
                衝突した箇所 ({result.conflicts.length}
                {result.conflictCount > result.conflicts.length
                  ? ` / 全 ${result.conflictCount}`
                  : ""}
                )
              </summary>
              <table class="mt-2 w-full text-xs">
                <thead>
                  <tr class="text-gray-500 text-left">
                    <th class="py-1">フィールド</th>
                    <th class="py-1">設問</th>
                  </tr>
                </thead>
                <tbody>
                  {result.conflicts.map((c) => (
                    <tr class="border-t">
                      <td class="py-1 font-mono">{c.field}</td>
                      <td class="py-1">
                        {c.qid !== null ? (
                          <a
                            href={`/e/${job?.slug}/q/${c.qid}`}
                            class="text-blue-600 hover:underline font-mono"
                          >
                            Q#{c.qid} を開く
                          </a>
                        ) : (
                          <span class="font-mono text-gray-500 break-all">
                            {c.url}
                          </span>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {!result.preferPeer && !result.dryRun && (
                <p class="mt-2 text-xs text-gray-500">
                  相手の訳を採用したい場合は「衝突は相手の訳で置き換える」を選んで再実行してください。
                </p>
              )}
            </details>
          )}
        </div>
      )}

      <h2 class="text-sm font-semibold mt-4 mb-1">ログ (SSE)</h2>
      <pre
        id="admin-sync-log"
        data-running={busy ? "true" : "false"}
        class="bg-gray-900 text-gray-100 text-xs font-mono p-3 rounded h-72 overflow-auto whitespace-pre-wrap break-all"
      ></pre>

      <AdminSyncScript />
    </Layout>
  );
};

const AdminSyncScript: FC = () => {
  const js = loadClientScript("admin-sync");
  return <script>{raw(js)}</script>;
};
