# AGENTS.md

Guide for AI coding agents (Claude Code, Codex CLI, Gemini CLI, Aider, etc.) working in
this fork of `examtopics-downloader`. Covers the post-processing pipeline added on top
of the upstream Go scraper, the schema invariants you must respect, and known
upstream bugs to compensate for.

## Repository layout

| Path | Purpose | Owned by |
|------|---------|----------|
| `cmd/`, `internal/`, `tests/` | Upstream Go scraper: examtopics → Markdown | upstream |
| `tools/` | Python (uv-run) post-processing: SQLite + translation + audits | this fork |
| `web/` | Bun + Hono SSR Web UI for studying | this fork |
| `examples/` | Sample upstream output (kept as-is) | upstream |
| `*.db`, root `*.md`, root `*.json` | Scraped data — **gitignored, not redistributable** | local only |

## Pipeline at a glance

```
[examtopics.com]
      | go run ./cmd/main.go -p amazon -s <exam-id> -c -save-links -o <exam>.md
      v
   <exam>.md
      | uv run tools/md_to_sqlite.py <exam>.md -o <exam>.db
      v
   <exam>.db   (questions, choices, _ja columns reserved)
      | uv run tools/translate.py -d <exam>.db {next, save, ...}
      | uv run tools/audit_*.py -d <exam>.db
      v
   <exam>.db   (translations, attempts, explanation threads)
      | bun run dev   (web/)
      v
   Browser study UI + agent dialogue at http://localhost:3000
```

## Tools (uv-run, stdlib-only Python)

All tool scripts use [PEP 723 inline metadata](https://peps.python.org/pep-0723/) so
`uv run tools/<script>.py ...` resolves dependencies (none) and executes without a venv.
This is the canonical interface — **do not embed any LLM SDK**. The contract is
"any agent that can shell out and produce JSON."

### `tools/md_to_sqlite.py` — Markdown dump → SQLite

```bash
uv run tools/md_to_sqlite.py <input>.md -o <out>.db [--append]
```

Schema created:

```sql
questions(id, exam, topic, question_number,
          question_text, question_text_ja,            -- _ja columns nullable, fill via translate.py
          suggested_answer, confirmed_answer, explanation_ja,
          timestamp, url UNIQUE, comments)
choices(question_id, label, text, text_ja,
        PRIMARY KEY(question_id, label))
```

`--append` deduplicates by `url` (`INSERT OR IGNORE`) so you can merge multiple exam
dumps into one DB if desired (this fork uses one DB per exam by default — see below).

### `tools/translate.py` — translation worktable + thread bus

Subcommands all accept `-d <path>.db` and emit JSON:

| Subcommand | Purpose |
|------------|---------|
| `status` | progress summary (questions/choices translated) |
| `list [--limit N]` | JSONL of pending question IDs |
| `show <id>` | full row JSON (English + existing translations + comments) |
| `next` | full payload of next pending question |
| `save <id>` | reads JSON `{question_text_ja, explanation_ja, choices_ja: {A: ...}}` from stdin |
| `bulk-save` | reads a JSON array, writes many rows in one transaction |
| `unsave <id>` | clear all `_ja` fields for a question |
| `threads [--awaiting agent\|user\|any]` | JSONL of open explanation threads |
| `next-thread` | full payload of oldest thread awaiting an agent reply |
| `show-thread <id>` | full payload of one thread |
| `reply <thread_id>` | reads JSON `{content, author?, resolve?, explanation_ja?}` from stdin |

#### Translation loop pattern

```
status -> next -> (translate internally) -> echo '{...}' | save <id> -> repeat
```

Save payload schema (omit any field you do not want to update):

```json
{
  "question_text_ja": "...",
  "explanation_ja":   "...",
  "choices_ja": { "A": "...", "B": "...", "C": "...", "D": "..." }
}
```

Translation guidelines that have worked well in practice:

- AWS service names stay in English (`Amazon S3`, `AWS Lambda`).
- Question/choice translations use plain declarative Japanese (である調), not honorifics.
- Choices are translated with consistent length and tone across A–E.
- `explanation_ja` is 2–3 sentences: "正解と理由 + 主要な不正解の根拠".
- Multi-select questions (where `suggested_answer.length > 1`) are prefixed with `(複数選択)` in `question_text_ja`.

### `tools/audit_schema.py` — SQL-level integrity audit

```bash
uv run tools/audit_schema.py -d <db> [--fix-prefix]
```

Emits JSONL of issues. Checks:

- `pending` — `_ja` columns still NULL
- `choice-empty` — choice rows with NULL/empty `text_ja`
- `empty-ja` — empty-string translations (vs NULL)
- `short-ja` — `question_text_ja` < 15% of `question_text` length (likely
  over-compression). 15% threshold is calibrated: Japanese is naturally ~30–50%
  the byte length of English, so 15% only fires on outliers.
- `missing-multiselect-prefix` — multi-select rows whose translation lacks
  `(複数選択)`. Auto-fixable with `--fix-prefix`.
- `choice-count` — questions with abnormal number of choices.

### `tools/audit_comments.py` — community-vote disagreement audit

```bash
uv run tools/audit_comments.py -d <db> [--min-votes N] [--all]
```

Parses `Selected Answer: X` patterns from the `comments` column, tallies per question,
and flags rows where the community-vote majority disagrees with `suggested_answer`.
Use `--min-votes 1` to surface every disagreement. Useful for sniffing out questions
where the upstream "Suggested Answer" looks wrong.

### Layered validation flow (recommended)

```
1. audit_schema  -> catch mechanical issues (cheap)
2. audit_comments -> narrow suspect set (cheap, ~3-5 of 75 typically)
3. AWS Docs MCP sub-agent on suspects + multi-select rows -> cite official docs
```

The third step uses an MCP-equipped sub-agent (e.g. AWS Knowledge MCP Server in
Claude Code, or any MCP runtime) with `aws___search_documentation` and
`aws___read_documentation`. Feed it the rows flagged by `audit_comments.py` plus
all multi-select rows; ask it to verify each claim against AWS docs and produce a
report with `CONFIRMED | LIKELY-WRONG | AMBIGUOUS` verdicts and citations. Apply
fixes via `translate.py save` for the small N that need adjustment.

## Web UI (`web/`, Bun + Hono SSR)

```bash
cd web && bun install && bun run dev    # http://localhost:3000
```

Routes:

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/` | Discover all `*.db` files at repo root, list per-exam stats |
| GET | `/e/:slug/q?filter=all\|wrong\|unanswered` | Question list for one exam |
| GET | `/e/:slug/q/:id` | Question detail + open thread |
| POST | `/e/:slug/q/:id/attempt` | Record an attempt, render result |
| POST | `/e/:slug/q/:id/threads` | Create a new explanation thread |
| POST | `/e/:slug/threads/:tid/{reply,resolve,dismiss}` | Thread operations |
| GET | `/e/:slug/review` | Per-exam wrong-answer queue |
| GET | `/requests` | **Cross-DB** open thread list |

The Web UI and `tools/translate.py` use the **same SQLite file as a shared bus**.
Whatever the agent writes via `reply` is immediately visible in the UI on the next
page load, and vice versa.

## Multi-turn explanation threads

Schema (created on first open by web/ or translate.py via `CREATE TABLE IF NOT EXISTS`):

```sql
explanation_threads (
  id INTEGER PRIMARY KEY,
  question_id INTEGER NOT NULL,
  status TEXT CHECK (status IN ('open','resolved','dismissed')) DEFAULT 'open',
  created_at TEXT, closed_at TEXT
)
explanation_messages (
  id INTEGER PRIMARY KEY,
  thread_id INTEGER REFERENCES explanation_threads(id) ON DELETE CASCADE,
  role TEXT CHECK (role IN ('user','agent')),
  author TEXT,                       -- 'web' | 'claude-code' | any agent identifier
  content TEXT,
  created_at TEXT
)
```

Key design points:

- **One question : N threads : M messages.** A user can spawn a fresh thread on the
  same question after a previous one is closed.
- **Awaiting state is derived, not stored.** `last_message.role == 'user'` ⇒ agent
  must reply; `'agent'` ⇒ user must reply. This avoids drift between a flag and the
  actual conversation state.
- **`role` ∈ {'user', 'agent'} only.** Specific agent identity lives in the `author`
  column. This keeps "whose turn" simple while preserving attribution.
- **At most one open thread per question.** The web layer enforces this by checking
  `getOpenThread` before creating; close (resolve/dismiss) before re-opening.

### Agent thread loop

```bash
# 1. find the oldest thread awaiting an agent reply (across the given DB)
uv run tools/translate.py -d <exam>.db next-thread

# 2. agent composes a reply internally based on the returned payload
#    (question text + choices + comments + full message history)

# 3. write back. resolve=true closes the thread; explanation_ja overwrites
#    questions.explanation_ja with a consolidated final answer.
echo '{
  "content": "agent reply markdown/text",
  "author": "claude-code",
  "resolve": true,
  "explanation_ja": "新しい合意済み解説 ..."
}' | uv run tools/translate.py -d <exam>.db reply <thread_id>
```

The user can also drive multi-turn dialogues via the web at `/e/:slug/q/:id` —
typing in the textarea appends a `user` message to the same thread.

## Multi-cert layout (per-DB)

Each AWS certification gets its own SQLite file at the repository root, named after
the lowercase exam ID (`soa-c03.db`, `sap-c02.db`, `dva-c02.db`, ...).

- `web/src/db.ts:discoverExams()` walks the repo root, opens each `*.db`, and treats
  any file containing a `questions` table as an exam. The file basename becomes the
  URL slug.
- `web/src/db.ts:openDb(slug)` is a memoized factory; each opened DB is initialized
  with the `attempts`, `explanation_threads`, and `explanation_messages` tables on
  first access.
- Cross-exam features (`/requests`, the nav badge for awaiting-agent count) iterate
  all discovered DBs and aggregate in JS. Per-question features query just that
  exam's DB.
- The CLI doesn't need any change — `-d <slug>.db` is the per-exam handle. Run the
  same `translate.py status` against each DB.
- Attempts and threads written to one DB never leak to another (verified in tests:
  thread creation on `sap-c02.db` does not affect `soa-c03.db`).

## Schema invariants you must respect

1. **Two writers, one schema.** `web/src/db.ts` and `tools/translate.py` both contain
   `CREATE TABLE IF NOT EXISTS ...` for the per-DB tables (`attempts`,
   `explanation_threads`, `explanation_messages`). Whichever side opens the DB first
   creates the tables; the other no-ops. **If you change the schema, update BOTH
   sides.** No migration framework is in place.

2. **`questions.url` is UNIQUE.** Use it as the dedup key for any merge/import.

3. **`_ja` columns are nullable.** UI must fall back to the English column when null
   (already done via `??` in views.tsx). Don't `INSERT` empty strings to fake
   completeness — leave NULL.

4. **`questions.confirmed_answer` may be truncated.** See "Upstream gotchas" below.
   Treat `suggested_answer` as the source of truth for multi-select.

## Upstream gotchas (engineering knowledge)

### `-s` flag is a grep against discussion-link text, not a slug match

The Go scraper's `-s` filter scans discussion-link contents for the substring you
pass. Two consequences:

- ✅ `-s soa-c03` works (current discussion links contain that ID).
- ❌ `-s soa-c02` returns 0 results — that exam ID is no longer present on
  examtopics. The CLI exits 0 with `Found 0 unique matching links:` and the output
  file contains only the markdown header. There is no error to catch.

**Always:** run `go run ./cmd/main.go -p amazon -exams` first to confirm the slug,
then verify post-scrape that stdout contained `Found N>0 unique matching links` and
that the output file is non-trivial (`wc -l > 4`).

### Cache vs manual scrape paths

The scraper first tries pre-built GitHub-cached dumps; if that fails it falls back
to "manual scraping" of all provider pages (`Going to manual scraping, cached data
failed.`).

- **Cache path** parses structured JSON, so comments are split per poster
  (`[Poster] content` lines) — high quality.
- **Manual path** lifts the entire `.discussion-container` text and concatenates —
  comments end up as one long blob. Lower quality for downstream comment parsing.

Pass `-t <GitHub PAT>` to raise the GitHub API limit and bias toward the cache path.

### Multi-select answer truncation (`internal/fetch/scraper.go:35`)

The scraper takes only the first byte of `.correct-answer`:

```go
answer = string(strings.ReplaceAll(strings.ReplaceAll(answerText, " ", ""), "\n", "")[0])
```

So multi-correct questions like `BD`, `CD`, `AE`, `BE` end up as `B`, `C`, `A`, `B`
respectively in `**Answer:**`. The MD body's `Suggested Answer: BD 🗳️` line is
preserved fully though.

After parsing into SQLite:
- `suggested_answer` (parsed from `Suggested Answer:`) is **complete**.
- `confirmed_answer` (parsed from `**Answer:**`) is **truncated** for multi-select.

Detect multi-select with `LENGTH(suggested_answer) > 1`. Always trust
`suggested_answer` over `confirmed_answer`.

## Encoding / Windows tips

When piping Japanese (or any non-ASCII) text on Windows PowerShell:

- PowerShell may default `$OutputEncoding` to `US-ASCII`, corrupting UTF-8 bytes
  through `|`. Set it to UTF-8 first:
  ```powershell
  $OutputEncoding = [System.Text.UTF8Encoding]::new()
  ```
- Prefer **temp files** over pipes for large or encoding-critical payloads
  (`echo '...' > tmp.json; uv run ... save 1 < tmp.json`).
- Receiving Python scripts in this repo (`translate.py`, audits) explicitly read
  stdin as UTF-8 and strip a leading BOM.

On bash / WSL / macOS none of this matters — pipes are 8-bit clean.

## Re-distribution caveat

The `*.db`, root-level `*.md`, and root-level `*.json` patterns are gitignored
because they contain scraped exam content from examtopics.com which has its own
terms. **Do not commit, push, attach to issues, or paste this content publicly.**
Only the code in this repo is intended for redistribution.
