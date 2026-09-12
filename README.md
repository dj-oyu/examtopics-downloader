# Exam Topics Downloader

This repo aims to make it possible for you to obtain all the exam questions from the examtopics website (which is paywalled). It ships two binaries:

- **`examtopicsdl`** — the Go CLI (scrape, quiz, sync, translate)
- **`examtopics-web`** — a Bun-compiled web server that browses the scraped DB, runs an interactive review loop, and dispatches retranslate / explanation jobs

Both binaries share a single `config.json` and operate on the same SQLite files in a configurable `dataDir`.

## Quick start

```bash
# 1. Build (or grab from a Release zip)
go build -o examtopicsdl ./cmd
( cd web && bun build --compile --minify src/server.tsx --outfile ../examtopics-web )

# 2. Scrape one exam into the data directory
./examtopicsdl fetch -p amazon -s saa-c03 -sqlite saa-c03.db

# 3. Start the web UI (default port 3000)
./examtopics-web
# → open http://127.0.0.1:3000
```

The first CLI invocation generates a `hostId` and writes it to a per-user `config.json` (see [Configuration](#configuration)). The web binary picks up the same file on the next start.

## Subcommands

| Subcommand | Purpose |
| --- | --- |
| `examtopicsdl fetch -p <provider> -s <slug>` | Scrape exam questions (the legacy flag-only form `examtopicsdl -p ... -s ...` still works as a backward-compat shim). |
| `examtopicsdl quiz -db <path>` | Walk the question list interactively, recording every answer to the `attempts` table with a UUIDv7 + `host_id`. |
| `examtopicsdl translate retranslate -db <path> -qid <id> -client <claude\|codex\|exec>` | Re-translate one row end-to-end: read row → spawn LLM CLI → validate JSON → UPDATE `*_ja`. |
| `examtopicsdl translate -client <name> -dry-run` | Materialize the bundled exam-translator skill into the chosen CLI's expected layout. |
| `examtopicsdl sync snapshot -d <db> -o <out>` | `VACUUM INTO` a peer-safe snapshot. |
| `examtopicsdl sync merge -d <local> --from <peer>` | Pull the peer's append-only rows + LWW thread updates. |
| `examtopicsdl sync content -d <local> --from <master>` | Replace `questions` / `choices` / `discussion` from the master DB. |
| `examtopicsdl providers` | Print the known scraper provider list as JSON. |
| `examtopicsdl config` | Print the resolved runtime configuration. |
| `examtopicsdl version` | Print the version embedded at build time. |

## Configuration

Two layered files, never one. Structural settings live in JSON, secrets stay in environment / `.env`:

```jsonc
// config.json — never put PATs / API keys here
{
  "hostId": "level-infinity-f5ec",   // auto-generated on first CLI run
  "dataDir": "~/examtopics-data",    // *.db files live here
  "logDir": "~/examtopics-data/logs",
  "downloaderBin": "",               // empty → resolve via PATH
  "web": {
    "host": "127.0.0.1",
    "port": 3000,
    "adminEnabled": true
  },
  "scrape": {
    "defaultProvider": "amazon",
    "noCache": false
  },
  "tools": {
    "translate": {
      "client": "claude",            // claude | codex | exec — drives `examtopicsdl translate retranslate / explain` adapter
      "model": "claude-sonnet-4-6",  // optional --model override; empty leaves the client's own default
      "bin": ""                      // optional path to the client binary; empty falls back to CLAUDE_BIN / PATH
    }
  }
}
```

```dotenv
# .env — keep next to config.json
GH_PAT=ghp_xxxxxxxxxxxxxxxxxxxx
ANTHROPIC_API_KEY=sk-ant-xxx     # only if you use the explain / claude adapter
```

### Search paths (highest priority first)

1. `EXAMTOPICS_CONFIG=/path/to/config.json` — explicit override
2. `<cwd>/config.json`
3. `<dir of binary>/config.json`
4. Per-user — Linux/macOS `$XDG_CONFIG_HOME/examtopics/config.json` (default `~/.config/examtopics/config.json`); Windows `%APPDATA%\examtopics\config.json`
5. Built-in defaults (`dataDir = cwd`, port 8787, etc.)

`.env` resolution mirrors the same locations. Process environment variables (`EXAMTOPICS_DATA_DIR`, `EXAMTOPICS_LOG_DIR`, `EXAMTOPICS_HOST_ID`, `EXAMTOPICS_DOWNLOADER_BIN`, `EXAMTOPICS_TRANSLATE_CLIENT`, `EXAMTOPICS_TRANSLATE_MODEL`, `EXAMTOPICS_TRANSLATE_BIN`, `GH_PAT`, …) override the JSON values.

### Forbidden keys

Parsing strips and warns on keys that hint at credential leakage: `ghPat`, `GH_PAT`, `token`, `Token`, `adminToken`, `EXAMTOPICS_ADMIN_TOKEN`, `apiKey`, `secret`. Move those values to `.env` or the process environment instead.

## Web server (`examtopics-web`)

The Bun binary serves the same SQLite DBs that the Go scraper wrote. Routes:

- `/` — exam list with translated / answered / open-thread counts
- `/e/<slug>/q` — question list with filter (`?filter=wrong|unanswered|all`)
- `/e/<slug>/q/<id>` — single question + multi-turn explanation thread
- `/e/<slug>/review` — wrong-answer review queue
- `/requests` — open explanation threads across all DBs
- `/e/<slug>/threads/<base32-id>/{messages.json,events,reply,resolve,dismiss}` — thread API; `<base32-id>` is the 26-char Crockford encoding of the UUIDv7 BLOB primary key

### Spawned helpers

| Trigger | Spawned binary | Override env |
| --- | --- | --- |
| Retranslate button | `examtopicsdl translate retranslate -client <name>` | `EXAMTOPICSDL_BIN`, `EXAMTOPICS_TRANSLATE_CLIENT` |
| Open explanation thread | `claude` CLI (Claude Code) | `CLAUDE_BIN` |

The retranslate path uses the bundled adapter contract; the explanation path still talks to `tools/translate.py` from inside Claude (multi-turn `--resume` migration is pending).

## Multi-host sync (Phase 1)

The intended topology is a single laptop / desktop ("母艦", intermittent) plus N always-on SBCs that hold the same DB. Migration 003 makes every multihost row UUIDv7 BLOB-keyed and stamps a `host_id`, so peer DBs merge with `INSERT OR IGNORE` (G-Set semantics) and `explanation_threads.updated_at` (LWW). Phase 1 ships scp-friendly subcommands; Phase 2 (HTTP `/sync/*`) is planned for a follow-up branch.

```bash
# desktop → SBC: distribute fresh content
examtopicsdl sync snapshot -d ~/data/saa-c03.db -o /tmp/saa.snap
scp /tmp/saa.snap user@sbc-a:/var/lib/examtopics/incoming.db
ssh user@sbc-a -- examtopicsdl sync content \
  -d /var/lib/examtopics/saa-c03.db \
  --from /var/lib/examtopics/incoming.db

# SBC → desktop: collect learning history
ssh user@sbc-a -- examtopicsdl sync snapshot \
  -d /var/lib/examtopics/saa-c03.db -o /tmp/sbc-a.snap
scp user@sbc-a:/tmp/sbc-a.snap C:/data/incoming-sbc-a.db
examtopicsdl sync merge -d C:/data/saa-c03.db --from C:/data/incoming-sbc-a.db
```

`sync merge` is **idempotent** — running it twice on the same peer snapshot is a no-op. `sync snapshot` uses `VACUUM INTO` so the resulting file is consistent regardless of WAL state.

### NTP requirement

LWW merge of `explanation_threads.updated_at` assumes monotonic clocks across all hosts. If a peer's clock skews backwards, newer remote updates may be discarded as "older" by the merge. Make sure each host has time sync enabled before running `sync merge`:

- Linux desktops / SBCs: `timedatectl status` should show `System clock synchronized: yes` and an active service (`ntpd`, `chronyd`, or `systemd-timesyncd`)
- Windows: `w32tm /query /status` should report a healthy `Source` and a recent `Last Successful Sync Time`

The DB stores all timestamps in UTC (SQLite `CURRENT_TIMESTAMP` + UUIDv7's millisecond prefix), so per-host timezone differences are display-only and do not affect merge correctness.

### Migration 003 safety

The first time a CLI subcommand opens a v2 DB it:

1. Takes a `VACUUM INTO`-based snapshot at `<db>.pre-003.bak`
2. Captures every `attempts` / `explanation_threads` / `explanation_messages` row
3. Runs the destructive `DROP / CREATE` SQL
4. Re-inserts captured rows with freshly minted UUIDv7 ids and the local `host_id`

Step 1 is a one-shot safety net — once you have verified the migrated DB is healthy, the `.pre-003.bak` files can be archived or deleted. Steps 2–4 happen inside one transaction, so a partial failure rolls everything back to the v2 state.

## Setting it Up

### Using docker

1. Make sure [docker](https://docs.docker.com/engine/install/) is installed on your system.
2. Pull the docker image:

```bash
docker pull ghcr.io/thatonecodes/examtopics-downloader:latest
```

3\. Run the container:

```bash
docker run -it \
  --name examtopics-downloader \
  ghcr.io/thatonecodes/examtopics-downloader:latest \
  -p google -s devops \
  -save-links -o output.md
docker cp examtopics-downloader:/app/output.md .
docker cp examtopics-downloader:/app/saved-links.txt .
docker rm examtopics-downloader
```

> [!NOTE]  
> If seeing `exec: format exec error` or warnings about unsuportted platforms, if you are on `linux/arm64`, modify the docker cmd to:

```bash
docker run -it \
  --name examtopics-downloader \
  --platform linux/arm64 \
  ghcr.io/thatonecodes/examtopics-downloader:latest \
  -p google -s devops \
  -save-links -o output.md
docker cp examtopics-downloader:/app/output.md .
docker cp examtopics-downloader:/app/saved-links.txt .
docker rm examtopics-downloader
```

### Using Dockerfile

1. `git clone https://github.com/thatonecodes/examtopics-downloader` and make sure docker is installed on your system.
2. Run `docker build -t examtopics-dl . && docker run --rm examtopics-dl -p google -s devops -save-links -o output.md`
3. After setup, it will give you a list of exams with the `cisco` provider.

### Building from Source

Both binaries cross-compile cleanly with no CGO and no native modules.

```bash
git clone https://github.com/thatonecodes/examtopics-downloader && cd examtopics-downloader

# Go CLI (Go ≥ 1.24)
go build -trimpath -ldflags="-s -w -X main.version=$(git rev-parse --short HEAD)" \
  -o examtopicsdl ./cmd
# … or cross-compile for another OS:
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o examtopicsdl-linux-arm64 ./cmd

# Bun web server (Bun ≥ 1.3)
( cd web && bun install --frozen-lockfile \
  && bun build --compile --minify src/server.tsx --outfile ../examtopics-web )
```

For ad-hoc runs without building:

```bash
go run ./cmd -p cisco -exams         # legacy flag form
go run ./cmd fetch -p cisco -exams   # subcommand form
( cd web && bun run dev )            # hot-reload web server
```

Pre-built binaries land on the [Releases](https://github.com/thatonecodes/examtopics-downloader/releases) page when a `vX.Y.Z` tag is pushed; the matrix covers `linux/amd64`, `linux/arm64`, `windows/amd64`, `windows/arm64` (Go) and `linux/amd64`, `linux/arm64`, `windows/amd64` (Bun web).

## `fetch` subcommand: command line arguments

Equivalent to the legacy flag-only form (`examtopicsdl -p ... -s ...`).

```
Each command line argument you can provide when running the program:

  -c	Optionally include all the comment/discussion text
  -exams
    	Optionally show all the possible exams for your selected provider and exit
  -no-cache
    	Optional argument, set to disable looking through cached data on github
  -o string
    	Optional path of the file where the data will be outputted (default "examtopics_output.md")
  -p string
    	Name of the exam provider (default -> google) (default "google")
  -s string
    	String to grep for in discussion links (required)
  -save-links
    	Optional argument to save unique links to questions
  -sqlite string
    	Optional path to a SQLite DB. When set, scraped data is written directly into this DB
    	(cache JSON preserves all fields; manual fallback writes a subset). When -sqlite is set
    	without an explicit -o, the legacy Markdown writer is skipped.
  -t string
    	GitHub PAT for cached scrape (env GH_PAT used when flag is empty; .env auto-loaded)
  -type string
    	Optionally include file type (default -> .md) (default "md")
```

## Possible Arguments List

### Exam Providers, `-p`

| Provider (-p)    | View Exams                                                                     | Notes     |
| ---------------- | ------------------------------------------------------------------------------ | --------- |
| amazon           | [Amazon Exams](https://www.examtopics.com/exams/amazon/)                       | AWS Certs |
| cisco            | [Cisco Exams](https://www.examtopics.com/exams/cisco/)                         |           |
| comptia          | [CompTIA Exams](https://www.examtopics.com/exams/comptia/)                     |           |
| salesforce       | [Salesforce Exams](https://www.examtopics.com/exams/salesforce/)               |           |
| fortinet         | [Fortinet Exams](https://www.examtopics.com/exams/fortinet/)                   |           |
| juniper          | [Juniper Exams](https://www.examtopics.com/exams/juniper/)                     |           |
| isaca            | [ISACA Exams](https://www.examtopics.com/exams/isaca/)                         |           |
| vmware           | [VMware Exams](https://www.examtopics.com/exams/vmware/)                       |           |
| isc2             | [ISC2 Exams](https://www.examtopics.com/exams/isc2/)                           | CISSP etc |
| servicenow       | [ServiceNow Exams](https://www.examtopics.com/exams/servicenow/)               |           |
| google           | [Google Exams](https://www.examtopics.com/exams/google/)                       |           |
| microsoft        | [Microsoft Exams](https://www.examtopics.com/exams/microsoft/)                 |           |
| ec-council       | [EC-Council Exams](https://www.examtopics.com/exams/ec-council/)               | CEH etc   |
| oracle           | [Oracle Exams](https://www.examtopics.com/exams/oracle/)                       |           |
| paloaltonetworks | [Palo Alto Networks Exams](https://www.examtopics.com/exams/paloaltonetworks/) |           |

> [!NOTE]  
> The more the amount of exams/discussion the provider has, the longer it will take to scrape through the exams.

### `-save-links` && `-output-save-links`

This is a bool flag, so the default is that it's set to `false`, deactivated. If `-save-links` is false `-output-save-links` will do nothing.
`-output-save-links` is a `string` which includes the output path for the saved links, default is `saved-links.txt`.

### Grep String, `-s`

The `-s` argument can take an exam ID (ex. 200-301) or a word, such as "devops". for example:

```bash
go run . -p google -s devops
```

would get all exams from the `google` provider containing the string `devops`.

### Comments and output, `-c` && `-o`

The `-c` argument is another bool flag, so it is defaultly set to false(as it creates a lot of noise in the `.md` file), but you can include it by adding the flag.
`-o` is the output path, based on `os.create(path)`, in the current working directory.

### Exams output, `-exams`

This argument will display output defaulted to such as and exit immediately.

```
Exams for provider 'google'

https://www.examtopics.com/exams/google/adwords-fundamentals/
https://www.examtopics.com/exams/google/associate-android-developer/
https://www.examtopics.com/exams/google/associate-cloud-engineer/
https://www.examtopics.com/exams/google/associate-data-practitioner/
https://www.examtopics.com/exams/google/associate-google-workspace-administrator/
https://www.examtopics.com/exams/google/cloud-digital-leader/
https://www.examtopics.com/exams/google/display-advertising/
https://www.examtopics.com/exams/google/google-analytics/
https://www.examtopics.com/exams/google/gsuite/
https://www.examtopics.com/exams/google/individual-qualification/
https://www.examtopics.com/exams/google/mobile-advertising/
https://www.examtopics.com/exams/google/professional-chromeos-administrator/
https://www.examtopics.com/exams/google/professional-cloud-architect/
https://www.examtopics.com/exams/google/professional-cloud-database-engineer/
https://www.examtopics.com/exams/google/professional-cloud-developer/
https://www.examtopics.com/exams/google/professional-cloud-devops-engineer/
https://www.examtopics.com/exams/google/professional-cloud-network-engineer/
https://www.examtopics.com/exams/google/professional-cloud-security-engineer/
https://www.examtopics.com/exams/google/professional-collaboration-engineer/
https://www.examtopics.com/exams/google/professional-data-engineer/
https://www.examtopics.com/exams/google/professional-google-workspace-administrator/
https://www.examtopics.com/exams/google/professional-machine-learning-engineer/
https://www.examtopics.com/exams/google/search-advertising/
https://www.examtopics.com/exams/google/shopping-advertising/
https://www.examtopics.com/exams/google/video-advertising/
```

### Token Input, `-t`

When you add you `Github` PAT, it allows for more requests to the API, (up to 5000) which is needed when scraping bigger things.
The cached data helps you access big dumps faster.

If `-t` is empty, the program reads `GH_PAT` from the environment. A `./.env` file in the working directory is auto-loaded on startup (existing env vars win), so you can keep the PAT in `.env` instead of exporting it each session.

### SQLite Output, `-sqlite`

Write scraped questions directly into a SQLite DB, skipping the Markdown intermediate:

```bash
go run ./cmd fetch -p amazon -s soa-c03 -c -sqlite soa-c03.db   # or ./examtopicsdl fetch ...
```

- The DB schema covers `questions` (id, exam, topic, question_number, question_text, suggested_answer, confirmed_answer, timestamp, url UNIQUE, comments) and `choices` (question_id, label, text). `_ja` columns are reserved for translations populated separately by `tools/translate.py`.
- Cache path preserves the full JSON payload; manual fallback writes a subset.
- When `-sqlite` is set without an explicit `-o`, the Markdown writer is skipped (no `examtopics_output.md` clobber). Pass both `-sqlite` and `-o` to emit both formats in a single run.
- Exits non-zero with a hint if zero questions matched (catches silent `-s` typos that the MD path swallowed as "Found 0 unique matching links"). The GitHub cache caps directory listing at 1000 entries — exams alphabetically after `AWS-Certified-SAP-on-AWS` miss the cache and need the manual fallback.

### No Cache Arg, `-no-cache`

When you add this argument, it tells the program to ignore the cached `Github` repoitories of updated exam info, however the scraper will take longer than the cache.
Useful when wanting to scrape realtime data.

### File Type, `-type`

When you use the `-type` argument, it tells the program to convert the default filetype of `.md` files to the option of your choice.  
Currently we have these types supported:  
- `html` -> generates `examtopics_output.html`
- `pdf` -> generates `examtopics_output.pdf`
- `txt` -> generates `examtopics_output.txt`
- `json` -> generates `examtopics_output.json`

> [!NOTE]  
> Files are kept in same/similar format as you would see in the `.md` file, for formatting changes, use other arguments.

> [!NOTE]  
> The `json` type emits structured data (one object per question, with `title`, `header`, `content`, `questions`, `answer`, `timestamp`, `question_link` and `comments`), suitable for programmatic use such as flash-card generators, study apps or further scripting.

## [For outputted file examples, see the examples folder](examples/google_devops.md)

## Demo

So, you have installed `go` on your system, and you're inside of the working directory. Let's say you would like the questions for the cisco exam 200-301.

Open your terminal and run:

```bash
go run . -p cisco -s 200-301
```

Note that you can put the id as the string to look for, as the program is compatible this way also.

After waiting a few moments, you would see the output end with:

```bash
Successfully saved output to {OUTPUT_LOCATION}.
```

If so, hooray, you have successfully saved all/most of the questions in a `.md` file!
The format would be such as (older, only scraping format):

```
----------------------------------------

## Exam 200-301 topic 1 question 532 discussion

Actual exam question from

Cisco's
200-301

Question #: 532
Topic #: 1

[All 200-301 Questions]

Refer to the exhibit. An engineer configured NAT translations and has verified that the configuration is correct. Which IP address is the source IP after the NAT has taken place?
Suggested Answer: D 🗳️

A. 10.4.4.4

B. 10.4.4.5

C. 172.23.103.10

D. 172.23.104.4

**Answer: D**

**Timestamp: Jan. 5, 2021, 9:48 p.m.**

[View on ExamTopics](https://www.examtopics.com/discussions/cisco/view/41599-exam-200-301-topic-1-question-532-discussion/)

----------------------------------------
```
