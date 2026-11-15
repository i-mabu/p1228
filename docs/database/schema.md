# 🗄️ SQLiteスキーマとデータ境界

SQLiteはGo Gatewayが管理します。WorkerがDBファイルを直接開くことはありません。

- **Migration:** `gateway/internal/storage/migrations/`
- **適用タイミング:** Go Gateway起動時
- **配置:** VPSローカルディスク
- **書き込み:** Go Storageのserialized writer boundary経由

## 📚 テーブル一覧

| Table | 用途 |
|---|---|
| `bots` | BOT identity / Discord Application mapping |
| `guilds`、`users` | Discord entity metadata |
| `guild_settings`、`user_settings` | scope別設定 |
| `dictionaries`、`dictionary_entries` | 共有辞書・revision |
| `read_aloud_settings` | Global / Guild / User読み上げ設定 |
| `audio_engine_settings` | Audio Engine設定slot |
| `gateway_sessions` | BOT別Gateway Resume state・last sequence |
| `event_inbox` | at-least-once Worker queue |
| `event_dead_letters` | retry exhausted / retention expired metadata |
| `worker_instances` | Worker identity・heartbeat |
| `discord_operations` | 外部副作用operation status |
| `event_idempotency_keys` | Handler側idempotency expiry |
| `audit_logs` | 設定・辞書の変更履歴 |
| `guild_feature_settings` | StarTomoのGuild別機能設定 |
| `guild_user_blacklist` | 自動ロール・Role Panel deny list |
| `sticky_messages` | channelごとの自動再投稿メッセージ |
| `point_accounts`、`point_ledger` | 残高・日次上限・取引履歴 |
| `shop_items`、`user_inventory` | Role Shop商品・購入状態 |
| `omikuji_draws`、`star_feature_actions` | 日次おみくじ・再試行可能なゲーム結果 |
| `schema_migrations` | 適用済みmigration version |

## ⚙️ SQLite設定

SQLiteは次の設定で利用します。

- WAL mode
- Foreign keys有効
- `busy_timeout=5000`
- `synchronous=FULL`
- 書き込みはGo Storageの直列化されたwriter boundaryを使用

DBはVPSのローカルディスクに置いてください。ネットワークファイルシステム上で稼働させないでください。Migrationに失敗した場合、Gatewayはreadinessを成立させません。

## 🧹 Event payload retention

初期設定は24時間です。TTLを超えたpending / processing eventはpayloadをscrubしてDLQへ移動します。ACK済み・dead inbox rowはTTL後に削除され、DLQ metadataは保持されますが、期限切れの `payload_json` は `{}` に置き換えられます。

> [!WARNING]
> 24時間は暫定値です。本番投入前にメッセージ内容・個人情報の要件、監査要件、削除ポリシーに照らして保持期間を確定してください（G-07 / G-11）。
