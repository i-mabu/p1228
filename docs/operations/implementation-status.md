# 🚦 実装状況と既知の制約

このリポジトリは設計に基づく初期実装です。**本番リリース済み・本番動作検証済みであることを意味しません。**

## ✅ ソース上で実装されている範囲

- Go GatewayのBOT別WebSocket supervisor、session resume state、Dispatch保存、Worker向け内部HTTP API、REST / Interaction proxy
- SQLite migrationとevent queue、Worker registry、共有設定、辞書、operation record用Storage API
- TypeScript Worker runtime、General Worker、独立したRead-aloud Worker、Custom Adapter境界、Voice session / audio queue、VOICEVOX互換HTTP adapter
- Windows / Ubuntu向け起動・配置スクリプト、systemd unit、journald制限、ローカル archive / backup / restore、容量監視
- 星燈の参加／退出メッセージ、Blacklist付き自動ロール・Role Panel、イベント／VCログ、Sticky再投稿、おみくじ、ポイント、抽選、Role Shop

## 🧪 CIで確認される範囲

GitHub ActionsのCI設定はGo 1.26.0 / Node.js 24.17.0を使用します。

- Go `go vet ./...` と `go test ./...`
- Workerの `npm ci`、`npm test`、`npm run build`
- Ubuntu上のBash構文検査と、同じくUbuntu runner上のPowerShell構文検査
- CI成功時のソースアーカイブ作成

Go Storage testsは機能設定、Blacklist、Sticky state、point cap、lottery、shop purchase、daily omikujiを対象とします。実行ごとの成功・失敗は、対象commitに紐づくGitHub Actions runで確認してください。CI成功はDiscord実環境での動作保証ではありません。CIの詳細は [.github/workflows/ci.yml](../../.github/workflows/ci.yml) を参照してください。

## ⚠️ 本番投入前に残る検証・作業

- Discord credentialsを用いたlive Gateway / REST / Interaction / Voice testは未実施です。ローカルバックアップ／リストアの実環境試験も未実施です。
- Voice実装はWindowsとUbuntuの双方でDAVE / AEAD / UDP / OpusのPoCを通す必要があります。Custom Adapterの存在だけではVoice経路全体の動作を証明しません。
- 既存private repository `ysgnem/yanagi` のコード・DBは監査・移行されていません。既存BOTの全機能移行を完了したものではありません。
- StarTomo機能は初期実装です。旧General BOTの全機能を移行したものではありません。
- VOICEVOX ENGINE互換APIは初期接続先です。利用するEngine、ライセンス、運用リソース予算はコードで確定していません。
- Worker healthは登録・heartbeatから判定します。通知経路とOperator dashboardは今後の作業です。
- Event payload TTLは24時間の暫定設定です。プライバシー要件に合わせて確定してください。

## 🧱 意図的に保守的な挙動

- Read-aloud向けVoice State / Server Updateはmemory-onlyで扱い、30秒超は破棄します。General Worker向け `VOICE_STATE_UPDATE` はVCログ用のdurable eventとして扱いますが、`session_id` はInbox payloadから除去します。
- バックアップ／アーカイブ障害はBOT runtimeを停止させず、ローカルファイル削除の根拠にもなりません。
- 成功済み `operation_id` の重複要求には保存済み結果を返します。外部結果不明の操作は盲目的に再実行しません。
- Storage monitorは通知のみです。SQLite / Docker Volumeの自動cleanupは行いません。
