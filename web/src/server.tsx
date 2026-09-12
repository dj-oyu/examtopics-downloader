import { Hono } from "hono";
import type { Context, MiddlewareHandler } from "hono";
import { marked } from "marked";
import { deleteCookie, getCookie, setCookie } from "hono/cookie";
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
import {
  ADMIN_COOKIE_NAME,
  describeCurrentJob,
  getAdminToken,
  isJobInFlight,
  isValidSlug,
  resolveDownloaderBin,
  startFetch,
  subscribeFetch,
  tokenMatches,
  tryLoadProviders,
} from "./admin";
import type { FetchEvent } from "./admin";
import { loadConfig } from "./config";
import {
  AdminFetch,
  AdminLogin,
  Home,
  Layout,
  QuestionList,
  QuestionView,
  Threads,
} from "./views";

marked.setOptions({ gfm: true, breaks: false });

const app = new Hono();

const reqCount = () => q.countAwaitingAgentAll();

app.use("*", async (c, next) => {
  if (c.req.method === "POST") {
    // CSRF guard skip for Bearer-authenticated requests: the threat
    // model targets ambient credentials (cookies). curl-driven admin
    // automation that presents `Authorization: Bearer ...` does not
    // ride a cookie, so same-origin is irrelevant. Cookie-only POSTs
    // still need a matching origin/referer.
    const auth = c.req.header("authorization") ?? c.req.header("Authorization");
    if (auth && /^Bearer\s+\S+/i.test(auth)) {
      await next();
      return;
    }
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

// ----------------------------------------------------------------------
// /admin/* — exam scrape UI. Plan §3.4 (Web → Go fetch UI) + §3.5
// (admin token contract).
// ----------------------------------------------------------------------

const ADMIN_DISABLED_BODY =
  "admin disabled — set EXAMTOPICS_ADMIN_TOKEN to enable";

function bearerToken(c: Context): string | null {
  const auth = c.req.header("authorization") ?? c.req.header("Authorization");
  if (!auth) return null;
  const m = auth.match(/^Bearer\s+(.+)$/i);
  if (!m) return null;
  return m[1].trim();
}

function isCookieAuthed(c: Context): boolean {
  const cookie = getCookie(c, ADMIN_COOKIE_NAME);
  return tokenMatches(cookie ?? null);
}

function isBearerAuthed(c: Context): boolean {
  return tokenMatches(bearerToken(c));
}

const requireAdmin: MiddlewareHandler = async (c, next) => {
  if (!getAdminToken()) {
    return c.text(ADMIN_DISABLED_BODY, 503);
  }
  // /admin/login is the bootstrap path: the unauth user submits the
  // token here to acquire the cookie. We cannot require auth on it.
  const path = new URL(c.req.url).pathname;
  if (path === "/admin/login") {
    await next();
    return;
  }
  if (isBearerAuthed(c) || isCookieAuthed(c)) {
    await next();
    return;
  }
  // GET → render the login form, POST → 401.
  if (c.req.method === "GET") {
    return c.html(<AdminLogin notice={null} requestCount={reqCount()} />, 401);
  }
  return c.text("unauthorized", 401);
};

app.use("/admin/*", requireAdmin);

app.get("/admin/login", (c) => {
  if (!getAdminToken()) return c.text(ADMIN_DISABLED_BODY, 503);
  if (isCookieAuthed(c) || isBearerAuthed(c)) {
    return c.redirect("/admin/fetch");
  }
  return c.html(<AdminLogin notice={null} requestCount={reqCount()} />);
});

app.post("/admin/login", async (c) => {
  if (!getAdminToken()) return c.text(ADMIN_DISABLED_BODY, 503);
  const form = await c.req.parseBody();
  const token = (form.token as string) ?? "";
  if (!tokenMatches(token)) {
    return c.html(
      <AdminLogin
        notice={{ kind: "error", text: "トークンが一致しません。" }}
        requestCount={reqCount()}
      />,
      401
    );
  }
  setCookie(c, ADMIN_COOKIE_NAME, token, {
    httpOnly: true,
    sameSite: "Strict",
    path: "/",
    // Secure stays off so dev (http://localhost) works. Operators behind
    // TLS can flip this via a reverse proxy that strips/re-emits cookies.
    secure: false,
  });
  return c.redirect("/admin/fetch");
});

app.post("/admin/logout", (c) => {
  if (!getAdminToken()) return c.text(ADMIN_DISABLED_BODY, 503);
  deleteCookie(c, ADMIN_COOKIE_NAME, { path: "/" });
  return c.redirect("/admin/login");
});

function renderAdminFetch(
  c: Context,
  notice: { kind: "info" | "error"; text: string } | null,
  status: 200 | 400 | 409 = 200
) {
  const cfg = loadConfig();
  let binPath: string | null = null;
  let binResolveError: string | null = null;
  try {
    binPath = resolveDownloaderBin();
  } catch (e) {
    binResolveError = e instanceof Error ? e.message : String(e);
  }

  const providersSet = tryLoadProviders();
  let providerLoadError: string | null = null;
  let providers: string[] = [];
  if (!providersSet) {
    providerLoadError =
      "examtopicsdl providers の実行に失敗しました。POST 時に詳細を返します。";
  } else {
    providers = [...providersSet].sort();
  }

  return c.html(
    <AdminFetch
      providers={providers}
      defaultProvider={cfg.scrape.defaultProvider}
      providerLoadError={providerLoadError}
      binPath={binPath}
      binResolveError={binResolveError}
      job={describeCurrentJob()}
      notice={notice}
      loginEnabled={true}
      cookieAuthed={isCookieAuthed(c)}
      requestCount={reqCount()}
    />,
    status
  );
}

app.get("/admin/fetch", (c) => renderAdminFetch(c, null));

app.post("/admin/fetch", async (c) => {
  const form = await c.req.parseBody();
  const provider = ((form.provider as string) ?? "").trim();
  const slug = ((form.slug as string) ?? "").trim();

  if (!provider || !slug) {
    return renderAdminFetch(
      c,
      { kind: "error", text: "provider と slug は必須です。" },
      400
    );
  }
  if (!isValidSlug(slug)) {
    return renderAdminFetch(
      c,
      {
        kind: "error",
        text: `slug が不正です: ${slug} (許容文字: A-Z a-z 0-9 . _ - / "." と ".." は不可)`,
      },
      400
    );
  }
  const providersSet = tryLoadProviders();
  if (!providersSet) {
    return renderAdminFetch(
      c,
      {
        kind: "error",
        text: "examtopicsdl providers の実行に失敗。バイナリの場所を確認してください。",
      },
      400
    );
  }
  if (!providersSet.has(provider)) {
    return renderAdminFetch(
      c,
      { kind: "error", text: `provider が許容リスト外です: ${provider}` },
      400
    );
  }
  if (isJobInFlight()) {
    // Plan asks for 409 with body "another fetch job is in flight" for
    // programmatic clients. For form POSTs (HTML accept) we still want
    // a useful page, so render the page with a notice but use 409.
    if (wantsHtml(c)) {
      return renderAdminFetch(
        c,
        { kind: "error", text: "another fetch job is in flight" },
        409
      );
    }
    return c.text("another fetch job is in flight", 409);
  }

  const result = startFetch({ provider, slug });
  if (!result.ok) {
    if (wantsHtml(c)) {
      return renderAdminFetch(
        c,
        { kind: "error", text: result.message },
        result.status === 409 ? 409 : 400
      );
    }
    return c.text(result.message, result.status);
  }
  return c.redirect("/admin/fetch", 303);
});

function wantsHtml(c: Context): boolean {
  const accept = c.req.header("accept") ?? "";
  if (!accept) return true;
  if (accept.includes("text/html")) return true;
  if (accept.includes("*/*")) return true;
  return false;
}

app.get("/admin/fetch/log", (c) => {
  const stream = new ReadableStream({
    start(controller) {
      const enc = new TextEncoder();
      const send = (event: FetchEvent) => {
        try {
          if (event.type === "line") {
            controller.enqueue(
              enc.encode(`data: ${JSON.stringify(event)}\n\n`)
            );
          } else {
            // Use a named SSE event for completion so the client can
            // listen specifically for `done` (mirror of the thread
            // events stream's typed payload).
            controller.enqueue(
              enc.encode(
                `event: done\ndata: ${JSON.stringify(event.done)}\n\n`
              )
            );
          }
        } catch {}
      };
      const unsub = subscribeFetch(send);
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
  const outcome = q.recordAttempt(slug, id, selected, data.q.suggested_answer);
  const fresh = q.getQuestion(slug, id)!;
  const thread = q.getOpenThread(slug, id);
  return c.html(
    <QuestionView
      slug={slug}
      {...fresh}
      result={{
        correct: outcome.correct,
        ungraded: outcome.ungraded,
        selected,
      }}
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

// app is exported (named) so tests can drive routes via app.request /
// app.fetch without standing up a real Bun.serve. The default export
// keeps the existing Bun runtime entry shape.
export { app };

const port = parseInt(process.env.PORT ?? "3000", 10);
const hostname = process.env.HOST ?? "127.0.0.1";
const isMain = import.meta.main;
if (isMain) {
  console.log(`listening on http://${hostname}:${port}`);
  recoverAwaitingThreads();
}
// idleTimeout: 0 keeps SSE connections open; default 10s would cut them
// before our 25s keep-alive ping fires.
export default { fetch: app.fetch, port, hostname, idleTimeout: 0 };
