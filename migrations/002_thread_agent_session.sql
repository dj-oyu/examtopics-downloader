-- 002_thread_agent_session.sql
-- Persist Claude Code session IDs per explanation thread so the Bun-side
-- autoresponder can call `claude --resume <session_id>` for follow-up turns,
-- inheriting prior reasoning and AWS Docs MCP context within a single thread.
--
-- Plain ALTER TABLE ADD COLUMN is sufficient: the new column is nullable and
-- introduces no CHECK constraint, so 12-step table reconstruction is not
-- required. The runner sets PRAGMA foreign_keys = OFF and wraps this in a
-- transaction; do NOT add BEGIN / COMMIT here.

ALTER TABLE explanation_threads ADD COLUMN agent_session_id TEXT;
