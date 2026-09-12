import { Database } from "bun:sqlite";
import { existsSync, readdirSync, statSync } from "node:fs";
import { hostname } from "node:os";
import { resolve, basename, sep } from "node:path";
// Migration SQL is embedded at build time so the compiled Bun binary
// does not depend on a `migrations/` directory next to the executable
// (§2 notice 2 of docs/plans/portable-builds.md). Adding a future
// migration: import it as text below and append to MIGRATIONS in
// version order.
import sql001 from "../../migrations/001_explanation_messages_grounding.sql" with { type: "text" };
import sql002 from "../../migrations/002_thread_agent_session.sql" with { type: "text" };
// Migration 003 lives next to the Go embed so both runtimes share a
// single authoritative SQL file. The Bun text import is just another
// view onto the same bytes.
import sql003 from "../../internal/sqlite/migrations/003_multihost_sync.sql" with { type: "text" };
import { loadConfig } from "./config";
import * as uuidx from "./uuidx";

// dataDir() is the directory we treat as the source of *.db files, and
// the boundary every slug must stay within (path-traversal guard).
// Resolved through loadConfig() on each call rather than captured at
// import time: `bun test` shares one process across files, and a caller
// may repoint dataDir with resetConfigCache() + EXAMTOPICS_DATA_DIR after
// this module has already loaded. Sourced from loadConfig() so the same
// EXAMTOPICS_DATA_DIR / config.json contract drives both the Go CLI and
// the Bun web; the previous import.meta.dir-based PROJECT_ROOT became a
// virtual path inside `bun build --compile` outputs (§2 notice 3).
// (agent.ts already resolves it per call for the same reason.)
function dataDir(): string {
  return loadConfig().dataDir;
}
const SLUG_RE = /^[A-Za-z0-9._-]+$/;

type Migration = { version: number; name: string; sql: string };
const MIGRATIONS: Migration[] = [
  { version: 1, name: "001_explanation_messages_grounding", sql: sql001 },
  { version: 2, name: "002_thread_agent_session", sql: sql002 },
  { version: 3, name: "003_multihost_sync", sql: sql003 },
];

// Stable host id for rows this Bun process writes. Falls back to a
// hostname-derived string when the loaded config doesn't carry one
// (e.g. before any Go CLI invocation has persisted a hostId for the
// machine). Persisting from the Bun side is intentionally deferred to
// the Go CLI — see the comment in config.ts.
let cachedHostId: string | null = null;
function hostId(): string {
  if (cachedHostId !== null) return cachedHostId;
  const cfg = loadConfig();
  let id = cfg.hostId;
  if (!id) {
    const raw = (hostname() || "host").toLowerCase();
    let cleaned = "";
    for (const ch of raw) {
      if (
        (ch >= "a" && ch <= "z") ||
        (ch >= "0" && ch <= "9") ||
        ch === "-" ||
        ch === "_"
      ) {
        cleaned += ch;
      }
    }
    if (!cleaned) cleaned = "host";
    if (cleaned.length > 32) cleaned = cleaned.slice(0, 32);
    id = `${cleaned}-fallback`;
  }
  cachedHostId = id;
  return id;
}

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
    closed_at TEXT,
    agent_session_id TEXT
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

// SCHEMA above creates the v2 INTEGER-PK forms of the multihost tables
// for fresh DBs so migrations 001 / 002 (which 12-step-reconstruct
// explanation_messages and ALTER explanation_threads) have something
// to operate on. Migration 003 then drops and recreates those tables
// with the v3 BLOB-PK shape; preservation hooks (captureLegacyV2 +
// restoreLegacyV2) carry any rows across with freshly minted UUIDv7
// ids and the host_id stamp.

const cache = new Map<string, Database>();

const SCHEMA_VERSION_DDL = `
  CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
  );
`;

// --- Multi-host preservation ---------------------------------------------

type V2Attempt = {
  question_id: number;
  selected: string;
  is_correct: number;
  attempted_at: string | null;
};
type V2Thread = {
  id: number;
  question_id: number;
  status: string;
  created_at: string;
  closed_at: string | null;
  agent_session_id: string | null;
};
type V2Message = {
  id: number;
  thread_id: number;
  role: string;
  author: string | null;
  content: string;
  reason_code: string | null;
  citations: string | null;
  translation_diff: string | null;
  created_at: string;
};
type CapturedV2 = {
  attempts: V2Attempt[];
  threads: V2Thread[];
  messages: V2Message[];
};

function hasV2Schema(db: Database, table: string): boolean {
  const cols = db
    .query<{ name: string }, [string]>("SELECT name FROM pragma_table_info(?)")
    .all(table);
  if (cols.length === 0) return false;
  return !cols.some((c) => c.name === "host_id");
}

function captureLegacyV2(db: Database): CapturedV2 {
  const c: CapturedV2 = { attempts: [], threads: [], messages: [] };
  if (hasV2Schema(db, "attempts")) {
    c.attempts = db
      .query<V2Attempt, []>(
        "SELECT question_id, selected, is_correct, attempted_at FROM attempts"
      )
      .all();
  }
  if (hasV2Schema(db, "explanation_threads")) {
    c.threads = db
      .query<V2Thread, []>(
        "SELECT id, question_id, status, created_at, closed_at, agent_session_id FROM explanation_threads"
      )
      .all();
  }
  if (hasV2Schema(db, "explanation_messages")) {
    c.messages = db
      .query<V2Message, []>(
        "SELECT id, thread_id, role, author, content, reason_code, citations, translation_diff, created_at FROM explanation_messages"
      )
      .all();
  }
  return c;
}

function restoreLegacyV2(db: Database, c: CapturedV2, host: string): void {
  if (!host) throw new Error("preserve v3: hostId must not be empty");
  // Insert threads first so the message FK targets exist. The int → BLOB
  // mapping is the only reason we can't preserve threads + messages in
  // independent passes.
  const threadIdMap = new Map<number, Uint8Array>();
  for (const t of c.threads) {
    const id = uuidx.newUuidV7();
    threadIdMap.set(t.id, id);
    const updatedAt =
      t.closed_at && t.closed_at > t.created_at ? t.closed_at : t.created_at;
    db.run(
      `INSERT INTO explanation_threads
         (id, question_id, status, agent_session_id, created_at, closed_at, updated_at, host_id)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
      [
        id,
        t.question_id,
        t.status,
        t.agent_session_id,
        t.created_at,
        t.closed_at,
        updatedAt,
        host,
      ]
    );
  }
  for (const a of c.attempts) {
    const id = uuidx.newUuidV7();
    const ts = a.attempted_at && a.attempted_at !== ""
      ? a.attempted_at
      : new Date().toISOString();
    db.run(
      `INSERT INTO attempts (id, question_id, selected, is_correct, attempted_at, host_id)
       VALUES (?, ?, ?, ?, ?, ?)`,
      [id, a.question_id, a.selected, a.is_correct, ts, host]
    );
  }
  for (const m of c.messages) {
    const newTid = threadIdMap.get(m.thread_id);
    if (!newTid) continue;
    const id = uuidx.newUuidV7();
    db.run(
      `INSERT INTO explanation_messages
         (id, thread_id, role, author, content, reason_code, citations, translation_diff, created_at, host_id)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
      [
        id,
        newTid,
        m.role,
        m.author,
        m.content,
        m.reason_code,
        m.citations,
        m.translation_diff,
        m.created_at,
        host,
      ]
    );
  }
}

function backupBeforeV3(db: Database, path: string): void {
  // schema_version may not exist yet on a freshly-opened legacy DB.
  db.exec(SCHEMA_VERSION_DDL);
  const v3 = db
    .query<{ n: number }, []>(
      "SELECT COUNT(*) AS n FROM schema_version WHERE version = 3"
    )
    .get();
  if (v3 && v3.n > 0) return;
  const bak = path + ".pre-003.bak";
  if (existsSync(bak)) return;
  // VACUUM INTO writes a consistent copy regardless of WAL state.
  // Single quotes are escaped per SQL literal rules.
  db.exec(`VACUUM INTO '${bak.replace(/'/g, "''")}'`);
}

function applyPendingMigrations(db: Database): void {
  db.exec(SCHEMA_VERSION_DDL);
  const applied = new Set(
    db
      .query<{ version: number }, []>("SELECT version FROM schema_version")
      .all()
      .map((r) => r.version)
  );
  // A DB written by the Go CLI already carries version 3 while recording
  // nothing for 001/002: the Go migration set is embedded from 003 onward,
  // so its versions start there. Those two scripts are then superseded —
  // replaying them rebuilds explanation_messages into its old INTEGER-PK
  // shape and aborts on the already-present agent_session_id column, which
  // made openDb() throw and discoverExams() skip every such DB (the UI
  // showed "試験 DB が見つかりません" while the files sat right there).
  // Same rule as tools/translate.py.
  const v3Applied = applied.has(3);
  for (const m of MIGRATIONS) {
    if (applied.has(m.version)) continue;
    if (v3Applied && m.version < 3) continue;
    db.exec("PRAGMA foreign_keys = OFF");
    try {
      db.exec("BEGIN");
      let captured: CapturedV2 | null = null;
      if (m.version === 3) {
        captured = captureLegacyV2(db);
      }
      db.exec(m.sql);
      if (m.version === 3 && captured) {
        restoreLegacyV2(db, captured, hostId());
      }
      db.run("INSERT INTO schema_version(version, name) VALUES (?, ?)", [
        m.version,
        m.name,
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
  const dir = dataDir();
  const p = resolve(dir, `${slug}.db`);
  if (!p.startsWith(dir.endsWith(sep) ? dir : dir + sep)) {
    throw new InvalidSlugError(`slug escapes data dir: ${slug}`);
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
  // Backup before any v2 → v3 migration so the destructive 003 has a
  // safety net even if its preservation step has a latent bug.
  backupBeforeV3(d, path);
  applyPendingMigrations(d);
  cache.set(slug, d);
  return d;
}

// --- Public types ---------------------------------------------------------

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

export type Attempt = {
  selected: string;
  is_correct: number;
  attempted_at: string;
};

export type Progress = { total: number; answered: number; correct: number };

export type QuestionDetail = {
  q: Question;
  choices: Choice[];
  attempts: Attempt[];
  prevId: number | null;
  nextId: number | null;
};

// Thread / Message ids are 26-char Crockford base32 (UUIDv7 BLOB
// encoded by uuidx.encode). Using strings instead of Uint8Array at
// the public boundary keeps URLs and HTML attributes straightforward;
// the SQL layer below this file translates to/from BLOB at the call
// site using uuidx.decode.

export type Thread = {
  id: string;
  question_id: number;
  status: "open" | "resolved" | "dismissed";
  created_at: string;
  closed_at: string | null;
  agent_session_id: string | null;
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
  id: string;
  thread_id: string;
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

// Internal row shapes — the BLOB columns come back as Uint8Array
// (Buffer instances in Bun, which extend Uint8Array). Mappers below
// convert to the public string-id types above.
type ThreadRow = Omit<Thread, "id"> & { id: Uint8Array };
type MessageRow = Omit<Message, "id" | "thread_id"> & {
  id: Uint8Array;
  thread_id: Uint8Array;
};

function toThread(r: ThreadRow): Thread {
  return { ...r, id: uuidx.encode(r.id) };
}
function toThreadWithMessages(
  r: ThreadRow,
  msgs: MessageRow[]
): ThreadWithMessages {
  return { ...toThread(r), messages: msgs.map(toMessage) };
}
function toMessage(r: MessageRow): Message {
  return {
    ...r,
    id: uuidx.encode(r.id),
    thread_id: uuidx.encode(r.thread_id),
  };
}

function decodeId(s: string): Uint8Array {
  return uuidx.decode(s);
}

export function parseCitations(json: string | null): Citation[] {
  if (!json) return [];
  try {
    const parsed = JSON.parse(json);
    if (!Array.isArray(parsed)) return [];
    return parsed.filter(
      (c): c is Citation =>
        c && typeof c === "object" && typeof c.url === "string"
    );
  } catch {
    return [];
  }
}

const sortLetters = (s: string) => s.split("").sort().join("");

export function discoverExams(): ExamSummary[] {
  let files: string[];
  try {
    files = readdirSync(dataDir()).filter(
      (f) => f.endsWith(".db") && !f.startsWith(".")
    );
  } catch {
    return [];
  }
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

export function getQuestion(slug: string, id: number): QuestionDetail | null {
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
    .query<Attempt, [number]>(
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

export function recordAttempt(
  slug: string,
  qid: number,
  selected: string,
  correctAnswer: string
): boolean {
  const db = openDb(slug);
  const isCorrect = sortLetters(selected) === sortLetters(correctAnswer) ? 1 : 0;
  const id = uuidx.newUuidV7();
  const ts = new Date().toISOString();
  db.run(
    "INSERT INTO attempts(id, question_id, selected, is_correct, attempted_at, host_id) VALUES(?,?,?,?,?,?)",
    [id, qid, selected, isCorrect, ts, hostId()]
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

export function progress(slug: string): Progress {
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

const lastMessageJoin = `
  LEFT JOIN (
    SELECT thread_id, role, content, created_at,
           ROW_NUMBER() OVER (PARTITION BY thread_id ORDER BY id DESC) AS rn
      FROM explanation_messages
  ) lm ON lm.thread_id = t.id AND lm.rn = 1
`;

export function getOpenThread(
  slug: string,
  qid: number
): ThreadWithMessages | null {
  const db = openDb(slug);
  const t = db
    .query<ThreadRow, [number]>(
      "SELECT * FROM explanation_threads WHERE question_id = ? AND status = 'open' " +
        "ORDER BY id DESC LIMIT 1"
    )
    .get(qid);
  if (!t) return null;
  const messages = db
    .query<MessageRow, [Uint8Array]>(
      "SELECT * FROM explanation_messages WHERE thread_id = ? ORDER BY id ASC"
    )
    .all(t.id);
  return toThreadWithMessages(t, messages);
}

export function getThread(
  slug: string,
  id: string
): ThreadWithMessages | null {
  const db = openDb(slug);
  const idBytes = decodeId(id);
  const t = db
    .query<ThreadRow, [Uint8Array]>(
      "SELECT * FROM explanation_threads WHERE id = ?"
    )
    .get(idBytes);
  if (!t) return null;
  const messages = db
    .query<MessageRow, [Uint8Array]>(
      "SELECT * FROM explanation_messages WHERE thread_id = ? ORDER BY id ASC"
    )
    .all(t.id);
  return toThreadWithMessages(t, messages);
}

export function createThread(
  slug: string,
  qid: number,
  firstMessage: string,
  author: string | null = null
): string {
  const db = openDb(slug);
  const tidBytes = uuidx.newUuidV7();
  const midBytes = uuidx.newUuidV7();
  const ts = new Date().toISOString();
  const host = hostId();
  const tx = db.transaction(() => {
    db.run(
      "INSERT INTO explanation_threads(id, question_id, status, created_at, updated_at, host_id) VALUES(?,?,?,?,?,?)",
      [tidBytes, qid, "open", ts, ts, host]
    );
    db.run(
      "INSERT INTO explanation_messages(id, thread_id, role, author, content, created_at, host_id) VALUES(?,?,?,?,?,?,?)",
      [midBytes, tidBytes, "user", author, firstMessage, ts, host]
    );
  });
  tx();
  return uuidx.encode(tidBytes);
}

export function appendMessage(
  slug: string,
  threadId: string,
  role: "user" | "agent",
  content: string,
  author: string | null = null
): Message | null {
  const db = openDb(slug);
  const tidBytes = decodeId(threadId);
  const t = db
    .query<{ status: string }, [Uint8Array]>(
      "SELECT status FROM explanation_threads WHERE id = ?"
    )
    .get(tidBytes);
  if (!t || t.status !== "open") return null;
  const midBytes = uuidx.newUuidV7();
  const ts = new Date().toISOString();
  const host = hostId();
  const tx = db.transaction(() => {
    db.run(
      "INSERT INTO explanation_messages(id, thread_id, role, author, content, created_at, host_id) VALUES(?,?,?,?,?,?,?)",
      [midBytes, tidBytes, role, author, content, ts, host]
    );
    db.run(
      "UPDATE explanation_threads SET updated_at = ? WHERE id = ?",
      [ts, tidBytes]
    );
  });
  tx();
  const row = db
    .query<MessageRow, [Uint8Array]>(
      "SELECT * FROM explanation_messages WHERE id = ?"
    )
    .get(midBytes);
  return row ? toMessage(row) : null;
}

export function closeThread(
  slug: string,
  id: string,
  status: "resolved" | "dismissed"
): void {
  const db = openDb(slug);
  const tidBytes = decodeId(id);
  const ts = new Date().toISOString();
  db.run(
    "UPDATE explanation_threads SET status = ?, closed_at = ?, updated_at = ? " +
      "WHERE id = ? AND status = 'open'",
    [status, ts, ts, tidBytes]
  );
}

export function listOpenThreads(
  slug: string,
  awaiting?: "user" | "agent"
): ThreadListRow[] {
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
  type Row = ThreadRow & {
    exam: string;
    question_number: number;
    question_text_ja: string | null;
    question_text: string;
    last_role: "user" | "agent" | null;
    last_content: string | null;
    last_at: string | null;
    message_count: number;
  };
  let rows: Row[];
  if (awaiting === undefined) {
    rows = db.query<Row, []>(sql).all();
  } else {
    const opposite = awaiting === "agent" ? "user" : "agent";
    rows = db.query<Row, [string]>(sql).all(opposite);
  }
  return rows.map(({ id, ...rest }) => ({
    ...rest,
    id: uuidx.encode(id),
  }));
}

export type ThreadListRowAll = ThreadListRow & {
  slug: string;
  exam_name: string;
};

export function listOpenThreadsAll(
  awaiting?: "user" | "agent"
): ThreadListRowAll[] {
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

export function updateExplanation(slug: string, qid: number, ja: string): void {
  const db = openDb(slug);
  db.run("UPDATE questions SET explanation_ja = ? WHERE id = ?", [ja, qid]);
}

export function clearQuestionTranslation(slug: string, qid: number): void {
  const db = openDb(slug);
  const tx = db.transaction((qid: number) => {
    db.run(
      "UPDATE questions SET question_text_ja = NULL, explanation_ja = NULL WHERE id = ?",
      [qid]
    );
    db.run("UPDATE choices SET text_ja = NULL WHERE question_id = ?", [qid]);
  });
  tx(qid);
}

export function getOpenThreadIdForQuestion(
  slug: string,
  qid: number
): string | null {
  const db = openDb(slug);
  const r = db
    .query<{ id: Uint8Array }, [number]>(
      "SELECT id FROM explanation_threads WHERE question_id = ? AND status = 'open' " +
        "ORDER BY id DESC LIMIT 1"
    )
    .get(qid);
  return r ? uuidx.encode(r.id) : null;
}

export function getThreadStatus(
  slug: string,
  tid: string
): "open" | "resolved" | "dismissed" | null {
  const db = openDb(slug);
  const r = db
    .query<{ status: "open" | "resolved" | "dismissed" }, [Uint8Array]>(
      "SELECT status FROM explanation_threads WHERE id = ?"
    )
    .get(decodeId(tid));
  return r ? r.status : null;
}

export function getThreadLastRole(
  slug: string,
  tid: string
): "user" | "agent" | null {
  const db = openDb(slug);
  const r = db
    .query<{ role: "user" | "agent" }, [Uint8Array]>(
      "SELECT role FROM explanation_messages WHERE thread_id = ? " +
        "ORDER BY id DESC LIMIT 1"
    )
    .get(decodeId(tid));
  return r ? r.role : null;
}

export function getThreadAgentSessionId(
  slug: string,
  tid: string
): string | null {
  const db = openDb(slug);
  const r = db
    .query<{ agent_session_id: string | null }, [Uint8Array]>(
      "SELECT agent_session_id FROM explanation_threads WHERE id = ?"
    )
    .get(decodeId(tid));
  return r ? r.agent_session_id : null;
}

export function setThreadAgentSessionId(
  slug: string,
  tid: string,
  sessionId: string | null
): void {
  const db = openDb(slug);
  db.run("UPDATE explanation_threads SET agent_session_id = ? WHERE id = ?", [
    sessionId,
    decodeId(tid),
  ]);
}

export function getLatestUserContent(
  slug: string,
  tid: string
): string | null {
  const db = openDb(slug);
  const r = db
    .query<{ content: string }, [Uint8Array]>(
      "SELECT content FROM explanation_messages " +
        "WHERE thread_id = ? AND role = 'user' " +
        "ORDER BY id DESC LIMIT 1"
    )
    .get(decodeId(tid));
  return r ? r.content : null;
}
