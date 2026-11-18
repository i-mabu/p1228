# 🔌 Internal API v1

Go GatewayがWorker向けに提供するloopback限定HTTP APIです。

- **Base URL:** `http://127.0.0.1:8787/internal/v1`
- **待受:** `127.0.0.1`（外部ネットワークへ公開しない）
- **認証:** Worker ID + WorkerごとのBearer Secret

## 🔐 認証ヘッダー

認証が必要なrouteでは、次のヘッダーを送信します。

```http
X-P1228-Worker-ID: read_aloud_a
Authorization: Bearer <worker-secret>
Content-Type: application/json
```

Worker identityはGateway設定から解決します。Body内の `bot_id` は信頼しません。StarTomo関連routeは設定済みの `general` Worker identityに制限されます。

## 🗺️ Route一覧

| Method                    | Path                                               | 用途                                         |
| ------------------------- | -------------------------------------------------- | -------------------------------------------- |
| GET                       | `/health`                                          | Storage / readiness probe（loopback限定）    |
| POST                      | `/worker/register`                                 | `instance_id` を登録                         |
| POST                      | `/worker/heartbeat`                                | Worker heartbeat                             |
| POST                      | `/events/claim`                                    | durable event / ephemeral voice eventをclaim |
| POST                      | `/events/{event_id}/ack`                           | durable eventをACK                           |
| POST                      | `/events/{event_id}/nack`                          | NACK・再試行・DLQ                            |
| POST                      | `/discord/operations`                              | 許可リスト経由のDiscord REST操作             |
| POST                      | `/interactions/{interaction_ref}/initial`          | 初回Interaction応答                          |
| POST                      | `/interactions/{interaction_ref}/edit-original`    | defer後のoriginal response編集               |
| POST                      | `/interactions/{interaction_ref}/followups`        | follow-up応答                                |
| POST                      | `/voice/gateway-payload`                           | Voice adapter → Gateway opcode 4             |
| GET / PUT                 | `/data/read-aloud/settings?scope=...&scope_id=...` | 共有読み上げ設定                             |
| GET / POST                | `/data/dictionary/entries?scope=...&scope_id=...`  | 共有辞書                                     |
| GET / PUT                 | `/data/star/settings?guild_id=...`                 | General BOTのGuild機能設定                   |
| GET / POST / DELETE       | `/data/star/blacklist`                             | 自動ロールBlacklist                          |
| GET / PUT / POST / DELETE | `/data/star/sticky`                                | Sticky message設定                           |
| POST                      | `/data/star/points`                                | ポイント・抽選・おみくじ                     |
| GET / PUT / POST          | `/data/star/shop`                                  | ロールショップ・購入                         |

`scope` は `global`、`guild`、`user` のいずれかです。`global` の場合は `scope_id=global` を指定します。

## 📦 Event envelope

Workerへ配送されるdurable eventの形式です。

```json
{
  "protocol_version": 1,
  "event_id": "general_..._123",
  "correlation_id": "corr_...",
  "bot_id": "general",
  "event_type": "MESSAGE_CREATE",
  "occurred_at": "2026-11-11T03:00:00Z",
  "ordering_key": "general:<guild_id>:<channel_id>",
  "session_id": "gateway-session",
  "sequence": 123,
  "payload": {},
  "lease_token": "lease_...",
  "attempts": 1
}
```

`VOICE_SERVER_UPDATE` とRead-aloud Worker向けの `VOICE_STATE_UPDATE` はephemeralなmemory-only eventです。lease tokenを持たず、対応するRead-aloud Workerだけに配送され、ACK/NACK対象にはなりません。General Worker向けの `VOICE_STATE_UPDATE` はVCログ用のdurable eventとして配送されますが、`session_id` はInbox payloadへ保存する前に除去されます。

## 🛡️ Discord operation allowlist

- `send_message`: `channel_id`、`content`
- `edit_message`: `channel_id`、`message_id`、`content`
- `delete_message`: `channel_id`、`message_id`
- `sync_commands`: `application_id`、`commands`
- `add_role` / `remove_role`: `guild_id`、`user_id`、`role_id`（General BOTのみ。`remove_role` はGateway側で許可・実装済みだが、Worker側からの呼び出しは未実装）
- `send_message` はRole Panel用の `components` を含められます。

任意のURL・HTTP method・pathは受け付けません。各操作には一意な `operation_id` を指定します。成功済みoperationの再試行には保存済み結果を返します。外部結果が失敗または不明な操作を盲目的に再実行することは拒否されます。

## ⏱️ Interaction応答ルール

- 実際のInteraction TokenはGo Gatewayが保持し、Workerはランダムな `interaction_ref` を使います。
- Initial responseは一度だけです。
- Gatewayが安全期限後に対象Interactionを自動deferした場合、後続のWorker `initial` はdeferred original responseの編集に変換されます。
- Autocomplete（Interaction type 4）はdeferされません。choicesを速やかに返す必要があります。

## 🎙️ 読み上げ設定・辞書

設定は `global` → `guild` → `user` の優先順位でマージします。設定レコードは `enabled`、`speed`、`volume`、`speaker_id` の4値を含みます。scope recordが存在しない場合、revisionは `0` で下位優先度の値を上書きしません。

次の例は、認証済みのローカルホストからGuild設定を更新します。

```bash
curl --fail --silent --show-error -X PUT \
  'http://127.0.0.1:8787/internal/v1/data/read-aloud/settings' \
  -H 'X-P1228-Worker-ID: read_aloud_a' \
  -H "Authorization: Bearer $P1228_READ_ALOUD_A_WORKER_SECRET" \
  -H 'Content-Type: application/json' \
  -d '{"scope":"guild","scope_id":"YOUR_GUILD_ID","settings":{"enabled":true,"speed":1.0,"volume":1.0,"speaker_id":1}}'
```

読み上げ設定は2体のRead-aloud BOTで共有されます。User設定は該当ユーザーに対してのみ上書きします。辞書も同じscope構造で統合され、上位scopeの同一語句が優先されます。置換時は長い語句から処理します。
