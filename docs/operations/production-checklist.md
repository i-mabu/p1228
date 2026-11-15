# 🚀 本番投入前チェックリスト

このチェックリストは、初回起動・Voice検証・Storage受け入れ・運用確認を分けています。**未確認項目を完了扱いにせず、実環境で結果を記録してください。**

## 🔑 初回起動前

- [ ] Discord Application ID ↔ BOT Token ↔ BOT名の対応を確認（星燈 / `general`、言ノ葉 / `read_aloud_a`、紡言 / `read_aloud_b`）。
- [ ] Gateway configの `watching` 表示を確認。READY / reconnect後にDiscord Watching activityとして反映されることを確認。
- [ ] Worker Secretを3つ用意。各Secretは32文字以上とし、Gatewayと対応Workerのenvで一致させ、他Workerとは異なる値にする。
- [ ] Developer Portalで各BOTのIntentと権限を確認。Message Contentは必要性と承認を確認したBOTだけで有効化。
- [ ] 星燈の参加／退出、ロール変更、自動ロールに必要なprivileged `GUILD_MEMBERS` intentを有効化。
- [ ] General BOTにManage Rolesを付与し、付与対象ロールより上位にBOTロールを配置。
- [ ] Welcome、Sticky、各ログchannelでView Channel / Send Messages権限を確認。VC category / channelの振り分けも確認。
- [ ] StarTomo command setをdeploy後に再登録。ポイント配布を告知する前に `/game-config` と `/shop` を設定。
- [ ] Outbound TCP 443、stateful UDP Voice traffic、loopback-only HTTP API、SSH接続元制限を確認。
- [ ] Windows / Ubuntu双方のGo、Node.js、FFmpeg、Opusのversionを確認。
- [ ] 対象toolchainで `go vet ./...`、`go test ./...`、`npm ci`、`npm run build` を実行。
- [ ] `/ping`、`/join`、`/leave`、`/say` を登録。Windowsでは `deploy/windows/sync-commands.ps1`、Ubuntuでは `/usr/local/sbin/p1228-sync-commands.sh` を使用。
- [ ] ローカルバックアップ先のディスク容量と保持期間（日次7世代・週次84日・ログ90日）を確認。
- [ ] local journald capを確認。
- [ ] Event payload TTL / privacy policyを確定（24時間は暫定値）。

## 🎙️ Voice PoC（必須）

- [ ] Custom Adapterの `sendPayload(payload): boolean` が、上限付きmemory queueへのenqueue後に同期的に返ることを確認。
- [ ] QueueされたGateway opcode 4 payloadが正しいBOT sessionへ届くこと、任意opcodeが拒否されることを確認。
- [ ] Voice State Update / Voice Server Updateが所有するRead-aloud Workerだけに届くことを確認。
- [ ] Voice Gateway v8、DAVE negotiation、MLS update、Protocol Transition、現行AEAD modeを確認。
- [ ] UDP discovery、RTP / Opus playback、Speaking notificationを確認。
- [ ] 言ノ葉と紡言が別channelで同時再生できることを確認。
- [ ] Aを切断してB・Generalが影響を受けないこと、逆方向も確認。
- [ ] Voice Server Token / session dataがDB・ログに存在しないことを確認。

## 💾 Storage受け入れ

- [ ] journaldに500 MB / 14日上限と2 GB free-space protectionを設定。
- [ ] バックアップ失敗時に不完全なローカル世代が残らないことを確認。
- [ ] バックアップのmanifestとSHA-256が一致すること、ログアーカイブが90日後に削除されることを確認。
- [ ] Online SQLite backupが `integrity_check` とmanifest SHA検証に合格。
- [ ] 別pathへrestoreし、DB read / writeを試験。
- [ ] Disk / inodeの70 / 85 / 95% alertが届くことを確認。
- [ ] SQLite、Docker Volume、config directoryを自動削除する処理がないことを確認。
- [ ] バックアップ／アーカイブ障害時もBOT processが稼働を継続することを確認。

## 🩺 運用コマンド

```bash
systemctl status p1228-gateway p1228-general p1228-read-aloud-a p1228-read-aloud-b
journalctl -u p1228-gateway -f
journalctl -u p1228-read-aloud-a -f
journalctl --disk-usage
df -h
df -i
```

Dockerは任意で、標準runtimeでは使用しません。将来Dockerを採用する場合は `deploy/docker/daemon.json` を適用してcontainerを再作成してください。Docker data-rootをネットワークファイルシステムへ移動しないでください。
