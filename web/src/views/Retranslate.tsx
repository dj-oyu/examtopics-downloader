import type { FC } from "hono/jsx";
import { raw } from "hono/html";

export const RetranslatePanel: FC<{
  slug: string;
  qid: number;
  pending: boolean;
}> = ({ slug, qid, pending }) => (
  <details class="border-t pt-4 text-sm" open={pending || undefined}>
    <summary class="cursor-pointer text-gray-500 hover:text-gray-700">
      ⚙ メンテナンス
      {pending && (
        <span class="ml-2 inline-flex items-center gap-1 text-xs text-orange-600">
          <span class="inline-block w-2 h-2 bg-orange-500 rounded-full animate-pulse"></span>
          翻訳実行中
        </span>
      )}
    </summary>
    <div class="mt-2 space-y-2">
      <p class="text-xs text-gray-500">
        和訳が壊れている / 意味が通らない場合、サーバ側で
        <code class="bg-gray-100 px-1 rounded">claude-sonnet-4-6</code>
        を使って 1 件だけ翻訳をやり直します (skill `exam-translator`
        のワークフローを単発実行)。
      </p>
      <form
        method="POST"
        action={`/e/${slug}/q/${qid}/retranslate`}
        class="flex items-center gap-2"
      >
        <button
          type="submit"
          disabled={pending || undefined}
          class={
            "px-3 py-1 text-xs rounded " +
            (pending
              ? "bg-gray-200 text-gray-400 cursor-not-allowed"
              : "bg-orange-100 hover:bg-orange-200 text-orange-800")
          }
        >
          翻訳をやり直す
        </button>
        <span
          id={`retrans-indicator-${qid}`}
          class={
            "text-xs inline-flex items-center gap-1 " +
            (pending ? "text-orange-600" : "text-gray-400")
          }
          style={pending ? "" : "display:none"}
        >
          <span class="inline-block w-2 h-2 bg-orange-500 rounded-full animate-pulse"></span>
          <span data-role="msg">翻訳やり直し中… 完了で自動リロード</span>
        </span>
      </form>
    </div>
  </details>
);

export const QuestionLiveScript: FC<{ slug: string; qid: number }> = ({
  slug,
  qid,
}) => {
  const slugLit = JSON.stringify(slug);
  const qidLit = String(qid);
  const js = `
(function(){
  var slug = ${slugLit};
  var qid = ${qidLit};
  var indicator = document.getElementById("retrans-indicator-" + qid);
  function setIndicator(text, kind){
    if (!indicator) return;
    indicator.style.display = text ? "inline-flex" : "none";
    indicator.className = "text-xs inline-flex items-center gap-1 " +
      (kind === "error" ? "text-red-600" : kind === "ok" ? "text-green-600" : "text-orange-600");
    var msg = indicator.querySelector('[data-role="msg"]');
    if (msg) msg.textContent = text;
    var dot = indicator.querySelector('span:not([data-role])');
    if (dot) {
      dot.className = "inline-block w-2 h-2 rounded-full " +
        (kind === "error" ? "bg-red-500" :
         kind === "ok" ? "bg-green-500" :
         "bg-orange-500 animate-pulse");
    }
  }
  var es = new EventSource("/e/" + slug + "/q/" + qid + "/events");
  es.onmessage = function(e){
    var ev; try { ev = JSON.parse(e.data); } catch(_) { return; }
    if (ev.type === "translation-updated") {
      setIndicator("✓ 翻訳完了 — リロード中…", "ok");
      setTimeout(function(){ location.reload(); }, 600);
    } else if (ev.type === "translation-failed") {
      setIndicator("⚠ 翻訳失敗: " + (ev.message || "unknown"), "error");
    }
  };
  es.onerror = function(){};
})();`;
  return <script>{raw(js)}</script>;
};
