#!/usr/bin/env python3
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Audit `suggested_answer` against the community-vote consensus parsed from comments.

ExamTopics commenters write 'Selected Answer: X' (or sometimes lowercase) before
their reasoning. This script tallies those votes per question and flags
divergence from the stored `suggested_answer`.

Output: JSONL — one line per question with a vote breakdown. By default only
suspicious rows are printed (majority disagrees, or no votes parsed).
Use --all to print every row.

Usage:
  uv run tools/audit_comments.py [-d DB] [--all] [--min-votes N]
"""
from __future__ import annotations

import argparse
import json
import re
import sqlite3
import sys
from collections import Counter
from pathlib import Path

VOTE_RE = re.compile(r"selected\s*answer\s*:\s*([A-Z]+)", re.IGNORECASE)


def majority(counter: Counter[str]) -> tuple[str | None, int]:
    if not counter:
        return None, 0
    top, n = counter.most_common(1)[0]
    return top, n


def run(db_path: Path, show_all: bool, min_votes: int) -> int:
    if not db_path.exists():
        sys.exit(f"DB not found: {db_path}")
    conn = sqlite3.connect(db_path)
    conn.row_factory = sqlite3.Row

    flagged = 0
    seen = 0
    for q in conn.execute(
        "SELECT id, question_number, suggested_answer, comments FROM questions ORDER BY id"
    ):
        seen += 1
        votes_raw = VOTE_RE.findall(q["comments"] or "")
        votes = Counter(v.upper() for v in votes_raw)
        top, n = majority(votes)
        agreement = (
            None if top is None else (top == q["suggested_answer"])
        )

        suspicious = (
            top is None  # no votes at all
            or (agreement is False and n >= min_votes)  # majority disagrees with enough votes
        )
        if not show_all and not suspicious:
            continue
        if suspicious:
            flagged += 1
        print(
            json.dumps(
                {
                    "id": q["id"],
                    "qn": q["question_number"],
                    "suggested": q["suggested_answer"],
                    "majority": top,
                    "votes": dict(votes.most_common()),
                    "total_votes": sum(votes.values()),
                    "agree": agreement,
                    "suspicious": suspicious,
                },
                ensure_ascii=False,
            )
        )

    print(
        f"\n# audited {seen} questions, flagged {flagged} suspicious",
        file=sys.stderr,
    )
    return 0


if __name__ == "__main__":
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("-d", "--db", type=Path, default=Path("soa-c03.db"))
    ap.add_argument("--all", action="store_true", help="print every row, not just suspicious")
    ap.add_argument(
        "--min-votes",
        type=int,
        default=2,
        help="minimum vote count for the majority to be considered conclusive (default 2)",
    )
    args = ap.parse_args()
    sys.exit(run(args.db, args.all, args.min_votes))
