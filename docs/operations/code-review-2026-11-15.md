# 🔍 コードレビュー・修正レポート（2026-11-15）

対象: p1228 全体（Go Gateway / TypeScript Workers / 配置スクリプト / CI）
方針: 挙動を変える修正は最小限に絞り、すべてテストで裏付けが取れるものだけを適用した。未検証の Discord 実接続まわりには手を入れていない。

## ✅ 修正前に確認したベースライン

| 検証 | 結果 |
|---|---|
| `go vet ./...` | 合格 |
| `go test ./...` | 全パッケージ合格 |
| `npm test`（Worker） | 5件合格 |
| `npm run build` | 合格 |
| `gofmt -l` | **3ファイルが未整形**（gateway.go / gateway_test.go / star_features.go） |
| Bash 構文検査 | 合格 |

## 🐛 修正したバグ

### 1. ポイント未保有ユーザーの `/buy` が「商品が見つかりません」と誤表示される
- 場所: `gateway/internal/storage/star_features.go`（`BuyShopItem`）
- 原因: ポイント口座がまだ無いユーザーでは残高取得が `sql.ErrNoRows` を返し、そのままエラーとして伝播していた。Worker 側は `no rows in result set` を「商品が見つかりません」に変換していたため、実際は残高不足のケースで誤った案内になっていた。
- 修正: 商品が無い場合は専用エラー `shop item not found` を返し、口座が無い場合は残高0として扱い `insufficient points` を返すよう変更。Worker 側のエラーマッピングにも `shop item not found` を追加。
- 回帰テスト: `TestBuyShopItemWithoutPointAccountReportsInsufficientPoints` / `TestBuyShopItemUnknownItemReportsNotFound` を追加。

### 2. `sync-commands` の直接実行が常に失敗する
- 場所: `workers/src/sync-commands.ts`
- 原因: Application ID は `P1228_APPLICATION_ID` のみから読んでいたが、`.env.example` にあるのは BOT 別の `P1228_GENERAL_APPLICATION_ID` 等のみ。配置スクリプト経由では統一変数が渡るため動くが、`npm run sync:commands` の直接実行では空文字が送られ Gateway に拒否されていた。
- 修正: BOT 別の環境変数へフォールバックし、どちらも無い場合は不足している変数名を明示して即座にエラーにする。
- 検証: 環境変数なしでは不足エラー、BOT 別変数のみでは Gateway 呼び出しまで進むことを実機で確認。

### 3. ログの秘匿処理が配列配下の秘密を漏らす
- 場所: `workers/src/core/logger.ts`（`sanitize`）
- 原因: 配列の再帰時にキー名を引き継いでいなかったため、`tokens: [...]` のような配列配下の値が `[REDACTED]` にならなかった。
- 修正: 配列要素にも元のキーを渡して判定する。

### 4. Gateway 再接続バックオフが上限を超える／回復後も最大のまま
- 場所: `gateway/internal/discord/gateway.go`（`runBot`）
- 原因: `backoff < 60s` のときだけ2倍化するため 32s → 64s と上限を超過していた。また長時間安定稼働した接続が切れても最大バックオフが維持されていた。
- 修正: `min(backoff*2, 60s)` で確実に上限を守り、5分以上継続した接続については初期値（2s）へ戻す。

### 5. イベントIDの衝突で実イベントが黙って欠落しうる
- 場所: `gateway/internal/discord/gateway.go`（`handleDispatch`）
- 原因: イベントIDに使うセッションハッシュが4バイト（32bit）しかなく、異なるセッション同士の衝突（誕生日境界で約6.5万セッション）とシーケンス番号の重なりが重なると、主キー重複で `INSERT OR IGNORE` により正規のイベントが保存されなかった。
- 修正: ハッシュを8バイト（64bit）に拡張。同一セッション内の重複 Dispatch の冪等性は維持される。

### 6. エラー文字列の切り捨てが UTF-8 を壊す
- 場所: `gateway/internal/server/server.go`（`truncate`）
- 原因: バイト単位で切るため、日本語などのマルチバイト文字の途中で切断され、不正な UTF-8 が DB の `last_error` 等に保存されていた。
- 修正: ルーン境界で切り捨てる。回帰テスト `TestTruncateKeepsValidUTF8` を追加。

### 7. Worker ハートビートだけ書き込みロックの対象外
- 場所: `gateway/internal/storage/storage.go`（`Heartbeat`）
- 原因: 他の全ての書き込みメソッドが `writeMu` を取る中で `Heartbeat` だけ無ロックだった。
- 修正: 他メソッドと同様にロックを取るよう統一。

### 8. 星燈 Worker の到達不能コードと未使用型
- 場所: `workers/src/general/star-features.ts`
- 内容: `handleStarEvent` 内の到達不能な重複チェックと、未使用の `CommandOption` 型を削除（後述の `noUnusedLocals` 有効化で検出）。`workers/src/general/index.ts` の未使用 import も削除。

## 🔐 セキュリティ強化

### Worker 認証のタイミング漏れを解消
- 場所: `gateway/internal/server/server.go`（`authenticate`）
- 内容: `subtle.ConstantTimeCompare` は長さが異なる入力では即座に不一致を返すため、設定済み Secret の長さがタイミングから推測可能だった。提示値と設定値の双方を SHA-256 化し、固定長ダイジェスト同士を定数時間比較するよう変更。
- 回帰テスト: `gateway/internal/server/server_test.go` を新設（正常系・異常系・切り捨ての3テスト）。

## ⚡ 最適化

| 対象 | 内容 | 効果 |
|---|---|---|
| `gateway/internal/discord/gateway.go` | REST 用 `http.Client` を呼び出しごとに生成していたのを Manager 共有に変更 | Discord API への接続再利用（Keep-Alive）が効き、毎回の TLS ハンドシェイクを削減 |
| `gateway/internal/discord/interactions.go` | Interaction 参照の有効期限スイープが生成のたびに全件走査（O(n)）だったのを、最大1分に1回＋件数上限時のみに変更 | Interaction 多発時のロック保持時間を削減 |
| `workers/src/core/worker.ts` | イベント待ちポーリングを固定250msから適応型（250ms→最大1s、受信時リセット）に変更 | 静かな時間帯の Gateway / SQLite への無駄なリクエストを最大で約1/4に削減 |
| `workers/src/read-aloud/voice/service.ts` | 再生キューの処理をアイテム単位で例外隔離 | 1件の合成失敗で後続キュー全体が中断しなくなった（セッション自体が消滅した場合は打ち切る） |
| `gateway/internal/config/config.go` | 設定読み込みの不要な文字列コピーを `bytes.NewReader` に変更 | 微小だが無駄なアロケーションを削除 |
| `gateway/cmd/gateway/main.go` | 同一内容の map コピーを削除し `cfg.Workers` を直接渡す | 起動経路の簡素化 |
| `gateway/internal/storage/storage.go` | 自前の `min` 関数を削除し Go 1.21+ の組み込み `min` を使用 | 重複実装の排除 |

## 🧹 整形・品質ゲート

- `gofmt` 未整形だった3ファイルを整形し、CI に `gofmt -l` チェックを追加（再発防止）。
- Worker の `tsconfig.json` に `noUnusedLocals` / `noUnusedParameters` を追加し、未使用コードをビルド時に検出できるようにした。
- `workers/package.json` のテストスクリプトを `test/*.test.ts` に変更し、今後テストファイルを追加しても自動で対象になるようにした。
- `.env.example` に Application ID の受け渡し方法（直接実行時と配置スクリプト経由の違い）を明記。

## 🧪 修正後の検証結果

| 検証 | 結果 |
|---|---|
| `gofmt -l gateway/` | 出力なし（全て整形済み） |
| `go vet ./...` | 合格 |
| `go test ./...` | 全パッケージ合格（server パッケージにテスト新設、storage に回帰テスト2件追加） |
| `go test -race ./...` | 合格（競合なし） |
| `npm run build` | 合格（厳格化した tsconfig でも通過） |
| `npm test` | 5件合格 |
| Bash 構文検査 | 合格 |
| sync-commands スモーク確認 | ID不足時は明示エラー、BOT別変数のみでも解決できることを確認 |

注: この検証環境には libopus 等のネイティブ依存がないため、Worker の依存導入は `npm ci --ignore-scripts` で行った。CI（libopus-dev / ffmpeg 導入済み）では通常の `npm ci` が対象。

## 📌 今回は変更しなかった事項（要判断・将来課題）

- **Discord 実接続の検証**: README・実装状況ドキュメントにある通り未実施のまま。本番投入前チェックリストの消化が前提。
- **CI の Action バージョン**: `actions/checkout@v4` 等は動作するが、最新メジャーへの更新は別途検証してからが安全。
- **星燈 Worker のメモリ内キャッシュ**（`memberRoles` 等）: ギルド数が大きい運用では無制限に増える可能性がある。現状の想定規模では問題にならないため、LRU 化などは実測に基づいて判断するのがよい。
- **読み上げの `join` / `say` 失敗時の再配送**: Interaction 応答済みのイベントが NACK で再配送される設計は既存仕様として維持した。気になる場合は「応答送信後のエラーは ACK する」ポリシーへの変更を別途検討。
- **バックアップスクリプトの保持世代数・監視閾値**: 運用ポリシーに依存するため未変更。
