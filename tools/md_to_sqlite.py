#!/usr/bin/env python3
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Parse examtopics-downloader markdown output into a SQLite database."""

from __future__ import annotations

import argparse
import re
import sqlite3
import sys
from pathlib import Path

SEPARATOR = "-" * 40

TITLE_RE = re.compile(
    r"^##\s+Exam\s+(.+?)\s+topic\s+(\d+)\s+question\s+(\d+)\s+discussion",
    re.MULTILINE,
)
QNUM_RE = re.compile(r"Question\s*#:\s*(\d+)")
TOPIC_RE = re.compile(r"Topic\s*#:\s*(\d+)")
ALL_Q_RE = re.compile(r"\[All [^\]]+ Questions\]")
SUGGESTED_RE = re.compile(r"Suggested Answer:\s*([A-Z]+)")
CHOICE_RE = re.compile(r"^([A-Z])\.\s+(.+?)\s*$", re.MULTILINE)
ANSWER_RE = re.compile(r"\*\*Answer:\s*([A-Z]+)\*\*")
TIMESTAMP_RE = re.compile(r"\*\*Timestamp:\s*(.+?)\*\*")
URL_RE = re.compile(r"\[View on ExamTopics\]\((.+?)\)")
COMMENTS_RE = re.compile(r"^Comments:\s*(.*)$", re.MULTILINE)

SCHEMA = """
CREATE TABLE IF NOT EXISTS questions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    exam TEXT NOT NULL,
    topic INTEGER NOT NULL,
    question_number INTEGER NOT NULL,
    question_text TEXT NOT NULL,
    question_text_ja TEXT,
    suggested_answer TEXT,
    confirmed_answer TEXT,
    explanation_ja TEXT,
    timestamp TEXT,
    url TEXT UNIQUE,
    comments TEXT,
    -- new columns mirrored from the Go writer (internal/sqlite/schema.go).
    -- Python side never writes these; Go writer populates on cache-direct path.
    exam_id INTEGER,
    is_mc INTEGER,
    answer_description TEXT,
    question_images TEXT,
    answer_images TEXT,
    content_hash TEXT,
    imported_at TEXT DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS choices (
    question_id INTEGER NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
    label TEXT NOT NULL,
    text TEXT NOT NULL,
    text_ja TEXT,
    PRIMARY KEY (question_id, label)
);

-- mirrored from internal/sqlite/schema.go; Python side does not populate.
CREATE TABLE IF NOT EXISTS discussion (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    question_id INTEGER NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
    idx INTEGER NOT NULL,
    poster TEXT,
    content TEXT NOT NULL,
    upvote_count INTEGER,
    posted_at TEXT,
    UNIQUE(question_id, idx)
);

CREATE INDEX IF NOT EXISTS idx_questions_exam ON questions(exam);
CREATE INDEX IF NOT EXISTS idx_questions_topic ON questions(topic, question_number);
CREATE INDEX IF NOT EXISTS idx_questions_url ON questions(url);
CREATE INDEX IF NOT EXISTS idx_discussion_qid ON discussion(question_id);
"""


def leading_letters(value: str) -> str:
    """Extract a bare answer letter set from either 'BD' or 'B. choice text'."""
    m = re.match(r"\s*([A-Z]+)", value or "")
    return m.group(1) if m else ""


def parse_block(block: str) -> dict | None:
    title_m = TITLE_RE.search(block)
    if not title_m:
        return None

    exam = title_m.group(1).strip()
    topic_from_title = int(title_m.group(2))
    qnum_from_title = int(title_m.group(3))

    qnum_m = QNUM_RE.search(block)
    topic_m = TOPIC_RE.search(block)
    qnum = int(qnum_m.group(1)) if qnum_m else qnum_from_title
    topic = int(topic_m.group(1)) if topic_m else topic_from_title

    all_q = ALL_Q_RE.search(block)
    suggested_m = SUGGESTED_RE.search(block)
    answer_m = ANSWER_RE.search(block)
    if not all_q or (suggested_m is None and answer_m is None):
        return None

    # Canonical layout (current Go writer): question text, `Suggested Answer:
    # BD 🗳️`, choices A–E, `**Answer: BD**`. Writers that predate the voted-
    # answers path emit no Suggested Answer line, so the region after the
    # question text then also contains the choice lines — split on the first
    # choice line in that case.
    text_end = (
        suggested_m.start()
        if suggested_m is not None
        else (answer_m.start() if answer_m is not None else len(block))
    )
    body_region = block[all_q.end() : text_end]

    first_choice = CHOICE_RE.search(body_region)
    question_text = (
        body_region[: first_choice.start()] if first_choice else body_region
    ).strip()

    if suggested_m is not None:
        choices_start = suggested_m.end()
        suggested_answer = suggested_m.group(1)
    else:
        choices_start = all_q.end() + (first_choice.start() if first_choice else 0)
        # Fall back to `**Answer:**`, which is the only answer signal present.
        suggested_answer = leading_letters(answer_m.group(1))

    choices_end = answer_m.start() if answer_m is not None else len(block)
    choices_region = block[choices_start:choices_end]
    choices = [(label, text.strip()) for label, text in CHOICE_RE.findall(choices_region)]

    ts_m = TIMESTAMP_RE.search(block)
    url_m = URL_RE.search(block)
    cmt_m = COMMENTS_RE.search(block)

    return {
        "exam": exam,
        "topic": topic,
        "question_number": qnum,
        "question_text": question_text,
        "suggested_answer": suggested_answer,
        "confirmed_answer": leading_letters(answer_m.group(1)) if answer_m else None,
        "timestamp": ts_m.group(1).strip() if ts_m else None,
        "url": url_m.group(1).strip() if url_m else None,
        "comments": cmt_m.group(1).strip() if cmt_m else None,
        "choices": choices,
    }


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("input", type=Path, help="examtopics .md file")
    ap.add_argument("-o", "--output", type=Path, default=Path("examtopics.db"))
    ap.add_argument(
        "--append",
        action="store_true",
        help="keep existing DB and INSERT OR IGNORE on url",
    )
    args = ap.parse_args()

    text = args.input.read_text(encoding="utf-8")
    blocks = text.split(SEPARATOR)

    parsed: list[dict] = []
    skipped = 0
    for blk in blocks:
        rec = parse_block(blk)
        if rec is None:
            if blk.strip():
                skipped += 1
            continue
        parsed.append(rec)

    if not parsed:
        print(f"No questions parsed from {args.input}", file=sys.stderr)
        return 1

    if args.output.exists() and not args.append:
        args.output.unlink()

    conn = sqlite3.connect(args.output)
    conn.executescript(SCHEMA)

    insert_sql = (
        "INSERT OR IGNORE INTO questions"
        if args.append
        else "INSERT INTO questions"
    )
    with conn:
        inserted = 0
        for rec in parsed:
            cur = conn.execute(
                f"""{insert_sql}(exam, topic, question_number, question_text,
                       suggested_answer, confirmed_answer, timestamp, url, comments)
                   VALUES(?,?,?,?,?,?,?,?,?)""",
                (
                    rec["exam"],
                    rec["topic"],
                    rec["question_number"],
                    rec["question_text"],
                    rec["suggested_answer"],
                    rec["confirmed_answer"],
                    rec["timestamp"],
                    rec["url"],
                    rec["comments"],
                ),
            )
            if cur.lastrowid and cur.rowcount:
                qid = cur.lastrowid
                conn.executemany(
                    "INSERT OR IGNORE INTO choices(question_id, label, text) VALUES(?,?,?)",
                    [(qid, label, txt) for label, txt in rec["choices"]],
                )
                inserted += 1

    print(
        f"Parsed {len(parsed)} questions ({skipped} blocks skipped), "
        f"{inserted} inserted -> {args.output}"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
