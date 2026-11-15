#!/usr/bin/env bash
set -Eeuo pipefail

# ===== 1. 設定と前提確認 =====
ROOT="${P1228_ROOT:-/opt/p1228}"
ENV_DIR="${P1228_ENV_DIR:-/etc/p1228}"
NODE="${P1228_NODE_BIN:-$(command -v node)}"
[[ -x "$NODE" ]] || {
  echo "Node.js executable not found: $NODE" >&2
  exit 1
}
[[ -f "$ROOT/workers/dist/sync-commands.js" ]] || {
  echo "Worker build not found. Run install-ubuntu.sh first." >&2
  exit 1
}

# ===== 2. Workerごとの登録処理 =====
run_one() {
  local file="$1"
  [[ -f "$file" ]] || {
    echo "Missing env file: $file" >&2
    return 1
  }
  unset P1228_GATEWAY_URL P1228_WORKER_ID P1228_BOT_ID P1228_WORKER_SECRET P1228_APPLICATION_ID
  set -a
  # These root-owned env files contain only controlled KEY=value entries.
  # shellcheck disable=SC1090
  source "$file"
  set +a
  : "${P1228_GATEWAY_URL:?Missing P1228_GATEWAY_URL in $file}"
  : "${P1228_WORKER_ID:?Missing P1228_WORKER_ID in $file}"
  : "${P1228_BOT_ID:?Missing P1228_BOT_ID in $file}"
  : "${P1228_WORKER_SECRET:?Missing P1228_WORKER_SECRET in $file}"
  : "${P1228_APPLICATION_ID:?Missing P1228_APPLICATION_ID in $file}"
  (
    cd "$ROOT/workers"
    exec env -i \
      PATH="$PATH" HOME="${HOME:-/root}" \
      P1228_GATEWAY_URL="$P1228_GATEWAY_URL" \
      P1228_WORKER_ID="$P1228_WORKER_ID" \
      P1228_BOT_ID="$P1228_BOT_ID" \
      P1228_WORKER_SECRET="$P1228_WORKER_SECRET" \
      P1228_APPLICATION_ID="$P1228_APPLICATION_ID" \
      "$NODE" dist/sync-commands.js
  )
  unset P1228_GATEWAY_URL P1228_WORKER_ID P1228_BOT_ID P1228_WORKER_SECRET P1228_APPLICATION_ID
}

# ===== 3. 全BOTへの登録実行 =====
run_one "$ENV_DIR/general.env"
run_one "$ENV_DIR/read-aloud-a.env"
run_one "$ENV_DIR/read-aloud-b.env"
echo "Application commands synced for all three BOTs."
