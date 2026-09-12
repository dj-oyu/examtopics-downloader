---
name: exam-translator
description: Autonomously bulk-translates AWS exam questions across one or many SQLite DBs to Japanese. The orchestrator (this skill) makes ALL flag/mode decisions; the lightweight worker subagent just executes literal commands.
---

## Pre-requisites
- One or more SQLite DBs built from ExamTopics at the **project root**.
- `tools/translate.py` providing `status`, `bulk-next` (with `--include-incomplete`), `bulk-save`.
- `.gemini/agents/exam-translator-worker.md` — the dedicated subagent (`max_turns: 200`, `model: gemini-3.1-flash-lite-preview`).
- `scripts/scan_dbs.py` — globs `*.db` and reports per-DB pending counts.
- `scripts/batch_helper.py` — UTF-8-safe pipe into `bulk-save`.
- `scripts/evaluate_skill.py` — KPI benchmark.

CWD = project root throughout.

## Design rule (CRITICAL — do not violate)

**The worker uses a lightweight model and CANNOT be trusted to choose CLI flags.** This skill (the orchestrator, running on the parent session's stronger model) is solely responsible for:
- Discovering which DBs need work.
- Deciding whether each DB needs a main pass, a mop-up pass, or both.
- Constructing the **exact `bulk-next` command line** the worker should run.
- Embedding that literal command string into the delegation prompt as `<FETCH_CMD>`.

The worker treats `<FETCH_CMD>` as opaque and runs it verbatim. **Never** ask the worker to "decide if mop-up is needed" or "use the appropriate flags." That is your job.

## Orchestration Workflow

### Step 0 — Discovery (always run first)
```
python .gemini/skills/exam-translator/scripts/scan_dbs.py
```
The output is a JSON array with `questions_pending`, `explanations_pending`, `choices_pending`, `fully_done` per DB. `fully_done: true` DBs are pre-filtered out.

### Step 1 — Plan passes per DB
For each DB in the scan result, decide which passes are needed (you decide, not the worker):

- `questions_pending > 0` → **main pass needed**.
  - `<FETCH_CMD>` = `uv run tools/translate.py -d <DB> bulk-next --limit 50`
- After main pass, OR if `questions_pending == 0` from the start but `explanations_pending > 0` or `choices_pending > 0` → **mop-up pass needed**.
  - `<FETCH_CMD>` = `uv run tools/translate.py -d <DB> bulk-next --limit 50 --include-incomplete`

Never send `--include-incomplete` during the main pass (wasted cost from the correlated subquery on still-empty rows).

### Step 2 — Delegate one pass at a time
Use this exact delegation prompt template — fill in `<DB>` and `<FETCH_CMD>` literally, and add nothing else about flags or mode:

> Translate `<DB>`.
> `<FETCH_CMD>` = `uv run tools/translate.py -d <DB> bulk-next --limit 50 [--include-incomplete if applicable]`
> Run `<FETCH_CMD>` verbatim each iteration. Exit when the fetch returns `[]`.

### Step 3 — Resume on limit
If the worker halts before `[]` (turn or time budget reached), re-invoke with the **same `<FETCH_CMD>`**. Don't re-evaluate which pass it is — the same command string keeps converging.

### Step 4 — Verify and move on
After each pass, re-run `scan_dbs.py`:
- Main-pass DB → expect `questions_pending: 0`. If `explanations_pending > 0` or `choices_pending > 0`, queue a mop-up pass for that DB.
- Mop-up-pass DB → expect `fully_done: true`. If not, the worker is converging on something the LLM can't translate (e.g. unusual choice label) — surface the row IDs to the user instead of looping forever.

When `scan_dbs.py` returns `[]`, all DBs are done. Report a one-line summary per DB.

## Why flag-decision authority sits with the orchestrator
- Worker model (flash-lite) is optimized for throughput on a fixed task, not for choosing between modes.
- A single misjudged flag costs either wasted DB scans (false `--include-incomplete`) or permanently invisible orphan rows (missed `--include-incomplete`).
- Centralizing the decision here means it's made once per pass on the stronger model, then frozen in the literal `<FETCH_CMD>` string.

## Benchmarking
`python .gemini/skills/exam-translator/scripts/evaluate_skill.py` measures one skill activation against `test-exams.db`.
