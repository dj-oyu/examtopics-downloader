#!/usr/bin/env python3
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Regression tests for md_to_sqlite.py.

Run:  uv run tools/test_md_to_sqlite.py

The Go side emits two Markdown layouts and can mix them inside one file:

* manual/live scrape — ``## Exam 010-160 topic 1 question 23 discussion``, with an
  ``[All ... Questions]`` marker, ``A. choice`` lines and no Suggested Answer line
  (before the voted-answers fix) or a full one (after it).
* GitHub-cache path — ``## Examtopics <exam>_<shard> question #N``, no marker,
  ``**A:** choice`` lines, ``Suggested Answer:`` present.

Both must parse. The cache layout is not hypothetical: until it was handled,
``go run ./cmd/main.go`` on the cache path (the recommended one, with a PAT)
produced a dump that ``md_to_sqlite.py`` refused with "No questions parsed" —
154 question blocks in, 0 rows out.
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

# --- fixtures ---------------------------------------------------------------

# manual/live layout, with the Suggested Answer line (current writer)
MANUAL_BLOCK = """## Exam 010-160 topic 1 question 1 discussion

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

# manual/live layout from a run that produced no Suggested Answer line
MANUAL_NO_SUGGESTED_BLOCK = """## Exam 010-160 topic 1 question 23 discussion

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

# GitHub-cache layout (verbatim shape from a real AIF-C01 cache dump)
CACHE_BLOCK = """## Examtopics AWS Certified AI Practitioner AIF C01_28 question #1

What are tokens in the context of generative AI models?

Suggested Answer: A 🗳️ 

**A:** Tokens are the basic units of input and output that a generative AI model operates on.

**B:** Tokens are the mathematical representations of words or concepts used in generative AI models.

**C:** Tokens are the pre-trained weights of a generative AI model.

**D:** Tokens are the specific prompts or instructions given to a generative AI model.



**Answer: A**

**Timestamp: 2024-11-20 06:11:00**

[View on ExamTopics](https://www.examtopics.com/discussions/amazon/view/151661-exam-aws-certified-ai-practitioner-aif-c01-topic-1-question/)

Comments: [PHD_CHENG] Selected Answer: A A is correct [Jessiii] Selected Answer: A smallest units

{sep}

""".format(sep=SEPARATOR)

# cache layout, HOTSPOT question: no answer, no choices, only text + image
CACHE_HOTSPOT_BLOCK = """## Examtopics AWS Certified AI Practitioner AIF C01_13 question #32

HOTSPOT
-

A company wants more customized responses to its generative AI models’ prompts.

Select the correct customization methodology from the following list for each use case.

//IMG//

https://img.examtopics.com/aws-certified-ai-practitioner-aif-c01/image11.png



**Answer: **

**Timestamp: 2025-04-07 09:00:00**

[View on ExamTopics](https://www.examtopics.com/discussions/amazon/view/160000-exam-aws-certified-ai-practitioner-aif-c01-topic-1-question/)

Comments: [someone] This is an image-based question

{sep}

""".format(sep=SEPARATOR)


class LayoutDetectionTest(unittest.TestCase):
    def test_manual_layout(self):
        rec = md_to_sqlite.parse_block(MANUAL_BLOCK)

        self.assertIsNotNone(rec)
        assert rec is not None
        self.assertEqual(rec["layout"], "manual")
        self.assertEqual(rec["exam"], "010-160")
        self.assertEqual((rec["topic"], rec["question_number"]), (1, 1))
        self.assertEqual(rec["suggested_answer"], "BD")
        self.assertEqual(rec["confirmed_answer"], "BD")
        self.assertTrue(rec["question_text"].startswith("Which of the following DNS"))
        self.assertNotIn("A. A", rec["question_text"])
        self.assertEqual([label for label, _ in rec["choices"]], ["A", "B", "C", "D", "E"])
        self.assertEqual(rec["choices"][3], ("D", "AAAA"))

    def test_manual_layout_without_suggested_answer_line(self):
        rec = md_to_sqlite.parse_block(MANUAL_NO_SUGGESTED_BLOCK)

        self.assertIsNotNone(rec)
        assert rec is not None
        self.assertEqual(rec["question_number"], 23)
        # Falls back to **Answer:** — the only answer signal in this layout.
        self.assertEqual(rec["suggested_answer"], "D")
        self.assertEqual(rec["confirmed_answer"], "D")
        self.assertTrue(rec["question_text"].startswith("Which of the following commands"))
        self.assertNotIn("find /home -name foo.txt", rec["question_text"])
        self.assertEqual(len(rec["choices"]), 5)

    def test_cache_layout(self):
        rec = md_to_sqlite.parse_block(CACHE_BLOCK)

        self.assertIsNotNone(rec)
        assert rec is not None
        self.assertEqual(rec["layout"], "cache")
        # Shard suffix ("_28") and the "Examtopics " wrapper are stripped so the
        # value matches what the SQLite-direct writer stores.
        self.assertEqual(rec["exam"], "AWS Certified AI Practitioner AIF C01")
        # topic comes from the URL, question number from the title.
        self.assertEqual(rec["topic"], 1)
        self.assertEqual(rec["question_number"], 1)
        self.assertEqual(rec["suggested_answer"], "A")
        self.assertEqual(rec["confirmed_answer"], "A")
        self.assertTrue(rec["question_text"].startswith("What are tokens"))
        self.assertNotIn("**A:**", rec["question_text"])
        self.assertEqual([label for label, _ in rec["choices"]], ["A", "B", "C", "D"])
        self.assertTrue(rec["choices"][0][1].startswith("Tokens are the basic units"))
        self.assertNotIn("**", rec["choices"][0][1])

    def test_cache_hotspot_without_answer_is_kept(self):
        rec = md_to_sqlite.parse_block(CACHE_HOTSPOT_BLOCK)

        self.assertIsNotNone(rec, "image-based questions must not be dropped")
        assert rec is not None
        self.assertEqual(rec["question_number"], 32)
        self.assertIsNone(rec["suggested_answer"])
        self.assertIsNone(rec["confirmed_answer"])
        self.assertEqual(rec["choices"], [])
        self.assertTrue(rec["question_text"].startswith("HOTSPOT"))
        self.assertIn("image11.png", rec["question_text"])

    def test_block_without_title_is_rejected(self):
        self.assertIsNone(md_to_sqlite.parse_block("no question header here"))


class ExamNameDerivationTest(unittest.TestCase):
    def test_strips_shard_and_json_suffixes(self):
        cases = {
            "AWS Certified AI Practitioner AIF C01_28": "AWS Certified AI Practitioner AIF C01",
            "AWS Certified AI Practitioner AIF C01_28.json?ref=main": "AWS Certified AI Practitioner AIF C01",
            "AWS-Certified-Developer---Associate-DVA-C02_5": "AWS Certified Developer Associate DVA C02",
            "AWS Certified Developer Associate DVA C02": "AWS Certified Developer Associate DVA C02",
        }
        for raw, want in cases.items():
            self.assertEqual(md_to_sqlite.exam_from_cache_title(raw), want, raw)


class EndToEndTest(unittest.TestCase):
    def test_parses_a_mixed_file(self):
        with tempfile.TemporaryDirectory() as tmp:
            md = Path(tmp) / "mixed.md"
            md.write_text(MANUAL_BLOCK + CACHE_BLOCK + CACHE_HOTSPOT_BLOCK, encoding="utf-8")
            db = Path(tmp) / "mixed.db"

            self.assertEqual(run_main([str(md), "-o", str(db)]), 0)

            conn = sqlite3.connect(db)
            rows = conn.execute(
                "SELECT exam, topic, question_number, suggested_answer, confirmed_answer"
                " FROM questions ORDER BY id"
            ).fetchall()
            self.assertEqual(rows, [
                ("010-160", 1, 1, "BD", "BD"),
                ("AWS Certified AI Practitioner AIF C01", 1, 1, "A", "A"),
                ("AWS Certified AI Practitioner AIF C01", 1, 32, None, None),
            ])
            choice_counts = conn.execute(
                "SELECT q.question_number, COUNT(c.label) FROM questions q"
                " JOIN choices c ON c.question_id = q.id GROUP BY q.id ORDER BY q.id"
            ).fetchall()
            self.assertEqual(choice_counts, [(1, 5), (1, 4)])
            conn.close()

    def test_cache_shaped_file_parses_every_block(self):
        with tempfile.TemporaryDirectory() as tmp:
            md = Path(tmp) / "cache.md"
            md.write_text(CACHE_BLOCK + CACHE_HOTSPOT_BLOCK, encoding="utf-8")
            db = Path(tmp) / "cache.db"

            self.assertEqual(run_main([str(md), "-o", str(db)]), 0)
            conn = sqlite3.connect(db)
            self.assertEqual(conn.execute("SELECT COUNT(*) FROM questions").fetchone()[0], 2)
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
