// Regression tests for openDb()'s migration gate.
//
// The Go CLI embeds its migration set from 003 onward, so a DB it creates
// records version 3 and *nothing* for the repo-root 001/002 scripts. Bun's
// runner used to replay those two anyway: 001 rebuilds explanation_messages
// into its old INTEGER-PK shape and 002 then aborts on the already-present
// agent_session_id column. openDb() threw, discoverExams() swallowed it, and
// the study UI reported "試験 DB が見つかりません" for perfectly good files.
import { resetConfigCache } from "./config";
import { Database } from "bun:sqlite";
import { afterAll, describe, expect, test } from "bun:test";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import sql001 from "../../migrations/001_explanation_messages_grounding.sql" with { type: "text" };
import sql002 from "../../migrations/002_thread_agent_session.sql" with { type: "text" };
import sql003 from "../../internal/sqlite/migrations/003_multihost_sync.sql" with { type: "text" };
import sql004 from "../../internal/sqlite/migrations/004_answer_verdicts.sql" with { type: "text" };

// dataDir is resolved through loadConfig(), which caches; reset it after
// pointing the environment at the tmpdir so the fixtures are picked up even
// when another test file loaded the config module first.
const dir = mkdtempSync(join(tmpdir(), "examstudio-db-test-"));
const savedEnv = {
  data: process.env.EXAMTOPICS_DATA_DIR,
  log: process.env.EXAMTOPICS_LOG_DIR,
};
process.env.EXAMTOPICS_DATA_DIR = dir;
process.env.EXAMTOPICS_LOG_DIR = join(dir, "logs");
resetConfigCache();

const { discoverExams, getQuestion, listWrong, openDb, recordAttempt } = await import("./db");

const SCHEMA_VERSION_DDL = `CREATE TABLE IF NOT EXISTS schema_version (
     version INTEGER PRIMARY KEY,
     name TEXT NOT NULL,
     applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
   )`;

const QUESTIONS_DDL = `CREATE TABLE IF NOT EXISTS questions (
     id INTEGER PRIMARY KEY, exam TEXT, topic INTEGER, question_number INTEGER,
     question_text TEXT, question_text_ja TEXT, suggested_answer TEXT,
     confirmed_answer TEXT, explanation_ja TEXT, url TEXT, comments TEXT)`;

const CHOICES_DDL = `CREATE TABLE IF NOT EXISTS choices (
     question_id INTEGER, label TEXT, text TEXT, text_ja TEXT)`;

/** Build a DB shaped the way the Go writer leaves it: 003 applied, version 3 recorded. */
function seedGoWrittenDb(slug: string): string {
  const path = join(dir, `${slug}.db`);
  const db = new Database(path);
  db.exec(SCHEMA_VERSION_DDL);
  db.exec(sql003);
  // 004 lands through openDb()'s runner; the fixtures that insert verdict rows
  // directly need the table up front, so create it here from the same file.
  db.exec(sql004);
  db.exec(QUESTIONS_DDL);
  db.exec(CHOICES_DDL);
  db.run("INSERT INTO schema_version(version, name) VALUES (3, '003_multihost_sync')");
  db.close();
  return path;
}

/** A seeded DB with one question (id 1) plus a verdict row for it. */
function seedGradedDb(
  slug: string,
  opts: {
    status: "settled" | "ambiguous" | "unknown";
    accepted: string[];
    suggested: string;
    community?: { label: string; votes: number; pct: number }[];
    totalVotes?: number;
    id?: number;
  }
): string {
  const path = seedGoWrittenDb(slug);
  const db = new Database(path);
  const id = opts.id ?? 1;
  db.run(
    "INSERT INTO questions (id, exam, topic, question_number, question_text, question_text_ja, suggested_answer) VALUES (?, 'Exam', 1, ?, 'Q?', '問?', ?)",
    [id, id, opts.suggested]
  );
  db.run("INSERT INTO choices (question_id, label, text, text_ja) VALUES (?, 'A', 'a', 'あ')", [id]);
  db.run("INSERT INTO choices (question_id, label, text, text_ja) VALUES (?, 'B', 'b', 'い')", [id]);
  db.run(
    "INSERT INTO answer_verdicts (question_id, status, accepted, community, total_votes, source, rationale) VALUES (?,?,?,?,?, 'discussion', ?)",
    [
      id,
      opts.status,
      JSON.stringify(opts.accepted),
      JSON.stringify(opts.community ?? []),
      opts.totalVotes ?? 0,
      "test",
    ]
  );
  db.close();
  return path;
}

// The pre-001 shape the app's own SCHEMA provides; 001 rebuilds
// explanation_messages from it, so a legacy fixture has to bring it along.
const LEGACY_BASE_SCHEMA = `
  CREATE TABLE IF NOT EXISTS explanation_threads (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    question_id INTEGER NOT NULL,
    status TEXT NOT NULL DEFAULT 'open',
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    closed_at TEXT
  );
  CREATE TABLE IF NOT EXISTS explanation_messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    thread_id INTEGER NOT NULL REFERENCES explanation_threads(id) ON DELETE CASCADE,
    role TEXT NOT NULL,
    author TEXT,
    content TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
  );
`;

/** Build a pre-003 DB: 001 and 002 applied and recorded, 003 still pending. */
function seedLegacyDb(slug: string): string {
  const path = join(dir, `${slug}.db`);
  const db = new Database(path);
  db.exec(SCHEMA_VERSION_DDL);
  db.exec(LEGACY_BASE_SCHEMA);
  db.exec(sql001);
  db.exec(sql002);
  db.exec(
    "CREATE TABLE IF NOT EXISTS questions (id INTEGER PRIMARY KEY, exam TEXT, question_number INTEGER, question_text TEXT, question_text_ja TEXT)"
  );
  db.run("INSERT INTO schema_version(version, name) VALUES (1, '001_explanation_messages_grounding')");
  db.run("INSERT INTO schema_version(version, name) VALUES (2, '002_thread_agent_session')");
  db.close();
  return path;
}

afterAll(() => {
  rmSync(dir, { recursive: true, force: true });
  if (savedEnv.data === undefined) delete process.env.EXAMTOPICS_DATA_DIR;
  else process.env.EXAMTOPICS_DATA_DIR = savedEnv.data;
  if (savedEnv.log === undefined) delete process.env.EXAMTOPICS_LOG_DIR;
  else process.env.EXAMTOPICS_LOG_DIR = savedEnv.log;
  resetConfigCache();
});

describe("openDb migration gate", () => {
  test("opens a Go-written v3 DB without replaying the superseded 001/002", () => {
    seedGoWrittenDb("go-written-v3");
    let db: ReturnType<typeof openDb> | null = null;
    expect(() => {
      db = openDb("go-written-v3");
    }).not.toThrow();
    const versions = db!
      .query<{ version: number }, []>("SELECT version FROM schema_version ORDER BY version")
      .all()
      .map((r) => r.version);
    // 1 and 2 stay skipped; the Go-owned 004 is applied on top.
    expect(versions).toEqual([3, 4]);
    // The post-003 shape survives: replaying 001 would have dropped this column.
    const cols = db!
      .query<{ name: string }, []>("PRAGMA table_info(explanation_threads)")
      .all()
      .map((c) => c.name);
    expect(cols).toContain("agent_session_id");
    expect(cols).toContain("host_id");
  });

  test("discoverExams lists the Go-written DB", () => {
    seedGoWrittenDb("go-written-listed");
    expect(discoverExams().map((e) => e.slug)).toContain("go-written-listed");
  });

  test("still upgrades a legacy v1/v2 DB to v3", () => {
    seedLegacyDb("legacy-v2");
    expect(() => openDb("legacy-v2")).not.toThrow();
    const versions = openDb("legacy-v2")
      .query<{ version: number }, []>("SELECT version FROM schema_version ORDER BY version")
      .all()
      .map((r) => r.version);
    expect(versions).toContain(3);
  });

  test("applies the Go-owned 004 migration (verdict table appears)", () => {
    seedGradedDb("has-verdicts", { status: "settled", accepted: ["A"], suggested: "A" });
    const db = openDb("has-verdicts");
    const versions = db
      .query<{ version: number }, []>("SELECT version FROM schema_version ORDER BY version")
      .all()
      .map((r) => r.version);
    expect(versions).toEqual([3, 4]);
  });
});

describe("grading against an answer verdict", () => {
  test("both sides of a contested answer are graded correct", () => {
    seedGradedDb("verdict-ambiguous", {
      status: "ambiguous",
      accepted: ["A", "D"],
      suggested: "A",
      community: [
        { label: "D", votes: 13, pct: 65 },
        { label: "A", votes: 6, pct: 30 },
        { label: "B", votes: 1, pct: 5 },
      ],
      totalVotes: 20,
    });
    expect(recordAttempt("verdict-ambiguous", 1, "D", "A")).toEqual({
      correct: true,
      ungraded: false,
    });
    expect(recordAttempt("verdict-ambiguous", 1, "A", "A")).toEqual({
      correct: true,
      ungraded: false,
    });
    expect(recordAttempt("verdict-ambiguous", 1, "B", "A")).toEqual({
      correct: false,
      ungraded: false,
    });
  });

  test("getQuestion exposes the verdict with its vote split", () => {
    seedGradedDb("verdict-exposed", {
      status: "ambiguous",
      accepted: ["A", "D"],
      suggested: "A",
      community: [{ label: "D", votes: 13, pct: 65 }],
      totalVotes: 20,
    });
    const detail = getQuestion("verdict-exposed", 1);
    expect(detail?.verdict.status).toBe("ambiguous");
    expect(detail?.verdict.accepted).toEqual(["A", "D"]);
    expect(detail?.verdict.community[0]).toEqual({ label: "D", votes: 13, pct: 65 });
  });

  test("a settled verdict grades against the single key", () => {
    seedGradedDb("verdict-settled", { status: "settled", accepted: ["A"], suggested: "A" });
    expect(recordAttempt("verdict-settled", 1, "A", "A").correct).toBe(true);
    expect(recordAttempt("verdict-settled", 1, "B", "A").correct).toBe(false);
  });

  test("an ungradeable question records nothing rather than a false miss", () => {
    const path = seedGradedDb("verdict-unknown", {
      status: "unknown",
      accepted: [],
      suggested: "",
    });
    expect(recordAttempt("verdict-unknown", 1, "A", "")).toEqual({
      correct: false,
      ungraded: true,
    });
    const raw = new Database(path);
    const n = raw.query<{ n: number }, []>("SELECT COUNT(*) AS n FROM attempts").get();
    raw.close();
    expect(n?.n).toBe(0);
  });

  test("the review queue drops rows that cannot be answered", () => {
    const path = seedGradedDb("verdict-review", { status: "settled", accepted: ["A"], suggested: "A" });
    seedGradedDb("verdict-review-unknown", { status: "unknown", accepted: [], suggested: "" });
    // One wrong attempt each, as if both predate the verdict table.
    const raw = new Database(path);
    raw.run(
      "INSERT INTO attempts (id, question_id, selected, is_correct, attempted_at, host_id) VALUES (?, 1, 'B', 0, '2026-01-01', 'h')",
      [new Uint8Array(16)]
    );
    raw.close();
    expect(listWrong("verdict-review").map((r) => r.id)).toEqual([1]);

    const other = new Database(join(dir, "verdict-review-unknown.db"));
    other.run(
      "INSERT INTO attempts (id, question_id, selected, is_correct, attempted_at, host_id) VALUES (?, 1, 'A', 0, '2026-01-01', 'h')",
      [new Uint8Array(16)]
    );
    other.close();
    expect(listWrong("verdict-review-unknown")).toEqual([]);
  });
});
