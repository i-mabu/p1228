#!/usr/bin/env bash
# p1228 のフルビルドを 1 コマンドで行うスクリプト。
# Go 製 Gateway と TypeScript 製 Workers の両方を順にビルドし、
# 失敗したステップで即座に止まって、どのステップで落ちたかを表示する。
# 最後に両方の結果をまとめて表示する。

# ===== 1. 準備 =====
set -euo pipefail
cd "$(dirname "$0")"

step() {
  echo
  echo "===== $1 ====="
}

fail() {
  echo "ERROR: ステップが失敗しました: $1" >&2
  exit 1
}

command -v go >/dev/null 2>&1 || fail "前提チェック (Go が見つかりません)"
command -v node >/dev/null 2>&1 || fail "前提チェック (Node.js が見つかりません)"
command -v npm >/dev/null 2>&1 || fail "前提チェック (npm が見つかりません)"

# ===== 2. Gateway (Go) のビルド =====
step "Gateway 1/2 ビルド (go build ./...)"
(cd gateway && go build ./...) || fail "Gateway ビルド (go build ./...)"

# ===== 3. Gateway (Go) のテスト =====
step "Gateway 2/2 テスト (go test ./...)"
(cd gateway && go test ./...) || fail "Gateway テスト (go test ./...)"

# ===== 4. Workers の依存関係のインストール =====
step "Workers 1/4 依存関係のインストール"
if [ -f workers/package-lock.json ]; then
  (cd workers && npm ci --no-audit --no-fund) || fail "Workers 依存関係のインストール (npm ci)"
else
  (cd workers && npm install --no-audit --no-fund) || fail "Workers 依存関係のインストール (npm install)"
fi

# ===== 5. Workers の型チェック =====
step "Workers 2/4 型チェック (npm run typecheck)"
(cd workers && npm run typecheck) || fail "Workers 型チェック (npm run typecheck)"

# ===== 6. Workers のビルド =====
step "Workers 3/4 ビルド (npm run build)"
(cd workers && npm run build) || fail "Workers ビルド (npm run build)"

# ===== 7. Workers のテスト =====
step "Workers 4/4 テスト (npm test)"
(cd workers && npm test) || fail "Workers テスト (npm test)"

# ===== 8. 完了 =====
echo
echo "BUILD OK: p1228 のビルドとテストがすべて成功しました"
echo "  Gateway (Go):       build OK / test OK"
echo "  Workers (TypeScript): install OK / typecheck OK / build OK / test OK (成果物: workers/dist/)"
