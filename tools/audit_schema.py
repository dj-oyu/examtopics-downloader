#!/usr/bin/env python3
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Schema integrity audit for the translated SQLite DB.

Runs SQL-level sanity checks. Reports issues as JSON lines so you can
pipe into other tools, plus a human summary at the end.

Checks:
  - pending: rows where _ja columns are still NULL
  - empty:   rows where _ja is "" (vs NULL)
  - short:   question_text_ja shorter than 15% of question_text  (likely over-compressed)
             Japanese is naturally ~30-50% the byte length of English so a 15% threshold
             only fires on outliers worth human review.
  - missing-multiselect-prefix: multi-select rows whose ja text doesn't start with (複数選択)
  - choice-count: questions with != 4 choices (acceptable if multi-select with 5)

Usage:
  uv run tools/audit_schema.py [-d DB] [--fix-prefix]
"""
from __future__ import annotations

import argparse
import json
import sqlite3
import sys
from pathlib import Path

MULTISELECT_PREFIX = "(複数選択)"


def run(db_path: Path, fix_prefix: bool) -> int:
    if not db_path.exists():
        sys.exit(f"DB not found: {db_path}")
    conn = sqlite3.connect(db_path)
    conn.row_factory = sqlite3.Row

    issues = []

    for r in conn.execute(
        "SELECT id, question_number FROM questions "
        "WHERE question_text_ja IS NULL OR explanation_ja IS NULL"
    ):
        issues.append({"check": "pending", "id": r["id"], "qn": r["question_number"]})

    for r in conn.execute(
        "SELECT q.id, q.question_number, c.label FROM questions q "
        "JOIN choices c ON c.question_id = q.id "
        "WHERE c.text_ja IS NULL OR c.text_ja = ''"
    ):
        issues.append(
            {"check": "choice-empty", "id": r["id"], "qn": r["question_number"], "label": r["label"]}
        )

    for r in conn.execute(
        "SELECT id, question_number, question_text_ja, question_text "
        "FROM questions WHERE question_text_ja = '' OR explanation_ja = ''"
    ):
        issues.append(
            {"check": "empty-ja", "id": r["id"], "qn": r["question_number"]}
        )

    for r in conn.execute(
        "SELECT id, question_number, "
        "       LENGTH(question_text_ja) AS ja_len, "
        "       LENGTH(question_text)    AS en_len "
        "FROM questions "
        "WHERE question_text_ja IS NOT NULL "
        "  AND LENGTH(question_text_ja) * 100 < LENGTH(question_text) * 15"
    ):
        issues.append(
            {
                "check": "short-ja",
                "id": r["id"],
                "qn": r["question_number"],
                "ja_len": r["ja_len"],
                "en_len": r["en_len"],
            }
        )

    multi_rows = list(
        conn.execute(
            "SELECT id, question_number, suggested_answer, question_text_ja "
            "FROM questions "
            "WHERE LENGTH(suggested_answer) > 1 "
            "  AND question_text_ja IS NOT NULL"
        )
    )
    fixable = []
    for r in multi_rows:
        if not r["question_text_ja"].startswith(MULTISELECT_PREFIX):
            issues.append(
                {
                    "check": "missing-multiselect-prefix",
                    "id": r["id"],
                    "qn": r["question_number"],
                    "sug": r["suggested_answer"],
                }
            )
            fixable.append(r["id"])

    if fix_prefix and fixable:
        with conn:
            for qid in fixable:
                conn.execute(
                    "UPDATE questions "
                    "SET question_text_ja = ? || ' ' || question_text_ja "
                    "WHERE id = ?",
                    (MULTISELECT_PREFIX, qid),
                )
        print(f"# fixed missing-multiselect-prefix on {len(fixable)} rows", file=sys.stderr)

    counts: dict[int, int] = {}
    for r in conn.execute(
        "SELECT question_id, COUNT(*) AS n FROM choices GROUP BY question_id"
    ):
        counts[r["question_id"]] = r["n"]
    for qid, n in counts.items():
        if n not in (4, 5):
            issues.append({"check": "choice-count", "id": qid, "count": n})

    for issue in issues:
        print(json.dumps(issue, ensure_ascii=False))

    summary: dict[str, int] = {}
    for issue in issues:
        summary[issue["check"]] = summary.get(issue["check"], 0) + 1
    print(
        "\n# summary: " + json.dumps(summary, ensure_ascii=False)
        if summary
        else "\n# summary: all checks passed",
        file=sys.stderr,
    )
    return 1 if issues and not fix_prefix else 0


if __name__ == "__main__":
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("-d", "--db", type=Path, default=Path("soa-c03.db"))
    ap.add_argument("--fix-prefix", action="store_true",
                    help="auto-prepend (複数選択) to multi-select question_text_ja that lack it")
    args = ap.parse_args()
    sys.exit(run(args.db, args.fix_prefix))
