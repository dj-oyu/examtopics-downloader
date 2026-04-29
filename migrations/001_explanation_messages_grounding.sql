-- 001_explanation_messages_grounding.sql
-- Add reason_code / citations / translation_diff columns to explanation_messages.
-- Uses 12-step table reconstruction so a CHECK constraint on reason_code is
-- enforced uniformly on fresh and migrated DBs.
--
-- The runner sets PRAGMA foreign_keys = OFF and wraps this script in a single
-- transaction. Do NOT add BEGIN / COMMIT here.

CREATE TABLE explanation_messages_new (
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

INSERT INTO explanation_messages_new (id, thread_id, role, author, content, created_at)
  SELECT id, thread_id, role, author, content, created_at
    FROM explanation_messages;

DROP TABLE explanation_messages;
ALTER TABLE explanation_messages_new RENAME TO explanation_messages;

CREATE INDEX IF NOT EXISTS idx_msg_thread ON explanation_messages(thread_id);
