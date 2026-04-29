# 学習ワークフロー: スクレイプ → SQLite → 和訳

ExamTopics の問題ダンプを構造化して、AIエージェントで和訳までを一気通貫に行うための手順。

```
[examtopics.com]
      │ go run ./cmd/main.go (-c)
      ▼
   *.md (Markdown ダンプ)
      │ uv run tools/md_to_sqlite.py
      ▼
   *.db (SQLite, _ja カラム空)
      │ uv run tools/translate.py  ← AIエージェント (Claude Code / Codex / Gemini CLI 等)
      ▼
   *.db (_ja カラム埋め完了)
```

## 前提

- Go 1.24+ (リポジトリルートで `go run` 可能)
- [uv](https://github.com/astral-sh/uv) (Python スクリプトを依存解決込みで実行)
- AI エージェント CLI を 1 つ (任意。Claude Code, Codex CLI, Gemini CLI, Aider など)

## Step 1. スクレイプ

リポジトリルート (`D:/vscode/aws/examtopics-downloader/`) で実行。

```bash
# 試験スラッグの確認 (初回のみ)
go run ./cmd/main.go -p amazon -exams

# 本番取得 (-c でディスカッションコメントを含める)
go run ./cmd/main.go -p amazon -s soa-c03 -c -save-links -o soa-c03.md
```

**チェックポイント:**
- `Found N unique matching links:` の `N` が 0 でないこと
- 出力 `*.md` が 4行超(ヘッダだけだと失敗)

**`-s` の落とし穴:** 引数はディスカッションリンク中の文字列に対する grep。試験ID(例 `soa-c03`)を小文字で。retired な ID(例 `soa-c02`)は 0件になる。AWS主要試験IDは `../memory/reference_aws_exam_slugs.md` 参照。

**経路差:** `Going to manual scraping, cached data failed.` が出るとHTML経路、コメントが1行に連結される。GitHub PAT を `-t <token>` で渡すとキャッシュ経路がヒットしやすく、コメントが投稿者単位に分解される。

## Step 2. SQLite 変換

```bash
uv run tools/md_to_sqlite.py soa-c03.md -o soa-c03.db
# Parsed N questions (0 blocks skipped), N inserted -> soa-c03.db
```

複数試験を1DBにまとめる場合:

```bash
uv run tools/md_to_sqlite.py saa-c03.md -o aws-exams.db
uv run tools/md_to_sqlite.py soa-c03.md -o aws-exams.db --append   # url で重複弾き
```

### 既知の不具合

`internal/fetch/scraper.go:35` のバグで、マルチセレクト問題(例 `Suggested Answer: BD`)の `**Answer:**` フィールドが先頭1文字に切り詰められる。SQLite では:

- `suggested_answer` ─ Markdown 本文の `Suggested Answer:` 由来。**完全な値** (例 `"BD"`)。マルチセレクト判定はこちらを使う
- `confirmed_answer` ─ `**Answer:**` 由来。**1文字に切り詰め済**。マルチセレクトでは欠損扱い

クエリでマルチセレクトを抽出:
```sql
SELECT id, suggested_answer FROM questions WHERE LENGTH(suggested_answer) > 1;
```

## Step 3. 和訳作成 (AIエージェント駆動)

`tools/translate.py` がDB I/Oを担当、エージェントが翻訳を担当。両者の境界はJSON。

### 進捗確認

```bash
uv run tools/translate.py -d soa-c03.db status
```

### エージェントが踏むループ

```bash
# 1. 未翻訳の1問を取得
uv run tools/translate.py -d soa-c03.db next

# 出力例:
# {
#   "id": 1,
#   "question_text": "A company is using ...",
#   "choices": [{"label":"A","text":"..."}, ...],
#   "suggested_answer": "C",
#   "comments": "...",
#   "existing_ja": { ... }
# }

# 2. エージェントが翻訳して JSON を組み立て、stdin で save
echo '{
  "question_text_ja": "...",
  "explanation_ja":   "...",
  "choices_ja": { "A": "...", "B": "...", "C": "...", "D": "..." }
}' | uv run tools/translate.py -d soa-c03.db save 1

# 3. status で残数 0 になるまで 1〜2 を繰り返し
```

`save` ペイロードは部分更新可能。送ったフィールドだけ反映される。誤訳をやり直すときは:

```bash
uv run tools/translate.py -d soa-c03.db unsave 1   # _ja 全消去
```

### エージェント別の起動例

どのエージェントでも上記のサブコマンドを呼ぶだけ。例:

- **Claude Code** ─ 「`uv run tools/translate.py -d soa-c03.db` で `next` を呼んで翻訳し `save` で書き戻すループを75回。専門用語は AWS 公式の日本語ドキュメント表記に合わせて。」と指示
- **Codex CLI / Gemini CLI / Aider** ─ 同じ指示文を渡す。ツールは `bash` 越しに呼べれば良い

### 翻訳ガイドライン (エージェントへの指示テンプレ)

```
- AWS サービス名は英語のまま (例: Amazon S3, AWS Lambda)
- 設問は丁寧体 (です・ます) ではなく簡潔な平叙文 (である調)
- 各選択肢は同じ文体・文体長を揃える
- explanation_ja には「正解と理由」を 2〜3 文。コメント欄に有用な反論があれば取り込む
- マルチセレクト (suggested_answer の長さ > 1) は冒頭に「(複数選択)」を付ける
```

## スキーマ

```sql
questions(
  id INTEGER PRIMARY KEY,
  exam, topic, question_number,
  question_text, question_text_ja,
  suggested_answer,        -- マルチセレクトでも完全 (例 "BD")
  confirmed_answer,        -- 1文字切り詰めの可能性あり
  explanation_ja,
  timestamp, url UNIQUE, comments
)

choices(
  question_id, label,
  text, text_ja,
  PRIMARY KEY (question_id, label)
)
```

### エージェント向け: スキルによる自動和訳 (推奨)

本プロジェクトには、AIエージェントが自律的に和訳を完遂するための「スキル」が定義されています。

**指示例:**
> 「`activate_skill` で `exam-translator` を起動し、`xxx.db` の和訳を完遂せよ。終わるまでバッチを回し続けろ。」

スキル本体が dedicated subagent (`exam-translator-worker`, `max_turns: 200`) に委譲し、UTF-8 セーフな保存 (`scripts/batch_helper.py`) と再起動ループを自動でハンドルします。

この指示により、エージェントはターン制限や文字化け問題を自己解決しながら、最小限の報告でタスクを完了させます。

# 任意の SQL を叩く (sqlite3 CLI が無くても uv で)
uv run python -c "
import sqlite3, sys
db = sqlite3.connect('soa-c03.db')
for r in db.execute('''
  SELECT q.question_number, q.question_text_ja, q.suggested_answer,
         GROUP_CONCAT(c.label || \". \" || COALESCE(c.text_ja, c.text), CHAR(10)) AS choices
  FROM questions q JOIN choices c ON c.question_id = q.id
  WHERE q.question_text_ja IS NOT NULL
  GROUP BY q.id ORDER BY q.id LIMIT 3
'''):
    print(r, end='\n\n')
"
```
