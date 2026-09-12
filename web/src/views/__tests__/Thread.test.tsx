import { describe, expect, test } from "bun:test";
import { ThreadPanel } from "../Thread";
import {
  fixtureId,
  makeMessage,
  makeQuestion,
  makeThreadWithMessages,
} from "./fixtures";
import { renderToString } from "./render";

// Stable 26-char base32 ids so HTML attribute / regex assertions can
// pin exact strings instead of random UUIDv7 output. fixtureId(n)
// pads small integers into the canonical id shape, keeping these
// tests close to their pre-4-E form while exercising the new types.
const T9 = fixtureId(9);
const T7 = fixtureId(7);
const M1 = fixtureId(1);
const M2 = fixtureId(2);
const M100 = fixtureId(100);
const M101 = fixtureId(101);

describe("ThreadPanel — no thread", () => {
  test("renders the create-thread form pointing at /q/:id/threads", async () => {
    const html = await renderToString(
      <ThreadPanel slug="soa-c03" q={makeQuestion()} thread={null} />
    );
    expect(html).toContain("解説スレッドを作成する");
    expect(html).toContain('action="/e/soa-c03/q/1/threads"');
    expect(html).not.toContain("messages-");
  });
});

describe("ThreadPanel — with messages", () => {
  test("renders user as plain text and agent as markdown HTML", async () => {
    const thread = makeThreadWithMessages({}, [
      makeMessage({
        id: M100,
        role: "user",
        content: "なぜ C が正解？",
      }),
      makeMessage({
        id: M101,
        role: "agent",
        author: "claude-code",
        content: "**Aurora Backtracking** は同一クラスター内で巻き戻せます。",
      }),
    ]);
    const html = await renderToString(
      <ThreadPanel
        slug="soa-c03"
        q={makeQuestion()}
        thread={thread}
      />
    );

    // user bubble: plain text, no <strong>
    expect(html).toContain("なぜ C が正解？");
    // agent bubble: markdown rendered to HTML inside the prose container
    expect(html).toContain("<strong>Aurora Backtracking</strong>");
    expect(html).toContain('class="prose prose-sm max-w-none leading-relaxed"');
    // each message bubble carries data-msg-id for client-side dedup
    expect(html).toContain(`data-msg-id="${M100}"`);
    expect(html).toContain(`data-msg-id="${M101}"`);
  });

  test("awaiting badge derives from last message role", async () => {
    const userLast = makeThreadWithMessages({}, [
      makeMessage({ id: M1, role: "user" }),
    ]);
    const userHtml = await renderToString(
      <ThreadPanel slug="x" q={makeQuestion()} thread={userLast} />
    );
    expect(userHtml).toContain("エージェント返信待ち");

    const agentLast = makeThreadWithMessages({}, [
      makeMessage({ id: M1, role: "user" }),
      makeMessage({ id: M2, role: "agent", content: "Reply." }),
    ]);
    const agentHtml = await renderToString(
      <ThreadPanel slug="x" q={makeQuestion()} thread={agentLast} />
    );
    expect(agentHtml).toContain("ユーザー返信待ち");
  });

  test("explainPending=true renders the inline header dot AND a thinking bubble", async () => {
    const thread = makeThreadWithMessages({ id: T9 });
    const html = await renderToString(
      <ThreadPanel
        slug="x"
        q={makeQuestion()}
        thread={thread}
        explainPending={true}
      />
    );
    // header inline status
    expect(html).toMatch(
      new RegExp(
        `id="agent-status-${T9}"[^>]*class="ml-2 text-xs inline-flex items-center gap-1 text-blue-600"`
      )
    );
    expect(html).toContain("🤖 エージェント応答中…");
    // chat-style thinking bubble after the messages list
    expect(html).toMatch(
      new RegExp(
        `id="agent-thinking-${T9}"[^>]*class="mt-2 pl-3 py-2 border-l-4 border-blue-400 bg-blue-50"[^>]*style=""`
      )
    );
    expect(html).toContain("animate-bounce");
    expect(html).toContain("応答中…");
  });

  test("explainPending=true grays out and disables the reply form", async () => {
    const thread = makeThreadWithMessages({ id: T9 });
    const html = await renderToString(
      <ThreadPanel
        slug="x"
        q={makeQuestion()}
        thread={thread}
        explainPending={true}
      />
    );
    expect(html).toMatch(
      /<textarea[^>]*name="content"[^>]*disabled=""[^>]*class="[^"]*bg-gray-100/
    );
    expect(html).toMatch(
      /<button[^>]*type="submit"[^>]*disabled=""[^>]*class="[^"]*bg-gray-300/
    );
    expect(html).toMatch(
      /<textarea[^>]*name="content"[^>]*placeholder="エージェント応答中…"/
    );
  });

  test("explainPending=false leaves the reply form active", async () => {
    const thread = makeThreadWithMessages({ id: T9 });
    const html = await renderToString(
      <ThreadPanel slug="x" q={makeQuestion()} thread={thread} />
    );
    expect(html).not.toMatch(/<textarea[^>]*name="content"[^>]*disabled/);
    expect(html).not.toMatch(/<button[^>]*type="submit"[^>]*disabled/);
    expect(html).toMatch(
      /<button[^>]*type="submit"[^>]*class="[^"]*bg-amber-500/
    );
    expect(html).toMatch(
      /<textarea[^>]*name="content"[^>]*placeholder="返信を入力"/
    );
  });

  test("explainPending=false hides both the header status and the thinking bubble", async () => {
    const thread = makeThreadWithMessages({ id: T9 });
    const html = await renderToString(
      <ThreadPanel slug="x" q={makeQuestion()} thread={thread} />
    );
    expect(html).toMatch(
      new RegExp(`id="agent-status-${T9}"[^>]*style="display:none"`)
    );
    expect(html).toMatch(
      new RegExp(`id="agent-thinking-${T9}"[^>]*style="display:none"`)
    );
    expect(html).not.toContain("🤖 エージェント応答中…");
  });

  test("renders ThreadLiveScript wired to the thread events endpoint", async () => {
    const thread = makeThreadWithMessages({ id: T7 });
    const html = await renderToString(
      <ThreadPanel slug="soa-c03" q={makeQuestion()} thread={thread} />
    );
    expect(html).toContain("/threads/${tid}/events");
    expect(html).toContain(
      `initThreadLive({"slug":"soa-c03","tid":"${T7}"})`
    );
    expect(html).toContain("agent-message");
  });

  test("agent message with reason_code='spec' shows the 仕様 badge", async () => {
    const thread = makeThreadWithMessages({}, [
      makeMessage({
        id: M1,
        role: "agent",
        author: "claude-code",
        content: "ALB の listener rule は上から評価される。",
        reason_code: "spec",
      }),
    ]);
    const html = await renderToString(
      <ThreadPanel slug="x" q={makeQuestion()} thread={thread} />
    );
    expect(html).toContain("仕様");
    expect(html).toContain("bg-blue-100 text-blue-800");
  });

  test("citations are rendered as a links list with titles + hosts", async () => {
    const thread = makeThreadWithMessages({}, [
      makeMessage({
        id: M1,
        role: "agent",
        author: "claude-code",
        content: "詳細はドキュメント参照。",
        reason_code: "spec",
        citations: JSON.stringify([
          {
            url: "https://docs.aws.amazon.com/elasticloadbalancing/listener-rules.html",
            title: "Update rules for ALB",
          },
          { url: "https://docs.aws.amazon.com/general/limits.html" },
        ]),
      }),
    ]);
    const html = await renderToString(
      <ThreadPanel slug="x" q={makeQuestion()} thread={thread} />
    );
    expect(html).toContain("📚 出典 (2)");
    expect(html).toContain(
      'href="https://docs.aws.amazon.com/elasticloadbalancing/listener-rules.html"'
    );
    expect(html).toContain("Update rules for ALB");
    // Item without a title falls back to the host
    expect(html).toContain("docs.aws.amazon.com");
    // External-link safety attrs
    expect(html).toContain('target="_blank"');
    expect(html).toContain('rel="noopener"');
  });

  test("malformed citations JSON is silently dropped, not rendered", async () => {
    const thread = makeThreadWithMessages({}, [
      makeMessage({
        id: M1,
        role: "agent",
        content: "x",
        reason_code: null,
        citations: "{not valid json",
      }),
    ]);
    const html = await renderToString(
      <ThreadPanel slug="x" q={makeQuestion()} thread={thread} />
    );
    // The <details> wrapper for the SSR-rendered citation list never appears
    // (the inline ThreadLiveScript source contains the literal string
    // "📚 出典 (", so we can't just `not.toContain("出典")`).
    expect(html).not.toMatch(/📚 出典 \(\d+\)/);
    expect(html).not.toContain('<ul class="mt-1 space-y-1 pl-4">');
  });
});
