# 🌟 星燈（StarTomo）機能の運用

これらのGuild機能はGeneral Workerが担当します。管理コマンドは **Manage Server** 権限を持つメンバーが実行してください。

## 🧑‍🤝‍🧑 メンバー・ロール機能

管理コマンド: `/welcome`、`/goodbye`、`/autorole`、`/blacklist`、`/logs`、`/sticky`、`/role-panel`、`/game-config`、`/shop add`

- `/welcome set` と `/goodbye set` は `{user}`、`{user_id}`、`{username}`、`{server}` placeholderを受け付けます。`{user}` はmentionになりますが、Gatewayは自動mention parsingを抑制します。
- `/autorole set` はメンバー参加時に設定roleを付与します。`/blacklist add/remove` でGuild別deny listを管理します。
- Blacklist対象者には自動roleを付与せず、設定済みの `autorole_refused` logへ記録します。
- `/role-panel create` は最大5個のrole buttonを投稿します。Blacklist対象者はPanelを利用できません。Panel / button設定はWorker再起動後も保持されます。
- General BOTにはManage Rolesが必要です。付与するすべてのroleよりBOTの最高roleを上に配置してください。

## 📝 ログ・Sticky message

`/logs set` はmessage edit / delete、member join / leave、role change、自動role拒否を個別にroutingします。

VC logのrouting優先順位:

1. `/logs set event:voice_channel target_id:<voice-channel-id>`
2. `/logs set event:voice_category target_id:<category-id>`
3. `/logs set event:voice`

WorkerはGuild / channel Gateway eventからcategory parent情報を更新します。Log messageにはIDとevent summaryを記録し、message bodyは含めません。

`/sticky set` はchannelごとに1件のmessageを保存し、人間のmessage投稿後に最下部へ移動します。Sticky message自体が削除された場合も再投稿します。`/sticky remove` は設定と現在の投稿を削除します。

## 🎮 ポイント・抽選・ショップ

| 機能 | 初期値・制約 |
|---|---|
| メッセージ報酬 | 1pt / message |
| 付与クールダウン | ユーザーごとに60秒 |
| 日次上限 | UTC日付ごとに50pt |
| `/lottery` | 参加費10pt、当選確率20%、当選時追加50pt |
| `/omikuji` | 無料、ユーザーごとにUTC日付1回 |
| Role Shop | `/shop add` で商品作成、同一商品はユーザーごとに1回購入 |

- `/game-config` でポイント報酬・ゲーム設定を調整します。
- `/shop list` で商品を表示し、`/buy item_id:<id>` でポイントを消費してroleを付与します。
- Message reward eventとgame interactionはSQLiteで重複排除します。
- Shop購入確定後にDiscord role付与が失敗した場合は管理者による確認が必要です。Discord側の結果が曖昧な場合、ポイントは自動返金されません。

## 🔑 必要なIntent・権限

- General BOTでDiscord Developer Portalのprivileged `GUILD_MEMBERS` intentを有効にしてください。
- 現在の設定ではmessage reward eventに `MESSAGE_CONTENT` intentも必要です。
- BOTには出力先channelのView Channel / Send Messages権限が必要です。
- Role機能にはManage Rolesと適切なrole hierarchyが必要です。
- Deploy後はSlash Command同期スクリプトを再実行してください。
