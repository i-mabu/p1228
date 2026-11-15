# 💾 ローカルバックアップと復元

ログとSQLiteバックアップはローカルディスクにのみ保持します。遠隔保管はありません。VPS障害に備えた機外コピーが必要な場合は、別途手動で取得してください。

## 🗂️ 保存場所と保持期間

| 対象 | 場所 | 保持期間 |
|---|---|---|
| journalアーカイブ | `/var/spool/p1228/archive/logs/` | 90日 |
| SQLite日次バックアップ | `/var/backups/p1228/sqlite/` | 直近7世代 |
| SQLite週次バックアップ | `/var/backups/p1228/sqlite/weekly/YYYY/WW/` | 84日（12週間） |

journalアーカイブは前日分を `deploy/scripts/archive-logs.sh`（`p1228-log-archive.timer`、毎日03:20 UTC）が作成します。SQLiteバックアップは `deploy/scripts/backup-sqlite.sh`（`p1228-db-backup.timer`、毎日03:50 UTC）が作成します。

## 🧹 バックアップデータ

SQLiteバックアップでは、作成時にevent payload bodyをredactします。backup copy内の `event_inbox.payload_json` と `event_dead_letters.payload_json` を空objectへ置換し、Inbox rowをacknowledged扱いにして、restore時に古いeventを再配送しないようにします。稼働中DBは変更しません。Manifest（`<backup>.json`）には `event_payloads: redacted` を記録します。

> [!NOTE]
> 現在のSQLite backup処理はDB snapshotを対象とします。設定ファイル・辞書ファイルの自動backupは実装されていません。

## ♻️ バックアップとrestore

Daily timerはSQLite `.backup` を作成し、backup copy内のevent payloadをredact、`PRAGMA integrity_check`、size / SHA-256 manifest作成、ローカル再読込による検証を行います。ローカルには最新7世代を保持します。Weekly snapshotは日曜日に `weekly/YYYY/WW` へコピーされます。

RestoreしたDBにはbackup前のqueued event payloadが含まれません。過去Inboxの再生・再開目的には使用しないでください。

```bash
sudo systemctl start p1228-db-backup.service
sudo journalctl -u p1228-db-backup.service -n 100 --no-pager

# 既存destinationを上書きせず、新しいpathへrestore
sudo /usr/local/sbin/p1228-restore-sqlite.sh \
  /var/backups/p1228/sqlite/p1228-YYYYMMDDTHHMMSSZ.db \
  /var/lib/p1228/database/restore-test.db
```

月1回のrestore testを実施してください。稼働DBを置き換える前に、サービス停止、復元DBの検査、owner / mode設定、rollback copy作成を完了してください。
