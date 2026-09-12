# Portable Builds — E2E Verification Checklist (task 8)

This doc is the manual procedure for the §7 完了条件 of
[portable-builds.md](portable-builds.md). Every box below must be
ticked before tagging `v0.1.0` (task 10).

The checklist runs in two layers:

- **Layer A** (offline, no LLM tokens) — covers ~80% of the surface
  using stub adapters / dry-run. Run first.
- **Layer B** (real LLM, real network) — exercises the claude /
  gemini spawn paths and the Web admin/fetch scrape. Costs real
  tokens / hits examtopics. Run on the host you actually deploy to.

Conventions in commands below:

- `<DATA>` = the directory you point `dataDir` at (e.g. `~/exam-data`)
- `<DB>` = a slug-named DB inside `<DATA>` (e.g. `<DATA>/saa-c03.db`)
- `<TID>` = a 26-char Crockford base32 thread id from
  `examtopics-web` URL or `sqlite3 <DB> "select hex(id) from
  explanation_threads"` round-tripped through `internal/uuidx`

## 0. Prerequisites

- [ ] Go ≥ 1.24 (`go version`)
- [ ] Bun ≥ 1.3 (`bun --version`)
- [ ] (Layer B only) `claude` CLI on PATH with a working `ANTHROPIC_API_KEY`
- [ ] (Layer B sync test) `scp` + ssh access to a second host, OR a
  second working tree on the same machine to fake the peer
- [ ] No existing `~/.config/examtopics/config.json` (or `%APPDATA%\examtopics\config.json`) — back it up if you have one. Layer A 1.4 needs a clean slate to verify the auto-generation path

## 1. Build & install

### 1.1 Cross-compile from source

```bash
git clone https://github.com/dj-oyu/examtopics-downloader && cd examtopics-downloader

# Go CLI (host-native)
go build -trimpath -ldflags="-s -w -X main.version=$(git rev-parse --short HEAD)" \
  -o examtopicsdl ./cmd

# Bun web (host-native)
( cd web && bun install --frozen-lockfile \
  && bun build --compile --minify src/server.tsx --outfile ../examtopics-web )
```

- [ ] Both binaries produced (`ls -la examtopicsdl examtopics-web`)
- [ ] `./examtopicsdl version` prints `dev-<sha>` matching the build
- [ ] `./examtopics-web` starts and prints `listening on http://...`. Ctrl+C to stop.

### 1.2 Cross-compile to other platforms

```bash
mkdir -p dist
for target in "linux amd64" "linux arm64" "windows amd64" "windows arm64"; do
  read -r os arch <<< "$target"; ext=""; [ "$os" = windows ] && ext=".exe"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
    -ldflags="-s -w -X main.version=v0.1.0-test" \
    -o dist/examtopicsdl-${os}-${arch}${ext} ./cmd
done
( cd web && for target in bun-linux-x64 bun-linux-arm64 bun-windows-x64; do
  bun build --compile --minify --target=$target src/server.tsx \
    --outfile ../dist/examtopics-web-${target#bun-}$([ "${target##*-}" = x64 ] && [ "${target%%-*}" = bun ] && echo "")$([ "${target}" = bun-windows-x64 ] && echo .exe)
done )
```

- [ ] All 4 Go targets compile (sizes ~15 MB each)
- [ ] All 3 Bun targets compile (sizes ~110 MB each)
- [ ] `bun-windows-arm64` is omitted (release.yml notes the gap; documented as build-from-source)

## 2. Configuration

### 2.1 First-run hostId auto-generation

```bash
cd /tmp && mkdir cleanrun && cd cleanrun
HOME=$PWD ./examtopicsdl version  # should not crash
HOME=$PWD ./examtopicsdl config | head -5
```

- [ ] `examtopicsdl config` emits a `hostId` like `<sanitized-hostname>-<4 hex>`
- [ ] A new `$HOME/.config/examtopics/config.json` (Linux) or `%APPDATA%\examtopics\config.json` (Windows) was created with that `hostId`
- [ ] Re-running prints the **same** `hostId` (no rotation)

### 2.2 config.json + .env search path

Place a config.json at each of the 4 search locations in turn (env, cwd, exec dir, user dir) and verify `examtopicsdl config` reports the right `loadedFrom`.

- [ ] `EXAMTOPICS_CONFIG=/tmp/x.json examtopicsdl config | grep loadedFrom` → `/tmp/x.json`
- [ ] `examtopicsdl config | grep loadedFrom` (with no env) → user-config path

### 2.3 Forbidden-key warning

Put `{ "ghPat": "redacted-fake" }` in config.json:

- [ ] stderr emits `[config] warning: key "ghPat" ... not allowed ... Ignoring.`
- [ ] `examtopicsdl config` output does NOT include `ghPat`

### 2.4 tools.translate driver

Add to config.json:
```json
{ "tools": { "translate": { "client": "claude", "model": "claude-sonnet-4-6" } } }
```

- [ ] `examtopicsdl translate retranslate -db /tmp/non-existent.db -qid 1 -dry-run` errors at the DB step (not at the flag/client step) → confirms client default came from config
- [ ] `EXAMTOPICS_TRANSLATE_CLIENT=gemini examtopicsdl translate retranslate -db /tmp/x.db -qid 1 -dry-run` errors with "client gemini not yet wired" → env override beats config

## 3. CLI smoke (Layer A — offline)

### 3.1 fetch (legacy + subcommand form)

```bash
examtopicsdl providers
examtopicsdl fetch -p amazon -exams                         # subcommand form
examtopicsdl -p amazon -exams                               # legacy form (back-compat)
```

- [ ] `providers` lists `amazon`, `cisco`, `comptia`, … on separate lines
- [ ] Both `fetch -p amazon -exams` and the bare-flag form print the same exam URLs

### 3.2 quiz (interactive TUI)

Reuse an existing migrated DB or start from one of the included DBs:

```bash
examtopicsdl quiz -db <DB> < /dev/null
```

- [ ] Prints the first question + choices, then `Done — 0 / 0 answered correctly` on EOF
- [ ] Piping `printf 'A\n\n'` selects A on Q1, then quits without prompting → `attempts` table grows by 1

### 3.3 sync (snapshot / merge / content)

```bash
cp <DB> /tmp/peer.db
examtopicsdl sync snapshot -d <DB> -o /tmp/snap.db
examtopicsdl sync merge -d /tmp/peer.db --from /tmp/snap.db
examtopicsdl sync content -d /tmp/peer.db --from /tmp/snap.db
```

- [ ] Snapshot file is non-empty and `sqlite3 /tmp/snap.db .tables` lists the same tables as the source
- [ ] `sync merge` is idempotent — re-running the merge prints the same success line and changes nothing
- [ ] WAL detection: `printf "fake" > /tmp/peer.db-wal && examtopicsdl sync merge -d <DB> --from /tmp/peer.db` exits non-zero with the "stale WAL" hint
- [ ] `--allow-wal` bypass works: same command + `--allow-wal --skip-time-check` → merge proceeds

### 3.4 translate retranslate (dry-run)

```bash
mkdir -p /tmp/rt-stage
examtopicsdl translate retranslate -db <DB> -qid 1 -dry-run -workdir /tmp/rt-stage
```

- [ ] `/tmp/rt-stage/input.json` is created with `id`, `question_text`, `choices[]`, `existing_ja` populated
- [ ] No `output.json` (adapter not invoked under `-dry-run`)

### 3.5 translate explain (dry-run, requires existing thread)

Find an open thread id (or create one via the web UI first):

```bash
examtopicsdl translate explain -db <DB> -tid <TID> -dry-run -workdir /tmp/ex-stage
```

- [ ] `/tmp/ex-stage/input.json` includes `thread_id`, `status`, `question.{...}`, `messages[]`
- [ ] Empty `<TID>` or non-existent thread → clear error, exit non-zero

### 3.6 translate -list-clients

- [ ] `examtopicsdl translate -list-clients` prints 4 rows (gemini / claude / codex / exec) with their skill paths
- [ ] `examtopicsdl translate -client gemini -dry-run` writes `.gemini/skills/exam-translator/SKILL.md` next to where you ran it

## 4. Web server smoke (Layer A — no LLM)

### 4.1 Boot

```bash
EXAMTOPICS_DATA_DIR=<DATA> ./examtopics-web
```

- [ ] `listening on http://127.0.0.1:3000` printed
- [ ] `[agent] log file: <DATA>/logs/agent.jsonl` printed
- [ ] No "ENOENT" / "import.meta" errors

### 4.2 Routes (browser)

- [ ] `GET /` lists every `*.db` in `<DATA>` with question / answered / open-thread counts
- [ ] `GET /e/<slug>/q` lists questions; filter `?filter=wrong` and `?filter=unanswered` change the list
- [ ] `GET /e/<slug>/q/1` renders the first question with choices and (if present) a thread panel
- [ ] `POST /e/<slug>/q/1/attempt` (form submit "A") records an attempt and shows correct/incorrect feedback
- [ ] `GET /requests` lists open threads across all DBs

### 4.3 Thread URL is base32 (task 4-F regression)

- [ ] Open a question that has an existing thread; thread anchor URL is `/e/<slug>/threads/<26-char-base32>/...`
- [ ] Hand-typing a malformed id (e.g. `/e/<slug>/threads/abcde/messages.json`) → 404, not 500

### 4.4 admin/fetch (without admin token)

- [ ] `GET /admin/fetch` (no `EXAMTOPICS_ADMIN_TOKEN` env) returns 503 body "admin disabled — set EXAMTOPICS_ADMIN_TOKEN to enable"

## 5. Migration safety on a v2 DB

Before this checklist landed, the user's 8 DBs were already migrated to v3. To repeat the verification on a fresh v2 file:

```bash
# Make a v2 DB by checking out a tag prior to migration 003 and creating one,
# OR find any saved .pre-003.bak from an earlier migration run.
cp <some-v2-fixture>.db /tmp/v2-test.db
sqlite3 /tmp/v2-test.db "INSERT INTO attempts(question_id,selected,is_correct) VALUES (1,'A',1)"
sqlite3 /tmp/v2-test.db "INSERT INTO explanation_threads(question_id) VALUES (1)"

examtopicsdl quiz -db /tmp/v2-test.db < /dev/null   # triggers migration on Open
```

- [ ] `/tmp/v2-test.db.pre-003.bak` was created (VACUUM INTO snapshot, smaller than original)
- [ ] `sqlite3 /tmp/v2-test.db "SELECT MAX(version) FROM schema_version"` → 3
- [ ] `sqlite3 /tmp/v2-test.db "SELECT COUNT(*) FROM attempts"` matches pre-migration count
- [ ] `sqlite3 /tmp/v2-test.db "SELECT DISTINCT host_id FROM attempts"` returns the local hostId

## 6. Multi-host sync loopback (3-machine, simulated)

You can simulate three machines with three working trees and three different `EXAMTOPICS_HOST_ID` values on the same box. Or use one Windows desktop + two SBCs.

```bash
# host A (master)
mkdir -p /tmp/hostA && cp <DB> /tmp/hostA/saa.db
EXAMTOPICS_HOST_ID=hostA HOME=/tmp/hostA examtopicsdl quiz -db /tmp/hostA/saa.db < /dev/null

# host B + C: similar setup with their own hostId / dirs
# B answers 3 questions (records attempts)
# C creates 1 thread + 1 reply

# A pulls each peer
EXAMTOPICS_HOST_ID=hostA examtopicsdl sync snapshot -d /tmp/hostB/saa.db -o /tmp/from-B.snap
EXAMTOPICS_HOST_ID=hostA examtopicsdl sync merge -d /tmp/hostA/saa.db --from /tmp/from-B.snap
EXAMTOPICS_HOST_ID=hostA examtopicsdl sync snapshot -d /tmp/hostC/saa.db -o /tmp/from-C.snap
EXAMTOPICS_HOST_ID=hostA examtopicsdl sync merge -d /tmp/hostA/saa.db --from /tmp/from-C.snap

# A re-distributes
EXAMTOPICS_HOST_ID=hostA examtopicsdl sync snapshot -d /tmp/hostA/saa.db -o /tmp/A-master.snap
EXAMTOPICS_HOST_ID=hostB examtopicsdl sync merge -d /tmp/hostB/saa.db --from /tmp/A-master.snap
EXAMTOPICS_HOST_ID=hostC examtopicsdl sync merge -d /tmp/hostC/saa.db --from /tmp/A-master.snap
```

- [ ] After convergence, `attempts` / `explanation_threads` / `explanation_messages` row counts and contents match across A/B/C
- [ ] `host_id` distribution: A's writes carry `hostA`, B's `hostB`, C's `hostC`
- [ ] Re-running any `sync merge` is a no-op (counts unchanged)

## 7. NTP / clock-sync abort

Linux:
```bash
sudo timedatectl set-ntp false
examtopicsdl sync merge -d <DB> --from /tmp/snap.db   # should abort
sudo timedatectl set-ntp true
```

- [ ] Without `--skip-time-check`, the merge aborts with the `--skip-time-check` hint
- [ ] With `--skip-time-check`, merge proceeds

Windows: `w32tm /query /status` returns a parseable date (per the locale-independent regex). The 7-day staleness threshold is hard to fail safely — verified instead via the unit test in `internal/sync/preflight_test.go`.

## 8. Layer B — real LLM (claude / gemini)

These cost real API tokens. Run once before tagging `v0.1.0`.

### 8.1 Real retranslate

```bash
examtopicsdl translate retranslate -db <DB> -qid 1 -client claude
```

- [ ] Exit 0, summary line lists `question_text_ja` char count + choice count
- [ ] `sqlite3 <DB> "SELECT question_text_ja FROM questions WHERE id=1"` returns Japanese text
- [ ] Re-running on the same qid produces a similar but not byte-identical translation (claude isn't deterministic)

### 8.2 Real explain (multi-turn)

Create a thread via the web UI ("解説スレッドを作成する"), wait for the agent reply, then post a follow-up:

- [ ] First reply lands as an `agent` row in `explanation_messages` with non-null `reason_code`
- [ ] `agent_session_id` is set on the thread row
- [ ] Follow-up reply uses `--resume`: `<DATA>/logs/agent.jsonl` shows `prior_session_id` matching what the previous turn returned
- [ ] If the agent picks `reason_code=spec` or `ambiguous`, `citations` includes at least one `docs.aws.amazon.com` URL
- [ ] If the agent picks `reason_code=translation`, `translation_diff` is populated and the corresponding `*_ja` columns updated

### 8.3 Web /admin/fetch (real scrape)

```bash
export EXAMTOPICS_ADMIN_TOKEN=$(openssl rand -hex 16)
EXAMTOPICS_DATA_DIR=<DATA> ./examtopics-web
```

- [ ] Browser to `http://127.0.0.1:3000/admin/login` — submit token, get redirected to `/admin/fetch`
- [ ] Submit form: provider=amazon, slug=clf-c02 (or any small exam) — page redirects, status block flips to "running"
- [ ] SSE log streams stdout lines from `examtopicsdl fetch`
- [ ] On exit-0, `<DATA>/clf-c02.db` is created (or updated)
- [ ] Curl-only path: `curl -X POST http://localhost:3000/admin/fetch -H "Authorization: Bearer $EXAMTOPICS_ADMIN_TOKEN" -d "provider=amazon&slug=clf-c02"` returns 303
- [ ] Concurrent POST while a job runs returns 409 "another fetch job is in flight"
- [ ] Wrong Bearer token returns 401

## 9. Cross-platform binary smoke

Run this on a fresh Linux VM (and ideally a Windows VM) using the binaries from §1.2's `dist/`:

- [ ] `examtopicsdl-linux-amd64 version` works on Linux x86_64
- [ ] `examtopicsdl-linux-arm64 version` works on a Raspberry Pi or Linux arm64 VM
- [ ] `examtopicsdl-windows-amd64.exe version` works on Windows 10/11
- [ ] On Linux, `examtopics-web-linux-amd64` boots, serves on :3000, lists DBs from `EXAMTOPICS_DATA_DIR`
- [ ] Same for Windows — `examtopics-web-windows-amd64.exe` boots and serves

## 10. CI / release.yml

After pushing the branch:

- [ ] `release.yml`'s `go-check` + `bun-check` jobs go green on the PR
- [ ] `go-build` matrix produces 4 Go artifacts; `bun-build` produces 3 Bun artifacts
- [ ] `release` job is **skipped** on PR (only fires on tag push)

After tagging `v0.1.0` (task 10):

```bash
git tag v0.1.0
git push fork v0.1.0
```

- [ ] Same matrix runs against the tag
- [ ] `release` job runs and creates a GitHub Release with all 7 artifacts attached
- [ ] Release notes include the auto-generated commit list

## Done?

Once every box above is ticked, edit [portable-builds.md](portable-builds.md) §0 to mark task 8 ✅ and proceed to task 10 (`v0.1.0` tag).
