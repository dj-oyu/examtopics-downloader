import { describe, expect, test } from "bun:test";
import { QuestionView } from "../QuestionView";
import { makeQuestion, makeQuestionDetail, makeThreadWithMessages, makeVerdict } from "./fixtures";
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

  describe("answer verdicts", () => {
    const ambiguous = makeVerdict({
      status: "ambiguous",
      accepted: ["A", "D"],
      community: [
        { label: "D", votes: 13, pct: 65 },
        { label: "A", votes: 6, pct: 30 },
        { label: "B", votes: 1, pct: 5 },
      ],
      total_votes: 20,
      rationale:
        "コミュニティ多数派 D (13/20 = 65%) が正解キー A と食い違う。A と D のいずれも正答として扱う。",
    });

    const render = (extra: Record<string, unknown>) =>
      renderToString(
        <QuestionView
          slug="aif-c01"
          {...makeQuestionDetail({ q: makeQuestion({ suggested_answer: "A" }), verdict: extra.verdict as never })}
          thread={null}
          retranslatePending={false}
          requestCount={0}
          {...(extra.props as object)}
        />
      );

    test("a contested question is marked before answering, without leaking the split", async () => {
      const html = await render({ verdict: ambiguous, props: {} });
      expect(html).toContain("あいまいな問題");
      expect(html).not.toContain("65%");
      expect(html).not.toContain("✓ 正解");
    });

    test("either side of the argument is graded correct, and the split shows afterwards", async () => {
      const html = await render({
        verdict: ambiguous,
        props: { result: { correct: true, selected: "D" } },
      });
      expect(html).toContain("✓ 正解");
      expect(html).toContain("コミュニティ投票（20票）");
      expect(html).toContain("65%");
      expect(html).toContain("30%");
      expect(html).toContain("どちらも正解として扱います");
    });

    test("a wrong answer names every accepted answer", async () => {
      const html = await render({
        verdict: ambiguous,
        props: { result: { correct: false, selected: "B" } },
      });
      expect(html).toContain("✗ 不正解");
      expect(html).toContain("正答: A または D");
    });

    test("an unknown verdict is marked and never graded", async () => {
      const verdict = makeVerdict({
        status: "unknown",
        accepted: [],
        community: [],
        total_votes: 0,
        rationale: "コミュニティ投票が無く、正解キーも空のため判定できない",
      });
      const before = await render({ verdict, props: {} });
      expect(before).toContain("正解が特定できない問題");

      const after = await render({
        verdict,
        props: { result: { correct: false, ungraded: true, selected: "A" } },
      });
      expect(after).toContain("判定不能");
      expect(after).not.toContain("✗ 不正解");
      expect(after).toContain("コミュニティ投票は記録されていません");
    });

    test("a settled question shows the split only after answering", async () => {
      const before = await render({ verdict: makeVerdict(), props: {} });
      expect(before).not.toContain("80%");
      expect(before).not.toContain("あいまいな問題");

      const after = await render({
        verdict: makeVerdict(),
        props: { result: { correct: true, selected: "C" } },
      });
      expect(after).toContain("80%");
      expect(after).toContain("20%");
    });
  });
});
