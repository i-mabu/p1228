# CI と整形自動化

このドキュメントは、p1228 の継続的インテグレーション（CI）とコード整形の自動化についてまとめたものです。

## 1. ワークフロー構成

| ワークフロー | ファイル | トリガー | 役割 |
| --- | --- | --- | --- |
| p1228 CI | `.github/workflows/ci.yml` | main への push / pull_request（ドキュメントのみの変更を除く） | 検証（チェックのみ。コードは変更しない） |
| Format (auto-fix) | `.github/workflows/format.yml` | main への push / 手動実行 | フォーマッタの自動適用と自動コミット |
| CodeQL | `.github/workflows/codeql.yml` | main への push / pull_request（対象パスの変更時のみ） / 毎週月曜 03:00 UTC | 静的セキュリティ解析 |

すべてのワークフロー共通の方針:

- トップレベルの権限は `permissions: contents: read` のみとし、追加の権限が要る場合はジョブ単位で最小限だけ付与する。
- `concurrency` により、同一 ref の古い実行はキャンセルする（自動整形ワークフローだけは、コミット途中で止めないようキャンセルしない）。
- 各ジョブには `timeout-minutes` を設定し、ハングした実行が残り続けないようにする。

## 2. CI（ci.yml）のジョブと検証内容

ジョブは責務ごとに分割し、並列実行します。

### 整形チェック（各ジョブに統合）

整形チェックは専用ジョブを置かず、各ジョブに統合しています（いずれも差分があれば失敗する blocking チェックです）。

- `gofmt -l`（gateway 配下に未整形の Go ファイルがないか）: go ジョブ
- Prettier `--check`（`npm run format:check`。対象は後述）: workers ジョブ
- `shfmt -i 2 -ci -d`（Shell スクリプトの整形差分がないか）: shell ジョブ

直し方は「5. 整形自動化」を参照してください。

### go ジョブ

- `gofmt` チェック、`go vet ./...`
- `go mod tidy` の差分チェック（`go.mod` / `go.sum` が tidy 済みでなければ失敗）
- `go test -race -shuffle=on -coverprofile=coverage.out ./...` とカバレッジサマリ表示、カバレッジファイルの artifact アップロード
- `govulncheck`（`golang.org/x/vuln/cmd/govulncheck@v1.1.4` をバージョン固定で実行）
- ソースアーカイブ（`git archive`）の作成と artifact アップロード

govulncheck は **non-blocking**（`continue-on-error: true`）です。固定している Go 1.23.2 は現在の脆弱性データベースでは標準ライブラリの既知の脆弱性が多数報告されるため、blocking にすると恒常的に CI が落ちます。結果はログで確認でき、対応は Go 本体のバージョンアップとして別途判断してください。

### workers ジョブ

- ネイティブ依存（`@discordjs/opus`）のビルドに必要な `ffmpeg` と `libopus-dev` のインストール
- `npm ci`、Prettier による整形チェック、型チェック（`npm run typecheck` = `tsc --noEmit`）、`npm test`、`npm run build` を個別ステップで実行
- `npm audit --audit-level=high`（**non-blocking**。結果はログに残ります。高深刻度の脆弱性は Dependabot の更新 PR で追跡する運用とし、既知の脆弱性で恒常的に CI が落ちる状態を避けます）
- `workers/dist` の artifact アップロード

### shell ジョブ

- `bash -n` による構文チェック（deploy 配下、`scripts/format.sh`、`.githooks/pre-commit`）
- `shfmt` による整形差分チェック（blocking）
- ShellCheck（apt でインストール）による静的解析。指摘は原則としてスクリプト側を直し、直せない場合のみ理由付きの `shellcheck disable` コメントを使います

### powershell-syntax ジョブ

- pwsh は Linux ランナーにも標準搭載されており、以下の検証は OS に依存しないため `ubuntu-24.04` で実行します（Windows ランナーは分数が2倍換算されるため）
- PowerShell パーサーによる構文検証（`deploy/windows/*.ps1` と `scripts/*.ps1`）
- PSScriptAnalyzer: Severity が Error の指摘は blocking、Warning は表示のみ
- `Invoke-Formatter` との差分チェック（表示のみ、non-blocking）

### CodeQL（codeql.yml）

- 対象言語: Go と javascript-typescript
- 実行は main への push と pull_request のうち対象パス（`gateway/**`、`workers/**`、`.github/workflows/**`）が変わった場合と、毎週月曜 03:00 UTC のスケジュールのみ（Actions の分数節約のため）
- ジョブ単位で `security-events: write` と `actions: read` を付与し、それ以外は読み取りのみ
- Code scanning（Advanced Security）は Organization 向けで、個人アカウントのプライベートリポジトリでは有効化できない。そのため結果は Code scanning へアップロードせず（`upload: never`）、SARIF を Actions の artifact（`codeql-sarif-*`、保持7日）として保存する。Organization へ移行して Code scanning を有効化した場合は、`upload` / `output` の指定と artifact 保存ステップを外せば Security タブ連携に戻せる

### Dependabot（.github/dependabot.yml）

- gomod（`/gateway`）、npm（`/workers`）、github-actions（`/`）の更新を毎週月曜に確認し、更新 PR を自動作成します
- PR 数の抑制のため、gomod / npm は minor / patch 更新を1本の PR にグループ化し、github-actions は全更新を1本にグループ化します。同時に開く PR は各エコシステム3本まで（`open-pull-requests-limit: 3`）

## 3. ローカルでの再現コマンド

CI の各チェックはローカルでも再現できます。

```bash
# Go
cd gateway
gofmt -l .
go mod tidy && git diff --exit-code -- go.mod go.sum
go vet ./...
go test -race -shuffle=on -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...

# Workers
cd workers
npm ci
npm run typecheck
npm test
npm run build
npm audit --audit-level=high
npm run format:check

# Shell
bash -n deploy/install-ubuntu.sh deploy/scripts/*.sh scripts/format.sh .githooks/pre-commit
shellcheck deploy/install-ubuntu.sh deploy/scripts/*.sh scripts/format.sh .githooks/pre-commit
shfmt -i 2 -ci -d deploy/install-ubuntu.sh deploy/scripts/*.sh scripts/format.sh .githooks/pre-commit
```

## 4. 整形ルール

| 対象 | ツール | 設定 |
| --- | --- | --- |
| Go | gofmt | 標準（追加設定なし） |
| TypeScript / JSON / YAML | Prettier 3.10.1（devDependency に固定） | リポジトリ直下の `.prettierrc.json`。単一引用符・セミコロンあり・インデント2スペース・`printWidth` 200（既存コードの長い行を尊重するため）。YAML のみ引用符は二重引用符を維持（override で `singleQuote: false`） |
| Shell | shfmt v3.10.0 | `-i 2 -ci`（インデント2スペース、case パターンをインデント） |
| PowerShell | PSScriptAnalyzer（`Invoke-Formatter`） | CI では差分表示のみ。ローカルで内容を確認して手動適用 |
| 全般 | `.editorconfig` | 文字コード・改行コード・インデントの宣言 |

Prettier の対象範囲は、Workers の TypeScript / JSON と、設定ファイル（`config/*.yaml`、`.github/workflows/*.yml`、`.github/dependabot.yml`）です。リポジトリ全体の Markdown は対象外です。

セクション見出しのバナーコメントは、言語ごとのコメント記号（Go / TypeScript は `//`、Shell / PowerShell / YAML は `#`、SQL は `--`）を使い、`=` の罫線の長さはタイトル行の表示幅（全角文字は2、半角文字は1として計算）に合わせて上下で揃えます。新しいセクションを追加するときも、この規則で罫線の長さをタイトルに合わせてください。

## 5. 整形自動化

### 何が自動で直るか

- `scripts/format.sh`（Windows は `scripts/format.ps1`）を実行すると、gofmt / Prettier / shfmt を一括で適用します。PowerShell の整形だけは自動適用せず、`Invoke-Formatter` で確認して手動で適用します（整形結果を目視確認してから取り込むため）。
- `.github/workflows/format.yml` は、main への push と手動実行（workflow_dispatch）のときに同じ整形を自動実行し、差分があれば `github-actions[bot]` が自動コミットします。

### 何がチェックで落ちるか

- CI の各ジョブに統合された整形チェックは、整形差分を検出すると失敗します（blocking）。pull_request では自動修正は行われないため、ローカルで `scripts/format.sh` を実行してから push してください。

### 自動整形ワークフローの安全設計

- 対象を main への push と手動実行に限定しています。pull_request イベントでは、fork からの PR で書き込み権限が得られず自動コミットできないため対象外です。
- 書き込み権限（`contents: write`）は format ジョブにだけ付与し、トップレベルは `contents: read` のままです。
- 自動コミットのメッセージには `[skip ci]` を付けます。また、通常は `GITHUB_TOKEN` による push は新たなワークフロー実行をトリガーしないため、二重に整形が走ることもありません。
- 整形以外の変更はコミットしません（フォーマッタの出力のみが差分になります）。

### pre-commit フック（任意）

外部ツールを増やさず、リポジトリ内のフックでコミット前に整形チェックできます。

```bash
git config core.hooksPath .githooks
```

`.githooks/pre-commit` は gofmt / Prettier / shfmt のチェックを行い、差分があればコミットを止めます。ツールが見つからないチェックはスキップされます。

### PowerShell のローカル整形手順

```powershell
Install-Module PSScriptAnalyzer -Scope CurrentUser
Invoke-Formatter -ScriptDefinition (Get-Content -Raw deploy/windows/start.ps1)
```

出力を確認し、問題なければファイルに書き戻してください。一括自動適用は行わない運用です。
