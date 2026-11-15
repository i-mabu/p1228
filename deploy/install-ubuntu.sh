#!/usr/bin/env bash
set -Eeuo pipefail

# ===== 1. 前提チェック =====
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
[[ "$(id -u)" == "0" ]] || {
  echo "Run with sudo: sudo $0" >&2
  exit 1
}

for command in go node npm; do command -v "$command" >/dev/null || {
  echo "Missing $command; install the required toolchain first." >&2
  exit 1
}; done
GO_VERSION="$(go version | awk '{print $3}' | sed 's/^go//')"
NODE_VERSION="$(node --version | sed 's/^v//')"
if [[ "$(printf '%s\n' 1.23.0 "$GO_VERSION" | sort -V | head -n1)" != "1.23.0" ]]; then
  echo "Go >= 1.23.0 required; found $GO_VERSION" >&2
  exit 1
fi
if [[ "$(printf '%s\n' 24.17.0 "$NODE_VERSION" | sort -V | head -n1)" != "24.17.0" ]]; then
  echo "Node.js >= 24.17.0 required; found $NODE_VERSION" >&2
  exit 1
fi

# ===== 2. パッケージとユーザー・ディレクトリの準備 =====
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl ffmpeg sqlite3 gzip python3 build-essential pkg-config libopus-dev
getent group p1228 >/dev/null || groupadd --system p1228
id p1228 >/dev/null 2>&1 || useradd --system --gid p1228 --home-dir /var/lib/p1228 --create-home --shell /usr/sbin/nologin p1228
for worker_user in p1228-general p1228-read-aloud-a p1228-read-aloud-b; do
  id "$worker_user" >/dev/null 2>&1 || useradd --system --gid p1228 --no-create-home --shell /usr/sbin/nologin "$worker_user"
done

install -d -o p1228 -g p1228 -m 0750 /opt/p1228 /opt/p1228/bin /var/lib/p1228/database /var/cache/p1228/audio /var/log/p1228
install -d -o root -g p1228 -m 0750 /etc/p1228
install -d -o root -g root -m 0750 /var/backups/p1228/sqlite /var/spool/p1228/archive/logs

# ===== 3. 設定ファイルと環境ファイルの配置 =====
if [[ ! -f /etc/p1228/gateway.yaml ]]; then install -m 0640 -o root -g p1228 "$ROOT/config/gateway.yaml" /etc/p1228/gateway.yaml; fi
install -d -m 0755 /etc/systemd/journald.conf.d
install -m 0644 "$ROOT/deploy/journald/limits.conf" /etc/systemd/journald.conf.d/p1228-limits.conf

# ----- 3.1 環境ファイルの生成 -----
write_env_if_missing() {
  local target="$1"
  if [[ ! -e "$target" ]]; then
    cat >"$target"
    echo "Created $target (fill in the placeholders before starting BOT services)."
  else
    echo "Preserved existing $target."
    cat >/dev/null
  fi
}

write_env_if_missing /etc/p1228/gateway.env <<'ENV'
P1228_GENERAL_BOT_TOKEN=REPLACE_ME
P1228_READ_ALOUD_A_BOT_TOKEN=REPLACE_ME
P1228_READ_ALOUD_B_BOT_TOKEN=REPLACE_ME
P1228_GENERAL_WORKER_SECRET=REPLACE_ME_GENERATE_RANDOM_64_HEX_CHARS
P1228_READ_ALOUD_A_WORKER_SECRET=REPLACE_ME_GENERATE_RANDOM_64_HEX_CHARS
P1228_READ_ALOUD_B_WORKER_SECRET=REPLACE_ME_GENERATE_RANDOM_64_HEX_CHARS
P1228_GENERAL_APPLICATION_ID=REPLACE_ME
P1228_READ_ALOUD_A_APPLICATION_ID=REPLACE_ME
P1228_READ_ALOUD_B_APPLICATION_ID=REPLACE_ME
ENV
write_env_if_missing /etc/p1228/general.env <<'ENV'
P1228_GATEWAY_URL=http://127.0.0.1:8787
P1228_WORKER_ID=general
P1228_BOT_ID=general
P1228_WORKER_SECRET=REPLACE_ME_GENERATE_RANDOM_64_HEX_CHARS
P1228_APPLICATION_ID=REPLACE_ME
ENV
write_env_if_missing /etc/p1228/read-aloud-a.env <<'ENV'
P1228_GATEWAY_URL=http://127.0.0.1:8787
P1228_WORKER_ID=read_aloud_a
P1228_BOT_ID=read_aloud_a
P1228_WORKER_SECRET=REPLACE_ME_GENERATE_RANDOM_64_HEX_CHARS
P1228_APPLICATION_ID=REPLACE_ME
P1228_AUDIO_ENGINE_URL=http://127.0.0.1:50021
P1228_AUDIO_CACHE_DIR=/var/cache/p1228/audio
P1228_AUDIO_CACHE_MAX_MB=512
P1228_AUDIO_CACHE_TTL_HOURS=24
ENV
write_env_if_missing /etc/p1228/read-aloud-b.env <<'ENV'
P1228_GATEWAY_URL=http://127.0.0.1:8787
P1228_WORKER_ID=read_aloud_b
P1228_BOT_ID=read_aloud_b
P1228_WORKER_SECRET=REPLACE_ME_GENERATE_RANDOM_64_HEX_CHARS
P1228_APPLICATION_ID=REPLACE_ME
P1228_AUDIO_ENGINE_URL=http://127.0.0.1:50021
P1228_AUDIO_CACHE_DIR=/var/cache/p1228/audio
P1228_AUDIO_CACHE_MAX_MB=512
P1228_AUDIO_CACHE_TTL_HOURS=24
ENV
write_env_if_missing /etc/p1228/storage.env <<'ENV'
P1228_DB_PATH=/var/lib/p1228/database/p1228.db
P1228_DB_BACKUP_DIR=/var/backups/p1228/sqlite
P1228_ALERT_WEBHOOK_URL=
ENV
# systemd (PID 1) reads EnvironmentFile before dropping privileges. Keep all secrets root-only.
chown root:root /etc/p1228/*.env
chmod 0600 /etc/p1228/*.env

# ===== 4. 運用スクリプトとsystemdユニットの配置 =====
install -d -m 0755 /usr/local/sbin
install -m 0755 "$ROOT/deploy/scripts/archive-logs.sh" /usr/local/sbin/p1228-archive-logs.sh
install -m 0755 "$ROOT/deploy/scripts/backup-sqlite.sh" /usr/local/sbin/p1228-backup-sqlite.sh
install -m 0755 "$ROOT/deploy/scripts/restore-sqlite.sh" /usr/local/sbin/p1228-restore-sqlite.sh
install -m 0755 "$ROOT/deploy/scripts/monitor-storage.sh" /usr/local/sbin/p1228-monitor-storage.sh
install -m 0755 "$ROOT/deploy/scripts/sync-commands.sh" /usr/local/sbin/p1228-sync-commands.sh

install -m 0644 "$ROOT/deploy/systemd/p1228-gateway.service" /etc/systemd/system/
install -m 0644 "$ROOT/deploy/systemd/p1228-general.service" /etc/systemd/system/
install -m 0644 "$ROOT/deploy/systemd/p1228-read-aloud-a.service" /etc/systemd/system/
install -m 0644 "$ROOT/deploy/systemd/p1228-read-aloud-b.service" /etc/systemd/system/
install -m 0644 "$ROOT/deploy/systemd/p1228-storage-monitor.service" /etc/systemd/system/
install -m 0644 "$ROOT/deploy/systemd/p1228-storage-monitor.timer" /etc/systemd/system/
install -m 0644 "$ROOT/deploy/systemd/p1228-log-archive.service" /etc/systemd/system/
install -m 0644 "$ROOT/deploy/systemd/p1228-log-archive.timer" /etc/systemd/system/
install -m 0644 "$ROOT/deploy/systemd/p1228-db-backup.service" /etc/systemd/system/
install -m 0644 "$ROOT/deploy/systemd/p1228-db-backup.timer" /etc/systemd/system/

chown -R p1228:p1228 /var/lib/p1228 /var/cache/p1228 /var/log/p1228
chmod 0700 /var/lib/p1228/database
chmod 0750 /var/cache/p1228 /var/log/p1228
chown p1228:p1228 /var/cache/p1228/audio
chmod 2770 /var/cache/p1228/audio
chown root:root /var/backups/p1228 /var/backups/p1228/sqlite /var/spool/p1228 /var/spool/p1228/archive /var/spool/p1228/archive/logs
chmod 0750 /var/backups/p1228 /var/backups/p1228/sqlite /var/spool/p1228 /var/spool/p1228/archive /var/spool/p1228/archive/logs

# ===== 5. ビルドとタイマーの有効化 =====
cd "$ROOT/gateway"
go build -trimpath -ldflags='-s -w' -o "$ROOT/bin/p1228-gateway" ./cmd/gateway
cd "$ROOT/workers"
npm ci --no-audit --no-fund
npm run build
chown -R p1228:p1228 "$ROOT/workers/dist" "$ROOT/workers/node_modules" "$ROOT/bin"
systemctl daemon-reload
systemctl restart systemd-journald
systemctl enable --now p1228-storage-monitor.timer p1228-log-archive.timer p1228-db-backup.timer

cat <<'DONE'
Base installation completed. Before starting BOT services:
1. Edit /etc/p1228/gateway.env and all Worker env files with real tokens/secrets/IDs.
   Use three unique random Worker secrets (e.g. openssl rand -hex 32) and copy each secret to its matching Worker env.
   Set the same Application IDs in gateway.env and their matching Worker env files.
2. Review /etc/p1228/storage.env (local backup and log archive paths).
3. Register application commands using: sudo /usr/local/sbin/p1228-sync-commands.sh
4. Start services: systemctl enable --now p1228-gateway p1228-general p1228-read-aloud-a p1228-read-aloud-b

No BOT service is started automatically, so empty placeholder secrets cannot trigger live connections.
DONE
