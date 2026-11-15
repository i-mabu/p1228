#!/usr/bin/env bash
set -Eeuo pipefail

# ===== 1. 準備 =====
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# ===== 2. Goの整形（gofmt） =====
if command -v gofmt >/dev/null 2>&1; then
  (cd gateway && gofmt -w .)
  echo "gofmt: done"
else
  echo "gofmt not found; skipping Go formatting." >&2
fi

# ===== 3. TypeScriptと設定ファイルの整形（Prettier） =====
if command -v npm >/dev/null 2>&1; then
  if [[ ! -d workers/node_modules ]]; then
    (cd workers && npm ci --ignore-scripts --no-audit --no-fund)
  fi
  (cd workers && npm run format)
  echo "prettier: done"
else
  echo "npm not found; skipping Prettier formatting." >&2
fi

# ===== 4. Shellの整形（shfmt） =====
if command -v shfmt >/dev/null 2>&1; then
  shfmt -i 2 -ci -w deploy/install-ubuntu.sh deploy/scripts/*.sh scripts/format.sh .githooks/pre-commit
  echo "shfmt: done"
else
  echo "shfmt not found; skipping Shell formatting. Install shfmt v3.10.0: https://github.com/mvdan/sh/releases" >&2
fi

echo "All formatters finished."
