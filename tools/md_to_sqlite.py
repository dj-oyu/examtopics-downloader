#!/usr/bin/env python3
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Parse examtopics-downloader markdown output into a SQLite database.

Two Markdown layouts are produced by the Go side and both — including a mix of
them inside one file — must parse:

* manual/live scrape (``utils.WriteData`` after ``fetch.GetAllPages``)::

      ## Exam 010-160 topic 1 question 23 discussion
      ...
      [All 010-160 Questions]
      <question text>
      A. choice
      **Answer: D**

* GitHub-cache path (``fetch.ConvertCachedJSON``)::

      ## Examtopics AWS Certified AI Practitioner AIF C01_28 question #1
      <question text>
      Suggested Answer: A 🗳️
      **A:** choice
      **Answer: A**

  The cache layout carries no ``topic``/``Question #:`` header block and no
  ``[All ... Questions]`` marker; ``topic`` is recovered from the question URL
  and ``question_number`` from the ``question #N`` in the title. HOTSPOT
  questions legitimately have an empty ``**Answer: **`` and are kept with NULL
  answers rather than dropped.
"""

from __future__ import annotations

import argparse
import re
import sqlite3
import sys
from pathlib import Path

SEPARATOR = "-" * 40

# --- layout detection -------------------------------------------------------

# manual/live layout
TITLE_RE = re.compile(
    r"^##\s+Exam\s+(.+?)\s+topic\s+(\d+)\s+question\s+(\d+)\s+discussion\s*$",
    re.MULTILINE,
)
# cache layout ("## Examtopics <exam>_<shard> question #N")
CACHE_TITLE_RE = re.compile(
    r"^##\s+Examtopics\s+(.+?)\s+question\s*#\s*(\d+)\s*$",
    re.MULTILINE,
)

QNUM_RE = re.compile(r"Question\s*#:\s*(\d+)")
TOPIC_RE = re.compile(r"Topic\s*#:\s*(\d+)")
ALL_Q_RE = re.compile(r"\[All [^\]]+ Questions\]")
SUGGESTED_RE = re.compile(r"Suggested Answer:\s*([A-Z]+)", re.MULTILINE)
# Choices come as "A. text" (manual) or "**A:** text" (cache — the bold closes
# after the colon). The optional bold markers keep "**Answer: ...**" and
# "**Timestamp: ...**" from matching, since those have no "."/":" directly
# after the single letter.
CHOICE_RE = re.compile(
    r"^\s*(?:\*\*)?([A-Z])(?:\*\*)?\s*[:.]\s*(?:\*\*)?\s*(.+?)\s*(?:\*\*)?\s*$",
    re.MULTILINE,
)
# The answer can legitimately be empty (HOTSPOT questions) — hence ``*``.
ANSWER_RE = re.compile(r"\*\*Answer:\s*([A-Z]*)\s*\*\*")
TIMESTAMP_RE = re.compile(r"\*\*Timestamp:\s*(.+?)\*\*")
URL_RE = re.compile(r"\[View on ExamTopics\]\((.+?)\)")
COMMENTS_RE = re.compile(r"^Comments:\s*(.*)$", re.MULTILINE)

TOPIC_IN_URL_RE = re.compile(r"topic-(\d+)")
# Shard/page suffix callers hand us, in both the raw ("X_5.json?ref=main") and
# the already-transformed ("X_5") form.
SHARD_SUFFIX_RE = re.compile(r"(_\d+)?(\.json)?(\?ref=main)?$")

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


def topic_from_url(url: str | None) -> int:
    """Mirror utils.ExtractTopicNum: the integer after 'topic-' in the URL."""
    if not url:
        return 0
    m = TOPIC_IN_URL_RE.search(url)
    return int(m.group(1)) if m else 0


def exam_from_cache_title(raw_name: str) -> str:
    """Mirror utils.DeriveExamDisplay for the cache layout's title.

    The title carries the transformed cache filename, e.g.
    ``AWS Certified AI Practitioner AIF C01_28`` (page shard suffix kept).
    The SQLite-direct writer stores the shard-less display name
    (``AWS Certified AI Practitioner AIF C01``) — both paths must agree.
    """
    s = raw_name.split("?")[0]
    s = SHARD_SUFFIX_RE.sub("", s)
    s = s.replace("-", " ")
    return " ".join(s.split())


def parse_block(block: str) -> dict | None:
    title_m = TITLE_RE.search(block)
    cache_m = None if title_m is not None else CACHE_TITLE_RE.search(block)
    if title_m is None and cache_m is None:
        return None

    url_m = URL_RE.search(block)
    url = url_m.group(1).strip() if url_m else None
    suggested_m = SUGGESTED_RE.search(block)
    answer_m = ANSWER_RE.search(block)

    if title_m is not None:
        layout = "manual"
        exam = title_m.group(1).strip()
        topic_fallback = int(title_m.group(2))
        qnum_fallback = int(title_m.group(3))
        # Question text starts after the "[All ... Questions]" marker; older /
        # header-only dumps fall back to the end of the "Topic #: N" line.
        marker = ALL_Q_RE.search(block) or TOPIC_RE.search(block)
        text_start = marker.end() if marker else title_m.end()
    else:
        layout = "cache"
        assert cache_m is not None
        exam = exam_from_cache_title(cache_m.group(1))
        topic_fallback = topic_from_url(url)
        qnum_fallback = int(cache_m.group(2))
        text_start = cache_m.end()

    qnum_m = QNUM_RE.search(block)
    topic_m = TOPIC_RE.search(block)
    qnum = int(qnum_m.group(1)) if qnum_m else qnum_fallback
    topic = int(topic_m.group(1)) if topic_m else topic_fallback

    # The question text runs from the layout's anchor up to the Suggested
    # Answer line (canonical) or the answer line; the choice block starts right
    # after whichever of the two is present.
    if suggested_m is not None and suggested_m.start() > text_start:
        text_end = suggested_m.start()
    elif answer_m is not None and answer_m.start() > text_start:
        text_end = answer_m.start()
    else:
        text_end = len(block)

    body_region = block[text_start:text_end]
    first_choice = CHOICE_RE.search(body_region)
    question_text = (
        body_region[: first_choice.start()] if first_choice else body_region
    ).strip()

    if suggested_m is not None:
        choices_start = suggested_m.end()
        suggested_raw = suggested_m.group(1)
    else:
        # No Suggested Answer line: the region after the question text holds the
        # choices, and `**Answer:**` is the only answer signal available.
        choices_start = text_start + (first_choice.start() if first_choice else 0)
        suggested_raw = answer_m.group(1) if answer_m is not None else ""

    choices_end = (
        answer_m.start()
        if answer_m is not None and answer_m.start() > choices_start
        else len(block)
    )
    choices = [
        (label, text.strip().rstrip("*").strip())
        for label, text in CHOICE_RE.findall(block[choices_start:choices_end])
    ]

    # HOTSPOT questions have no answer and no choices, only text + images: keep
    # the row (with NULL answers) instead of silently dropping the question.
    if not question_text and not choices:
        return None

    ts_m = TIMESTAMP_RE.search(block)
    cmt_m = COMMENTS_RE.search(block)

    return {
        "layout": layout,
        "exam": exam,
        "topic": topic,
        "question_number": qnum,
        "question_text": question_text,
        "suggested_answer": leading_letters(suggested_raw) or None,
        "confirmed_answer": (
            leading_letters(answer_m.group(1)) or None if answer_m is not None else None
        ),
        "timestamp": ts_m.group(1).strip() if ts_m else None,
        "url": url,
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
