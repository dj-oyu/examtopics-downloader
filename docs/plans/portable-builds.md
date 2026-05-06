# Portable Multi-Platform Builds (Plan)

Status: 計画段階 (実装前)
Branch: `chore/portable-builds`
Last updated: 2026-05-06

## 1. 目的とスコープ

- Go CLI (`cmd/main.go` → `examtopicsdl`) と Bun web サーバ (`web/src/server.tsx` → `examtopics-web`) の両方を、配布可能な単一バイナリとしてビルドする。
- ターゲット: `windows/amd64`, `windows/arm64`, `linux/amd64`, `linux/arm64` (4 プラットフォーム × 2 バイナリ = 計 8 成果物。ただし Bun の windows/arm64 は要検証。後述)。
- ビルド前に静的チェック (formatter, linter, test) を必ず通す。
- 実行時に生成される SQLite DB (`*.db`) と log file (`logs/`, `log.txt`) はバイナリには同梱せず、実行時にカレント or 設定パスへ生成する。
- CI は GitHub Actions で実行し、tag push 時にリリース成果物として attach、PR 時には smoke build のみ。

非スコープ (今回はやらない):
- macOS ビルド (必要になったら matrix に追加するだけなので拡張容易)
- Docker イメージ更新 (既存の `docker.yml` はそのまま残す)
- インストーラ / コード署名

追加目的 (本リビジョンで追記):
- **Web フロントエンドからのデータ取得 UI**: 現在 `go run ./cmd/main.go -p amazon -s saa-c03 -sqlite saa-c03.db` を手動で叩いている取得ステップを、Bun web 上の画面 (フォーム POST) からトリガできるようにする。Web から Go バイナリを子プロセス起動する形にする (新たに HTTP API を生やすより配布が単純)。
- **設定の一元化 (`config.json` + secrets)**: ビルド済みバイナリでも、ユーザが「DB ディレクトリはここ、ログはここ」を一度書けば Go / Bun 両方が同じ設定を読む形にする。秘密値 (GitHub PAT, admin token) は JSON に書かず env / `.env` に分離する。
- **インタラクティブ CLI クライアント**: 取得済み問題に対する出題ループ・正誤履歴記録・和訳実行を、Web を立ち上げなくても CLI 単体で行える `examtopicsdl quiz` / `examtopicsdl translate` サブコマンドを用意する。これらも `config.json` を共通読み込みする。
- **翻訳クライアントの抽象化 (LLM 非依存)**: 翻訳実行は gemini 固定にせず、`gemini` / `claude` / `codex` / 任意 (`exec`) を `config.json` で切替可能にする。skill の中身は **クライアント横断のポータブル文書** 1 つを真とし、クライアントごとの配置/呼出規約は薄いアダプタが吸収する。Go バイナリには portable skill を `//go:embed` で同梱し、起動時に各クライアントが期待するレイアウトへ展開する。

## 2. 現状把握

| 項目 | 現状 |
| ---- | ---- |
| Go バージョン | `go.mod` で `go 1.25.0` (CI は 1.24 のまま — ズレあり) |
| Go SQLite | `modernc.org/sqlite` (pure Go, **CGO 不要**) ← ✅ クロスコンパイル容易 |
| Bun ランタイム | `web/bunfig.toml` + `package.json` (hono + marked + bun:sqlite) |
| Bun SQLite | `import { Database } from "bun:sqlite"` (Bun 組み込み) ← ✅ ネイティブモジュール不要 |
| 既存 CI | `.github/workflows/go-tests.yml` (test のみ), `docker.yml` (linux/amd64,arm64 image) |
| 静的チェック | `go test` のみ。Go の vet/fmt/lint と Bun の typecheck/format は未実行 |
| Bun の外部ファイル依存 | `web/src/db.ts` が `migrations/*.sql` と repo ルートの `*.db` を `readFileSync` で読む |

主な留意点:
1. **Bun の windows/arm64 サポート**: `bun build --compile --target=bun-windows-arm64` は知識カットオフ時点で未提供 (公式ターゲットは `bun-linux-x64`, `bun-linux-x64-baseline`, `bun-linux-arm64`, `bun-windows-x64`, `bun-windows-x64-baseline`, `bun-darwin-x64`, `bun-darwin-arm64`)。実装時に再確認し、未対応なら "Bun は windows/arm64 を欠番" と決める。
2. **Bun コンパイル時の外部ファイル**: `migrations/*.sql` は `--compile` 後の単一バイナリには自動では含まれない。`import sql from "./migrations/01.sql" with { type: "text" }` で埋め込むか、バイナリと同じ階層に `migrations/` を配布する 2 択。本計画では **text import で埋め込み** を選択 (single-file ポータビリティ優先)。
3. **DB ファイル発見ロジック**: `db.ts` は `PROJECT_ROOT = resolve(import.meta.dir, "../..")` でルートを決めて `*.db` を列挙している。`bun --compile` 後は `import.meta.dir` が virtual path になるため、`process.cwd()` か `EXAMTOPICS_DATA_DIR` env か `path.dirname(process.execPath)` を優先する切替を実装する必要がある。
4. **CGO**: Go は `CGO_ENABLED=0` を明示してビルド (`modernc.org/sqlite` が pure Go なので問題ないが、誰かが将来 `mattn/go-sqlite3` を入れたときに早期検知できるようガードする)。
5. **バージョンスキー**: `go.mod` は 1.25 だが CI は 1.24。1.25 に統一 or `stable` 指定にする。
6. **runtime TS transpile (`web/src/client/loader.ts`)**: SSR 時にクライアント用 `.ts` を `import.meta.dir` 起点で `readFileSync` し、`Bun.Transpiler.transformSync` でブラウザ向け JS へ変換している (現状 `thread-live.ts`, `question-live.ts` の 2 本)。`bun --compile` 後は `.ts` ソースが FS に存在せず `statSync` で落ちるため、**text import への移行が必須**。`import threadLiveTs from "./thread-live.ts" with { type: "text" }` 形に書き換え、loader はキャッシュ層と Transpiler 呼び出しだけ残す (`Bun.Transpiler` 自体は compile binary でも利用可)。将来クライアント TS が増えたら明示インポートでマップに足す運用にする。
7. **`AGENTS.md` の埋め込み (`web/src/agent.ts`)**: `loadRulesExcerpt()` がリポジトリルートの `AGENTS.md` を `readFileSync` し、`<!-- AGENT_REPLY_PROMPT_START/END -->` マーカで囲まれた区間を explanation エージェントのプロンプトに差し込んでいる。配布バイナリには `import agentsMd from "../../AGENTS.md" with { type: "text" }` で焼き付け、ファイル不在時の空文字フォールバックは撤去する (バイナリでは常に存在保証されるため)。
8. **Web 側依存ライブラリ**: `web/package.json` の dependencies は `hono`, `marked` の 2 つのみで、両方 pure JS。ネイティブモジュール / WASM / `web/public/` 配下の静的アセット は **存在しない**。バイナリ化の阻害要因は全て上記の runtime fs アクセス側に局所化されており、依存追加・削除は不要。

## 3. アプローチ概要

### 3.1 Go (`examtopicsdl`)

クロスコンパイルは `GOOS`/`GOARCH` だけで済む (CGO なし)。
- 静的チェック: `gofmt -l`, `go vet ./...`, `golangci-lint run`, `go test -race ./...`
- ビルド: `CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags="-s -w -X main.version=$tag" -o examtopicsdl[ext] ./cmd`
- 4 ターゲットを matrix で並列。

### 3.2 Bun (`examtopics-web`)

- 静的チェック: `bun fmt --check` (Bun 1.2+ にビルトイン。なければ Biome 導入)、`bun run typecheck`、`bun test`
- 単一バイナリ化: `bun build --compile --minify --sourcemap --target=$target src/server.tsx --outfile examtopics-web[ext]`
- 外部ファイル埋め込み:
  - `migrations/*.sql` → `import` 構文で text として取り込み、`db.ts` を「ファイル列挙でなく明示インポート配列」へ書き換える。
  - `*.db` は **同梱しない** (運用時生成 / ユーザ提供データ)。`EXAMTOPICS_DATA_DIR` env または `--data <dir>` 引数 (なければ `process.cwd()`) で探索する。
- 3 (or 4) ターゲット: linux-x64, linux-arm64, windows-x64, (windows-arm64 — Bun 対応次第)。

### 3.3 ランタイム生成物の方針

| 種別 | 生成パス (デフォルト) | 切替手段 |
| ---- | -------------------- | -------- |
| Go CLI が書く `*.db` | `-sqlite` 引数で指定 (現状通り) | フラグ |
| Web が読む `*.db` | `EXAMTOPICS_DATA_DIR` (default: `process.cwd()`) | env |
| Web のログ (`logs/agent.log`) | `EXAMTOPICS_LOG_DIR` (default: `<DATA_DIR>/logs`) | env |
| `log.txt` (ad-hoc) | 削除 / 廃止 (どこから書いているか確認のうえ撤去) | — |

## 3.4 Web → Go の取得 UI 連携

現状: 取得は手動で `go run ./cmd/main.go -p amazon -s <slug> -sqlite <db>`、Web は別プロセスで起動。

提案: Web に「取得画面」を追加し、フォーム POST を受けて **Go バイナリを子プロセス起動** する。HTTP API を Go 側に生やすより、配布物の数も依存も増えない。

```
[browser] --POST /admin/fetch--> [bun web] --spawn--> [examtopicsdl]
                                     |                     |
                                     |<-- stdout/stderr ---|
                                     |   (進捗を SSE で配信)
                                     |
                                  writes <slug>.db に直接書き込み
                                     |
[browser] <-- SSE /admin/fetch/log -- [bun web]
```

### サーバ実装方針 (`web/src/server.tsx`)

- 新ルート:
  - `GET /admin/fetch` … provider/slug 入力フォーム + 進行中ジョブの状態表示
  - `POST /admin/fetch` … パラメータ検証してジョブ起動 (1 ジョブ並列上限 = 1)
  - `GET /admin/fetch/log` … SSE で stdout/stderr を逐次配信、終了コードで close
- バイナリ解決順 (`resolveDownloaderPath()`):
  1. env `EXAMTOPICS_DOWNLOADER_BIN`
  2. `path.dirname(process.execPath)` 直下の `examtopicsdl` (`.exe` on win)
  3. `process.cwd()` 直下の `examtopicsdl`
  4. `which examtopicsdl` (PATH)
- 子プロセス起動: `Bun.spawn([bin, "-p", provider, "-s", slug, "-sqlite", path.join(dataDir, `${slug}.db`)], { env: process.env, cwd: dataDir })`
- 入力検証:
  - `provider` は `internal/constants` のホワイトリストと同じ集合 (Bun 側にミラー定数を持つ or `examtopicsdl -providers` のようなサブコマンドを追加して動的に得る)
  - `slug` は `^[A-Za-z0-9._-]+$` (既存 SLUG_RE を再利用)
- 認可: `POST /admin/*` は単純な `EXAMTOPICS_ADMIN_TOKEN` env 一致を要求。未設定なら起動時に warning 出して 503 を返す (生公開を避ける)。
- セキュリティ: shell を介さず `argv` 配列で起動、ユーザ入力を flag 値に直接渡してもインジェクションは出ないが、必ず正規表現でホワイトリスト。

### CLI 側の小改修

子プロセス連携で必要になる:
1. `cmd/main.go` の `log.Printf` を `stdout` 一本化 + 行バッファ flush (SSE で読みやすくするため `bufio.NewWriter` は使わない)
2. 進捗バー (`cheggaaa/pb`) は TTY 検出して非 TTY (=spawn 時) では off → 既に `pb` は自動判定するはずだが要確認
3. `-providers` (or `-providers-json`) サブコマンドを追加し、Web 側がプロバイダ一覧を JSON で取得可能にする (定数の二重管理を避ける)

## 3.5 設定の一元化: `config.json` + secrets 分離

現状の課題:
- Go CLI は `.env` を cwd からのみ読む。ビルド後バイナリで cwd が変わると壊れる。
- Web は `process.env` と `import.meta.dir` 起点のパス決め打ち。配布バイナリで詰まる。
- DB ディレクトリ・ログディレクトリ・サーバポートなど「秘密でない設定」が散らばっている (env / フラグ / コード内ハードコード混在)。

### 方針: 二層構成

**構造設定 (非秘密)** → `config.json` 1 ファイルに集約。Go / Bun 両方が同じファイルを読む。
**秘密値 (PAT, admin token)** → 引き続き env / `.env`。JSON には書かない (誤コミット・ログ出力リスクを下げる)。

優先順位 (高 → 低、上位が下位を上書き):
1. CLI フラグ (`-sqlite`, `-data-dir` など)
2. プロセス環境変数 (`GH_PAT`, `EXAMTOPICS_DATA_DIR`, `EXAMTOPICS_ADMIN_TOKEN`, ...)
3. `.env` (env と同じキー空間。env が既に set 済みなら無視 — 現 `LoadDotEnv` の挙動を維持)
4. `config.json` (構造設定のみ)
5. ビルトインのデフォルト

### `config.json` スキーマ (案)

```jsonc
{
  "$schema": "./config.schema.json",
  "dataDir": "~/examtopics-data",          // *.db の置き場 (web が読む / cli が書く)
  "logDir": "~/examtopics-data/logs",      // 省略時 dataDir/logs
  "downloaderBin": "",                      // 空なら自動解決 (§3.4 と同じ順)
  "web": {
    "host": "127.0.0.1",
    "port": 8787,
    "adminEnabled": true                    // false なら /admin/* を 404 化
  },
  "scrape": {
    "defaultProvider": "amazon",
    "noCache": false
  }
}
```

スキーマ:
- 秘密値の項目は **意図的に置かない** (`ghPat` 的なフィールドを生やさない)
- `~` 展開は読み込み時に `os.UserHomeDir()` で解決
- 未知キーは warn して無視 (将来追加に備える)
- `config.schema.json` を repo にコミットして IDE 補完を効かせる (任意)

### 設定ファイルの探索順 (Go / Bun 共通)

1. env `EXAMTOPICS_CONFIG` で明示指定 (CI / Docker)
2. `--config <path>` フラグ (Go CLI のみ追加)
3. `process.cwd()/config.json`
4. バイナリと同階層 (`os.Executable()` / `process.execPath`) の `config.json`
5. ユーザ設定ディレクトリ:
   - Linux/macOS: `$XDG_CONFIG_HOME/examtopics/config.json` (default `~/.config/examtopics/config.json`)
   - Windows: `%APPDATA%\examtopics\config.json`
6. なければビルトインデフォルトのみで動作 (config なしでも起動可能を維持)

### `.env` の探索順 (秘密値専用)

config.json と **同じ階層を順に見る**。両方を同じディレクトリに置けば、構造と秘密がワンセットで運ぶ:
1. env `EXAMTOPICS_ENV_FILE`
2. `process.cwd()/.env`
3. バイナリ同階層
4. `$XDG_CONFIG_HOME/examtopics/.env` / `%APPDATA%\examtopics\.env`
5. なければ既存環境変数のみ

### Go 実装スケッチ

```go
// internal/config/config.go (新規)
type Config struct {
    DataDir       string `json:"dataDir"`
    LogDir        string `json:"logDir"`
    DownloaderBin string `json:"downloaderBin"`
    Web struct {
        Host         string `json:"host"`
        Port         int    `json:"port"`
        AdminEnabled bool   `json:"adminEnabled"`
    } `json:"web"`
    Scrape struct {
        DefaultProvider string `json:"defaultProvider"`
        NoCache         bool   `json:"noCache"`
    } `json:"scrape"`

    // 読み込み元 (デバッグ用、JSON 出力対象外)
    LoadedFrom string `json:"-"`
}

func Load() (*Config, error) {
    cfg := defaults()
    for _, p := range candidatePaths() {
        if data, err := os.ReadFile(p); err == nil {
            if err := json.Unmarshal(data, cfg); err != nil {
                return nil, fmt.Errorf("parse %s: %w", p, err)
            }
            cfg.LoadedFrom = p
            break
        }
    }
    cfg.applyEnvOverrides()
    cfg.expandHomeAndAbs()
    return cfg, nil
}
```

`cmd/main.go` 起動シーケンス:
```go
utils.LoadDotEnvAuto()          // 秘密値を env に流し込む
cfg, err := config.Load()        // config.json をロード (env 上書きは内部で適用)
// ... フラグ flag.Parse() の後で flag が cfg を上書き
```

### Bun 実装スケッチ

`web/src/config.ts` (新規):
```ts
export interface Config { /* JSON と同じ形 */ }

export function loadConfig(): Config {
  for (const p of candidatePaths()) {
    if (existsSync(p)) {
      const raw = JSON.parse(readFileSync(p, "utf-8"));
      return applyEnvOverrides(withDefaults(raw));
    }
  }
  return applyEnvOverrides(defaults());
}
```

`db.ts` の `PROJECT_ROOT` を `loadConfig().dataDir` 起点に置換。`.env` は Bun が cwd から自動で読むので、まず `process.chdir(cfg.dataDir or binaryDir)` するか、明示 `dotenv` ロード関数を実装する。

### 移行戦略

- 既存の `.env` (PAT のみ) は **そのまま動く**。`config.json` がなくてもデフォルトで起動できる設計を維持。
- 既に存在する `EXAMTOPICS_DATA_DIR` 等の env 変数も互換維持 (config.json より優先)。
- 初回起動時、`config.json` が見つからなければ「テンプレを `~/.config/examtopics/config.json` に書きますか?」を `examtopicsdl -init-config` サブコマンドで提供 (任意・後回し可)。

### セキュリティ留意

- **JSON に PAT を書かない**: スキーマに項目を作らない、ドキュメントにも例示しない。書いてあったら parse 時に warn (`"ghPat" is not allowed in config.json — use .env or environment variable instead`)。
- Web 管理 UI からは設定ファイルの値を **表示のみ**、編集 API は作らない (誤操作で PAT を JSON 側に流す危険を避ける)。
- ログ出力は **パスのみ**、JSON の中身は出さない。

## 3.6 CLI クライアント (サブコマンド化) と gemini skill

### サブコマンド構成

現状の `examtopicsdl` は `flag` パッケージで「取得専用 CLI」になっている。配布バイナリ 1 本で取得・出題・和訳をカバーするため、**サブコマンド方式** に改める。

```
examtopicsdl <subcmd> [flags]

  fetch       既存の取得ロジック (今のフラグ群はそのまま fetch のフラグへ)
  quiz        対話的に出題して正誤を記録
  translate   gemini-cli を呼んで未訳行を和訳しつつ DB 更新
  providers   provider 一覧を JSON で出力 (Web 連携用、§3.4)
  config      設定の解決結果を JSON で表示 (デバッグ用)
  version     -version の代替

  (引数なし、または既知でない先頭トークンは fetch の旧互換として扱う)
```

- `flag.NewFlagSet` を subcmd ごとに分けるか、shipping 時に `urfave/cli/v2` などへ移行するか。**初手は `flag.NewFlagSet` ベース** で外部依存を増やさない。
- 既存ユーザが `examtopicsdl -p amazon -s saa-c03` をそのまま叩いても動くよう、第 1 引数が unknown subcmd か `-` で始まるなら `fetch` 互換にフォールバックさせる。

### `examtopicsdl quiz` 仕様

- 入力: `-db <path>` (省略時 `config.json` の `dataDir/<slug>.db`)、`-slug <exam>`、`-filter <all|incorrect|never|tagged:foo>`、`-shuffle`、`-limit N`、`-japanese` (訳優先表示)
- 出題ループ: 問題本文 → 選択肢 → 入力受付 (`A`/`B`/`AC` 等) → 正誤判定 → 解説/コメント表示 → 次へ
- 永続化: 既存の `attempts` テーブルにそのまま書き込む。Web の閲覧画面と履歴を共有できる。
- 終了: `q` または Ctrl+C。途中までの集計サマリ (正答率、要復習リスト) を出して終了。
- 端末描画: ANSI カラー + ターミナル幅検出 (`golang.org/x/term`)。非 TTY 時はカラー無効化。
- 入力検証: 大文字小文字無視、複数選択は文字並び順無視 (`CA` == `AC`)。
- マルチターン解説スレッド連携 (memo の `project_explanation_threads`): `?` キーで「この問題に解説スレッドを開く」→ Web の `explanation_threads` に行を立てるだけ (実 LLM 起動は Web のエージェント側)。

### `examtopicsdl translate` 仕様

現状 `tools/translate.py` (Python) で行っている処理を Go から呼べる体験にする。実装は **Python 廃止せず LLM-CLI を spawn** する薄いラッパに留める (LLM プロンプト周りは skill ファイルに任せる)。

- 入力: `-db <path>`、`-slug <exam>`、`-batch <N>`、`-only <未訳|全件|失敗のみ>`、`-dry-run`、`-client <gemini|claude|codex|exec>` (省略時 `config.json` の `tools.translate.client`)
- フロー:
  1. DB から未訳行を SELECT → JSON にシリアライズ
  2. 設定された **クライアントアダプタ** に「skill ID + 入力 JSON」を渡し、stdout に翻訳結果 JSON を返してもらう (アダプタの責務は spawn だけ。プロンプトは skill が決める)
  3. 結果 JSON を検証し DB に書き戻し
  4. 進捗バー (`pb`) + 失敗時は raw stdout/stderr を `<logDir>/translate-<ts>.log` に保存
- クライアントが PATH にない場合: `config.json` の `tools.translate.bin` で上書き可、未指定時は PATH 検索。見つからなければ「`<client>` 未インストール」エラーで早期終了。

### 翻訳クライアントの抽象化

各 LLM CLI で `--skill` フラグの有無 / プロンプト渡し方 / 標準入出力の扱い / 設定ファイルの場所が違う。これを 1 つの interface で吸収する。

```go
// internal/translate/client.go
type Client interface {
    Name() string                   // "gemini" / "claude" / "codex" / "exec"
    Prepare(skillsRoot string) error // 必要なら skill を所定の位置へ symlink/copy
    Run(ctx context.Context, in TranslateInput) (TranslateOutput, error)
}

// 実装一覧:
//   geminiClient  spawn `gemini`,  expects   .gemini/skills/   in cwd
//   claudeClient  spawn `claude`,  expects   .claude/agents/   in cwd or ~/.claude/agents/
//   codexClient   spawn `codex`,   instruction を --system / stdin で渡す
//   execClient    config.tools.translate.exec.cmd を生で起動 (任意の自前ラッパ用エスケープハッチ)
```

- 共通 I/O プロトコル: アダプタは「JSON in → JSON out」を満たすだけにする。プロンプトは skill 内で固定し、アダプタ側は「この skill を読んで JSON を返してね」というシステム指示テンプレを各クライアント流儀で組み立てる。
- `Prepare()`: クライアントが要求するレイアウトへ展開する責務をここに局所化:
  - gemini: `<workdir>/.gemini/skills/exam-translator.md` を配置
  - claude: `<workdir>/.claude/agents/exam-translator.md` (or `~/.claude/agents/exam-translator.md`) を配置
  - codex: skill 全文を `--system` プロンプト or stdin に流し込む (FS 配置不要)
  - exec: ユーザ自前なので何もしない
- 認証情報: 各 CLI が独自に持つ (gemini は `GEMINI_API_KEY`、claude は `ANTHROPIC_API_KEY` 等)。examtopics 側はそれらを **触らない**。`.env` に書きたい人は書ける、というだけ。

### skill の同梱戦略 (クライアント横断)

問題: `.gemini/skills/exam-translator.md` は gemini 固有の置き場所。配布バイナリだけある環境で、しかもユーザが claude や codex を使う場合に skill が無いと translate が動かない。

採用案: **クライアント横断の "portable skill" を 1 ファイル真として持ち、各クライアント形式へ展開する**。

```
リポジトリ構成:
  skills/
    exam-translator.md          ← source of truth (LLM 中立な markdown)
  .gemini/skills/
    exam-translator.md          ← go generate で skills/ から同期 (gemini ローカル運用用)
  internal/skills/assets/
    exam-translator.md          ← go generate で skills/ から同期 (//go:embed 用)
```

```go
// internal/skills/skills.go
//go:embed assets/exam-translator.md
var examTranslatorSkill []byte

// Materialize writes the embedded skill into a per-client subdir under
// the cache root and returns that subdir. Idempotent (byte-identity check).
//
// layoutFor(client) examples:
//   gemini -> <root>/gemini/.gemini/skills/exam-translator.md
//   claude -> <root>/claude/.claude/agents/exam-translator.md
//   codex  -> <root>/codex/exam-translator.md  (file path, not dir)
func Materialize(client string) (workdir string, err error) { ... }
```

- 展開先のキャッシュルート: `XDG_CACHE_HOME/examtopics/skills/` (Win: `%LOCALAPPDATA%\examtopics\skills\`)。`config.json` の `tools.skillsDir` で上書き可。
- ユーザが skill 内容自体を差し替えたい場合: `config.json` の `tools.skillFile` で portable skill ファイルを指定 → そちらを source として `Materialize` する (embed 版より優先)。
- 同期ガード: `go generate` が `skills/exam-translator.md → .gemini/skills/exam-translator.md` と `→ internal/skills/assets/exam-translator.md` を同時にコピー。CI で `git diff --exit-code` をかけてドリフトを禁止。
- skill 先頭にメタヘッダ:

```markdown
---
id: exam-translator
version: 1
clients: [gemini, claude, codex, exec]
io:
  input:  { type: questions-json, schema: ./io/questions-input.schema.json }
  output: { type: questions-json, schema: ./io/questions-output.schema.json }
---

# Exam Translator

You are an English-to-Japanese translator for AWS exam questions...
```

これにより LLM 非依存の skill ファイルを 1 つだけ書けばよくなり、クライアントを足すときはアダプタ実装だけで済む。

### `config.json` 拡張

§3.5 のスキーマに `tools` セクションを追加 (LLM クライアント中立):

```jsonc
{
  // ...既存...
  "tools": {
    "skillsDir": "",          // 空なら埋め込み skill を XDG_CACHE_HOME に展開
    "skillFile": "",          // 空なら埋め込み skill を使用、指定時はそのファイルを source に
    "translate": {
      "client": "gemini",     // gemini | claude | codex | exec
      "bin": "",              // 空なら PATH 検索 (`gemini`, `claude`, `codex`)
      "batch": 8,
      "model": "",            // 空ならクライアントのデフォルト
      "exec": {               // client="exec" のときだけ参照
        "cmd": ["mywrapper"], // argv 配列。stdin に input JSON、stdout に output JSON を期待
        "env": {}             // 追加 env (秘密値はここに書かない、name のみ列挙して値は process.env から引く設計を別途検討)
      }
    }
  },
  "quiz": {
    "shuffle": true,
    "showJapaneseFirst": false
  }
}
```

- `tools.translate.client` が `exec` のときは `tools.translate.exec.cmd` を必須化。validation で漏れていたら起動時 error。
- `bin` を絶対パスにすればクライアント切替なしに「同じ gemini を別バージョンで」のような運用も可能。

### Bun 側との関係

- `quiz` / `translate` は **Go バイナリ単独**。Bun には実装しない (TUI 描画と LLM spawn は Go の方が単純)。
- ただし `config.json` の `quiz.showJapaneseFirst` 等は Web の表示既定値としても流用する想定 (両者が同じファイルを読む利点)。
- gemini-cli を呼ぶ責務は **Go の translate サブコマンドに一本化**。Web からは `examtopicsdl translate ...` を spawn (§3.4 の `/admin/fetch` と同じ仕組み) で再利用する。

### 移行戦略

- 既存 `tools/translate.py` は **当面残す** (運用中なので)。`examtopicsdl translate -client gemini` が python 版と同じ DB を更新できることを CI でゴールデンテスト → 実用安定後に Python 版を deprecate。
- skill の真の置き場所を `skills/exam-translator.md` (新設) に移動し、`.gemini/skills/exam-translator.md` は **`go generate` の生成物** に格下げ:
  - `skills/` を CODEOWNERS / lint 対象に
  - `.gemini/skills/` と `internal/skills/assets/` は `.gitignore` で除外し、ビルド/`go generate` 時に再生成
  - `.gitignore` 既存ホワイトリスト (`!.gemini/skills/exam-translator.md`) は削除
- 各クライアント追加は **アダプタ 1 つ + テストを足すだけ** で済む構造を維持 (claude / codex 追加時に skill 本体を書き直さなくて良い)。

## 3.7 マシン間同期 (multi-host sync)

### 背景と前提

母艦 (Windows desktop, intermittent) + 複数 SBC (常時稼働、現状 SBC-A / SBC-B) の構成で、**どのマシンでも学習・解説スレッド対話を行える**ようにする。母艦は取得・翻訳の独占権を持ち、SBC は配布された DB をベースに学習履歴と Q&A だけを書き込む。

時計前提 (LWW のため必須):
- 母艦: Windows w32time
- SBC-A: `ntpd` (classic) running, `synchronized: yes`
- SBC-B: `systemd-timesyncd` (本計画起草時に有効化済み), `synchronized: yes`
- TZ は表示用なので各機異なって OK (DB は SQLite `CURRENT_TIMESTAMP` = UTC 固定、UUIDv7 内部時刻も epoch ms = UTC)

### 3.7.1 同期対象テーブルと戦略

| テーブル | 性質 | 同期戦略 |
| ---- | ---- | ---- |
| `questions` / `choices` / `discussion` | 母艦のみ書込 (スクレイパ生成物) | **母艦 → 各機への上書きコピー** (`sync content`)。SBC では `fetch` 不可 |
| `schema_version` | migration 履歴 | 同期しない。各機が起動時に migration を流す |
| `attempts` | 学習履歴。追記専用 (1 解答 = 1 行、編集なし) | **G-Set** (`INSERT OR IGNORE` で union) |
| `explanation_messages` | スレッドメッセージ。追記専用 | **G-Set** (同上) |
| `explanation_threads` | スレッド本体。`status`/`agent_session_id`/`closed_at` のみ可変 | **LWW** (`updated_at` が新しい側を採用) |

### 3.7.2 スキーマ変更 (破壊的、migration 003)

既存 INTEGER `AUTOINCREMENT` PK は host 横断で衝突するため廃止。**UUIDv7 (BLOB 16 byte)** を全機共通の論理 PK に採用。

採用根拠:
- BLOB(16) は TEXT(36) より storage 効率良し (約 1/2)
- UUIDv7 先頭 48 bit = epoch ms なので時間順ソート可 (`id > <last_seen>` で増分取得 — phase 2 の HTTP sync で活用)
- `WITHOUT ROWID` で TEXT/BLOB PK 自体が rowid となり、PK lookup の段数が 1 段減る
- URL 化が必要な箇所では **base32 Crockford 26 文字** へエンコード (`/threads/01HZX5K2...` 形式、`internal/uuidx` パッケージで encode/decode)

```sql
-- migration 003_multihost_sync.sql

-- 既存 attempts/explanation_threads/explanation_messages を全廃 (起草時点で母艦データのみ存在、ユーザ確認済みで破棄可)
DROP TABLE IF EXISTS attempts;
DROP TABLE IF EXISTS explanation_messages;  -- FK 解除のため先に
DROP TABLE IF EXISTS explanation_threads;

CREATE TABLE attempts (
  id BLOB(16) PRIMARY KEY,                -- UUIDv7
  question_id INTEGER NOT NULL REFERENCES questions(id),
  selected TEXT NOT NULL,
  is_correct INTEGER NOT NULL,
  attempted_at TEXT NOT NULL,             -- ISO8601 UTC (UUIDv7 内部時刻と整合)
  host_id TEXT NOT NULL                   -- "desktop-win", "sbc-a", "sbc-b" 等
) WITHOUT ROWID;
CREATE INDEX idx_attempts_q ON attempts(question_id);
CREATE INDEX idx_attempts_host_time ON attempts(host_id, attempted_at);

CREATE TABLE explanation_threads (
  id BLOB(16) PRIMARY KEY,
  question_id INTEGER NOT NULL REFERENCES questions(id),
  status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','resolved','dismissed')),
  agent_session_id TEXT,
  created_at TEXT NOT NULL,
  closed_at TEXT,
  updated_at TEXT NOT NULL,               -- LWW タイムスタンプ。可変列 UPDATE のたび bump
  host_id TEXT NOT NULL                   -- 作成元 (デバッグ用)
) WITHOUT ROWID;
CREATE INDEX idx_thr_qid ON explanation_threads(question_id);
CREATE INDEX idx_thr_status ON explanation_threads(status);
CREATE INDEX idx_thr_updated ON explanation_threads(updated_at);

CREATE TABLE explanation_messages (
  id BLOB(16) PRIMARY KEY,
  thread_id BLOB(16) NOT NULL REFERENCES explanation_threads(id) ON DELETE CASCADE,
  role TEXT NOT NULL CHECK (role IN ('user','agent')),
  author TEXT,
  content TEXT NOT NULL,
  reason_code TEXT CHECK (
    reason_code IS NULL OR
    reason_code IN ('comprehension','spec','ambiguous','translation')
  ),
  citations TEXT,
  translation_diff TEXT,
  created_at TEXT NOT NULL,
  host_id TEXT NOT NULL
) WITHOUT ROWID;
CREATE INDEX idx_msg_thread ON explanation_messages(thread_id);
```

実装影響:
- Go 側: `github.com/gofrs/uuid` v5 で UUIDv7 生成 (`uuid.NewV7()`)。`internal/uuidx` パッケージに encode/decode (BLOB ⇄ Crockford base32) を集約
- Bun 側: `Bun.randomUUIDv7()` (Bun 1.2+) で生成。ない環境用に `crypto.randomUUID()` ベースの fallback も用意 (時間ビットを epoch ms で上書き)
- `web/src/db.ts` の `Number(r.lastInsertRowid)` 経路 (`createThread`, `appendMessage`) を **「INSERT 前に UUIDv7 を生成して明示渡し」** に書き換え (4-5 箇所)
- URL ルート: `/threads/:id` の `id` を 26 文字 base32 として受け取る regex に変更 (`/^[0-9A-HJKMNP-TV-Z]{26}$/i`)

### 3.7.3 host_id の付与

- `config.json` に `"hostId": "<id>"` を必須化 (camelCase 統一)
- 未設定なら起動時に `os.Hostname()` の小文字化 + ランダム 4 文字 suffix を生成して `config.json` に **書き戻し**、以後変えない
- 推奨命名例: `desktop-win`, `sbc-a`, `sbc-b`

### 3.7.4 Phase 1: ローカル merge (本ブランチに実装)

#### サブコマンド

```
examtopicsdl sync snapshot -d <local.db> -o <out.db>
  # local.db を VACUUM INTO で整合性のあるスナップショットへ。scp 前段で必須

examtopicsdl sync merge -d <local.db> --from <peer.db>
  # peer.db の append-only 行を G-Set union、threads を LWW 反映。idempotent

examtopicsdl sync content -d <local.db> --from <master.db>
  # questions/choices/discussion を母艦版で上書き (SBC が母艦から content を受け取るとき)
```

#### マージ SQL (sync merge の中身)

```sql
ATTACH DATABASE '<peer.db>' AS r;
BEGIN;

-- G-Sets: id 衝突なしの union
INSERT OR IGNORE INTO attempts SELECT * FROM r.attempts;
INSERT OR IGNORE INTO explanation_messages SELECT * FROM r.explanation_messages;

-- LWW: 新規 thread は挿入、既存は updated_at が新しい側を採用
INSERT OR IGNORE INTO explanation_threads SELECT * FROM r.explanation_threads;
UPDATE explanation_threads SET
  status           = (SELECT status           FROM r.explanation_threads rt WHERE rt.id = explanation_threads.id),
  agent_session_id = (SELECT agent_session_id FROM r.explanation_threads rt WHERE rt.id = explanation_threads.id),
  closed_at        = (SELECT closed_at        FROM r.explanation_threads rt WHERE rt.id = explanation_threads.id),
  updated_at       = (SELECT updated_at       FROM r.explanation_threads rt WHERE rt.id = explanation_threads.id)
WHERE EXISTS (
  SELECT 1 FROM r.explanation_threads rt
   WHERE rt.id = explanation_threads.id
     AND rt.updated_at > explanation_threads.updated_at
);

COMMIT;
DETACH DATABASE r;
```

#### 運用フロー (Phase 1)

```bash
# --- 母艦 → SBC 配布 (取得・翻訳後) ---
examtopicsdl sync snapshot -d saa-c03.db -o /tmp/saa.snap
scp /tmp/saa.snap user@sbc-a:/var/lib/examtopics/incoming.db
ssh user@sbc-a -- examtopicsdl sync content \
  -d /var/lib/examtopics/saa-c03.db \
  --from /var/lib/examtopics/incoming.db

# --- SBC → 母艦 学習結果合流 ---
ssh user@sbc-a -- examtopicsdl sync snapshot \
  -d /var/lib/examtopics/saa-c03.db -o /tmp/saa.snap
scp user@sbc-a:/tmp/saa.snap C:/data/incoming-sbc-a.db
examtopicsdl sync merge -d C:/data/saa-c03.db --from C:/data/incoming-sbc-a.db
```

USB メモリ経由 (air-gap) でも同等手順で動作する。

### 3.7.5 Phase 2: HTTP sync (本ブランチでは未実装、別 PR で追加)

#### 配置の方向 (重要設計判断)

母艦は intermittent / SBC は常時稼働なので、**SBC 側が HTTP サーバ、母艦側がクライアント**の向きで設計する。母艦が起動したタイミングで自分から push/pull できるのが要件。

```
[母艦 (intermittent)]                        [SBC (always-on)]
     |                                              |
     | examtopicsdl sync sync http://sbc-a:3000    |
     |--------- POST /sync/append (rows) ---------->|
     |<-------- GET  /sync/since (rows) ------------|
     |                                              |
```

#### サーバ側 (各 SBC の `examtopics-web`)

- `GET /sync/since?host=<peer_id>&attempt_id=<hex>&msg_id=<hex>&thread_updated=<iso>` — peer の watermark 以降の差分を JSON で返す
- `POST /sync/append` — peer から受け取った append 行と LWW 更新を `INSERT OR IGNORE` + LWW UPDATE で取り込む
- 認可: `EXAMTOPICS_ADMIN_TOKEN` Bearer (§3.4 の `/admin/fetch` と共通)

#### クライアント側 (母艦)

- `examtopicsdl sync push <url> -d <local.db>` — local の差分を peer へ送信
- `examtopicsdl sync pull <url> -d <local.db>` — peer の差分を local へ取り込み
- `examtopicsdl sync sync <url> -d <local.db>` — pull → push の双方向収束 (1 コマンド)
- `examtopicsdl sync sync-all -d <local.db>` — `config.json` の `peers` 配列を全件回す
- `config.json` 例:
  ```json
  {
    "hostId": "desktop-win",
    "peers": [
      "http://sbc-a.local:3000",
      "http://sbc-b.local:3000"
    ]
  }
  ```

#### 増分同期の watermark

ULID v7 の時間順序性により、UUID 比較で正しく未取得行のみ取れる:

```sql
SELECT * FROM attempts
  WHERE id > :last_attempt_id_from_peer;   -- peer 側で発行済み &
                                            -- かつ自分が未受信のもの

SELECT * FROM explanation_threads
  WHERE updated_at > :last_thread_watermark;
```

母艦のみ `sync_watermarks (peer_host_id TEXT PRIMARY KEY, last_attempt_id BLOB, last_msg_id BLOB, last_thread_updated_at TEXT)` テーブルを持つ (migration 004 — Phase 2 で追加、Phase 1 のスキーマには影響しない)。SBC 側は受信専用設計のため watermark テーブル不要。

#### SBC 同士の合流

母艦が intermittent なので、SBC ペア間も cron で `sync sync` を回すと最終収束が早い:

```cron
*/30 * * * *  examtopicsdl sync sync http://sbc-b.local:3000 -d /var/lib/examtopics/saa-c03.db
```

これで「いつでも誰かがオンラインなら時間が経てば全機合流」が成立。

#### Phase 2 を後回しにできる根拠

Phase 1 のスキーマ (UUIDv7 BLOB PK + host_id + LWW updated_at) は Phase 2 の HTTP 増分同期でも**そのまま使える**。Phase 2 で追加するのは:
- HTTP ハンドラ 2 本 (web 側)
- CLI サブコマンド 4 本 (Go 側)
- watermark テーブル 1 つ (母艦のみ)

これらは全て**純追加**で、Phase 1 の SBC 配布物に手を入れる必要がない。よって本ブランチでは Phase 1 のみ実装し、運用上 scp が面倒になった時点で Phase 2 を別ブランチで足す。

### 3.7.6 既知のリスクと対処

| リスク | 影響 | 対策 |
| ---- | ---- | ---- |
| SBC の時計が NTP 失敗で巻き戻る | LWW で新しい更新が古い扱いされ消失 | `examtopicsdl sync` 実行前に `timedatectl` の `synchronized: yes` を確認、no なら abort。README に監視 cron 例を載せる |
| WAL 動作中の DB を直接 scp して不整合 | merge 時に foreign key 違反 / 半端な行 | **`sync snapshot` (`VACUUM INTO`) を必須前段に**。素の `scp <db>` は CLI で warn を出す (md5 で WAL 検出) |
| BLOB UUID の URL encode/decode バグ | 既存 thread/message URL が壊れる | `internal/uuidx` を独立パッケージ化し、ラウンドトリップ property test (`encode(decode(x)) == x` を 10000 ランダム値) を CI に入れる |
| `host_id` を運用中に変更 | 同じ機械の以前のデータが「他 host のデータ」扱いになり重複表示 | `config.json` に書き込んだら以後変更禁止のロックフィールドにする (起動時 hash 確認) |
| Phase 2 で SBC が母艦からの POST を信用しすぎる | 悪意ある peer による任意 INSERT | Bearer token + 受信時に `host_id` フィールドが peer 側 token のオーナーと一致するか検証 |

### 3.7.7 検証シナリオ (3 機ループバック)

```
1. 母艦で saa-c03.db を fetch + 5 問翻訳
2. sync snapshot → scp で 2 SBC へ配布、各 SBC で sync content
3. SBC-A で 3 問解答 (attempts 3 行追加)
4. SBC-B で 1 スレッド作成 (threads 1 行 + messages 1 行追加)
5. 母艦から: scp で 2 SBC のスナップショットを取得 → sync merge を 2 回
6. 母艦のスナップショットを 2 SBC へ再配布 → 各 SBC で sync merge
7. 全機の attempts/threads/messages 件数が一致することを確認
8. 同じ peer.db を再度 sync merge してもデータが変わらないこと (idempotency)
```

これを CI のゴールデンテスト (3 つの一時 DB ファイルでループバック) として自動化。

## 4. GitHub Actions 設計

新規ワークフロー: `.github/workflows/release.yml`

```yaml
name: release

on:
  push:
    tags: ["v*"]
  workflow_dispatch:
  pull_request:
    paths:
      - "cmd/**"
      - "internal/**"
      - "web/**"
      - "go.mod"
      - "go.sum"
      - ".github/workflows/release.yml"

jobs:
  go-check:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: "stable", cache: true }
      - run: gofmt -l . | tee /tmp/fmt && test ! -s /tmp/fmt
      - run: go vet ./...
      - uses: golangci/golangci-lint-action@v6
        with: { version: "latest" }
      - run: go test -race -count=1 ./...

  bun-check:
    runs-on: ubuntu-latest
    defaults: { run: { working-directory: web } }
    steps:
      - uses: actions/checkout@v4
      - uses: oven-sh/setup-bun@v2
      - run: bun install --frozen-lockfile
      - run: bun fmt --check        # 不可なら biome に置換
      - run: bun run typecheck
      - run: bun test

  go-build:
    needs: go-check
    runs-on: ubuntu-latest
    strategy:
      fail-fast: false
      matrix:
        include:
          - { os: linux,   arch: amd64, ext: ""    }
          - { os: linux,   arch: arm64, ext: ""    }
          - { os: windows, arch: amd64, ext: ".exe"}
          - { os: windows, arch: arm64, ext: ".exe"}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: "stable", cache: true }
      - env:
          CGO_ENABLED: "0"
          GOOS: ${{ matrix.os }}
          GOARCH: ${{ matrix.arch }}
        run: |
          mkdir -p dist
          go build -trimpath \
            -ldflags="-s -w -X main.version=${GITHUB_REF_NAME:-dev}" \
            -o dist/examtopicsdl-${{ matrix.os }}-${{ matrix.arch }}${{ matrix.ext }} \
            ./cmd
      - uses: actions/upload-artifact@v4
        with:
          name: examtopicsdl-${{ matrix.os }}-${{ matrix.arch }}
          path: dist/*

  bun-build:
    needs: bun-check
    runs-on: ubuntu-latest
    defaults: { run: { working-directory: web } }
    strategy:
      fail-fast: false
      matrix:
        include:
          - { target: bun-linux-x64,    os: linux,   arch: amd64, ext: ""    }
          - { target: bun-linux-arm64,  os: linux,   arch: arm64, ext: ""    }
          - { target: bun-windows-x64,  os: windows, arch: amd64, ext: ".exe"}
          # bun-windows-arm64: 未対応なら除外。実装時に bun --help で確認。
    steps:
      - uses: actions/checkout@v4
      - uses: oven-sh/setup-bun@v2
      - run: bun install --frozen-lockfile
      - run: |
          mkdir -p dist
          bun build --compile --minify \
            --target=${{ matrix.target }} \
            src/server.tsx \
            --outfile dist/examtopics-web-${{ matrix.os }}-${{ matrix.arch }}${{ matrix.ext }}
      - uses: actions/upload-artifact@v4
        with:
          name: examtopics-web-${{ matrix.os }}-${{ matrix.arch }}
          path: web/dist/*

  release:
    if: startsWith(github.ref, 'refs/tags/v')
    needs: [go-build, bun-build]
    runs-on: ubuntu-latest
    permissions: { contents: write }
    steps:
      - uses: actions/download-artifact@v4
        with: { path: artifacts, merge-multiple: true }
      - uses: softprops/action-gh-release@v2
        with: { files: artifacts/* }
```

注:
- PR 時はチェックジョブだけでなく build も走らせる (smoke)。`release` ジョブだけ tag-only。
- `go-build` / `bun-build` を並列にすることで 4 ターゲットの所要時間が直列より明確に短くなる。
- `golangci-lint` を初導入すると既存コードに違反が出る可能性あり。**初回は warning fix の小さい PR を別途切る**。

## 5. 実装タスク (順番)

1. **計画ドキュメント commit** ← 本 PR の最初のコミット (このファイル)。本計画は **2 段階で commit 済み** — (a) 初稿 (`9766af6`)、(b) Bun バイナリ化のブロッカ追記 + マシン間同期 §3.7 追記 (本 commit)。以降の commit は本リスト 2〜11 の各タスクに対応
2. **Go 側の hardening**:
   - `go.mod` の Go バージョンと CI の `go-version` を揃える (`stable` 推奨)
   - `cmd/main.go` に `var version = "dev"` を追加
   - `internal/config` パッケージ新設 (§3.5 の `Config` / `Load()`)。`hostId` フィールドを必須化 (§3.7.3)、未設定時は `os.Hostname()` + ランダム 4 文字 suffix を生成して書き戻し
   - `internal/utils/dotenv.go` に `LoadDotEnvAuto()` を追加 (config.json と同じ探索順)
   - `internal/uuidx` パッケージ新設 (§3.7.2): UUIDv7 生成 (`gofrs/uuid` v5)、BLOB(16) ⇄ Crockford base32 26 文字の encode/decode、ラウンドトリップ property test
   - 進捗バー / log 出力を非 TTY で扱いやすい形に整える
   - `golangci-lint` の `.golangci.yml` 最小設定 (default linters + `errcheck`, `staticcheck`, `govet`)
3. **Go CLI のサブコマンド化** (§3.6 / §3.7.4):
   - `cmd/main.go` を dispatcher に書き換え (`fetch` / `quiz` / `translate` / `providers` / `config` / `version` / `sync`)
   - 旧フラグの後方互換 fallback (`examtopicsdl -p amazon -s ...` を `fetch` 互換扱い)
   - `internal/quiz` パッケージ (TUI 出題ループ、attempts 永続化 — UUIDv7 を `INSERT` 前に生成)
   - `internal/translate` パッケージ:
     - `Client` interface + `gemini` / `claude` / `codex` / `exec` アダプタ
     - 未訳行の DB 反映ロジック (クライアント非依存)
   - `internal/skills` パッケージ + `//go:embed` で portable skill 同梱、`Materialize(client)`
   - `skills/exam-translator.md` を真とし、`go generate` で `.gemini/skills/` と `internal/skills/assets/` へ同期
   - `.gitignore` から `!.gemini/skills/exam-translator.md` の例外を撤去 (生成物化)
   - `internal/sync` パッケージ + サブコマンド (§3.7.4):
     - `sync snapshot -d <db> -o <out>` (`VACUUM INTO` で WAL 整合スナップショット)
     - `sync merge -d <db> --from <peer.db>` (G-Set + LWW の SQL を ATTACH で実行)
     - `sync content -d <db> --from <master.db>` (questions/choices/discussion 上書きコピー)
     - 実行前に `timedatectl` 同期確認 (Linux のみ; Windows では w32time クエリ) — `synchronized: no` なら abort + ヘルプ
     - 起動時に DB ファイルが WAL 状態 (`-wal`/`-shm` 残存 + 書込中) なら scp 不可警告
   - migration `003_multihost_sync.sql` を追加 (§3.7.2)。既存 attempts/threads/messages を破棄 + UUIDv7 BLOB スキーマ再作成
4. **Web 側の hardening**:
   - `web/src/config.ts` 新設 (§3.5 の Bun 版 `loadConfig`)。`hostId` を `loadConfig()` 経由で読み、INSERT 時に列に反映
   - `db.ts` の migrations 読込を text import 配列へリファクタ (バイナリ単体動作のため)。新規 `003_multihost_sync.sql` を配列に追加
   - `db.ts` の `PROJECT_ROOT` を `loadConfig().dataDir` 起点に置換
   - `db.ts` の PK 関連書き換え (§3.7.2): `Number(r.lastInsertRowid)` を使う `createThread` / `appendMessage` 等を **「INSERT 前に `Bun.randomUUIDv7()` で BLOB(16) を生成して明示渡し」** に変更。`thread_id` / `id` の型を `Uint8Array` (BLOB) に統一
   - URL ルートの id parser 変更: `/threads/:id` の `:id` を 26 文字 Crockford base32 として decode する helper を追加 (`internal/uuidx` の Bun 移植)
   - `client/loader.ts` を text import 化 (§2 注意点 6): `thread-live.ts` / `question-live.ts` を `with { type: "text" }` で取り込み、`readFileSync` + `import.meta.dir` 依存を撤去。loader API (`loadClientScript(name)`) は維持し、内部マップで分岐
   - `agent.ts` の `AGENTS.md` 読込を text import 化 (§2 注意点 7): `loadRulesExcerpt()` を import 値ベースに置換、try/catch フォールバックは撤去
   - `agent_log.ts` の `PROJECT_ROOT` 算出を `loadConfig().dataDir` 起点に置換 (`AGENT_LOG_DIR` env は引き続き優先)
   - `package.json` に `format` / `format:check` スクリプト追加 (Bun fmt or Biome)
   - 翻訳/解説エージェント呼び出しを `examtopicsdl translate` spawn に切替 (Python 版 `tools/translate.py` の依存を将来削除する布石)
   - `bun build --compile` での smoke build を `bun test` の隣に追加し、上記 fs 撤去の回帰を CI で検出
5. **Web の取得 UI 追加** (§3.4):
   - `resolveDownloaderPath()` ヘルパ + `Bun.spawn` 連携 (`examtopicsdl fetch` を呼ぶ)
   - `GET/POST /admin/fetch` ルートと SSE ログストリーム
   - `EXAMTOPICS_ADMIN_TOKEN` 認可
   - 入力検証 (provider ホワイトリスト + slug regex)
   - フォーム/状態表示の View (`web/src/views/AdminFetch.tsx`)
6. **新 workflow** `.github/workflows/release.yml` 追加 (§4)
7. **既存 workflow** `go-tests.yml` の整理 (release.yml の go-check と重複するため削除 or trigger 限定)
9. **動作確認**:
   - PR で smoke build が 4+3 ターゲットで通ること
   - 実バイナリで `config.json` 自動探索が機能すること (cwd / バイナリ隣 / `XDG_CONFIG_HOME`)
   - 実バイナリで `.env` 自動探索が機能すること (同上)
   - `config.json` に `"ghPat"` が混入したら warn が出ること
   - Web 管理画面から取得ジョブを起動し、SSE ログが流れ、`<dataDir>/<slug>.db` が生成されること
   - `examtopicsdl quiz -slug saa-c03` が DB を読んで対話出題できること
   - `examtopicsdl translate -slug saa-c03 -dry-run -client gemini` が embedded skill を展開し gemini を起動できること
   - 同コマンドを `-client claude` / `-client codex` に切り替えても起動できること (skill 配置形式が自動で切り替わること)
   - Windows VM (amd64) で `examtopicsdl.exe fetch -p amazon -exams` が動くこと
   - 旧構文 `examtopicsdl.exe -p amazon -exams` が後方互換で動くこと
10. **README 更新**: バイナリ取得方法 / `config.json` スキーマ / `.env` 配置場所 / 各サブコマンド / Web 取得 UI の使い方を追加
11. tag `v0.1.0` を切ってリリース動作を一発確認

## 6. リスクと対策

| リスク | 影響 | 対策 |
| ------ | ---- | ---- |
| Bun の windows/arm64 がまだ未対応 | windows/arm64 web バイナリが配れない | 実装時に確認 → 未対応なら matrix から除外し README に明記。別途 `npm i -g bun && bun src/server.tsx` の代替手順を案内 |
| `bun --compile` が `readdirSync(migrations)` を埋め込めない | 実行時に migrations 不明エラー | `db.ts` を「text import + 配列定義」にリファクタ (実装タスク 3) |
| Web が読む `*.db` の発見が壊れる | 実行時に DB が見つからない | `EXAMTOPICS_DATA_DIR` env を明示設定する手順をドキュメント化 |
| `golangci-lint` 初導入で大量警告 | CI 赤化で時間ロス | 段階導入: 別 PR で fix、または `enable-only` を最小から始めて段階拡張 |
| `go.mod` 1.25 vs CI 1.24 の不一致 | 既に build 済んでいるが理屈上不整合 | `setup-go` を `stable` (現状 ≥ 1.25) に統一 |
| バイナリサイズが大きい (Bun compile は ~90MB) | 配布コスト | `--minify` + tag リリースのみ artifact 保存 (PR は build のみ、保存なし) |
| `.env` / `config.json` がユーザの `~/.config/examtopics/` に置かれた場合 Bun が自動では読まない | Web プロセスが設定を見つけられず動作不全 | Bun 側にも同じ探索順を実装した `loadConfig` / `loadDotEnv` ローダを書く (§3.5 の `web/src/config.ts`) |
| `config.json` のフィールド命名が Go (snake or camel) と Bun (camel) でズレる | 両方読めなくなる | **camelCase 統一** (例: `dataDir`, `downloaderBin`)。Go 側は `json` タグで明示、JSON Schema も同じ命名に揃える |
| ユーザが PAT を `config.json` に書いてしまう | 誤コミット時に GitHub に PAT 流出 | parse 時に `ghPat`/`GH_PAT`/`token` 系キーを検出したら警告 + 無視。README に「秘密値は .env / env のみ」を太字で記載 |
| Web 管理 UI を生公開すると PAT 経由のスクレイピング権限が漏れる | 悪用リスク | `EXAMTOPICS_ADMIN_TOKEN` 必須化、未設定時は 503、README で localhost / 内部ネットワーク前提を明示 |
| Web から spawn する `examtopicsdl` が PATH 上にない | 取得 UI が動かない | §3.4 の `resolveDownloaderPath()` の探索順 (env → exec dir → cwd → PATH) で吸収。リリースは両バイナリ同梱 zip を Releases に出す方針 |
| サブコマンド化で旧フラグ呼び出しが壊れる | 既存ユーザの自動化スクリプトが落ちる | 第 1 引数が unknown subcmd or `-` 始まりなら `fetch` に dispatch する後方互換層を入れる。最低 1 リリース挟んでから deprecate を検討 |
| 各 LLM CLI の skill 受け渡し方法 (フラグ / env / 設定 dir) が違う or 将来変わる | translate サブコマンドが動かない | クライアントアダプタ層 (`Client` interface) で局所化。新規 CLI 対応はアダプタ 1 個追加で済む構造を維持 |
| embedded skill と repo 内 `skills/exam-translator.md` がドリフト | translate の挙動が repo と配布で食い違う | `skills/` を真とし、`go generate` で `.gemini/skills/` と `internal/skills/assets/` を再生成。CI で `git diff --exit-code` ガード |
| portable skill が「クライアント中立」になりきれず特定 LLM (例 gemini) 流儀の指示に偏る | 他クライアントで翻訳品質が落ちる | skill 本体は LLM 中立 markdown に保ち、クライアント固有の system プロンプトはアダプタ側に持たせる。各クライアントごとにゴールデンテスト (1 問で正常 JSON が返る) を CI で回す |
| LLM API キーが各クライアントで env 名が違う (`GEMINI_API_KEY` / `ANTHROPIC_API_KEY` / `OPENAI_API_KEY`) | 認証エラーで翻訳失敗 | examtopics 側は env を中継しない (各 CLI が自分で読む)。README に「使う client が要求する env を `.env` に書け」とだけ記載 |
| SBC の時計が NTP 失敗で巻き戻る | LWW 同期で新しい更新が古い扱いされ消失 | `sync` 実行前に `timedatectl` の `synchronized: yes` を確認、no なら abort。README に同期監視 cron 例を載せる (§3.7.6) |
| WAL 動作中の DB を直接 scp して不整合 | `sync merge` 時に foreign key 違反 / 半端な行 | `sync snapshot` (`VACUUM INTO`) を必須前段とする。素の `scp <db>` 直叩きを README で非推奨と明記 (§3.7.6) |
| BLOB UUIDv7 の URL encode/decode バグ | 既存 thread / message URL が壊れる | `internal/uuidx` を Go/Bun の双方で実装し、ラウンドトリップ property test (`encode(decode(x)) == x` を 10000 ランダム値) を CI で実行 (§3.7.6) |
| `host_id` を運用中に変更 | 同じ機械の以前のデータが「他 host のデータ」扱いになり重複表示 | `config.json` に書き込み後はロックフィールドにする (起動時 hash 確認、変更時は warn して旧 id を残す) |
| 既存母艦 DB の attempts/threads/messages が破壊的 migration で消える | 学習履歴・スレッドのロスト | 起草時点で母艦のみが存在し本人合意済 (本ブランチ前提)。**migration 003 を流す前に手動バックアップを取る手順を README に明記**。CI でも migration 003 を走らせる前に `examtopicsdl sync snapshot` を実行する手順を例示 |
| Phase 2 で SBC が母艦からの POST を信用しすぎる | 悪意ある peer による任意 INSERT (将来) | Phase 2 実装時に Bearer token + 受信時に `host_id` フィールドが peer 側 token のオーナーと一致するか検証 (§3.7.5) |

## 7. 完了条件

- [ ] `release.yml` が main への push で warm に通る
- [ ] tag push 時に Releases ページに 4×Go + 3〜4×Bun バイナリが出る
- [ ] Linux/Windows どちらかの VM で実バイナリが起動する手動チェック完了
- [ ] **`bun build --compile` 後の単一バイナリで Web が起動し、(a) `migrations/*.sql`, (b) `AGENTS.md`, (c) `client/*.ts` を一切 FS から読まずに正常動作することを確認 (起動後にバイナリ隣の関連ファイルを全削除しても動くこと)**
- [ ] **`config.json` / `.env` が cwd / バイナリ隣 / `XDG_CONFIG_HOME` のいずれにあっても拾えることを Go/Bun 双方で確認**
- [ ] **`config.json` に PAT 系キーが混入したとき warn が出て無視されることを確認**
- [ ] **Web 管理画面 `/admin/fetch` から `examtopicsdl fetch` を spawn でき、SSE ログが流れ、`<dataDir>/<slug>.db` が生成されることを確認**
- [ ] **`EXAMTOPICS_ADMIN_TOKEN` 未設定時は 503 が返ることを確認**
- [ ] **`examtopicsdl quiz` / `translate` がリポジトリ外 (バイナリ単独配置) でも動作する**
- [ ] **`go generate` 後 `skills/exam-translator.md` を真とし、`.gemini/skills/` と `internal/skills/assets/` が byte 一致 (CI ガード)**
- [ ] **`-client gemini` / `-client claude` / `-client codex` のいずれでも translate が起動し、各クライアントの skill 配置レイアウトに展開されることを確認**
- [ ] **旧フラグ構文 `examtopicsdl -p amazon -s ...` が `fetch` サブコマンドへ後方互換 dispatch される**
- [ ] **migration 003 適用後の DB で UUIDv7 BLOB(16) PK が機能し、`Bun.randomUUIDv7()` / `gofrs/uuid` v5 で生成した ID が両言語で互換 (Go で書いた行を Bun で読める / 逆も)**
- [ ] **`internal/uuidx` のラウンドトリップ property test が Go/Bun の双方で通る (`encode(decode(x)) == x` を 10000 ランダム値)**
- [ ] **`/threads/:id` URL に 26 文字 Crockford base32 を渡してアクセスできる (整数 ID へのフォールバックは無し)**
- [ ] **`examtopicsdl sync snapshot` が `VACUUM INTO` で一貫したスナップショットを出力する**
- [ ] **3 機ループバック検証 (§3.7.7) が成功: 母艦 → 2 SBC 配布 → 各機で書込 → 母艦合流 → 再配布 → 全機の `attempts` / `explanation_threads` / `explanation_messages` 行数とコンテンツが一致**
- [ ] **同じ peer.db を 2 回 `sync merge` してもデータが変わらない (idempotency)**
- [ ] **`config.json` の `hostId` 未設定時に自動生成され、再起動後も同じ値が維持される**
- [ ] **`sync` 実行前の時計同期チェックで `synchronized: no` の場合に abort し、ヘルプメッセージを出す**
- [ ] README に取得方法 / `config.json` スキーマ / `.env` 配置場所 / サブコマンド一覧 / 管理 UI の使い方 / **マシン間同期の運用手順 (§3.7.4) と NTP 前提** が入っている
