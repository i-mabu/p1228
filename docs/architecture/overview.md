# 🏗️ アーキテクチャ概要

p1228は、Discord BOT 3体の接続・永続化・REST操作をGo Gatewayに集約し、機能処理を独立したTypeScript Workerへ分離する構成です。

## 🧭 システム構成

```text
Discord Gateway x 3
  ├─ 星燈 / general
  ├─ 言ノ葉 / read_aloud_a
  └─ 紡言 / read_aloud_b
          │
          ▼
      Go Gateway Proxy
      ├─ Gateway WS / Identify / Resume / Heartbeat
      ├─ Dispatch → SQLite event_inbox
      │              (transaction + sequence checkpoint)
      ├─ Worker Registry / authenticated HTTP API
      ├─ REST allowlist + per-BOT token / rate retry
      ├─ Interaction token refs (memory only) / ACK-defer
      ├─ Voice opcode 4 outbound proxy
      └─ SQLite WAL / migrations / serialized writer
          │
          ├─ HTTP claim / ACK / NACK ── General Worker
          ├─ HTTP claim / ACK / NACK ── Read-aloud A Worker
          ├─ HTTP claim / ACK / NACK ── Read-aloud B Worker
          │                                │
          │                                ├─ Voice WS / UDP / RTP / DAVE / Opus
          │                                └─ Audio Engine HTTP + bounded local cache
          └─ Shared Data API (settings / dictionary / revision)

Local disk: active SQLite, WAL, executables, bounded cache, recent journal
Local archive: compressed log archives + verified SQLite backups
```

## 🧩 コンポーネントの責務

| コンポーネント | 責務 |
|---|---|
| Go Gateway | Discord Gateway接続、Resume、REST許可リスト、Interaction応答、内部HTTP API |
| SQLite Storage | イベントInbox、Worker状態、設定、辞書、操作記録、機能データ |
| General Worker | 星燈のサーバー機能・管理コマンド |
| Read-aloud Workers | 各BOTの読み上げコマンド、Voice接続、再生キュー |
| Audio Engine | VOICEVOX ENGINE互換HTTP APIによる音声合成 |
| Local archive | ログ・SQLiteバックアップのローカル保持 |

言ノ葉と紡言は同じWorkerコードを別プロセスで実行します。Voiceセッション・キュー・プロセスは独立し、設定と辞書は共有します。

## 🔐 信頼境界と機密データ

- BOT TokenはGo Gatewayだけが保持します。WorkerはDiscord RESTを直接実行せず、型付きoperation allowlistを利用します。
- Worker認証は `X-P1228-Worker-ID` とWorker別Bearer Secretの組み合わせです。内部HTTPはloopbackのみで待ち受けます。
- `INTERACTION_CREATE.token` はGatewayメモリにのみ保持し、Inbox payloadから除去してWorkerへ `interaction_ref` を通知します。
- `VOICE_SERVER_UPDATE.token` と `VOICE_STATE_UPDATE.session_id` は短期メモリqueue経由で配送し、DB・ログへ書き込みません。30秒を超えたephemeral voice eventは破棄します。
- Event deliveryはat-least-onceです。外部副作用には `operation_id` を使いますが、外部結果が不明な場合は同じ操作を盲目的に再実行しません。
- UbuntuではGatewayと各Workerを別サービスユーザーで実行します。

## 🔄 イベント配送ライフサイクル

1. Go GatewayがDiscord Gateway Dispatchを受信します。
2. Inboxへの挿入と `gateway_sessions.last_sequence` の更新を同一トランザクションで確定します。
3. Workerがleaseをclaimし、イベントを処理してACKまたはNACKします。
4. lease期限切れのイベントは再配送されます。再試行上限に達するとdead-letterへ移動します。
5. TTLを超えたpayloadは削除・scrubされます。古い未ACKイベントはpayloadを消去してDLQへ移します。

> [!IMPORTANT]
> **Exactly-once配送は保証しません。** Handlerは冪等に設計してください。HTTP timeoutだけを理由に、外部副作用を再実行しないでください。

## 💾 データ配置

- **ローカルディスク:** 稼働中SQLite、WAL、実行ファイル、容量制限付きキャッシュ、直近ログ
- **ローカルアーカイブ:** 圧縮ログと検証済みSQLiteバックアップ（ログ90日、SQLite日次7世代・週次84日）
- **アーカイブ対象外:** 稼働中SQLite/WAL、Docker data-root、実行ファイル、一時ファイル

アーカイブはローカルディスク上のコピーです。遠隔保管はなく、稼働データを直接読み書きする場所でもありません。
