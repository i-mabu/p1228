# 🌌 p1228 — Discord BOT Gateway & Workers

Discord BOT 3体を、**Go Gateway + 独立した TypeScript Worker** で運用するためのプロジェクトです。

> [!WARNING]
> **本番運用前の注意:** このリポジトリは設計に基づく初期実装です。Discord実接続、権限、Slash Command、Voice/DAVE、ローカルバックアップ復元の実環境検証は別途必要です。既存BOTからの全機能移行も完了していません。

## ✨ BOT構成

| BOT    | Worker ID      | 主な役割                      | Watching表示       |
| ------ | -------------- | ----------------------------- | ------------------ |
| 星燈   | `general`      | サーバー機能・管理コマンド    | サーバーを見守り中 |
| 言ノ葉 | `read_aloud_a` | 読み上げ・独立Voiceセッション | 言葉を紡ぎ中       |
| 紡言   | `read_aloud_b` | 読み上げ・独立Voiceセッション | 声をお届け中       |

Go Gatewayが各BOTのDiscord Gateway接続、REST操作、Interaction応答、SQLite、Worker向け内部APIを担当します。表示文言は `config/gateway.yaml` または `config/gateway.windows.yaml` の各BOTにある `watching` で変更できます。変更後はGatewayを再起動してください。

言ノ葉と紡言は同じWorkerコードを別プロセスで実行します。Voice接続・再生キューは独立し、読み上げ設定・辞書はGateway内のStorageを共有します。音声合成にはVOICEVOX ENGINE互換HTTP APIを利用します。

## 🧩 主な機能

### 🌟 星燈（General）

- 参加／退出メッセージ、自動ロール、ブラックリスト
- イベント別・VC別ログ、固定メッセージ、ロールパネル
- おみくじ、ポイント、抽選、ロールショップ

管理コマンド: `/welcome`、`/goodbye`、`/autorole`、`/blacklist`、`/logs`、`/sticky`、`/role-panel`、`/game-config`、`/shop`

一般ユーザー向け: `/omikuji`、`/points`、`/lottery`、`/buy`

### 🔊 言ノ葉・紡言（Read Aloud）

- `/join`、`/leave`、`/say`
- 設定に応じたメッセージ読み上げ
- Global → Guild → User の順で設定を適用
- BOTごとに独立したVoiceセッションと再生キュー

### 🛠️ 共通基盤

- `/ping`、Workerイベント配送、SQLite永続化
- Workerごとの認証、Heartbeat監視
- ログ／DBバックアップ用スクリプト

ポイントの初期値はメッセージごとに1pt、60秒クールダウン、UTC日次上限50ptです。抽選は10pt、当選確率20%、当選時の追加報酬50pt。おみくじはUTC日付ごとに1回、ショップ商品は同一ユーザーにつき1回購入できます。詳細は [星燈機能の運用](./docs/operations/star-features.md) を参照してください。

> [!NOTE]
> 既存BOTの完全な機能移行、管理UI、辞書編集コマンドは未実装です。既存リポジトリ `ysgnem/yanagi` のコード・DBも本リポジトリでは監査・移行されていません。

## 💻 必要環境

| 項目          | 要件・用途                                          |
| ------------- | --------------------------------------------------- |
| OS            | Windows 10/11（開発）、Ubuntu 24.04 LTS（本番手順） |
| Go            | 1.23以上                                            |
| Node.js / npm | Node.js 24.17以上、npm 10以上を推奨                 |
| FFmpeg / Opus | Voice音声再生                                       |
| SQLite CLI    | 運用時のDB検査・バックアップ                        |

Voice関連依存はバージョン固定です。更新時はCustom AdapterとDAVEを含むVoice E2E試験を再実施してください。

## 🔨 ビルド（ワンコマンド）

Go 製 Gateway と TypeScript 製 Workers の両方を 1 コマンドでビルドする
スクリプトを用意している。Gateway は `go build ./...` と `go test ./...`、
Workers は依存関係のインストール → 型チェック → ビルド → テストを実行する。
失敗した場合は、そのステップで止まってどこで落ちたかを表示し、最後に
両方の結果をまとめて表示する。

```bat
build.bat
```

```sh
./build.sh
```

## 🚀 Windowsで起動

1. Go、Node.js、FFmpegをインストールし、必要な実行ファイルをPATHに追加します。
2. `.env.example` を `.env` にコピーします。
3. 3 BOT分のToken、Application ID、相互に異なるWorker Secretを設定します。Secretは各64桁hex相当のランダム値を推奨します。
4. `config/gateway.windows.yaml` のBOT IDとApplication IDの対応を確認します。BOT Tokenを使用するのはGo Gatewayだけです。
5. PowerShellで起動します。

```powershell
Copy-Item .env.example .env
# .env を編集
Set-ExecutionPolicy -Scope Process Bypass
.\deploy\windows\start.ps1
```

起動後、Slash Commandを登録または更新します。

```powershell
.\deploy\windows\sync-commands.ps1
```

停止時は `.\deploy\windows\stop.ps1` を実行してください。ログとPIDファイルはGit管理外の `.runtime/` に作成されます。

## 🐧 Ubuntu 24.04へ配置

1. リポジトリを `/opt/p1228` に配置し、Go 1.23以上とNode.js 24.17以上をインストールします。
2. ベース環境をインストールします。スクリプトはBOTサービスを自動起動しません。

```bash
sudo /opt/p1228/deploy/install-ubuntu.sh
```

3. `/etc/p1228/gateway.env` と3つのWorker用envファイルにToken、Application ID、Worker Secretを設定します。各SecretはGatewayと対応するWorkerで一致させ、他Workerとは異なる値にします。
4. 必要に応じて `/etc/p1228/gateway.yaml` を編集します。
5. Gatewayを起動し、Slash Commandを同期してからWorkerを起動します。

```bash
sudo systemctl enable --now p1228-gateway
sudo /usr/local/sbin/p1228-sync-commands.sh
sudo systemctl enable --now p1228-general p1228-read-aloud-a p1228-read-aloud-b
systemctl status p1228-gateway p1228-general p1228-read-aloud-a p1228-read-aloud-b
```

ログとSQLiteバックアップはローカルに保持します。手順は [ローカルバックアップと復元](./docs/operations/local-backup.md) を参照してください。

## 🔐 セキュリティとデータ

- Gateway内部APIは `127.0.0.1:8787` のみで待ち受けます。仕様は [内部API](./docs/api/internal-api.md) を参照してください。
- Discord BOT TokenはGo Gatewayだけが保持します。Workerは個別Secretで認証され、Discord REST操作はGatewayの許可リストを介します。
- UbuntuではGatewayと各Workerを別サービスユーザーで実行します。
- SQLiteには稼働データを保存します。DBはVPSのローカルディスクに置き、ネットワークファイルシステム上で直接稼働させないでください。
- バックアップ用スクリプトは圧縮ログと整合性確認済みSQLiteバックアップをローカルに保持します。設定・辞書の自動バックアップは含まれません。
- ログアーカイブは90日後にローカルから削除されます。SQLiteバックアップは直近7世代と週次コピー（84日）をローカルに保持します。
- ディスク／inode監視は通知のみで、DBやDockerデータを自動削除しません。
- Voice接続用tokenや `session_id`、Interaction tokenは短期データとして扱います。Read-aloud向けVoice eventはmemory-onlyで、General向けVCログeventには `session_id` を含めません。Inbox payloadの保持期間24時間は暫定値です。

## 🧪 検証状況

GitHub Actions CIでは、Go 1.23.2 / Node.js 24.17.0を使用し、`go vet ./...`、`go test ./...`、`npm ci`、`npm test`、`npm run build`、Bash・PowerShell構文検査を実行します。CI成功はDiscord実接続や本番環境の動作を保証するものではありません。

Discord実接続、コマンド登録、権限、Voice/DAVE/UDP/Opus、ローカルバックアップ・復元は未検証です。本番化前に [本番投入前チェックリスト](./docs/operations/production-checklist.md) を完了してください。

## 📚 ドキュメント

| ドキュメント                                                                              | 内容                                         |
| ----------------------------------------------------------------------------------------- | -------------------------------------------- |
| [アーキテクチャ概要](./docs/architecture/overview.md)                                     | コンポーネント、イベント配送、信頼境界       |
| [内部API](./docs/api/internal-api.md)                                                     | Worker認証、イベント、REST・Interaction API  |
| [データベーススキーマ](./docs/database/schema.md)                                         | テーブル、永続化、保持期間                   |
| [星燈機能の運用](./docs/operations/star-features.md)                                      | コマンド、権限、ポイント・ショップ           |
| [ローカルバックアップと復元](./docs/operations/local-backup.md)                           | バックアップ、復元、保持期間                 |
| [Worker障害時のPresence](./docs/operations/worker-failover.md)                            | Heartbeatと障害表示                          |
| [実装状況と既知の制約](./docs/operations/implementation-status.md)                        | 実装済み範囲、未検証項目                     |
| [本番投入前チェックリスト](./docs/operations/production-checklist.md)                     | 本番化の受け入れ確認                         |
| [コードレビュー・修正レポート（2026-11-15）](./docs/operations/code-review-2026-11-15.md) | 全体レビューで修正したバグ・最適化と検証結果 |

---

<p align="center">
  <sub>p1228 · Go Gateway / TypeScript Workers</sub>
</p>
