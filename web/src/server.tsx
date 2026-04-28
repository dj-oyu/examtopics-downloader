import { Hono } from "hono";
import * as q from "./db";
import { Home, QuestionList, QuestionView, Layout, Threads } from "./views";

const app = new Hono();

const reqCount = () => q.countAwaitingAgentAll();

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
      requestCount={reqCount()}
    />
  );
});

app.post("/e/:slug/q/:id/threads", async (c) => {
  const slug = c.req.param("slug");
  const id = parseInt(c.req.param("id"), 10);
  const form = await c.req.parseBody();
  const content = ((form.content as string) || "").trim();
  if (!content) return c.redirect(`/e/${slug}/q/${id}`);
  if (q.getOpenThread(slug, id)) return c.redirect(`/e/${slug}/q/${id}`);
  q.createThread(slug, id, content, "web");
  return c.redirect(`/e/${slug}/q/${id}`);
});

app.post("/e/:slug/threads/:id/reply", async (c) => {
  const slug = c.req.param("slug");
  const tid = parseInt(c.req.param("id"), 10);
  const form = await c.req.parseBody();
  const content = ((form.content as string) || "").trim();
  if (content) q.appendMessage(slug, tid, "user", content, "web");
  const t = q.getThread(slug, tid);
  return c.redirect(t ? `/e/${slug}/q/${t.question_id}` : "/requests");
});

app.post("/e/:slug/threads/:id/resolve", (c) => {
  const slug = c.req.param("slug");
  const tid = parseInt(c.req.param("id"), 10);
  const t = q.getThread(slug, tid);
  q.closeThread(slug, tid, "resolved");
  return c.redirect(t ? `/e/${slug}/q/${t.question_id}` : "/requests");
});

app.post("/e/:slug/threads/:id/dismiss", (c) => {
  const slug = c.req.param("slug");
  const tid = parseInt(c.req.param("id"), 10);
  const t = q.getThread(slug, tid);
  q.closeThread(slug, tid, "dismissed");
  return c.redirect(t ? `/e/${slug}/q/${t.question_id}` : "/requests");
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
console.log(`listening on http://localhost:${port}`);
export default { fetch: app.fetch, port };
