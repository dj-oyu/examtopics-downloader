import { Hono } from "hono";
import type { Context } from "hono";
import { marked } from "marked";
import * as q from "./db";
import * as uuidx from "./uuidx";
import { formatLocalTimestamp } from "./views/timestamps";

// parseThreadId validates a 26-char Crockford base32 thread id from
// req.param("id"). Returns the canonical form on success, null when
// the segment is the wrong shape (length, charset, oversized first
// char). Routes use this in place of the legacy parseInt to avoid
// silently coercing garbage to NaN.
function parseThreadId(c: Context): string | null {
  const raw = c.req.param("id") ?? "";
  try {
    uuidx.decode(raw);
    return raw;
  } catch {
    return null;
  }
}
import {
  enqueueExplain,
  enqueueRetranslate,
  isExplainPending,
  isRetranslatePending,
  recoverAwaitingThreads,
  requestClose,
  subscribe,
  subscribeQuestion,
} from "./agent";
import type { QuestionSseEvent, SseEvent } from "./agent";
import { Home, QuestionList, QuestionView, Layout, Threads } from "./views";

marked.setOptions({ gfm: true, breaks: false });

const app = new Hono();

const reqCount = () => q.countAwaitingAgentAll();

app.use("*", async (c, next) => {
  if (c.req.method === "POST") {
    const origin = c.req.header("origin");
    const referer = c.req.header("referer");
    const host = c.req.header("host");
    const expected = host ? [`http://${host}`, `https://${host}`] : [];
    const sameOrigin =
      (origin && expected.includes(origin)) ||
      (referer && expected.some((e) => referer.startsWith(e + "/")));
    if (!sameOrigin) {
      return c.text("forbidden: cross-origin POST blocked", 403);
    }
  }
  await next();
});

app.use("/e/:slug/*", async (c, next) => {
  const slug = c.req.param("slug");
  if (!q.isKnownExam(slug)) return c.notFound();
  await next();
});

app.get("/", (c) => {
  return c.html(<Home exams={q.discoverExams()} requestCount={reqCount()} />);
});

app.get("/e/:slug/q", (c) => {
  const slug = c.req.param("slug");
  let exams: q.ExamSummary | undefined;
  try {
    exams = q.discoverExams().find((e) => e.slug === slug);
    if (!exams) return c.notFound();
  } catch {
    return c.notFound();
  }
  const filter = c.req.query("filter") ?? "all";
  const all = q.listQuestions(slug);
  const rows = all.filter((r) => {
    if (filter === "wrong") return r.last_correct === 0;
    if (filter === "unanswered") return r.last_correct === null;
    return true;
  });
  return c.html(
    <QuestionList
      slug={slug}
      exam={exams.name}
      filter={filter}
      rows={rows}
      prog={q.progress(slug)}
      requestCount={reqCount()}
    />
  );
});

app.get("/e/:slug/q/:id", (c) => {
  const slug = c.req.param("slug");
  const id = parseInt(c.req.param("id"), 10);
  if (Number.isNaN(id)) return c.notFound();
  let data;
  try {
    data = q.getQuestion(slug, id);
  } catch {
    return c.notFound();
  }
  if (!data) return c.notFound();
  const thread = q.getOpenThread(slug, id);
  return c.html(
    <QuestionView
      slug={slug}
      {...data}
      thread={thread}
      retranslatePending={isRetranslatePending(slug, id)}
      explainPending={thread ? isExplainPending(slug, thread.id) : false}
      requestCount={reqCount()}
    />
  );
});

app.post("/e/:slug/q/:id/attempt", async (c) => {
  const slug = c.req.param("slug");
  const id = parseInt(c.req.param("id"), 10);
  const data = q.getQuestion(slug, id);
  if (!data) return c.notFound();
  const form = await c.req.parseBody({ all: true });
  const raw = form["selected"];
  const picks = (Array.isArray(raw) ? raw : raw ? [raw] : [])
    .map(String)
    .sort();
  const selected = picks.join("");
  const correct = q.recordAttempt(slug, id, selected, data.q.suggested_answer);
  const fresh = q.getQuestion(slug, id)!;
  const thread = q.getOpenThread(slug, id);
  return c.html(
    <QuestionView
      slug={slug}
      {...fresh}
      result={{ correct, selected }}
      thread={thread}
      retranslatePending={isRetranslatePending(slug, id)}
      explainPending={thread ? isExplainPending(slug, thread.id) : false}
      requestCount={reqCount()}
    />
  );
});

app.post("/e/:slug/q/:id/retranslate", (c) => {
  const slug = c.req.param("slug");
  const id = parseInt(c.req.param("id"), 10);
  if (Number.isNaN(id)) return c.notFound();
  const data = q.getQuestion(slug, id);
  if (!data) return c.notFound();
  q.clearQuestionTranslation(slug, id);
  enqueueRetranslate(slug, id);
  return c.redirect(`/e/${slug}/q/${id}`);
});

app.get("/e/:slug/q/:id/events", (c) => {
  const slug = c.req.param("slug");
  const qid = parseInt(c.req.param("id"), 10);
  if (Number.isNaN(qid)) return c.notFound();
  if (!q.getQuestion(slug, qid)) return c.notFound();

  const stream = new ReadableStream({
    start(controller) {
      const enc = new TextEncoder();
      const send = (event: QuestionSseEvent) => {
        try {
          controller.enqueue(
            enc.encode(`data: ${JSON.stringify(event)}\n\n`)
          );
        } catch {}
      };
      const unsub = subscribeQuestion(slug, qid, send);
      controller.enqueue(enc.encode(`: connected\n\n`));
      const keepAlive = setInterval(() => {
        try {
          controller.enqueue(enc.encode(`: ping\n\n`));
        } catch {}
      }, 25000);
      const abort = () => {
        clearInterval(keepAlive);
        unsub();
        try {
          controller.close();
        } catch {}
      };
      c.req.raw.signal.addEventListener("abort", abort);
    },
  });

  return new Response(stream, {
    headers: {
      "Content-Type": "text/event-stream",
      "Cache-Control": "no-cache",
      "X-Accel-Buffering": "no",
    },
  });
});

app.post("/e/:slug/q/:id/threads", async (c) => {
  const slug = c.req.param("slug");
  const id = parseInt(c.req.param("id"), 10);
  const form = await c.req.parseBody();
  const content = ((form.content as string) || "").trim();
  if (!content) return c.redirect(`/e/${slug}/q/${id}`);
  if (q.getOpenThread(slug, id)) return c.redirect(`/e/${slug}/q/${id}`);
  const tid = q.createThread(slug, id, content, "web");
  enqueueExplain(slug, tid);
  return c.redirect(`/e/${slug}/q/${id}`);
});

app.post("/e/:slug/threads/:id/reply", async (c) => {
  const slug = c.req.param("slug");
  const tid = parseThreadId(c);
  if (tid === null) return c.notFound();
  const form = await c.req.parseBody();
  const content = ((form.content as string) || "").trim();
  if (content) {
    const m = q.appendMessage(slug, tid, "user", content, "web");
    if (m) enqueueExplain(slug, tid);
  }
  const t = q.getThread(slug, tid);
  return c.redirect(t ? `/e/${slug}/q/${t.question_id}` : "/requests");
});

app.post("/e/:slug/threads/:id/resolve", (c) => {
  const slug = c.req.param("slug");
  const tid = parseThreadId(c);
  if (tid === null) return c.notFound();
  const t = q.getThread(slug, tid);
  requestClose(slug, tid, "resolved");
  return c.redirect(t ? `/e/${slug}/q/${t.question_id}` : "/requests");
});

app.post("/e/:slug/threads/:id/dismiss", (c) => {
  const slug = c.req.param("slug");
  const tid = parseThreadId(c);
  if (tid === null) return c.notFound();
  const t = q.getThread(slug, tid);
  requestClose(slug, tid, "dismissed");
  return c.redirect(t ? `/e/${slug}/q/${t.question_id}` : "/requests");
});

app.get("/e/:slug/threads/:id/messages.json", (c) => {
  const slug = c.req.param("slug");
  const tid = parseThreadId(c);
  if (tid === null) return c.notFound();
  const t = q.getThread(slug, tid);
  if (!t) return c.notFound();
  return c.json({
    id: t.id,
    status: t.status,
    messages: t.messages.map((m) => ({
      id: m.id,
      role: m.role,
      author: m.author,
      content: m.content,
      content_html:
        m.role === "agent"
          ? (marked.parse(m.content, { async: false }) as string)
          : null,
      reason_code: m.reason_code,
      citations: q.parseCitations(m.citations),
      created_at: formatLocalTimestamp(m.created_at),
    })),
  });
});

app.get("/e/:slug/threads/:id/events", (c) => {
  const slug = c.req.param("slug");
  const tid = parseThreadId(c);
  if (tid === null) return c.notFound();
  if (!q.getThread(slug, tid)) return c.notFound();

  const stream = new ReadableStream({
    start(controller) {
      const enc = new TextEncoder();
      const send = (event: SseEvent) => {
        try {
          controller.enqueue(
            enc.encode(`data: ${JSON.stringify(event)}\n\n`)
          );
        } catch {}
      };
      const unsub = subscribe(slug, tid, send);
      controller.enqueue(enc.encode(`: connected\n\n`));
      const keepAlive = setInterval(() => {
        try {
          controller.enqueue(enc.encode(`: ping\n\n`));
        } catch {}
      }, 25000);
      const abort = () => {
        clearInterval(keepAlive);
        unsub();
        try {
          controller.close();
        } catch {}
      };
      c.req.raw.signal.addEventListener("abort", abort);
    },
  });

  return new Response(stream, {
    headers: {
      "Content-Type": "text/event-stream",
      "Cache-Control": "no-cache",
      "X-Accel-Buffering": "no",
    },
  });
});

app.get("/requests", (c) => {
  const rows = q.listOpenThreadsAll();
  return c.html(<Threads rows={rows} requestCount={reqCount()} />);
});

app.get("/e/:slug/review", (c) => {
  const slug = c.req.param("slug");
  let summary: q.ExamSummary | undefined;
  try {
    summary = q.discoverExams().find((e) => e.slug === slug);
    if (!summary) return c.notFound();
  } catch {
    return c.notFound();
  }
  const rows = q.listWrong(slug);
  return c.html(
    <Layout title="復習キュー" requestCount={reqCount()}>
      <div class="mb-4">
        <a href={`/e/${slug}/q`} class="text-blue-600 hover:underline text-sm">
          ← {summary.name}
        </a>
      </div>
      <h1 class="text-xl font-bold mb-4">復習キュー (直近で誤答した問題)</h1>
      {rows.length === 0 ? (
        <p class="text-gray-500">該当なし。すべて正解中か未解答です。</p>
      ) : (
        <ul class="divide-y bg-white rounded shadow">
          {rows.map((r) => (
            <li>
              <a
                href={`/e/${slug}/q/${r.id}`}
                class="flex gap-3 items-baseline px-4 py-3 hover:bg-gray-50"
              >
                <span class="font-mono text-sm w-12 text-gray-500">
                  #{r.question_number}
                </span>
                <span class="flex-1 text-sm line-clamp-2">
                  {r.question_text_ja ?? r.question_text}
                </span>
                <span class="text-xs text-red-600">誤</span>
              </a>
            </li>
          ))}
        </ul>
      )}
    </Layout>
  );
});

const port = parseInt(process.env.PORT ?? "3000", 10);
const hostname = process.env.HOST ?? "127.0.0.1";
console.log(`listening on http://${hostname}:${port}`);
recoverAwaitingThreads();
// idleTimeout: 0 keeps SSE connections open; default 10s would cut them
// before our 25s keep-alive ping fires.
export default { fetch: app.fetch, port, hostname, idleTimeout: 0 };
