# ❤️ Worker heartbeat障害時のPresence表示

Go Gatewayは各BOTのDiscord Gateway WebSocket接続を所有しています。BOTごとの最新Worker heartbeatを監視し、Workerが応答しなくなった場合は、既存のGateway接続を使ってWatching activityを更新します。

## ⏱️ 判定値

| 項目 | 値 |
|---|---|
| Heartbeat cadence | 10秒（Worker側） |
| Failure threshold | 35秒 heartbeatなし |
| Monitor interval | 5秒 |
| 未登録Workerの初期猶予 | Gateway起動後60秒 |
| 障害時表示 | `障害対応中：Worker応答なし` |
| 復旧時 | configに設定された `watching` 表示へ戻す |

`general`、`read_aloud_a`、`read_aloud_b` はそれぞれ独立して判定されます。

## 🔄 初期化・切断時の挙動

- Gateway起動後60秒間は未登録Workerをprovisioning中として扱い、Presenceを変更しません。猶予後も登録がない場合は障害表示へ切り替えます。
- Discord Gatewayが切断中の場合、Presenceを即時更新できません。
- READYまたはRESUMED後に、現在のhealthy / degraded状態を再適用します。

> [!NOTE]
> この機能は状態表示のfallbackです。Worker commandの実行、queued eventの処理、Voice / audio sessionの引き継ぎは行いません。
