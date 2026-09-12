#!/usr/bin/env python3
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Regression tests for translate.py's migration runner.

Run:  python3 tools/test_translate_migrations.py

Two writers share these DBs and they keep *separate* migration bookkeeping:

* the Go CLI embeds its own set starting at 003 (internal/sqlite/migrations) and
  creates the v3 "multihost sync" shape directly — BLOB(16) UUIDv7 thread ids
  plus host_id / updated_at;
* `tools/translate.py` (and the Bun side) read the repo-root `migrations/` dir,
  which still holds the legacy 001 / 002 scripts that rebuild those same tables
  into the old INTEGER-PK shape.

Opening a Go-created DB with the legacy runner therefore used to explode with
`sqlite3.OperationalError: duplicate column name: agent_session_id` — and, worse,
001 was applied first, silently downgrading `explanation_messages`. The runner
must skip the superseded scripts when the DB is already at v3.
"""

from __future__ import annotations

import sqlite3
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import translate  # noqa: E402


def v3_shaped_db(path: Path) -> sqlite3.Connection:
    """A DB as the Go writer leaves it after migration 003."""
    conn = sqlite3.connect(path)
    conn.executescript(
        """
        CREATE TABLE schema_version (
          version INTEGER PRIMARY KEY,
          name TEXT NOT NULL,
          applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
        );
        INSERT INTO schema_version(version, name) VALUES(3, 'multihost_sync');

        CREATE TABLE explanation_threads (
          id BLOB(16) PRIMARY KEY,
          question_id INTEGER NOT NULL,
          status TEXT NOT NULL DEFAULT 'open',
          agent_session_id TEXT,
          created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
          closed_at TEXT,
          updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
          host_id TEXT NOT NULL
        );

        CREATE TABLE explanation_messages (
          id BLOB(16) PRIMARY KEY,
          thread_id BLOB(16) NOT NULL REFERENCES explanation_threads(id) ON DELETE CASCADE,
          role TEXT NOT NULL CHECK (role IN ('user','agent')),
          content TEXT NOT NULL,
          reason_code TEXT,
          citations TEXT,
          translation_diff TEXT,
          created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
          updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
          host_id TEXT NOT NULL
        );
        """
    )
    conn.commit()
    return conn


class MigrationRunnerTest(unittest.TestCase):
    def test_v3_db_is_not_downgraded_by_legacy_migrations(self):
        with tempfile.TemporaryDirectory() as tmp:
            conn = v3_shaped_db(Path(tmp) / "v3.db")

            # must not raise
            translate.apply_pending_migrations(conn)

            versions = {r[0] for r in conn.execute("SELECT version FROM schema_version")}
            self.assertNotIn(1, versions, "legacy 001 must not run against a v3 DB")
            self.assertNotIn(2, versions, "legacy 002 must not run against a v3 DB")

            cols = {r[1] for r in conn.execute("PRAGMA table_info(explanation_messages)")}
            self.assertIn("host_id", cols, "v3 explanation_messages was rebuilt into the legacy shape")
            self.assertIn("updated_at", cols)
            self.assertNotIn(
                "explanation_messages_new", {
                    r[0] for r in conn.execute(
                        "SELECT name FROM sqlite_master WHERE type = 'table'"
                    )
                },
                "001's reconstruction scratch table leaked",
            )
            conn.close()

    def test_fresh_legacy_db_gets_001_and_002(self):
        with tempfile.TemporaryDirectory() as tmp:
            conn = sqlite3.connect(Path(tmp) / "legacy.db")
            # the pre-003 shape: INTEGER-PK explanations, no agent_session_id
            conn.executescript(
                """
                CREATE TABLE explanation_threads (
                  id INTEGER PRIMARY KEY,
                  question_id INTEGER NOT NULL,
                  status TEXT NOT NULL DEFAULT 'open',
                  created_at TEXT,
                  closed_at TEXT
                );
                CREATE TABLE explanation_messages (
                  id INTEGER PRIMARY KEY AUTOINCREMENT,
                  thread_id INTEGER NOT NULL REFERENCES explanation_threads(id) ON DELETE CASCADE,
                  role TEXT NOT NULL,
                  author TEXT,
                  content TEXT NOT NULL,
                  created_at TEXT
                );
                """
            )
            conn.commit()

            translate.apply_pending_migrations(conn)

            versions = {r[0] for r in conn.execute("SELECT version FROM schema_version")}
            # 1 and 2 (legacy) plus 4 (Go-owned); 003 is never run from Python.
            self.assertEqual(versions, {1, 2, 4})
            cols = {r[1] for r in conn.execute("PRAGMA table_info(explanation_threads)")}
            self.assertIn("agent_session_id", cols)
            msg_cols = {r[1] for r in conn.execute("PRAGMA table_info(explanation_messages)")}
            self.assertIn("reason_code", msg_cols)
            conn.close()

    def test_migrations_are_idempotent(self):
        with tempfile.TemporaryDirectory() as tmp:
            conn = sqlite3.connect(Path(tmp) / "legacy.db")
            conn.executescript(
                """
                CREATE TABLE explanation_threads (
                  id INTEGER PRIMARY KEY, question_id INTEGER NOT NULL,
                  status TEXT NOT NULL DEFAULT 'open', created_at TEXT, closed_at TEXT
                );
                CREATE TABLE explanation_messages (
                  id INTEGER PRIMARY KEY AUTOINCREMENT,
                  thread_id INTEGER NOT NULL, role TEXT NOT NULL, author TEXT,
                  content TEXT NOT NULL, created_at TEXT
                );
                """
            )
            conn.commit()

            translate.apply_pending_migrations(conn)
            translate.apply_pending_migrations(conn)  # second call must be a no-op

            counts = dict(conn.execute("SELECT version, COUNT(*) FROM schema_version GROUP BY version"))
            self.assertEqual(counts, {1: 1, 2: 1, 4: 1})
            # Regression: with 004 recorded, MAX(version) >= 3 made a later call
            # think the DB was already at v3 and run the destructive 003 on it.
            self.assertNotIn(3, counts)
            idtype = conn.execute(
                "SELECT type FROM pragma_table_info('explanation_messages') WHERE name = 'id'"
            ).fetchone()[0]
            self.assertEqual(idtype, "INTEGER", "003 must not have rebuilt the legacy table")
            conn.close()

    def test_is_v3_db_detection(self):
        with tempfile.TemporaryDirectory() as tmp:
            conn = v3_shaped_db(Path(tmp) / "v3.db")
            self.assertTrue(translate.is_v3_db(conn))
            conn.close()

            legacy = sqlite3.connect(Path(tmp) / "legacy.db")
            legacy.executescript(translate.SCHEMA_VERSION_DDL)
            self.assertFalse(translate.is_v3_db(legacy))
            legacy.close()


if __name__ == "__main__":
    unittest.main(verbosity=2)
