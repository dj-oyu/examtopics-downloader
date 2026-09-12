import { describe, expect, test } from "bun:test";

import { AdminSync } from "../AdminSync";
import type { AdminSyncProps } from "../AdminSync";
import { renderToString } from "./render";

const CONFLICT_URL =
  "https://www.examtopics.com/discussions/amazon/view/150663-exam-aws-certified-ai-practitioner-aif-c01-topic-1-question/";

const baseProps: AdminSyncProps = {
  exams: [
    { slug: "aif-c01", name: "AWS Certified AI Practitioner AIF-C01", total: 154, translated: 154 },
    { slug: "soa-c03", name: "AWS Certified CloudOps Engineer SOA-C03", total: 100, translated: 0 },
  ],
  defaultSlug: "aif-c01",
  peers: [
    {
      path: "/data/incoming/other-machine.db",
      name: "other-machine.db",
      dir: "/data/incoming",
      size: 2048,
      mtimeMs: Date.UTC(2026, 8, 12, 3, 0, 0),
      isExamDb: false,
    },
    {
      path: "/data/aif-c01.db",
      name: "aif-c01.db",
      dir: "/data",
      size: 1228800,
      mtimeMs: Date.UTC(2026, 8, 12, 4, 0, 0),
      isExamDb: true,
    },
  ],
  snapshots: [
    { name: "aif-c01-2026-09-12T04-00-00.db", size: 1228800, mtimeMs: Date.UTC(2026, 8, 12, 4, 0, 0) },
  ],
  binPath: "/usr/local/bin/examtopicsdl",
  binResolveError: null,
  job: null,
  loginEnabled: true,
  cookieAuthed: true,
  requestCount: 0,
};

const render = (overrides: Partial<AdminSyncProps> = {}) =>
  renderToString(<AdminSync {...baseProps} {...overrides} />);

describe("AdminSync", () => {
  test("renders both directions and the peer candidates", async () => {
    const html = await render();
    expect(html).toContain("① 和訳を取り込む");
    expect(html).toContain("② スナップショットを書き出す");
    expect(html).toContain('action="/admin/sync/translations"');
    expect(html).toContain('action="/admin/sync/snapshot"');
    expect(html).toContain("aif-c01 — AWS Certified AI Practitioner AIF-C01 (154/154 和訳済み)");
    expect(html).toContain("other-machine.db");
    // The exam DB itself is offered but flagged.
    expect(html).toContain("⚠ 試験DB");
    expect(html).toContain("bin: /usr/local/bin/examtopicsdl");
  });

  test("links the outgoing snapshots for download", async () => {
    const html = await render();
    expect(html).toContain(
      'href="/admin/sync/download?name=aif-c01-2026-09-12T04-00-00.db"'
    );
  });

  test("falls back to a free-text peer input when nothing was found", async () => {
    const html = await render({ peers: [] });
    expect(html).toContain('placeholder="/data/incoming/other-machine.db"');
    expect(html).toContain("相手マシンのスナップショット");
  });

  test("prompts to fetch when there is no exam DB", async () => {
    const html = await render({ exams: [], defaultSlug: "" });
    expect(html).toContain("試験 DB が見つかりません");
  });

  test("surfaces a binary resolution failure", async () => {
    const html = await render({ binPath: null, binResolveError: "examtopicsdl binary not found" });
    expect(html).toContain("バイナリ解決エラー");
    expect(html).toContain("examtopicsdl binary not found");
  });

  test("a running job disables both submits and starts the SSE client", async () => {
    const html = await render({
      job: {
        kind: "translations",
        slug: "aif-c01",
        peerPath: "/data/incoming/other-machine.db",
        startedAt: Date.UTC(2026, 8, 12, 4, 0, 0),
        running: true,
        exitCode: null,
        result: null,
        snapshotPath: null,
      },
    });
    expect(html).toContain('data-running="true"');
    expect(html).toContain("実行中…");
    expect(html).toContain("disabled");
  });

  test("a finished merge shows the counts and links each conflict to its question", async () => {
    const html = await render({
      job: {
        kind: "translations",
        slug: "aif-c01",
        peerPath: "/data/incoming/other-machine.db",
        startedAt: Date.UTC(2026, 8, 12, 4, 0, 0),
        running: false,
        exitCode: 0,
        snapshotPath: null,
        result: {
          filledQuestions: 306,
          filledChoices: 602,
          conflictCount: 2,
          overwritten: 0,
          preferPeer: false,
          dryRun: false,
          conflicts: [
            { field: "question_text_ja", url: CONFLICT_URL, qid: 145 },
            { field: "explanation_ja", url: "https://example.test/unknown", qid: null },
          ],
        },
      },
    });
    expect(html).toContain("埋めた設問フィールド: <b>306</b>");
    expect(html).toContain("選択肢: <b>602</b>");
    expect(html).toContain("衝突: <b>2</b>");
    expect(html).toContain("このマシンの訳を保持");
    expect(html).toContain('href="/e/aif-c01/q/145"');
    // An unknown url is shown as text rather than a dead link.
    expect(html).toContain("https://example.test/unknown");
    expect(html).toContain("相手の訳を採用したい場合は");
  });

  test("a dry run is labelled as not written", async () => {
    const html = await render({
      job: {
        kind: "translations",
        slug: "aif-c01",
        peerPath: "/data/incoming/other-machine.db",
        startedAt: Date.UTC(2026, 8, 12, 4, 0, 0),
        running: false,
        exitCode: 0,
        snapshotPath: null,
        result: {
          filledQuestions: 1,
          filledChoices: 0,
          conflictCount: 0,
          overwritten: 0,
          preferPeer: false,
          dryRun: true,
          conflicts: [],
        },
      },
    });
    expect(html).toContain("プレビュー結果（未書き込み）");
    expect(html).toContain("埋めた設問フィールド: <b>1</b>");
  });

  test("an overwriting run says so", async () => {
    const html = await render({
      job: {
        kind: "translations",
        slug: "aif-c01",
        peerPath: "/data/incoming/other-machine.db",
        startedAt: Date.UTC(2026, 8, 12, 4, 0, 0),
        running: false,
        exitCode: 0,
        snapshotPath: null,
        result: {
          filledQuestions: 0,
          filledChoices: 0,
          conflictCount: 2,
          overwritten: 2,
          preferPeer: true,
          dryRun: false,
          conflicts: [{ field: "question_text_ja", url: CONFLICT_URL, qid: 145 }],
        },
      },
    });
    expect(html).toContain("相手の訳で置換: 2");
    expect(html).not.toContain("相手の訳を採用したい場合は");
  });

  test("a snapshot job reports the written file", async () => {
    const html = await render({
      job: {
        kind: "snapshot",
        slug: "aif-c01",
        peerPath: null,
        startedAt: Date.UTC(2026, 8, 12, 4, 0, 0),
        running: false,
        exitCode: 0,
        snapshotPath: "/data/outgoing/aif-c01-2026-09-12T04-00-00.db",
        result: null,
      },
    });
    expect(html).toContain("出力: /data/outgoing/aif-c01-2026-09-12T04-00-00.db");
  });
});
