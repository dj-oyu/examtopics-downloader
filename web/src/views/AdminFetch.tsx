import type { FC } from "hono/jsx";
import { raw } from "hono/html";
import { Layout } from "./Layout";
import { loadClientScript } from "../client/loader";
import { formatLocalTimestamp } from "./timestamps";

export type AdminFetchJobView = {
  provider: string;
  slug: string;
  startedAt: number;
  running: boolean;
  exitCode: number | null;
};

export type AdminFetchProps = {
  providers: string[];
  defaultProvider: string;
  /** Surfaced when `examtopicsdl providers` failed at module init. */
  providerLoadError: string | null;
  binPath: string | null;
  binResolveError: string | null;
  job: AdminFetchJobView | null;
  /** Optional notice (e.g. flash message after a failed POST). */
  notice?: { kind: "info" | "error"; text: string } | null;
  loginEnabled: boolean;
  cookieAuthed: boolean;
  requestCount?: number;
};

const formatStarted = (ms: number): string => {
  const d = new Date(ms);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
};

export const AdminFetch: FC<AdminFetchProps> = ({
  providers,
  defaultProvider,
  providerLoadError,
  binPath,
  binResolveError,
  job,
  notice,
  loginEnabled,
  cookieAuthed,
  requestCount = 0,
}) => {
  const formDisabled = job !== null && job.running;
  const submitLabel = formDisabled ? "実行中…" : "取得を開始";

  return (
    <Layout title="管理: 取得" requestCount={requestCount}>
      <h1 class="text-xl font-bold mb-1">取得 (admin)</h1>
      <p class="text-xs text-gray-500 mb-4">
        provider / slug を指定して
        <code class="bg-white px-1 rounded border ml-1">
          examtopicsdl fetch
        </code>{" "}
        を子プロセス起動します。出力は下のログ枠に SSE で流れます。1
        ジョブ並列上限 = 1。
      </p>

      {binPath && (
        <p class="text-xs text-gray-400 mb-2 font-mono">
          bin: {binPath}
        </p>
      )}
      {binResolveError && (
        <div class="mb-3 p-3 bg-red-50 border border-red-200 rounded text-sm text-red-800">
          <p class="font-semibold mb-1">バイナリ解決エラー</p>
          <p class="font-mono text-xs whitespace-pre-wrap break-all">
            {binResolveError}
          </p>
        </div>
      )}
      {providerLoadError && (
        <div class="mb-3 p-3 bg-amber-50 border border-amber-200 rounded text-sm text-amber-800">
          <p class="font-semibold mb-1">プロバイダ一覧の取得に失敗</p>
          <p class="font-mono text-xs whitespace-pre-wrap break-all">
            {providerLoadError}
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
          <form
            method="post"
            action="/admin/login"
            class="mt-2 flex gap-2 items-center"
          >
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
          <form
            method="post"
            action="/admin/logout"
            class="inline"
          >
            <button
              type="submit"
              class="text-blue-600 hover:underline"
            >
              ログアウト
            </button>
          </form>
        </p>
      )}

      {providers.length > 0 ? (
        <form
          method="post"
          action="/admin/fetch"
          class="space-y-3 p-4 bg-white rounded shadow mb-4"
        >
          <div>
            <label
              htmlFor="admin-fetch-provider"
              class="block text-sm font-medium mb-1"
            >
              provider
            </label>
            <select
              id="admin-fetch-provider"
              name="provider"
              class="w-full px-2 py-1 border rounded font-mono"
              required
            >
              {providers.map((p) => (
                <option value={p} selected={p === defaultProvider || undefined}>
                  {p}
                </option>
              ))}
            </select>
          </div>
          <div>
            <label
              htmlFor="admin-fetch-slug"
              class="block text-sm font-medium mb-1"
            >
              slug
            </label>
            <input
              id="admin-fetch-slug"
              name="slug"
              type="text"
              required
              pattern="[A-Za-z0-9._\-]+"
              placeholder="saa-c03"
              class="w-full px-2 py-1 border rounded font-mono"
            />
            <p class="text-xs text-gray-400 mt-1">
              許容: <code>[A-Za-z0-9._-]+</code> (
              <code>.</code> / <code>..</code> は不可)
            </p>
          </div>
          <div class="flex items-center gap-3 pt-1">
            <button
              type="submit"
              data-role="admin-fetch-submit"
              disabled={formDisabled || undefined}
              class={
                "px-4 py-2 rounded text-white text-sm " +
                (formDisabled
                  ? "bg-gray-400 cursor-not-allowed"
                  : "bg-blue-600 hover:bg-blue-700")
              }
            >
              {submitLabel}
            </button>
            <span
              id="admin-fetch-status"
              class="ml-2 text-xs text-blue-600"
            ></span>
          </div>
        </form>
      ) : (
        <div class="mb-4 p-4 bg-gray-50 rounded border text-sm text-gray-600">
          プロバイダが解決できないためフォームを表示できません。上のエラーを参照してください。
        </div>
      )}

      {job ? (
        <div
          class={
            "mb-3 p-3 rounded border text-sm " +
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
            : provider=<b>{job.provider}</b> slug=<b>{job.slug}</b>
            <span class="ml-2 text-xs">
              started at {formatStarted(job.startedAt)} (
              {formatLocalTimestamp(new Date(job.startedAt).toISOString())})
            </span>
          </p>
        </div>
      ) : (
        <p class="mb-3 text-xs text-gray-400">
          実行中のジョブはありません。
        </p>
      )}

      <h2 class="text-sm font-semibold mb-1">ログ (SSE)</h2>
      <pre
        id="admin-fetch-log"
        class="bg-gray-900 text-gray-100 text-xs font-mono p-3 rounded h-72 overflow-auto whitespace-pre-wrap break-all"
      ></pre>

      <AdminFetchScript />
    </Layout>
  );
};

const AdminFetchScript: FC = () => {
  const js = loadClientScript("admin-fetch");
  return <script>{raw(js)}</script>;
};

/**
 * Standalone view used when EXAMTOPICS_ADMIN_TOKEN is set but the user
 * has not yet authenticated via cookie / Bearer. Shows just the login
 * form (the fetch UI is gated behind requireAdmin).
 */
export const AdminLogin: FC<{
  notice?: { kind: "info" | "error"; text: string } | null;
  requestCount?: number;
}> = ({ notice, requestCount = 0 }) => (
  <Layout title="admin login" requestCount={requestCount}>
    <h1 class="text-xl font-bold mb-3">admin login</h1>
    <p class="text-xs text-gray-500 mb-4">
      取得 UI に入るには <code>EXAMTOPICS_ADMIN_TOKEN</code> を入力してください。
      Bearer ヘッダで直接 POST する場合はログイン不要です。
    </p>
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
    <form
      method="post"
      action="/admin/login"
      class="space-y-3 p-4 bg-white rounded shadow max-w-md"
    >
      <div>
        <label htmlFor="admin-login-token" class="block text-sm mb-1">
          admin token
        </label>
        <input
          id="admin-login-token"
          type="password"
          name="token"
          required
          class="w-full px-2 py-1 border rounded font-mono"
          placeholder="EXAMTOPICS_ADMIN_TOKEN"
        />
      </div>
      <button
        type="submit"
        class="px-4 py-2 bg-blue-600 hover:bg-blue-700 text-white rounded"
      >
        ログイン
      </button>
    </form>
  </Layout>
);
