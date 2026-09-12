#!/usr/bin/env python3
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Compute an honest answer verdict per question and store it in `answer_verdicts`.

ExamTopics answers are community votes, not a ground truth: on some questions the
community never settled (two camps, or a plurality too thin to call), and on
others there is no key at all (image/HOTSPOT items). This tool marks those rows
instead of letting a single key masquerade as the truth:

  settled   - consensus agrees with `suggested_answer` and is conclusive
  ambiguous - contested; every answer in `accepted` counts as correct, and the
              vote split in `community` is shown to the learner after they answer
  unknown   - no votes and no usable key; nothing is gradeable

Default is a dry run that prints JSONL for the rows it would change. Pass
`--apply` to write `answer_verdicts` (the table is created from migration 004).

Usage:
  uv run tools/answer_verdict.py [-d DB] [--apply] [--all]
                                [--threshold 0.6] [--max-accepted 2]
"""
from __future__ import annotations

import argparse
import json
import re
import sqlite3
import sys
from collections import Counter
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from translate import apply_pending_migrations  # noqa: E402  (same-dir sibling tool)

VOTE_RE = re.compile(r"selected\s*answer\s*:\s*([A-Z]+)", re.IGNORECASE)

# A plurality below this share of the votes is not conclusive, even when it
# happens to match suggested_answer.
DEFAULT_THRESHOLD = 0.6


def canonical(letters: str) -> str:
    """Order-insensitive, case-insensitive form: 'db' -> 'BD', ' b d ' -> 'BD'."""
    return "".join(sorted(set(re.sub(r"\s+", "", letters).upper())))


def tally(conn: sqlite3.Connection, qid: int, comments: str | None) -> tuple[Counter, str]:
    """Community votes per answer combination, deduped per poster.

    `discussion` is the structured source (one row per comment, with the poster
    name); the flat `comments` text is the fallback for DBs built before it
    existed. A poster who states the same pick twice counts once.
    """
    votes: Counter[str] = Counter()
    seen: set[tuple[str, str]] = set()
    rows = conn.execute(
        "SELECT poster, content FROM discussion WHERE question_id = ? ORDER BY idx",
        (qid,),
    ).fetchall()
    if rows:
        for poster, content in rows:
            for m in VOTE_RE.findall(content or ""):
                key = (poster or "", canonical(m))
                if key in seen:
                    continue
                seen.add(key)
                votes[canonical(m)] += 1
        return votes, "discussion"
    for m in VOTE_RE.findall(comments or ""):
        votes[canonical(m)] += 1
    return votes, "comments"


def community_json(votes: Counter, total: int) -> str:
    return json.dumps(
        [
            {"label": label, "votes": n, "pct": round(100.0 * n / total, 1)}
            for label, n in votes.most_common()
        ],
        ensure_ascii=False,
    )


def verdict_for(
    suggested: str | None,
    votes: Counter,
    threshold: float = DEFAULT_THRESHOLD,
    max_accepted: int = 2,
) -> dict:
    """Decide one question's status / accepted set / rationale from its votes."""
    total = sum(votes.values())
    key = canonical(suggested or "")
    if total == 0:
        return {
            "status": "unknown",
            "accepted": [],
            "community": "[]",
            "total_votes": 0,
            "rationale": (
                "コミュニティ投票が無く、正解キーも空のため判定できない"
                if not key
                else f"コミュニティ投票が無い。正解キーは {key} のみ"
            ),
        }
    ranked = votes.most_common()
    leader, leader_votes = ranked[0]
    share = leader_votes / total
    contenders = [label for label, n in ranked if n / total >= max(threshold, 1e-9)]
    if not contenders:
        contenders = [leader]

    if not key:
        # No key at all: the community is the only signal we have, and saying so
        # is more honest than inventing a single correct answer.
        return {
            "status": "ambiguous",
            "accepted": [leader],
            "community": community_json(votes, total),
            "total_votes": total,
            "rationale": (
                f"正解キーが空。コミュニティ多数派は {leader} "
                f"({leader_votes}/{total} = {share * 100:.0f}%) のみが根拠"
            ),
        }

    if leader == key and share >= threshold:
        return {
            "status": "settled",
            "accepted": [key],
            "community": community_json(votes, total),
            "total_votes": total,
            "rationale": (
                f"コミュニティ多数派 {leader} ({leader_votes}/{total} = "
                f"{share * 100:.0f}%) が正解キーと一致"
            ),
        }

    # Contested: keep the key and the community leader, both acceptable. They can
    # be combinations for multi-select ("AB" vs "AC"), so compare whole combos.
    # Both sides of the disagreement always fit; any further contender only fills
    # whatever slots --max-accepted leaves over.
    others = [label for label in contenders if label not in (key, leader)]
    accepted: list[str] = [key] + ([leader] if leader and leader != key else [])
    accepted += others[: max(0, max_accepted - len(accepted))]
    if share >= threshold:
        why = (
            f"コミュニティ多数派 {leader} ({leader_votes}/{total} = {share * 100:.0f}%) "
            f"が正解キー {key} と食い違う"
        )
    else:
        why = (
            f"最多でも {leader} が {leader_votes}/{total} = {share * 100:.0f}% に留まり"
            f"結論が出ていない（正解キーは {key}）"
        )
    return {
        "status": "ambiguous",
        "accepted": accepted,
        "community": community_json(votes, total),
        "total_votes": total,
        "rationale": f"{why}。{' と '.join(accepted)} のいずれも正答として扱う。",
    }


def run(db_path: Path, apply: bool, show_all: bool, threshold: float, max_accepted: int) -> int:
    if not db_path.exists():
        sys.exit(f"DB not found: {db_path}")
    conn = sqlite3.connect(db_path)
    conn.row_factory = sqlite3.Row
    if apply:
        apply_pending_migrations(conn)

    counts: Counter[str] = Counter()
    changed = 0
    for q in conn.execute(
        "SELECT id, question_number, suggested_answer, comments FROM questions ORDER BY id"
    ):
        votes, source = tally(conn, q["id"], q["comments"])
        v = verdict_for(q["suggested_answer"], votes, threshold, max_accepted)
        counts[v["status"]] += 1
        row = {
            "id": q["id"],
            "qn": q["question_number"],
            "suggested": q["suggested_answer"] or "",
            "status": v["status"],
            "accepted": v["accepted"],
            "votes": dict(votes.most_common()),
            "total_votes": v["total_votes"],
            "rationale": v["rationale"],
        }
        if apply:
            conn.execute(
                """INSERT INTO answer_verdicts
                     (question_id, status, accepted, community, total_votes, source, rationale)
                   VALUES (?,?,?,?,?,?,?)
                   ON CONFLICT(question_id) DO UPDATE SET
                     status=excluded.status, accepted=excluded.accepted,
                     community=excluded.community, total_votes=excluded.total_votes,
                     source=excluded.source, rationale=excluded.rationale,
                     computed_at=CURRENT_TIMESTAMP""",
                (
                    q["id"], v["status"], json.dumps(v["accepted"]), v["community"],
                    v["total_votes"], source, v["rationale"],
                ),
            )
            changed += 1
        if show_all or v["status"] != "settled":
            print(json.dumps(row, ensure_ascii=False))

    if apply:
        conn.commit()
    print(
        f"\n# {'wrote' if apply else 'would write'} {changed} verdicts: "
        + ", ".join(f"{k}={counts[k]}" for k in ("settled", "ambiguous", "unknown") if counts[k]),
        file=sys.stderr,
    )
    if not apply:
        print("# dry run — re-run with --apply to store them", file=sys.stderr)
    return 0


if __name__ == "__main__":
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("-d", "--db", type=Path, default=Path("examtopics.db"))
    ap.add_argument("--apply", action="store_true", help="write the answer_verdicts table")
    ap.add_argument("--all", action="store_true", help="print every row, not just contested/unknown ones")
    ap.add_argument(
        "--threshold",
        type=float,
        default=DEFAULT_THRESHOLD,
        help=f"share of votes a plurality needs to be conclusive (default {DEFAULT_THRESHOLD})",
    )
    ap.add_argument(
        "--max-accepted",
        type=int,
        default=2,
        help="how many answers may count as correct on a contested question (default 2)",
    )
    args = ap.parse_args()
    sys.exit(run(args.db, args.apply, args.all, args.threshold, args.max_accepted))
