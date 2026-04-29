import { Database } from "bun:sqlite";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { resolve, basename, sep } from "node:path";

const PROJECT_ROOT = resolve(import.meta.dir, "../..");
const PROJECT_ROOT_PREFIX = PROJECT_ROOT.endsWith(sep) ? PROJECT_ROOT : PROJECT_ROOT + sep;
const MIGRATIONS_DIR = resolve(PROJECT_ROOT, "migrations");
const SLUG_RE = /^[A-Za-z0-9._-]+$/;

const SCHEMA = `
  CREATE TABLE IF NOT EXISTS attempts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    question_id INTEGER NOT NULL,
    selected TEXT NOT NULL,
    is_correct INTEGER NOT NULL,
    attempted_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
  );
  CREATE INDEX IF NOT EXISTS idx_attempts_q ON attempts(question_id);

  CREATE TABLE IF NOT EXISTS explanation_threads (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    question_id INTEGER NOT NULL,
    status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','resolved','dismissed')),
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    closed_at TEXT
  );
  CREATE INDEX IF NOT EXISTS idx_thr_qid ON explanation_threads(question_id);
  CREATE INDEX IF NOT EXISTS idx_thr_status ON explanation_threads(status);

  CREATE TABLE IF NOT EXISTS explanation_messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    thread_id INTEGER NOT NULL REFERENCES explanation_threads(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK (role IN ('user','agent')),
    author TEXT,
    content TEXT NOT NULL,
    reason_code TEXT CHECK (
      reason_code IS NULL OR
      reason_code IN ('comprehension','spec','ambiguous','translation')
    ),
    citations TEXT,
    translation_diff TEXT,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
  );
  CREATE INDEX IF NOT EXISTS idx_msg_thread ON explanation_messages(thread_id);

  -- mirrored from internal/sqlite/schema.go so a UI-first DB still has the
  -- table available for future per-poster discussion features. Web side never
  -- populates these rows; the Go scraper writes them.
  CREATE TABLE IF NOT EXISTS discussion (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    question_id INTEGER NOT NULL,
    idx INTEGER NOT NULL,
    poster TEXT,
    content TEXT NOT NULL,
    upvote_count INTEGER,
    posted_at TEXT,
    UNIQUE(question_id, idx)
  );
  CREATE INDEX IF NOT EXISTS idx_discussion_qid ON discussion(question_id);
`;

const cache = new Map<string, Database>();

const SCHEMA_VERSION_DDL = `
  CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
  );
`;

function applyPendingMigrations(db: Database): void {
  db.exec(SCHEMA_VERSION_DDL);
  const applied = new Set(
    db
      .query<{ version: number }, []>("SELECT version FROM schema_version")
      .all()
      .map((r) => r.version)
  );
  let entries: string[];
  try {
    entries = readdirSync(MIGRATIONS_DIR);
  } catch {
    return;
  }
  const files = entries
    .filter((f) => /^\d+_.*\.sql$/.test(f))
    .sort((a, b) => a.localeCompare(b));
  for (const f of files) {
    const m = f.match(/^(\d+)_(.*)\.sql$/);
    if (!m) continue;
    const version = parseInt(m[1], 10);
    if (applied.has(version)) continue;
    const sql = readFileSync(resolve(MIGRATIONS_DIR, f), "utf-8");
    db.exec("PRAGMA foreign_keys = OFF");
    try {
      db.exec("BEGIN");
      db.exec(sql);
      db.run("INSERT INTO schema_version(version, name) VALUES (?, ?)", [
        version,
        f.replace(/\.sql$/, ""),
      ]);
      db.exec("COMMIT");
    } catch (e) {
      try {
        db.exec("ROLLBACK");
      } catch {}
      db.exec("PRAGMA foreign_keys = ON");
      throw e;
    }
    db.exec("PRAGMA foreign_keys = ON");
  }
}

export class InvalidSlugError extends Error {}

function validateSlug(slug: string): void {
  if (!SLUG_RE.test(slug) || slug === "." || slug === "..") {
    throw new InvalidSlugError(`invalid slug: ${slug}`);
  }
}

export function slugToPath(slug: string): string {
  validateSlug(slug);
  const p = resolve(PROJECT_ROOT, `${slug}.db`);
  if (!p.startsWith(PROJECT_ROOT_PREFIX)) {
    throw new InvalidSlugError(`slug escapes project root: ${slug}`);
  }
  return p;
}

export function isKnownExam(slug: string): boolean {
  if (!SLUG_RE.test(slug)) return false;
  let path: string;
  try {
    path = slugToPath(slug);
  } catch {
    return false;
  }
  try {
    statSync(path);
  } catch {
    return false;
  }
  return true;
}

export function openDb(slug: string): Database {
  let d = cache.get(slug);
  if (d) return d;
  const path = slugToPath(slug);
  try {
    statSync(path);
  } catch {
    throw new InvalidSlugError(`exam DB not found: ${slug}`);
  }
  d = new Database(path);
  d.exec(SCHEMA);
  applyPendingMigrations(d);
  cache.set(slug, d);
  return d;
}

export type ExamSummary = {
  slug: string;
  name: string;
  total: number;
  translated: number;
  answered: number;
  correct: number;
  open_threads: number;
  awaiting_agent: number;
};

export function discoverExams(): ExamSummary[] {
  const files = readdirSync(PROJECT_ROOT).filter(
    (f) => f.endsWith(".db") && !f.startsWith(".")
  );
  const out: ExamSummary[] = [];
  for (const f of files) {
    const slug = basename(f, ".db");
    if (!SLUG_RE.test(slug) || slug === "." || slug === "..") continue;
    let db: Database;
    try {
      db = openDb(slug);
    } catch {
      continue;
    }
    // Skip files without `questions` (could be other sqlite files in repo)
    const hasQ = db
      .query<{ n: number }, []>(
        "SELECT COUNT(*) AS n FROM sqlite_master WHERE type='table' AND name='questions'"
      )
      .get();
    if (!hasQ || hasQ.n === 0) continue;
    const meta = db
      .query<{ exam: string; n: number; tn: number }, [string]>(
        `SELECT
           COALESCE((SELECT exam FROM questions GROUP BY exam ORDER BY COUNT(*) DESC LIMIT 1), ?) AS exam,
           (SELECT COUNT(*) FROM questions) AS n,
           (SELECT COUNT(*) FROM questions WHERE question_text_ja IS NOT NULL) AS tn
        `
      )
      .get(slug)!;
    const att = db
      .query<{ a: number; c: number }, []>(
        `SELECT
           (SELECT COUNT(DISTINCT question_id) FROM attempts) AS a,
           (SELECT COUNT(*) FROM (
              SELECT question_id, MAX(is_correct) AS best
                FROM attempts GROUP BY question_id
            ) WHERE best = 1) AS c
        `
      )
      .get()!;
    const thr = db
      .query<{ open_n: number; awa: number }, []>(
        `SELECT
           (SELECT COUNT(*) FROM explanation_threads WHERE status='open') AS open_n,
           (SELECT COUNT(*) FROM explanation_threads t
              LEFT JOIN (
                SELECT thread_id, role,
                       ROW_NUMBER() OVER (PARTITION BY thread_id ORDER BY id DESC) AS rn
                  FROM explanation_messages
              ) lm ON lm.thread_id = t.id AND lm.rn = 1
              WHERE t.status='open' AND lm.role='user') AS awa
        `
      )
      .get()!;
    out.push({
      slug,
      name: meta.exam,
      total: meta.n,
      translated: meta.tn,
      answered: att.a,
      correct: att.c,
      open_threads: thr.open_n,
      awaiting_agent: thr.awa,
    });
  }
  out.sort((a, b) => a.slug.localeCompare(b.slug));
  return out;
}

export type Question = {
  id: number;
  exam: string;
  topic: number;
  question_number: number;
  question_text: string;
  question_text_ja: string | null;
  suggested_answer: string;
  confirmed_answer: string | null;
  explanation_ja: string | null;
  url: string | null;
  comments: string | null;
};

export type Choice = {
  question_id: number;
  label: string;
  text: string;
  text_ja: string | null;
};

export type QuestionListRow = Question & { last_correct: number | null };

const sortLetters = (s: string) => s.split("").sort().join("");

export function listQuestions(slug: string): QuestionListRow[] {
  const db = openDb(slug);
  return db
    .query<QuestionListRow, []>(
      `SELECT q.*, (
         SELECT is_correct FROM attempts a
         WHERE a.question_id = q.id ORDER BY a.id DESC LIMIT 1
       ) AS last_correct
       FROM questions q
       ORDER BY q.topic, q.question_number`
    )
    .all();
}

export function getQuestion(slug: string, id: number) {
  const db = openDb(slug);
  const q = db
    .query<Question, [number]>("SELECT * FROM questions WHERE id = ?")
    .get(id);
  if (!q) return null;
  const choices = db
    .query<Choice, [number]>(
      "SELECT * FROM choices WHERE question_id = ? ORDER BY label"
    )
    .all(id);
  const attempts = db
    .query<
      { selected: string; is_correct: number; attempted_at: string },
      [number]
    >(
      "SELECT selected, is_correct, attempted_at FROM attempts " +
        "WHERE question_id = ? ORDER BY id DESC LIMIT 5"
    )
    .all(id);
  const ids = db
    .query<{ id: number }, []>(
      "SELECT id FROM questions ORDER BY topic, question_number"
    )
    .all()
    .map((r) => r.id);
  const idx = ids.indexOf(q.id);
  return {
    q,
    choices,
    attempts,
    prevId: idx > 0 ? ids[idx - 1] : null,
    nextId: idx >= 0 && idx < ids.length - 1 ? ids[idx + 1] : null,
  };
}

export function recordAttempt(slug: string, qid: number, selected: string, correctAnswer: string) {
  const db = openDb(slug);
  const isCorrect = sortLetters(selected) === sortLetters(correctAnswer) ? 1 : 0;
  db.run(
    "INSERT INTO attempts(question_id, selected, is_correct) VALUES(?,?,?)",
    [qid, selected, isCorrect]
  );
  return isCorrect === 1;
}

export function listWrong(slug: string) {
  const db = openDb(slug);
  return db
    .query<QuestionListRow, []>(
      `SELECT q.*, a.is_correct AS last_correct
       FROM questions q
       JOIN (
         SELECT question_id, is_correct,
                ROW_NUMBER() OVER (PARTITION BY question_id ORDER BY id DESC) AS rn
         FROM attempts
       ) a ON a.question_id = q.id AND a.rn = 1
       WHERE a.is_correct = 0
       ORDER BY q.topic, q.question_number`
    )
    .all();
}

export function progress(slug: string) {
  const db = openDb(slug);
  const total = db.query<{ n: number }, []>("SELECT COUNT(*) AS n FROM questions").get()!.n;
  const answered = db
    .query<{ n: number }, []>("SELECT COUNT(DISTINCT question_id) AS n FROM attempts")
    .get()!.n;
  const correct = db
    .query<{ n: number }, []>(
      `SELECT COUNT(*) AS n FROM (
         SELECT question_id, MAX(is_correct) AS best
         FROM attempts GROUP BY question_id
       ) WHERE best = 1`
    )
    .get()!.n;
  return { total, answered, correct };
}

export type Thread = {
  id: number;
  question_id: number;
  status: "open" | "resolved" | "dismissed";
  created_at: string;
  closed_at: string | null;
};

export type ReasonCode =
  | "comprehension"
  | "spec"
  | "ambiguous"
  | "translation";

export type Citation = { url: string; title?: string };

export type TranslationDiff = {
  before: {
    question_text_ja?: string | null;
    explanation_ja?: string | null;
    choices_ja?: Record<string, string | null>;
  };
  after: {
    question_text_ja?: string;
    explanation_ja?: string;
    choices_ja?: Record<string, string>;
  };
};

export type Message = {
  id: number;
  thread_id: number;
  role: "user" | "agent";
  author: string | null;
  content: string;
  reason_code: ReasonCode | null;
  citations: string | null;
  translation_diff: string | null;
  created_at: string;
};

export type ThreadWithMessages = Thread & { messages: Message[] };

export type ThreadListRow = Thread & {
  exam: string;
  question_number: number;
  question_text_ja: string | null;
  question_text: string;
  last_role: "user" | "agent" | null;
  last_content: string | null;
  last_at: string | null;
  message_count: number;
};

const lastMessageJoin = `
  LEFT JOIN (
    SELECT thread_id, role, content, created_at,
           ROW_NUMBER() OVER (PARTITION BY thread_id ORDER BY id DESC) AS rn
      FROM explanation_messages
  ) lm ON lm.thread_id = t.id AND lm.rn = 1
`;

export function getOpenThread(slug: string, qid: number): ThreadWithMessages | null {
  const db = openDb(slug);
  const t = db
    .query<Thread, [number]>(
      "SELECT * FROM explanation_threads WHERE question_id = ? AND status = 'open' " +
        "ORDER BY id DESC LIMIT 1"
    )
    .get(qid);
  if (!t) return null;
  const messages = db
    .query<Message, [number]>(
      "SELECT * FROM explanation_messages WHERE thread_id = ? ORDER BY id ASC"
    )
    .all(t.id);
  return { ...t, messages };
}

export function getThread(slug: string, id: number): ThreadWithMessages | null {
  const db = openDb(slug);
  const t = db
    .query<Thread, [number]>("SELECT * FROM explanation_threads WHERE id = ?")
    .get(id);
  if (!t) return null;
  const messages = db
    .query<Message, [number]>(
      "SELECT * FROM explanation_messages WHERE thread_id = ? ORDER BY id ASC"
    )
    .all(t.id);
  return { ...t, messages };
}

export function createThread(slug: string, qid: number, firstMessage: string, author: string | null = null) {
  const db = openDb(slug);
  const tx = db.transaction((qid: number, content: string, author: string | null) => {
    const r = db.run(
      "INSERT INTO explanation_threads(question_id) VALUES(?)",
      [qid]
    );
    const tid = Number(r.lastInsertRowid);
    db.run(
      "INSERT INTO explanation_messages(thread_id, role, author, content) VALUES(?,?,?,?)",
      [tid, "user", author, content]
    );
    return tid;
  });
  return tx(qid, firstMessage, author);
}

export function appendMessage(
  slug: string,
  threadId: number,
  role: "user" | "agent",
  content: string,
  author: string | null = null
): Message | null {
  const db = openDb(slug);
  const t = db
    .query<{ status: string }, [number]>(
      "SELECT status FROM explanation_threads WHERE id = ?"
    )
    .get(threadId);
  if (!t || t.status !== "open") return null;
  const r = db.run(
    "INSERT INTO explanation_messages(thread_id, role, author, content) VALUES(?,?,?,?)",
    [threadId, role, author, content]
  );
  return db
    .query<Message, [number]>("SELECT * FROM explanation_messages WHERE id = ?")
    .get(Number(r.lastInsertRowid));
}

export function closeThread(slug: string, id: number, status: "resolved" | "dismissed") {
  const db = openDb(slug);
  db.run(
    "UPDATE explanation_threads SET status = ?, closed_at = CURRENT_TIMESTAMP " +
      "WHERE id = ? AND status = 'open'",
    [status, id]
  );
}

export function listOpenThreads(slug: string, awaiting?: "user" | "agent"): ThreadListRow[] {
  const db = openDb(slug);
  const where =
    awaiting === undefined
      ? "WHERE t.status = 'open'"
      : "WHERE t.status = 'open' AND lm.role = ?";
  const sql = `
    SELECT t.*, q.exam, q.question_number, q.question_text_ja, q.question_text,
           lm.role AS last_role, lm.content AS last_content, lm.created_at AS last_at,
           (SELECT COUNT(*) FROM explanation_messages WHERE thread_id = t.id) AS message_count
      FROM explanation_threads t
      JOIN questions q ON q.id = t.question_id
    ${lastMessageJoin}
    ${where}
    ORDER BY (lm.created_at IS NULL), lm.created_at DESC, t.id DESC
  `;
  if (awaiting === undefined) {
    return db.query<ThreadListRow, []>(sql).all();
  }
  const opposite = awaiting === "agent" ? "user" : "agent";
  return db.query<ThreadListRow, [string]>(sql).all(opposite);
}

export type ThreadListRowAll = ThreadListRow & { slug: string; exam_name: string };

export function listOpenThreadsAll(awaiting?: "user" | "agent"): ThreadListRowAll[] {
  const exams = discoverExams();
  const out: ThreadListRowAll[] = [];
  for (const e of exams) {
    const rows = listOpenThreads(e.slug, awaiting);
    for (const r of rows) {
      out.push({ ...r, slug: e.slug, exam_name: e.name });
    }
  }
  out.sort((a, b) => (b.last_at ?? "").localeCompare(a.last_at ?? ""));
  return out;
}

export function countAwaitingAgentAll(): number {
  const exams = discoverExams();
  let n = 0;
  for (const e of exams) n += e.awaiting_agent;
  return n;
}

export function updateExplanation(slug: string, qid: number, ja: string) {
  const db = openDb(slug);
  db.run("UPDATE questions SET explanation_ja = ? WHERE id = ?", [ja, qid]);
}
