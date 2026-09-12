#!/usr/bin/env python3
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Regression tests for tools/answer_verdict.py.

Run:  python3 tools/test_answer_verdict.py
"""
from __future__ import annotations

import json
import sqlite3
import sys
import tempfile
import unittest
from collections import Counter
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import answer_verdict as av  # noqa: E402


def votes(*pairs: str) -> Counter:
    return Counter(av.canonical(p) for p in pairs)


class VerdictTest(unittest.TestCase):
    def test_canonical_normalises_case_and_order(self):
        self.assertEqual(av.canonical("db"), "BD")
        self.assertEqual(av.canonical(" b d "), "BD")
        self.assertEqual(av.canonical("A"), "A")

    def test_conclusive_consensus_is_settled(self):
        v = av.verdict_for("A", votes("A", "A", "A", "B"))
        self.assertEqual(v["status"], "settled")
        self.assertEqual(v["accepted"], ["A"])
        self.assertEqual(v["total_votes"], 4)

    def test_thin_plurality_is_ambiguous_and_accepts_both(self):
        # 3/7 = 43% < 0.6: the key matches the leader but the community never settled.
        v = av.verdict_for("A", votes("A", "A", "A", "B", "B", "B", "B"))
        self.assertEqual(v["status"], "ambiguous")
        self.assertEqual(v["accepted"], ["A", "B"])
        self.assertIn("結論が出ていない", v["rationale"])

    def test_key_disagreeing_with_consensus_accepts_both(self):
        v = av.verdict_for("A", votes(*(["D"] * 13 + ["A"] * 6 + ["B"])))
        self.assertEqual(v["status"], "ambiguous")
        self.assertEqual(v["accepted"], ["A", "D"])
        community = json.loads(v["community"])
        self.assertEqual(community[0], {"label": "D", "votes": 13, "pct": 65.0})
        self.assertEqual(community[1]["label"], "A")

    def test_multi_select_combinations_are_compared_whole(self):
        # "AB" vs "AC" is a real disagreement, not a two-letter overlap.
        v = av.verdict_for("AB", votes("AB", "AB", "AC", "AC", "AC"))
        self.assertEqual(v["status"], "ambiguous")
        self.assertEqual(v["accepted"], ["AB", "AC"])

    def test_no_votes_is_unknown_and_not_gradeable(self):
        v = av.verdict_for("", Counter())
        self.assertEqual(v["status"], "unknown")
        self.assertEqual(v["accepted"], [])
        self.assertEqual(v["community"], "[]")

    def test_empty_key_with_votes_reports_the_community_only(self):
        v = av.verdict_for("", votes("C", "C", "D"))
        self.assertEqual(v["status"], "ambiguous")
        self.assertEqual(v["accepted"], ["C"])
        self.assertIn("正解キーが空", v["rationale"])

    def test_percentages_sum_to_100(self):
        v = av.verdict_for("A", votes("A", "A", "B", "C"))
        self.assertAlmostEqual(sum(e["pct"] for e in json.loads(v["community"])), 100.0, places=1)


class TallyTest(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.conn = sqlite3.connect(Path(self.dir.name) / "t.db")
        self.conn.executescript(
            """CREATE TABLE questions (id INTEGER PRIMARY KEY, question_number INTEGER,
                                      suggested_answer TEXT, comments TEXT);
               CREATE TABLE discussion (id INTEGER PRIMARY KEY, question_id INTEGER,
                                        idx INTEGER, poster TEXT, content TEXT,
                                        upvote_count INTEGER, posted_at TEXT);"""
        )

    def tearDown(self):
        self.conn.close()
        self.dir.cleanup()

    def test_discussion_is_preferred_and_falls_back_to_comments(self):
        self.conn.execute(
            "INSERT INTO questions VALUES (1, 1, 'A', '[someone] Selected Answer: B')"
        )
        self.conn.execute(
            "INSERT INTO discussion VALUES (1, 1, 0, 'p', 'Selected Answer: B', 0, '')"
        )
        votes, source = av.tally(self.conn, 1, "[someone] Selected Answer: B")
        self.assertEqual(source, "discussion")
        self.assertEqual(dict(votes), {"B": 1})

        votes, source = av.tally(self.conn, 99, "[x] Selected Answer: B [y] Selected Answer: b")
        self.assertEqual(source, "comments")
        self.assertEqual(dict(votes), {"B": 2})

    def test_one_poster_stating_the_same_pick_twice_counts_once(self):
        for i in range(2):
            self.conn.execute(
                "INSERT INTO discussion VALUES (?, 1, ?, 'p', 'Selected Answer: A', 0, '')",
                (i + 1, i),
            )
        self.conn.execute(
            "INSERT INTO discussion VALUES (3, 1, 2, 'other', 'Selected Answer: D', 0, '')"
        )
        votes, _ = av.tally(self.conn, 1, None)
        self.assertEqual(dict(votes), {"A": 1, "D": 1})

    def test_lowercase_and_spacing_variants_are_parsed(self):
        votes, source = av.tally(self.conn, 2, "selected answer: db")
        self.assertEqual(source, "comments")
        self.assertEqual(dict(votes), {"BD": 1})
        # Without posters there is nothing to dedupe on, so two statements count twice.
        votes, _ = av.tally(self.conn, 2, "selected answer: db ... Selected Answer:  DB")
        self.assertEqual(dict(votes), {"BD": 2})


if __name__ == "__main__":
    unittest.main(verbosity=2)
