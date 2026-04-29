#!/usr/bin/env python3
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""SQLite translation worktable for AI agent CLIs.

Designed to be driven by any AI coding agent (Claude Code, Codex CLI,
Gemini CLI, Aider, etc.) that can shell out. The agent reads source text
via `show`/`next` and writes translations back via `save` (stdin JSON).
The script owns the DB; the agent owns the translation.

Workflow for an agent:
  1. uv run tools/translate.py -d soa-c03.db status
  2. uv run tools/translate.py -d soa-c03.db next         # get a row
  3. produce JSON with `question_text_ja`, `explanation_ja`, `choices_ja`
  4. echo '<json>' | uv run tools/translate.py -d soa-c03.db save <id>
  5. loop until status reports 0 pending.

The `save` payload schema (omit any field you do not want to update):
{
  "question_text_ja": "...",
  "explanation_ja":   "...",
  "choices_ja": { "A": "...", "B": "...", "C": "...", "D": "..." }
}
"""

from __future__ import annotations

import argparse
import json
import re
import sqlite3
import sys
from pathlib import Path

DEFAULT_DB = Path("examtopics.db")
MIGRATIONS_DIR = Path(__file__).resolve().parent.parent / "migrations"
VALID_REASON_CODES = ("comprehension", "spec", "ambiguous", "translation")


EXPLANATION_SCHEMA = """
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
"""


SCHEMA_VERSION_DDL = """
CREATE TABLE IF NOT EXISTS schema_version (
  version INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
"""


_MIG_RE = re.compile(r"^(\d+)_(.*)\.sql$")


def apply_pending_migrations(conn: sqlite3.Connection) -> None:
    conn.executescript(SCHEMA_VERSION_DDL)
    applied = {row[0] for row in conn.execute("SELECT version FROM schema_version")}
    if not MIGRATIONS_DIR.is_dir():
        return
    files = sorted(
        f for f in MIGRATIONS_DIR.iterdir()
        if f.is_file() and _MIG_RE.match(f.name)
    )
    for f in files:
        m = _MIG_RE.match(f.name)
        if not m:
            continue
        version = int(m.group(1))
        if version in applied:
            continue
        sql = f.read_text(encoding="utf-8")
        conn.execute("PRAGMA foreign_keys = OFF")
        try:
            with conn:
                conn.executescript(sql)
                conn.execute(
                    "INSERT INTO schema_version(version, name) VALUES (?, ?)",
                    (version, f.stem),
                )
        except Exception:
            conn.execute("PRAGMA foreign_keys = ON")
            raise
        conn.execute("PRAGMA foreign_keys = ON")


def open_db(path: Path) -> sqlite3.Connection:
    if not path.exists():
        sys.exit(f"DB not found: {path}")
    conn = sqlite3.connect(path)
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA foreign_keys = ON")
    conn.executescript(EXPLANATION_SCHEMA)
    apply_pending_migrations(conn)
    return conn


def fetch_payload(conn: sqlite3.Connection, qid: int) -> dict:
    q = conn.execute("SELECT * FROM questions WHERE id = ?", (qid,)).fetchone()
    if not q:
        sys.exit(f"no question with id={qid}")
    choices = conn.execute(
        "SELECT label, text, text_ja FROM choices WHERE question_id = ? ORDER BY label",
        (qid,),
    ).fetchall()
    return {
        "id": q["id"],
        "exam": q["exam"],
        "topic": q["topic"],
        "question_number": q["question_number"],
        "url": q["url"],
        "suggested_answer": q["suggested_answer"],
        "confirmed_answer": q["confirmed_answer"],
        "question_text": q["question_text"],
        "choices": [{"label": c["label"], "text": c["text"]} for c in choices],
        "comments": q["comments"],
        "existing_ja": {
            "question_text_ja": q["question_text_ja"],
            "explanation_ja": q["explanation_ja"],
            "choices_ja": {c["label"]: c["text_ja"] for c in choices if c["text_ja"]},
        },
    }


def cmd_status(conn: sqlite3.Connection, _args) -> None:
    total = conn.execute("SELECT COUNT(*) FROM questions").fetchone()[0]
    q_done = conn.execute(
        "SELECT COUNT(*) FROM questions WHERE question_text_ja IS NOT NULL"
    ).fetchone()[0]
    expl_done = conn.execute(
        "SELECT COUNT(*) FROM questions WHERE explanation_ja IS NOT NULL"
    ).fetchone()[0]
    c_total = conn.execute("SELECT COUNT(*) FROM choices").fetchone()[0]
    c_done = conn.execute(
        "SELECT COUNT(*) FROM choices WHERE text_ja IS NOT NULL"
    ).fetchone()[0]
    print(
        json.dumps(
            {
                "questions_total": total,
                "questions_translated": q_done,
                "questions_pending": total - q_done,
                "explanations_translated": expl_done,
                "choices_total": c_total,
                "choices_translated": c_done,
                "choices_pending": c_total - c_done,
            },
            ensure_ascii=False,
            indent=2,
        )
    )


def cmd_list(conn: sqlite3.Connection, args) -> None:
    rows = conn.execute(
        "SELECT id, exam, topic, question_number FROM questions "
        "WHERE question_text_ja IS NULL ORDER BY id LIMIT ?",
        (args.limit,),
    ).fetchall()
    for r in rows:
        print(json.dumps(dict(r), ensure_ascii=False))


def cmd_show(conn: sqlite3.Connection, args) -> None:
    print(json.dumps(fetch_payload(conn, args.id), ensure_ascii=False, indent=2))


def cmd_next(conn: sqlite3.Connection, _args) -> None:
    row = conn.execute(
        "SELECT id FROM questions WHERE question_text_ja IS NULL ORDER BY id LIMIT 1"
    ).fetchone()
    if not row:
        print(json.dumps({"pending": 0}, ensure_ascii=False))
        return
    print(json.dumps(fetch_payload(conn, row["id"]), ensure_ascii=False, indent=2))


def cmd_save(conn: sqlite3.Connection, args) -> None:
    # Force reading from stdin as UTF-8
    try:
        raw = sys.stdin.buffer.read().decode("utf-8")
    except UnicodeDecodeError:
        # Fallback if somehow not utf-8, though we prefer utf-8
        sys.exit("save: stdin must be UTF-8 encoded")
    
    if raw.startswith("\ufeff"):
        raw = raw.lstrip("\ufeff")
    if not raw.strip():
        sys.exit("save: stdin is empty (pipe a JSON object)")
    try:
        payload = json.loads(raw)
    except json.JSONDecodeError as e:
        sys.exit(f"save: invalid JSON ({e})")
    if not isinstance(payload, dict):
        sys.exit("save: top-level JSON must be an object")

    qid = args.id
    if not conn.execute("SELECT 1 FROM questions WHERE id = ?", (qid,)).fetchone():
        sys.exit(f"save: no question with id={qid}")

    updated: list[str] = []
    with conn:
        sets, vals = [], []
        if "question_text_ja" in payload:
            sets.append("question_text_ja = ?")
            vals.append(payload["question_text_ja"])
            updated.append("question_text_ja")
        if "explanation_ja" in payload:
            sets.append("explanation_ja = ?")
            vals.append(payload["explanation_ja"])
            updated.append("explanation_ja")
        if sets:
            vals.append(qid)
            conn.execute(
                f"UPDATE questions SET {', '.join(sets)} WHERE id = ?", vals
            )

        choices_ja = payload.get("choices_ja") or {}
        if not isinstance(choices_ja, dict):
            sys.exit("save: choices_ja must be an object {label: text}")
        for label, text_ja in choices_ja.items():
            cur = conn.execute(
                "UPDATE choices SET text_ja = ? WHERE question_id = ? AND label = ?",
                (text_ja, qid, label),
            )
            if cur.rowcount:
                updated.append(f"choice:{label}")

    print(json.dumps({"id": qid, "updated": updated}, ensure_ascii=False))


def cmd_unsave(conn: sqlite3.Connection, args) -> None:
    qid = args.id
    with conn:
        conn.execute(
            "UPDATE questions SET question_text_ja = NULL, explanation_ja = NULL "
            "WHERE id = ?",
            (qid,),
        )
        conn.execute("UPDATE choices SET text_ja = NULL WHERE question_id = ?", (qid,))
    print(json.dumps({"id": qid, "cleared": True}, ensure_ascii=False))


def cmd_bulk_next(conn: sqlite3.Connection, args) -> None:
    if args.include_incomplete:
        sql = (
            "SELECT id FROM questions q "
            "WHERE q.question_text_ja IS NULL "
            "   OR q.explanation_ja IS NULL "
            "   OR EXISTS (SELECT 1 FROM choices c "
            "              WHERE c.question_id = q.id AND c.text_ja IS NULL) "
            "ORDER BY id LIMIT ?"
        )
    else:
        sql = (
            "SELECT id FROM questions WHERE question_text_ja IS NULL "
            "ORDER BY id LIMIT ?"
        )
    rows = conn.execute(sql, (args.limit,)).fetchall()
    if not rows:
        print(json.dumps([], ensure_ascii=False))
        return

    payloads = [fetch_payload(conn, r["id"]) for r in rows]
    print(json.dumps(payloads, ensure_ascii=False, indent=2))


def cmd_bulk_save(conn: sqlite3.Connection, _args) -> None:
    # Force reading from stdin as UTF-8
    try:
        raw = sys.stdin.buffer.read().decode("utf-8")
    except UnicodeDecodeError:
        sys.exit("bulk-save: stdin must be UTF-8 encoded")
    
    if raw.startswith("\ufeff"):
        raw = raw.lstrip("\ufeff")
    if not raw.strip():
        sys.exit("bulk-save: stdin is empty (pipe a JSON array)")
    try:
        payloads = json.loads(raw)
    except json.JSONDecodeError as e:
        sys.exit(f"bulk-save: invalid JSON ({e})")
    
    if not isinstance(payloads, list):
        sys.exit("bulk-save: top-level JSON must be an array of objects")

    results = []
    with conn:
        for payload in payloads:
            qid = payload.get("id")
            if not qid:
                results.append({"error": "missing id", "payload": payload})
                continue
            
            updated = []
            sets, vals = [], []
            if "question_text_ja" in payload:
                sets.append("question_text_ja = ?")
                vals.append(payload["question_text_ja"])
                updated.append("question_text_ja")
            if "explanation_ja" in payload:
                sets.append("explanation_ja = ?")
                vals.append(payload["explanation_ja"])
                updated.append("explanation_ja")
            
            if sets:
                vals.append(qid)
                conn.execute(f"UPDATE questions SET {', '.join(sets)} WHERE id = ?", vals)

            choices_ja = payload.get("choices_ja") or {}
            for label, text_ja in choices_ja.items():
                cur = conn.execute(
                    "UPDATE choices SET text_ja = ? WHERE question_id = ? AND label = ?",
                    (text_ja, qid, label),
                )
                if cur.rowcount:
                    updated.append(f"choice:{label}")
            results.append({"id": qid, "updated": updated})

    print(json.dumps(results, ensure_ascii=False))


def fetch_thread(conn: sqlite3.Connection, tid: int) -> dict | None:
    t = conn.execute(
        "SELECT * FROM explanation_threads WHERE id = ?", (tid,)
    ).fetchone()
    if not t:
        return None
    q = conn.execute(
        "SELECT * FROM questions WHERE id = ?", (t["question_id"],)
    ).fetchone()
    choices = conn.execute(
        "SELECT label, text, text_ja FROM choices WHERE question_id = ? ORDER BY label",
        (q["id"],),
    ).fetchall()
    messages = conn.execute(
        "SELECT id, role, author, content, reason_code, citations, "
        "translation_diff, created_at FROM explanation_messages "
        "WHERE thread_id = ? ORDER BY id ASC",
        (tid,),
    ).fetchall()
    return {
        "thread_id": t["id"],
        "status": t["status"],
        "created_at": t["created_at"],
        "closed_at": t["closed_at"],
        "question": {
            "id": q["id"],
            "exam": q["exam"],
            "topic": q["topic"],
            "question_number": q["question_number"],
            "url": q["url"],
            "suggested_answer": q["suggested_answer"],
            "question_text": q["question_text"],
            "question_text_ja": q["question_text_ja"],
            "explanation_ja": q["explanation_ja"],
            "comments": q["comments"],
            "choices": [
                {
                    "label": c["label"],
                    "text": c["text"],
                    "text_ja": c["text_ja"],
                }
                for c in choices
            ],
        },
        "messages": [dict(m) for m in messages],
    }


def cmd_threads(conn: sqlite3.Connection, args) -> None:
    awaiting = args.awaiting
    sql = """
        SELECT t.id, t.question_id, t.status, t.created_at,
               q.question_number, q.exam,
               lm.role AS last_role, lm.content AS last_content,
               lm.created_at AS last_at,
               (SELECT COUNT(*) FROM explanation_messages
                  WHERE thread_id = t.id) AS message_count
          FROM explanation_threads t
          JOIN questions q ON q.id = t.question_id
     LEFT JOIN (
            SELECT thread_id, role, content, created_at,
                   ROW_NUMBER() OVER (PARTITION BY thread_id ORDER BY id DESC) AS rn
              FROM explanation_messages
        ) lm ON lm.thread_id = t.id AND lm.rn = 1
         WHERE t.status = 'open'
    """
    params: tuple = ()
    if awaiting in ("agent", "user"):
        # awaiting=agent → agent should act next → last message was from user
        opposite = "user" if awaiting == "agent" else "agent"
        sql += " AND lm.role = ?"
        params = (opposite,)
    sql += " ORDER BY (lm.created_at IS NULL), lm.created_at ASC, t.id ASC"
    rows = conn.execute(sql, params).fetchall()
    for r in rows:
        print(json.dumps(dict(r), ensure_ascii=False))


def cmd_next_thread(conn: sqlite3.Connection, _args) -> None:
    """Return the oldest open thread where the last message is from the user."""
    row = conn.execute(
        """
        SELECT t.id FROM explanation_threads t
        LEFT JOIN (
          SELECT thread_id, role, created_at,
                 ROW_NUMBER() OVER (PARTITION BY thread_id ORDER BY id DESC) AS rn
            FROM explanation_messages
        ) lm ON lm.thread_id = t.id AND lm.rn = 1
        WHERE t.status = 'open' AND lm.role = 'user'
        ORDER BY lm.created_at ASC, t.id ASC LIMIT 1
        """
    ).fetchone()
    if not row:
        print(json.dumps({"awaiting_agent": 0}, ensure_ascii=False))
        return
    payload = fetch_thread(conn, row["id"])
    print(json.dumps(payload, ensure_ascii=False, indent=2))


def cmd_show_thread(conn: sqlite3.Connection, args) -> None:
    payload = fetch_thread(conn, args.id)
    if not payload:
        sys.exit(f"no thread with id={args.id}")
    print(json.dumps(payload, ensure_ascii=False, indent=2))


def _read_stdin_json() -> dict:
    try:
        raw = sys.stdin.buffer.read().decode("utf-8")
    except UnicodeDecodeError:
        sys.exit("stdin must be UTF-8 encoded")
    if raw.startswith("﻿"):
        raw = raw.lstrip("﻿")
    if not raw.strip():
        sys.exit("stdin is empty (pipe a JSON object)")
    try:
        payload = json.loads(raw)
    except json.JSONDecodeError as e:
        sys.exit(f"invalid JSON ({e})")
    if not isinstance(payload, dict):
        sys.exit("top-level JSON must be an object")
    return payload


def _validate_citations(citations) -> str | None:
    if citations is None:
        return None
    if not isinstance(citations, list):
        return "citations must be an array of {url, title?} objects"
    for c in citations:
        if not isinstance(c, dict) or not isinstance(c.get("url"), str):
            return "each citation must be an object with a string 'url' field"
        if "title" in c and not isinstance(c["title"], (str, type(None))):
            return "citation 'title' must be a string when present"
    return None


def _validate_reason(
    reason_code, citations, translation_fix
) -> str | None:
    if reason_code is None:
        return None
    if reason_code not in VALID_REASON_CODES:
        return (
            "reason_code must be one of "
            f"{'|'.join(VALID_REASON_CODES)}, got {reason_code!r}"
        )
    if reason_code in ("spec", "ambiguous"):
        if not isinstance(citations, list) or not citations:
            return f"reason_code={reason_code} requires non-empty 'citations'"
        if not any(
            isinstance(c, dict)
            and "docs.aws.amazon.com" in (c.get("url") or "")
            for c in citations
        ):
            return (
                f"reason_code={reason_code} requires at least one citation "
                "whose url contains 'docs.aws.amazon.com' (AWS Docs MCP grounding)"
            )
    if reason_code == "translation":
        if not isinstance(translation_fix, dict) or not translation_fix:
            return "reason_code=translation requires non-empty 'translation_fix'"
        keys = ("question_text_ja", "explanation_ja", "choices_ja")
        if not any(k in translation_fix for k in keys):
            return (
                "translation_fix must include at least one of "
                f"{'|'.join(keys)}"
            )
        if "choices_ja" in translation_fix and not isinstance(
            translation_fix["choices_ja"], dict
        ):
            return "translation_fix.choices_ja must be an object {label: text}"
    return None


def _apply_translation_fix(
    conn: sqlite3.Connection, qid: int, fix: dict
) -> str:
    """Snapshot existing _ja values, apply fix, return JSON-encoded diff."""
    old_q = conn.execute(
        "SELECT question_text_ja, explanation_ja FROM questions WHERE id = ?",
        (qid,),
    ).fetchone()
    old_choices = conn.execute(
        "SELECT label, text_ja FROM choices WHERE question_id = ? ORDER BY label",
        (qid,),
    ).fetchall()
    before = {
        "question_text_ja": old_q["question_text_ja"] if old_q else None,
        "explanation_ja": old_q["explanation_ja"] if old_q else None,
        "choices_ja": {c["label"]: c["text_ja"] for c in old_choices},
    }

    after: dict = {}
    sets, vals = [], []
    if "question_text_ja" in fix:
        sets.append("question_text_ja = ?")
        vals.append(fix["question_text_ja"])
        after["question_text_ja"] = fix["question_text_ja"]
    if "explanation_ja" in fix:
        sets.append("explanation_ja = ?")
        vals.append(fix["explanation_ja"])
        after["explanation_ja"] = fix["explanation_ja"]
    if sets:
        vals.append(qid)
        conn.execute(
            f"UPDATE questions SET {', '.join(sets)} WHERE id = ?", vals
        )

    after_choices: dict = {}
    for label, txt in (fix.get("choices_ja") or {}).items():
        conn.execute(
            "UPDATE choices SET text_ja = ? WHERE question_id = ? AND label = ?",
            (txt, qid, label),
        )
        after_choices[label] = txt
    if after_choices:
        after["choices_ja"] = after_choices

    return json.dumps({"before": before, "after": after}, ensure_ascii=False)


def cmd_reply(conn: sqlite3.Connection, args) -> None:
    """
    Append an agent message to a thread. stdin payload:
      {
        "content": "agent reply markdown/text",
        "author": "claude-code",            // optional
        "resolve": true,                     // optional
        "reason_code": "comprehension"       // optional. one of:
                       | "spec"              //   - spec/ambiguous => citations required,
                       | "ambiguous"         //     at least one docs.aws.amazon.com URL
                       | "translation",      //   - translation => translation_fix required
        "citations": [{"url":"https://docs.aws.amazon.com/...","title":"..."}, ...],
        "translation_fix": {                 // required iff reason_code == "translation"
          "question_text_ja": "...",         //   any subset of these keys
          "explanation_ja":   "...",
          "choices_ja": { "A": "...", "B": "..." }
        },
        "explanation_ja": "..."              // legacy: standalone explanation_ja update
                                             //   (only honored when reason_code != "translation")
      }
    """
    payload = _read_stdin_json()
    tid = args.id
    t = conn.execute(
        "SELECT id, question_id, status FROM explanation_threads WHERE id = ?",
        (tid,),
    ).fetchone()
    if not t:
        sys.exit(f"reply: no thread id={tid}")
    if t["status"] != "open":
        sys.exit(f"reply: thread #{tid} is {t['status']} (not open)")

    content = (payload.get("content") or "").strip()
    author = payload.get("author")
    resolve = bool(payload.get("resolve"))
    reason_code = payload.get("reason_code")
    citations = payload.get("citations")
    translation_fix = payload.get("translation_fix")
    new_expl = payload.get("explanation_ja")

    err = _validate_citations(citations)
    if err:
        sys.exit(f"reply: {err}")
    err = _validate_reason(reason_code, citations, translation_fix)
    if err:
        sys.exit(f"reply: {err}")

    if reason_code is not None and not content:
        sys.exit(
            "reply: 'content' is required (non-empty) when reason_code is set"
        )
    if reason_code is None and not content and not new_expl:
        sys.exit(
            "reply: payload must include 'content' or 'explanation_ja' "
            "(or set 'reason_code' for the structured path)"
        )

    qid = t["question_id"]
    actions: list[str] = []
    translation_diff_json: str | None = None
    citations_json = (
        json.dumps(citations, ensure_ascii=False) if citations else None
    )

    with conn:
        if reason_code == "translation":
            translation_diff_json = _apply_translation_fix(
                conn, qid, translation_fix
            )
            actions.append("applied_translation_fix")

        if content:
            conn.execute(
                "INSERT INTO explanation_messages"
                "(thread_id, role, author, content, reason_code, "
                "citations, translation_diff) "
                "VALUES(?, 'agent', ?, ?, ?, ?, ?)",
                (
                    tid,
                    author,
                    content,
                    reason_code,
                    citations_json,
                    translation_diff_json,
                ),
            )
            actions.append("appended_message")

        if (
            isinstance(new_expl, str)
            and new_expl.strip()
            and reason_code != "translation"
        ):
            conn.execute(
                "UPDATE questions SET explanation_ja = ? WHERE id = ?",
                (new_expl, qid),
            )
            actions.append("updated_explanation_ja")

        if resolve:
            conn.execute(
                "UPDATE explanation_threads SET status = 'resolved', "
                "closed_at = CURRENT_TIMESTAMP WHERE id = ?",
                (tid,),
            )
            actions.append("resolved")

    print(
        json.dumps(
            {"thread_id": tid, "question_id": qid, "actions": actions},
            ensure_ascii=False,
        )
    )


def main() -> int:
    ap = argparse.ArgumentParser(
        description=__doc__,
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    ap.add_argument(
        "-d", "--db", type=Path, default=DEFAULT_DB, help="path to sqlite DB"
    )
    sub = ap.add_subparsers(dest="cmd", required=True)

    sub.add_parser("status", help="JSON summary of translation progress")
    p_list = sub.add_parser("list", help="list pending question IDs (JSONL)")
    p_list.add_argument("--limit", type=int, default=200)

    p_show = sub.add_parser("show", help="dump one row as JSON")
    p_show.add_argument("id", type=int)

    sub.add_parser("next", help="dump the next pending row as JSON")

    p_bnext = sub.add_parser("bulk-next", help="dump multiple pending rows as a JSON array")
    p_bnext.add_argument("--limit", type=int, default=20)
    p_bnext.add_argument(
        "--include-incomplete",
        action="store_true",
        help="also return rows where question_text_ja is set but "
        "explanation_ja or any choice.text_ja is still NULL "
        "(mop-up pass for orphan rows)",
    )

    p_save = sub.add_parser("save", help="read JSON from stdin, write _ja fields")
    p_save.add_argument("id", type=int)

    sub.add_parser("bulk-save", help="read JSON array from stdin, write _ja fields for many rows")

    p_unsave = sub.add_parser("unsave", help="clear all _ja fields for a row")
    p_unsave.add_argument("id", type=int)

    p_threads = sub.add_parser(
        "threads", help="list open explanation threads as JSONL"
    )
    p_threads.add_argument(
        "--awaiting",
        choices=["agent", "user", "any"],
        default="any",
        help="filter by which side is expected to reply next",
    )

    sub.add_parser(
        "next-thread",
        help="dump the oldest thread that's awaiting an agent reply",
    )

    p_show_t = sub.add_parser("show-thread", help="dump one thread as JSON")
    p_show_t.add_argument("id", type=int)

    p_reply = sub.add_parser(
        "reply",
        help="read JSON {content, author?, resolve?, reason_code?, citations?, "
        "translation_fix?, explanation_ja?} from stdin and append an agent "
        "message. spec/ambiguous => citations with docs.aws.amazon.com URL "
        "required; translation => translation_fix required (snapshots "
        "before/after into translation_diff).",
    )
    p_reply.add_argument("id", type=int)

    args = ap.parse_args()
    conn = open_db(args.db)
    handler = {
        "status": cmd_status,
        "list": cmd_list,
        "show": cmd_show,
        "next": cmd_next,
        "bulk-next": cmd_bulk_next,
        "save": cmd_save,
        "bulk-save": cmd_bulk_save,
        "unsave": cmd_unsave,
        "threads": cmd_threads,
        "next-thread": cmd_next_thread,
        "show-thread": cmd_show_thread,
        "reply": cmd_reply,
    }[args.cmd]
    handler(conn, args)
    return 0


if __name__ == "__main__":
    sys.exit(main())
