#!/usr/bin/env python3
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Regression tests for md_to_sqlite.py.

Run:  uv run tools/test_md_to_sqlite.py

Covers the two Markdown layouts the parser must accept:

* canonical — question text, `Suggested Answer: BD 🗳️`, choices, `**Answer: BD**`
  (what the Go writer emits as of the voted-answers fix)
* legacy/no-suggested-line — the region after the question text also holds the
  choice lines, so the parser has to find the choice block itself and fall back
  to `**Answer:**` for suggested_answer.

The second layout is not hypothetical: `go run ./cmd/main.go` in manual-scrape
mode produced exactly that until the `Suggested Answer:` line was restored, and
it made `md_to_sqlite.py` exit 1 with "No questions parsed".
"""

from __future__ import annotations

import sqlite3
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import md_to_sqlite  # noqa: E402

SEPARATOR = "-" * 40

# Layout emitted by the current Go writer (Suggested Answer line present).
CANONICAL_BLOCK = """## Exam 010-160 topic 1 question 1 discussion

Actual exam question from

LPI's

010-160

Question #: 1
Topic #: 1

[All 010-160 Questions]

Which of the following DNS record types hold an IP address? (Choose two.)
Suggested Answer: BD 🗳️ 

A. A

B. CNAME

C. MX

D. AAAA

E. SOA

**Answer: BD**

**Timestamp: Feb. 26, 2020, 2:58 p.m.**

[View on ExamTopics](https://www.examtopics.com/discussions/lpi/view/1-exam-010-160-topic-1-question-1-discussion/)

Comments: alice Selected Answer: BD upvoted 3 times

{sep}

""".format(sep=SEPARATOR)

# Layout from a run without the Suggested Answer line: the choice lines sit in
# the same region as the question text.
NO_SUGGESTED_BLOCK = """## Exam 010-160 topic 1 question 23 discussion

Actual exam question from

LPI's

010-160

Question #: 23
Topic #: 1

[All 010-160 Questions]

Which of the following commands will search for the file foo.txt under the directory /home? 

A. search /home -file foo.txt

B. search /home foo. txt

C. find /home - file foo.txt

D. find /home -name foo.txt

E. find /home foo.txt

**Answer: D**

**Timestamp: Feb. 26, 2020, 2:58 p.m.**

[View on ExamTopics](https://www.examtopics.com/discussions/lpi/view/2-exam-010-160-topic-1-question-23-discussion/)

Comments: bob Selected Answer: D upvoted 1 times

{sep}

""".format(sep=SEPARATOR)


class ParseBlockTest(unittest.TestCase):
    def test_canonical_layout(self):
        rec = md_to_sqlite.parse_block(CANONICAL_BLOCK)

        self.assertIsNotNone(rec)
        assert rec is not None
        self.assertEqual(rec["exam"], "010-160")
        self.assertEqual((rec["topic"], rec["question_number"]), (1, 1))
        self.assertEqual(rec["suggested_answer"], "BD")
        self.assertEqual(rec["confirmed_answer"], "BD")
        # The question text must stop before the first choice line.
        self.assertTrue(rec["question_text"].startswith("Which of the following DNS"))
        self.assertNotIn("A. A", rec["question_text"])
        self.assertEqual([label for label, _ in rec["choices"]], ["A", "B", "C", "D", "E"])
        self.assertEqual(rec["choices"][3], ("D", "AAAA"))
        self.assertIn("upvoted 3 times", rec["comments"] or "")

    def test_layout_without_suggested_answer_line(self):
        rec = md_to_sqlite.parse_block(NO_SUGGESTED_BLOCK)

        self.assertIsNotNone(rec)
        assert rec is not None
        self.assertEqual(rec["question_number"], 23)
        # Falls back to **Answer:** — the only answer signal in this layout.
        self.assertEqual(rec["suggested_answer"], "D")
        self.assertEqual(rec["confirmed_answer"], "D")
        self.assertTrue(rec["question_text"].startswith("Which of the following commands"))
        self.assertNotIn("find /home -name foo.txt", rec["question_text"])
        self.assertEqual(len(rec["choices"]), 5)
        self.assertEqual(rec["choices"][0], ("A", "search /home -file foo.txt"))

    def test_block_without_title_is_rejected(self):
        self.assertIsNone(md_to_sqlite.parse_block("no question header here"))


class EndToEndTest(unittest.TestCase):
    def test_parses_both_layouts_into_one_db(self):
        with tempfile.TemporaryDirectory() as tmp:
            md = Path(tmp) / "mixed.md"
            md.write_text(CANONICAL_BLOCK + NO_SUGGESTED_BLOCK, encoding="utf-8")
            db = Path(tmp) / "mixed.db"

            rc = run_main([str(md), "-o", str(db)])
            self.assertEqual(rc, 0)

            conn = sqlite3.connect(db)
            rows = conn.execute(
                "SELECT question_number, suggested_answer, confirmed_answer, question_text"
                " FROM questions ORDER BY question_number"
            ).fetchall()
            self.assertEqual(len(rows), 2)
            self.assertEqual(rows[0][1], "BD")
            self.assertEqual(rows[1][1], "D")

            choice_counts = conn.execute(
                "SELECT q.question_number, COUNT(c.label) FROM questions q"
                " JOIN choices c ON c.question_id = q.id GROUP BY q.id ORDER BY q.question_number"
            ).fetchall()
            self.assertEqual(choice_counts, [(1, 5), (23, 5)])
            conn.close()

    def test_no_questions_is_a_loud_failure(self):
        with tempfile.TemporaryDirectory() as tmp:
            md = Path(tmp) / "empty.md"
            md.write_text("# Exam Topics Questions\n\n@thatonecodes\n", encoding="utf-8")

            rc = run_main([str(md), "-o", str(Path(tmp) / "empty.db")])
            self.assertEqual(rc, 1, "a header-only dump must not exit 0 (silent-zero is the old failure mode)")


def run_main(argv: list[str]) -> int:
    """Invoke md_to_sqlite.main() with argv patched in."""
    old = sys.argv
    sys.argv = ["md_to_sqlite.py", *argv]
    try:
        return md_to_sqlite.main()
    finally:
        sys.argv = old


if __name__ == "__main__":
    unittest.main(verbosity=2)
