import { describe, expect, test } from "bun:test";
import { Home, Layout } from "../Layout";
import { makeExamSummary } from "./fixtures";
import { renderToString } from "./render";

describe("Layout", () => {
  test("page title and Tailwind typography plugin are wired", async () => {
    const html = await renderToString(
      <Layout title="Q1">body</Layout>
    );
    expect(html).toContain("<title>Q1 — Exam Studio</title>");
    expect(html).toContain("cdn.tailwindcss.com?plugins=typography");
  });

  test("requestCount badge shows when > 0 and is hidden at 0", async () => {
    const withBadge = await renderToString(
      <Layout title="x" requestCount={5}>
        body
      </Layout>
    );
    expect(withBadge).toMatch(/bg-amber-500[^>]*>5</);

    const noBadge = await renderToString(
      <Layout title="x" requestCount={0}>
        body
      </Layout>
    );
    expect(noBadge).not.toMatch(/bg-amber-500[^>]*>0</);
  });
});

describe("Home", () => {
  test("renders one card per exam with progress and awaiting badge", async () => {
    const html = await renderToString(
      <Home
        exams={[makeExamSummary({ slug: "soa-c03", awaiting_agent: 3 })]}
        requestCount={3}
      />
    );
    expect(html).toContain("soa-c03.db");
    expect(html).toContain("agent 返信待ち 3");
    expect(html).toContain('href="/e/soa-c03/q"');
  });

  test("empty list renders the placeholder text, not a <ul>", async () => {
    const html = await renderToString(<Home exams={[]} requestCount={0} />);
    expect(html).toContain("試験 DB が見つかりません");
    expect(html).not.toContain("<ul");
  });
});
