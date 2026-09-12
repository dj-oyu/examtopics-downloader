---
name: exam-translator
description: Autonomously bulk-translates AWS exam questions across one or many SQLite DBs to Japanese. The orchestrator (this skill) makes ALL flag/mode decisions; the lightweight worker subagent only executes literal commands. LLM-client neutral — drive it with Claude Code, Codex, or any agent runtime that can shell out.
---

## Pre-requisites

- One or more SQLite DBs built from ExamTopics at the **project root** (see `tools/README.md` Step 1–2).
- `tools/translate.py` (stdlib-only, run via `uv run` or plain `python3`) providing `status`,
  `bulk-next` (with `--include-incomplete`), `bulk-save`.
- Optional: the Go single-row path, `examtopicsdl translate retranslate -db <DB> -qid <N> -client claude`
  (`go build -o examtopicsdl ./cmd`), which spawns the configured LLM CLI for one row instead of a batch.
- A worker subagent / second agent session able to run shell commands. No specific CLI is required:
  Claude Code subagents (`.claude/agents/`), Codex, or a fresh non-interactive session all work — the
  worker contract is "run the command I give you, verbatim" (see `agents/exam-translator-worker.md`).

CWD = project root throughout.

## Design rule (CRITICAL — do not violate)

**The worker runs a cheaper/faster model and CANNOT be trusted to choose CLI flags.** This skill (the
orchestrator, running on the parent session's stronger model) is solely responsible for:

- Discovering which DBs need work.
- Deciding whether each DB needs a main pass, a mop-up pass, or both.
- Constructing the **exact `bulk-next` command line** the worker should run.
- Embedding that literal command string into the delegation prompt as `<FETCH_CMD>`.

The worker treats `<FETCH_CMD>` as opaque and runs it verbatim. **Never** ask the worker to "decide if
mop-up is needed" or "use the appropriate flags." That is your job.

## Orchestration Workflow

### Step 0 — Discovery (always run first)

```bash
for db in *.db; do
  echo "== $db"
  uv run tools/translate.py -d "$db" status
done
```

Each `status` prints `questions_total` / `questions_translated` / `questions_pending` /
`explanations_translated` / `choices_total` / `choices_translated` / `choices_pending`. A DB with
`*_pending` all zero is done — skip it.

### Step 1 — Plan passes per DB

- `questions_pending > 0` → **main pass needed**.
  - `<FETCH_CMD>` = `uv run tools/translate.py -d <DB> bulk-next --limit 50`
- After the main pass, OR if `questions_pending == 0` but `choices_pending > 0` or
  `explanations_translated < questions_translated` → **mop-up pass needed**.
  - `<FETCH_CMD>` = `uv run tools/translate.py -d <DB> bulk-next --limit 50 --include-incomplete`

Never send `--include-incomplete` during the main pass (wasted cost from the correlated subquery on
still-empty rows).

### Step 2 — Delegate one pass at a time

Use this exact delegation prompt template — fill in `<DB>` and `<FETCH_CMD>` literally, and add nothing
else about flags or mode:

> Translate `<DB>`.
> `<FETCH_CMD>` = `uv run tools/translate.py -d <DB> bulk-next --limit 50 [--include-incomplete if applicable]`
> Run `<FETCH_CMD>` verbatim each iteration. Exit when the fetch returns `[]`.
> Follow the per-row rules in `agents/exam-translator-worker.md`.

### Step 3 — Resume on limit

If the worker halts before `[]` (turn or time budget reached), re-invoke with the **same `<FETCH_CMD>`**.
Don't re-evaluate which pass it is — the same command string keeps converging.

### Step 4 — Verify and move on

Re-run the Step 0 loop (or `status` for the single DB):

- Main-pass DB → expect `questions_pending: 0`. If `choices_pending > 0`, queue a mop-up pass.
- Mop-up-pass DB → expect all `*_pending: 0`. If not, the worker is converging on something that cannot be
  translated automatically (e.g. an unusual choice label) — surface the row IDs to the user instead of
  looping forever.

Then run the cheap audit before considering a DB finished:

```bash
uv run tools/audit_schema.py -d <DB>          # mechanical issues (pending / empty-ja / short-ja / prefixes)
uv run tools/audit_comments.py -d <DB> --min-votes 2   # community-vote disagreement
```

When every DB reports no pending rows, report a one-line summary per DB and stop.

## Translation rules (the worker applies these; keep them in one place)

- AWS service names stay in English (`Amazon S3`, `AWS Lambda`, `Amazon Aurora`).
- 平叙文 / である調 — not 敬体.
- Multi-select (`LENGTH(suggested_answer) > 1`, e.g. `"BD"`): prepend `(複数選択) ` to `question_text_ja`.
- Choices A–E: keep length and tone consistent across the choices of one question.
- `explanation_ja`: 2–3 sentences covering 正解の根拠 + 主要な不正解の根拠, drawing on the `comments`
  column when it adds counter-arguments or community consensus.

## Why flag-decision authority sits with the orchestrator

- The worker model is optimised for throughput on a fixed task, not for choosing between modes.
- A single misjudged flag costs either wasted DB scans (false `--include-incomplete`) or permanently
  invisible orphan rows (missed `--include-incomplete`).
- Centralising the decision here means it is made once per pass on the stronger model, then frozen in the
  literal `<FETCH_CMD>` string — which is also what makes the flow portable across worker runtimes.
