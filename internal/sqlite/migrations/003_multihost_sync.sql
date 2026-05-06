-- 003_multihost_sync.sql
-- Replace the per-host INTEGER-PK schema for attempts / explanation_threads /
-- explanation_messages with a UUIDv7 BLOB-PK schema that supports multi-host
-- sync (G-Set + LWW merge) per docs/plans/portable-builds.md §3.7.2. Hosts
-- write their own host_id into each row so peer DBs can be merged via simple
-- INSERT OR IGNORE without primary-key collisions.
--
-- This migration is destructive: any existing rows in the affected tables are
-- discarded. The plan documents this trade-off (§6 risk note "既存母艦 DB の
-- attempts/threads/messages が破壊的 migration で消える"); a snapshot must be
-- taken beforehand if the data matters.
--
-- The runner sets PRAGMA foreign_keys = OFF and wraps this in a transaction.

DROP TABLE IF EXISTS explanation_messages;
DROP TABLE IF EXISTS explanation_threads;
DROP TABLE IF EXISTS attempts;

CREATE TABLE attempts (
  id BLOB(16) PRIMARY KEY,
  question_id INTEGER NOT NULL REFERENCES questions(id),
  selected TEXT NOT NULL,
  is_correct INTEGER NOT NULL,
  attempted_at TEXT NOT NULL,
  host_id TEXT NOT NULL
) WITHOUT ROWID;
CREATE INDEX idx_attempts_q ON attempts(question_id);
CREATE INDEX idx_attempts_host_time ON attempts(host_id, attempted_at);

CREATE TABLE explanation_threads (
  id BLOB(16) PRIMARY KEY,
  question_id INTEGER NOT NULL REFERENCES questions(id),
  status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','resolved','dismissed')),
  agent_session_id TEXT,
  created_at TEXT NOT NULL,
  closed_at TEXT,
  updated_at TEXT NOT NULL,
  host_id TEXT NOT NULL
) WITHOUT ROWID;
CREATE INDEX idx_thr_qid ON explanation_threads(question_id);
CREATE INDEX idx_thr_status ON explanation_threads(status);
CREATE INDEX idx_thr_updated ON explanation_threads(updated_at);

CREATE TABLE explanation_messages (
  id BLOB(16) PRIMARY KEY,
  thread_id BLOB(16) NOT NULL REFERENCES explanation_threads(id) ON DELETE CASCADE,
  role TEXT NOT NULL CHECK (role IN ('user','agent')),
  author TEXT,
  content TEXT NOT NULL,
  reason_code TEXT CHECK (
    reason_code IS NULL OR
    reason_code IN ('comprehension','spec','ambiguous','translation')
  ),
  citations TEXT,
  translation_diff TEXT,
  created_at TEXT NOT NULL,
  host_id TEXT NOT NULL
) WITHOUT ROWID;
CREATE INDEX idx_msg_thread ON explanation_messages(thread_id);
