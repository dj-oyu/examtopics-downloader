import { describe, expect, test } from "bun:test";
import { QuestionView } from "../QuestionView";
import {
  makeQuestion,
  makeQuestionDetail,
  makeThreadWithMessages,
} from "./fixtures";
import { renderToString } from "./render";

describe("QuestionView", () => {
  test("renders multi-select question with [複数選択] badge", async () => {
    const detail = makeQuestionDetail({
      q: makeQuestion({
        suggested_answer: "BD",
        question_text_ja: "(複数選択) 該当するものを選べ",
      }),
    });
    const html = await renderToString(
      <QuestionView
        slug="sap-c02"
        {...detail}
        thread={null}
        retranslatePending={false}
        requestCount={0}
      />
    );
    expect(html).toContain("[複数選択]");
    expect(html).toContain('type="checkbox"');
    expect(html).not.toContain('type="radio"');
  });

  test("single-select uses radio inputs and submits to /attempt", async () => {
    const detail = makeQuestionDetail();
    const html = await renderToString(
      <QuestionView
        slug="soa-c03"
        {...detail}
        thread={null}
        retranslatePending={false}
        requestCount={0}
      />
    );
    expect(html).toContain('type="radio"');
    expect(html).toContain('action="/e/soa-c03/q/1/attempt"');
  });

  test("retranslatePending propagates to the indicator", async () => {
    const detail = makeQuestionDetail();
    const html = await renderToString(
      <QuestionView
        slug="soa-c03"
        {...detail}
        thread={null}
        retranslatePending={true}
        requestCount={0}
      />
    );
    expect(html).toContain("翻訳実行中");
    expect(html).toContain('id="retrans-indicator-1"');
  });

  test("Result panel only appears after an attempt", async () => {
    const detail = makeQuestionDetail();

    const before = await renderToString(
      <QuestionView
        slug="x"
        {...detail}
        thread={null}
        retranslatePending={false}
        requestCount={0}
      />
    );
    expect(before).not.toContain("✓ 正解");
    expect(before).not.toContain("✗ 不正解");

    const after = await renderToString(
      <QuestionView
        slug="x"
        {...detail}
        result={{ correct: true, selected: "C" }}
        thread={makeThreadWithMessages()}
        retranslatePending={false}
        requestCount={0}
      />
    );
    expect(after).toContain("✓ 正解");
  });
});
