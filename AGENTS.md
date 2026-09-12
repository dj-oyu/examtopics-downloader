# AGENTS.md

Guide for AI coding agents (Claude Code, Codex CLI, Aider, etc.) working in
this fork of `examtopics-downloader`. Covers the post-processing pipeline added on top
of the upstream Go scraper, the schema invariants you must respect, and known
upstream bugs to compensate for.

## Repository layout

| Path | Purpose | Owned by |
|------|---------|----------|
| `cmd/`, `internal/`, `tests/` | Upstream Go scraper: examtopics → Markdown (multi-file `cmd` package: `go run ./cmd <subcommand>`) | upstream |
| `tools/` | Python (stdlib-only, uv-run) post-processing: SQLite + translation + audits | this fork |
| `web/` | Bun + Hono SSR Web UI for studying | this fork |
| `examples/` | Sample upstream output (kept as-is) | upstream |
| `*.db`, root `*.md`, root `*.json` | Scraped data — **gitignored, not redistributable** | local only |

## Pipeline at a glance

```
[examtopics.com]
      | go run ./cmd fetch -p amazon -s <exam-id> -c -save-links -o <exam>.md
      v
   <exam>.md
      | uv run tools/md_to_sqlite.py <exam>.md -o <exam>.db
      v
   <exam>.db   (questions, choices, _ja columns reserved)
      | exam-translator skill (client-neutral) orchestrates tools/translate.py
      | uv run tools/audit_*.py -d <exam>.db
      v
   <exam>.db   (translations, attempts, explanation threads)
      | bun run dev   (web/)
      v
   Browser study UI + agent dialogue at http://localhost:3000
```

## End-to-end workflow: 問題取得 → SQLite → 和訳 (client-neutral skill)

This is the canonical happy path for adding a new AWS exam to the studio. Step 3
is orchestrated by the **`exam-translator`** skill: the master lives at
`skills/exam-translator.md`, `go generate ./...` mirrors it into
`internal/translate/assets/` (for `//go:embed`) and into the Claude Code layout,
and `examtopicsdl translate -client <name> -dry-run` materializes it for whichever
LLM CLI you use. Any agent runtime that can run shell commands can drive it, and
`tools/translate.py` can always be driven directly with the same JSON contract —
the skill is an *automation layer*, not a hard requirement.

### Step 0. Confirm the exam slug

```bash
go run ./cmd -p amazon -exams
# pick the lowercase ID found in discussion-link slugs (e.g. soa-c03, dva-c02)
```

The `-s` flag is a substring grep, not a slug lookup. Verifying the ID up front
prevents the silent-empty-output failure described under "Upstream gotchas".

### Step 1. Scrape Markdown — **always pass `-t <GitHub PAT>`**

```bash
# one-time: copy the schema and fill in your PAT (gitignored)
cp .env.example .env && $EDITOR .env

# every run: load the env, then scrape
set -a; source .env; set +a
go run ./cmd -p amazon -s <exam-id> -c -save-links \
                     -t "$GH_PAT" -o <exam-id>.md
# -c: include discussion comments (REQUIRED for high-quality explanation_ja)
# -t: GitHub PAT — see "PAT recommended" rationale below
```

**Why PAT is the default, not a fallback:**

- The scraper first attempts to download a pre-built JSON dump from a public
  GitHub repo (`thatonecodes/examtopics-data`). Anonymous GitHub API access is
  rate-limited to **60 requests/hour**, which the scraper exhausts almost
  immediately on a real exam. With a PAT the limit jumps to **5000 req/h**,
  enabling the cache path to actually complete.
- **Cache path** parses structured JSON, so each comment ends up tagged with
  its poster (`[Poster] content` lines) — much higher quality for
  `audit_comments.py` to detect community-vote disagreement (Step 4b).
- **Manual fallback** (no PAT, or PAT exhausted, or exam not in cache) lifts
  the entire `.discussion-container` text and concatenates it into one blob.
  Comments are still usable but vote attribution is harder. Manual mode is also
  **10+ minutes** for a 500+ question exam vs. seconds for cache.
- Required PAT scope: **fine-grained PAT with Public Repositories (read-only)
  access, no additional scopes**. Anything more is unnecessary and increases
  blast radius if leaked.
- **Where to store the PAT:** the repo ships a committed `.env.example` showing
  the schema. Copy it to `.env` (gitignored via `.env*` with a `!.env.example`
  whitelist) and fill in `GH_PAT=...`. The `.env*` pattern blocks accidental
  commits of any environment-named file (`.env`, `.env.local`,
  `.env.production`, ...) while still letting the schema travel with the repo.
  Cross-project users who keep one PAT in `~/.gh-pub-read` instead can load it
  with `export GH_PAT=$(cat ~/.gh-pub-read)` — the scraper only cares about the
  `-t` argument, not the source.

Verify after run:

- stdout contains `Successfully saved cached output to ...` ⇒ cache path won.
- stdout contains `Going to manual scraping, cached data failed.` ⇒ either the
  exam is not in the cache (e.g. exams alphabetically after the GitHub Contents
  API's 1000-file cap), the PAT was missing, or the PAT was rate-limited.
  The scrape will still complete via manual scraping; just expect the longer
  runtime and the comment-blob format.
- stdout contains `Found N>0 unique matching links:` and `<exam-id>.md` is
  more than 4 lines (silent-zero is the failure mode described under
  "Upstream gotchas").

### Step 2. Markdown → SQLite

```bash
uv run tools/md_to_sqlite.py <exam-id>.md -o <exam-id>.db
# Parsed N questions ... -> <exam-id>.db
```

Creates `questions`, `choices`, `discussion` and their indexes. `_ja` columns are
nullable and filled in Step 3. `--append` deduplicates by `url` if you want to
merge multiple dumps into one DB.

### Step 3. 和訳オーケストレーション (client-neutral exam-translator skill)

Activate the skill from any runtime that can spawn a worker session (Claude Code
subagents, Codex, or a fresh non-interactive session). The skill mandates execution
mode and self-paced batching, so a single user prompt drives the run to completion:

```text
agent prompt:
  Follow skills/exam-translator.md and complete the translation of <exam-id>.db.
  Aim for questions_pending: 0. Report only range summaries.
```

What the skill does on your behalf (`skills/exam-translator.md`, mirrored by
`go generate` to `internal/translate/assets/exam-translator.md` and
`.claude/skills/exam-translator/SKILL.md`):

1. **Status probe.** `uv run tools/translate.py -d <exam-id>.db status` — captures
   `questions_total` / `questions_pending` baseline.
2. **Worker delegation in EXECUTION MODE.** Spawns the dedicated
   `exam-translator-worker` subagent (template at
   `agents/exam-translator-worker.md`, materialized to `.claude/agents/`). The
   worker definition embeds the translation guidelines:
   - AWS service names stay in English (`Amazon S3`, `AWS Lambda`, `Amazon Aurora`).
   - 平叙文 (である調), not 敬体 (です・ます).
   - `(複数選択)` prefix on `question_text_ja` when `LENGTH(suggested_answer) > 1`.
   - Choices A–E consistent in length and tone.
   - `explanation_ja`: 2–3 sentences covering 正解の根拠 + 主要な不正解の根拠,
     drawing on the `comments` column when it contributes counter-arguments.
3. **UTF-8-safe writes on Windows.** Stage the batch as `_staging.json` and pipe
   it with `cat _staging.json | uv run tools/translate.py -d <exam>.db bulk-save`
   — a temp file plus redirect, never `echo` through PowerShell's US-ASCII pipe
   (see "Encoding / Windows tips").
4. **Self-paced re-invocation.** When a worker reaches its turn/token limit, the
   orchestrator inspects the reported ID range and re-invokes for the next batch
   without prompting the user.
5. **Final validation.** Loops `status` until `questions_pending == 0`, samples
   3–5 rows to confirm no mojibake, then runs the Step 4 audits.

Single-row alternative (no worker, no batch):
`examtopicsdl translate retranslate -db <exam>.db -qid <id> -client claude` reads
one row, spawns the configured LLM CLI, validates the returned JSON and updates
`*_ja`. `-client` accepts `claude | codex | exec` — **gemini-cli support was
removed** (upstream development wound down); configuration lives in
`tools.translate.{client,model,bin}` of `config.json`.

For a runtime with no subagent mechanism, drive the loop manually:

```
status -> next -> (translate internally) -> echo '{...}' | save <id> -> repeat
```

### Step 4. Layered audits (cheap → expensive)

```bash
# (a) SQL-level integrity (sub-second)
uv run tools/audit_schema.py -d <exam-id>.db --fix-prefix

# (b) Community-vote disagreement (no network, ~3-5 of 75 rows typically)
uv run tools/audit_comments.py -d <exam-id>.db --min-votes 2

# (c) AWS Docs MCP cross-check on flagged + multi-select rows
#     Spawn an MCP-equipped sub-agent with aws___search_documentation /
#     aws___read_documentation; feed it the rows from (b) plus
#     `WHERE LENGTH(suggested_answer) > 1`. Apply individual fixes via
#     `translate.py save <id>` for the small N that need adjustment.
```

The `15%` short-ja threshold in (a) and `--min-votes 2` in (b) are calibrated to
fire only on outliers; layered narrowing is the design intent — do **not** run a
full Docs MCP audit over every row.

### Step 5. Web で学習 + 解説スレッド

```bash
cd web && bun install && bun run dev    # http://localhost:3000
```

The user answers questions, builds a wrong-answer review queue, and can request
agent explanations on any specific question via "解説スレッドを作成する". Agent
replies are appended via `tools/translate.py reply <thread_id>` and must respect
the grounding rules under "Multi-turn explanation threads" — most importantly
`reason_code='spec'|'ambiguous'` requires AWS Docs citations.

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
| `reply <thread_id>` | reads JSON `{content, author?, resolve?, reason_code?, citations?, translation_fix?, explanation_ja?}` from stdin (see "Multi-turn explanation threads" for grounding rules) |

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

<!-- AGENT_REPLY_PROMPT_START -->
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
  content TEXT NOT NULL,
  reason_code TEXT CHECK (
    reason_code IS NULL OR
    reason_code IN ('comprehension','spec','ambiguous','translation')
  ),
  citations TEXT,                    -- JSON: [{"url": "...", "title": "..."}, ...]
  translation_diff TEXT,             -- JSON: {"before": {...}, "after": {...}}
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
#    (question text + choices + comments + full message history) and
#    classifies the user's confusion via reason_code.

# 3. write back. The required fields depend on reason_code (see below).
echo '{...}' | uv run tools/translate.py -d <exam>.db reply <thread_id>
```

#### reply payload by reason_code

The agent **must** classify each reply with one of four `reason_code` values.
The CLI rejects payloads that violate the grounding rule for the chosen code:

| `reason_code` | When to use | Required fields |
|---|---|---|
| `comprehension` | User misread a modifier (least, most, EXCEPT, MUST), confused two services in their own head, or made a careless slip. | `content` only. Citations are *not* required — the fix is to point at the missed word. |
| `spec` | User has a specific AWS service spec wrong (e.g. "EBS volumes are region-scoped" when they're AZ-scoped). | `content` + `citations` containing **at least one URL with `docs.aws.amazon.com`**. AWS Docs MCP grounding is enforced at CLI level. |
| `ambiguous` | The question or community votes are genuinely split, or the suggested answer is contestable. | `content` + `citations` (same AWS Docs requirement) + ideally a note on the community-vote split. |
| `translation` | The Japanese translation itself is misleading or wrong, causing the user to misread the question. | `content` + `translation_fix` `{question_text_ja?, explanation_ja?, choices_ja?}`. Snapshots the existing `_ja` values, applies the fix, and stores `{before, after}` in `translation_diff` automatically. |

```jsonc
// reason_code='spec' example
{
  "content": "ALB listener rules evaluate top-down; first match wins.",
  "author": "claude-code",
  "reason_code": "spec",
  "citations": [
    {
      "url": "https://docs.aws.amazon.com/elasticloadbalancing/latest/application/listener-update-rules.html",
      "title": "Update rules for your Application Load Balancer"
    }
  ],
  "resolve": true
}

// reason_code='translation' example — fixes a misleading 訳語
{
  "content": "「冗長性」は redundancy ではなく resiliency の訳でした。修正します。",
  "author": "claude-code",
  "reason_code": "translation",
  "translation_fix": {
    "question_text_ja": "...修正後の設問文...",
    "choices_ja": { "A": "...修正後の選択肢..." }
  },
  "resolve": true
}
```

Selection priority (when multiple categories seem to apply):

1. Compare English `question_text` with `question_text_ja`. Divergence ⇒
   `translation` (fix the source of the confusion, not just the explanation).
2. Otherwise check the user message for missed modifiers ⇒ `comprehension`.
3. Otherwise the user is wrong about an AWS spec ⇒ `spec` (cite docs).
4. Otherwise the question itself is genuinely contestable ⇒ `ambiguous`.

The user can also drive multi-turn dialogues via the web at `/e/:slug/q/:id` —
typing in the textarea appends a `user` message to the same thread.
<!-- AGENT_REPLY_PROMPT_END -->

### Bun-driven agent autoresponder (web/src/agent.ts)

When the web UI receives a user message (new thread or follow-up reply), Bun
spawns a local `claude` CLI process to compose the agent reply automatically.
Implementation invariants:

- **Concurrency 1.** A single in-process LIFO stack drives one Claude process
  at a time. Multiple awaiting threads queue; pushing the same `(slug, tid)`
  twice is a no-op while it sits in-flight or in the queue.
- **Session resume.** The first spawn for a thread runs without `--resume`;
  the agent's stdout JSON yields a `session_id` which Bun stores in
  `explanation_threads.agent_session_id`. Subsequent spawns for that thread
  pass `--resume <session_id> -p "<latest user content>"` so Claude inherits
  prior reasoning + AWS Docs MCP context.
- **Resume failure fallback.** If `--resume` fails (session expired or
  invalidated), the spawner retries once from scratch with the full INITIAL
  prompt and overwrites `agent_session_id`.
- **Deferred close.** If the user clicks resolve/dismiss while a thread is
  in-flight, the close action is buffered in memory and applied after the
  agent finishes — agent reply takes priority over user-side close.
- **Re-enqueue on tail user.** After a spawn finishes, if the thread's last
  message role is still `'user'` (e.g. the user added another reply during
  the run), the thread is pushed back onto the stack for another cycle.
- **SSE notification.** Web clients viewing a question subscribe to
  `/e/:slug/threads/:tid/events`. The spawner pushes `agent-message` /
  `error` events; the client fetches `messages.json` and appends new bubbles
  without a page reload.
- **Allowed tools.** `--allowed-tools "Bash,mcp__claude_ai_AWS_Knowledge_MCP_Server__*"`.
  Bash is required to invoke `translate.py show-thread` / `reply`; the AWS
  Docs MCP namespace is required for `reason_code='spec'|'ambiguous'`
  citations.
- **Working directory.** Repo root (so `uv run tools/translate.py -d <slug>.db`
  resolves correctly).

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

1. **Two writers, one schema, plus a shared migrations directory.**
   `web/src/db.ts` and `tools/translate.py` both contain `CREATE TABLE IF NOT
   EXISTS ...` for the per-DB tables (`attempts`, `explanation_threads`,
   `explanation_messages`). Whichever side opens the DB first creates the
   tables; the other no-ops. **Both writers also run `applyPendingMigrations()`
   on `openDb()`**, replaying any pending `migrations/NNN_*.sql` files in
   ascending order and recording applied versions in the `schema_version`
   table. The runner uses 12-step table reconstruction so CHECK constraints
   can be added/changed cleanly. **Policy:** all schema changes go through a
   new migration file; do not hand-roll `ALTER TABLE` in inline DDL. Update
   the inline DDL in *both* writers to reflect the post-migration shape so a
   freshly created DB and a migrated old DB end up identical (verified by
   comparing `PRAGMA table_info` and the stored `sqlite_master.sql`).
   `tools/md_to_sqlite.py` and `tools/audit_*.py` currently bypass the
   migration runner because they only touch `questions` / `choices` /
   `discussion`; if a future migration changes those tables, route them
   through the runner too.

2. **`questions.url` is UNIQUE.** Use it as the dedup key for any merge/import.

3. **`_ja` columns are nullable.** UI must fall back to the English column when null
   (already done via `??` in views.tsx). Don't `INSERT` empty strings to fake
   completeness — leave NULL.

4. **Answers: `suggested_answer` is the community-voted majority; `confirmed_answer`
   is the same question's `**Answer:**` line.** On a fresh scrape both carry the
   full letter set (the old first-byte truncation is gone — see "Answers and the
   two answer fields" below). Only the checked-in `examples/` dumps are still
   truncated (`CD` → `C`), so treat `suggested_answer` as the source of truth for
   multi-select and when mixing old dumps into one DB.

## Upstream gotchas (engineering knowledge)

### `-s` flag is a grep against discussion-link text, not a slug match

The Go scraper's `-s` filter scans discussion-link contents for the substring you
pass. Two consequences:

- ✅ `-s soa-c03` works (current discussion links contain that ID).
- ❌ `-s soa-c02` returns 0 results — that exam ID is no longer present on
  examtopics. The run ends with `Found 0 unique matching links:` and **now exits 1
  with an actionable message instead of writing a header-only `.md`** (before, it
  exited 0 and reported success).

**Always:** confirm the slug with `go run ./cmd -p amazon -exams` first,
then verify post-scrape that stdout contained `Found N>0 unique matching links`
and that the output file is non-trivial (`wc -l > 4`).

### Cache vs manual scrape paths

The scraper first tries pre-built GitHub-cached dumps; if that fails it falls back
to "manual scraping" of all provider pages (`Going to manual scraping, cached data
failed.`).

- **Cache path** parses structured JSON, so comments are split per poster
  (`[Poster] content` lines) — high quality.
- **Manual path** lifts the entire `.discussion-container` text and concatenates —
  comments end up as one long blob. Lower quality for downstream comment parsing.

**A PAT is mandatory in practice, not an optimisation.** The cache listing lives at
`api.github.com/repos/thatonecodes/examtopics-data/contents/<Provider>` (provider
name = `CapitalizeFirstLetter(lowercase -p value)`, so directories are `Amazon`,
`Google`, `Linux-foundation`, ...), one request per shard file
(`<Exam-Name>_<shard>.json`). Anonymous GitHub API access is 60 requests/hour per
IP and the API answers **403** once it is gone, which used to drop whole files
silently: a real 31-file / AIF-C01 run returned 135 of 154 questions and still
printed "Successfully saved 135 questions". With `GH_PAT` set (`.env`, see below)
the budget is 5000/hour and the same run returns all 154. Provider directories that
were never mirrored (e.g. `Lpi`, the one the integration test uses) **404** — that
is a normal cache miss that falls back to manual scraping, not an error.

### Answers and the two answer fields

`internal/fetch/scraper.go:getDataFromLink` reads the community-voted answer from
the `.voted-answers-tally` JSON and `cleanAnswer()` keeps the full letter sequence;
the legacy `.correct-answer` fallback is no longer truncated either. The cache JSON
carries two different answer fields — do not conflate them:

| field | meaning | examples |
|---|---|---|
| `answer` | community-voted majority (identical to `answers_community`) | `A`, `BD`, `U`, `UB` |
| `answer_ET` | ExamTopics' own answer | `A`, `DEF` |

`SuggestedAnswer` is populated from `answer`, i.e. the community vote, which is what
`audit_comments.py` cross-checks against the votes quoted in the comments. The two
disagree on ~5% of questions (one sampled case: community `A` 100% vs `answer_ET`
`DEF` on a "Choose three" question) — **never use `answer_ET` to overwrite
`suggested_answer`**; if the site's own answer is wanted in the DB it needs its own
column, and schema changes go through a migration file (see schema invariants).
Values like `U` / `UB` are literal vote results (the poll offered a `U` option), not
corruption.

Answer-less question types (HOTSPOT / SIMULATION / FILL BLANK) keep their row with
`suggested_answer` and `confirmed_answer` NULL — `tools/md_to_sqlite.py` used to
drop those blocks entirely, losing the question text and images.

### Cache-path Markdown layout (and why the parser reads both)

`ConvertCachedJSON` writes a different body from the manual path, and both may end
up in one file:

```
## Examtopics AWS Certified AI Practitioner AIF C01_28 question #1
<question text>
Suggested Answer: A 🗳️
**A:** choice            <- the bold closes after the colon
**Answer: A**
```

versus the manual layout (`## Exam <id> topic N question N discussion`, an
`[All ... Questions]` marker, `A. choice`). `tools/md_to_sqlite.py` detects the
layout per block: on the cache path it recovers `exam` by stripping the
`Examtopics ` wrapper and the `_<shard>(.json)?(?ref=main)?` suffix (mirroring
`utils.DeriveExamDisplay`), `topic` from `topic-(\d+)` in the URL, and
`question_number` from the title. Run `python3 tools/test_md_to_sqlite.py` after
touching either writer — a cache-path dump that parses to zero rows is the failure
mode this covers (154 blocks in, 0 rows out, before).

### Question numbering on the cache path

Cache URLs have no `-question-N-discussion` segment, so the number comes from the
cache JSON's `question_id` (the exam's own sequential number), surfaced to the
writer via `QuestionExtras.QuestionID` and embedded in the Title as `question #N`.
`utils.SortQuestionDataByPageNumber` orders by it and is stable, so the Markdown
output ascends in exam order. Do **not** reintroduce a package-level counter to
number converted rows: `ConvertCachedJSON` runs in one goroutine per cache file, a
shared counter is a data race (`go test -race` catches it) and the numbers come out
order-dependent and collision-prone.

### HTTP failures must be retried or reported, never swallowed

`internal/fetch/fetch.go:FetchURL` retries **429 and 5xx** with exponential backoff
plus jitter and honours `Retry-After` (delta-seconds or HTTP-date, capped by
`constants.RetryAfterCap` so a one-hour GitHub reset cannot look like a hang).

- **403 is not retried** — it means a quota/bot wall, and waiting inside the same
  run cannot help. It is logged with the URL and counted.
- Every lost URL is counted (`fetch.FetchFailures`) and each phase logs
  `WARNING: N fetch(es) failed during <phase> — the result is incomplete`; the CLI
  repeats it on stderr and exits 1 when a run produced zero questions.
- Real-world evidence for why this matters: examtopics.com 429s hard at ~2 req/s,
  and a 37-page listing run recorded 32 failures. Before the fix such a run printed
  `Successfully saved output ...` with a near-empty file — the shape of upstream
  issues #11 / #12 / #16 ("missing questions", "output file empty"). If you see a
  low `Found N unique matching links:` for a valid exam, suspect throttling before
  suspecting the exam ID, and do not re-run in a loop: that extends the limit window.
- Probing a provider's cache directory uses `FetchURLProbe`, which exempts 404
  (provider never mirrored) but not 403 (real loss).

### Client separation: examtopics.com must never see the GitHub token

`FetchCachedLinks` upgrades the package-level `client` to an authenticated GitHub
client when a PAT is present. **examtopics-facing code must use `siteClient`**
(getDataFromLink, getLinksFromPage, getMaxNumPages, GetProviderExams) —
`TestGitHubTokenNeverReachesSiteClient` pins both halves. Merging the two clients
back (or routing a new examtopics request through `client`) sends the PAT to a
third-party site in the `Authorization` header, which was a real bug until it was
split.

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
