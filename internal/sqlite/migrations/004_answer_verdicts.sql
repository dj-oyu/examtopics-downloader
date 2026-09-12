-- 004_answer_verdicts.sql
-- Per-question answer verdict, so the study UI can be honest about questions
-- whose answer is not settled instead of pretending one key is the truth.
--
--   settled   - the community consensus matches suggested_answer and is
--               conclusive; `accepted` holds that single answer.
--   ambiguous - the answer is genuinely contested (consensus disagrees with the
--               key, or the plurality is too weak to call); `accepted` holds
--               EVERY combination that counts as correct, so a learner who
--               picks either side of the argument is graded right, and
--               `community` carries the vote split to show after they answer.
--   unknown   - no votes and no usable key (image/HOTSPOT items); nothing is
--               gradeable, and `accepted` is empty.
--
-- Derived data: every host that runs tools/answer_verdict.py --apply over the
-- same exam DB computes the same rows from `discussion` + `suggested_answer`,
-- so this table intentionally carries no host_id — it is recomputable, not
-- authored, and must not become a merge conflict between peers.
--
-- The runner sets PRAGMA foreign_keys = OFF and wraps this in a transaction.

CREATE TABLE IF NOT EXISTS answer_verdicts (
  question_id INTEGER PRIMARY KEY REFERENCES questions(id) ON DELETE CASCADE,
  status      TEXT NOT NULL CHECK (status IN ('settled','ambiguous','unknown')),
  -- JSON array of canonical (sorted, upper-case) answer combinations, ANY of
  -- which is a correct answer. '["AD"]' means "the letters A and D, both".
  accepted    TEXT NOT NULL DEFAULT '[]',
  -- JSON [{label, votes, pct}] in descending vote order — percentages are shown
  -- to the learner only AFTER they answer.
  community   TEXT NOT NULL DEFAULT '[]',
  total_votes INTEGER NOT NULL DEFAULT 0,
  source      TEXT NOT NULL DEFAULT 'discussion',
  rationale   TEXT,
  computed_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_verdict_status ON answer_verdicts(status);
