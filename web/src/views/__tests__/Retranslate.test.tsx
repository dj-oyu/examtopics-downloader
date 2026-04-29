import { describe, expect, test } from "bun:test";
import { QuestionLiveScript, RetranslatePanel } from "../Retranslate";
import { renderToString } from "./render";

describe("RetranslatePanel", () => {
  test("submit button targets the retranslate route", async () => {
    const html = await renderToString(
      <RetranslatePanel slug="soa-c03" qid={42} pending={false} />
    );
    expect(html).toContain('action="/e/soa-c03/q/42/retranslate"');
    expect(html).toContain("翻訳をやり直す");
  });

  test("pending state opens the details, disables the button, shows pulse dot", async () => {
    const html = await renderToString(
      <RetranslatePanel slug="soa-c03" qid={42} pending={true} />
    );
    expect(html).toMatch(/<details[^>]*open/);
    expect(html).toContain("disabled");
    expect(html).toContain("animate-pulse");
    expect(html).toContain("翻訳実行中");
  });

  test("idle state hides the indicator span", async () => {
    const html = await renderToString(
      <RetranslatePanel slug="soa-c03" qid={42} pending={false} />
    );
    expect(html).toMatch(/id="retrans-indicator-42"[^>]*style="display:none"/);
  });
});

describe("QuestionLiveScript", () => {
  test("subscribes to the question SSE endpoint and reloads on update", async () => {
    const html = await renderToString(
      <QuestionLiveScript slug="soa-c03" qid={42} />
    );
    expect(html).toContain('"/e/" + slug + "/q/" + qid + "/events"');
    expect(html).toContain('"translation-updated"');
    expect(html).toContain("location.reload()");
  });
});
