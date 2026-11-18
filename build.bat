@echo off
rem p1228 のフルビルドを 1 コマンドで行うスクリプト (Windows 用)。
rem Go 製 Gateway と TypeScript 製 Workers の両方を順にビルドし、
rem 失敗したステップで即座に止まって、どのステップで落ちたかを表示する。
rem 最後に両方の結果をまとめて表示する。
setlocal
chcp 65001 >nul
cd /d "%~dp0"

where go >nul 2>nul
if errorlevel 1 (
  echo ERROR: Go が見つかりません。Go 1.26 以上をインストールしてください。 1>&2
  exit /b 1
)
where npm >nul 2>nul
if errorlevel 1 (
  echo ERROR: npm が見つかりません。Node.js 24 以上をインストールしてください。 1>&2
  exit /b 1
)

echo.
echo ===== Gateway 1/2 ビルド (go build ./...) =====
call go build -C gateway ./...
if errorlevel 1 goto fail_gateway_build

echo.
echo ===== Gateway 2/2 テスト (go test ./...) =====
call go test -C gateway ./...
if errorlevel 1 goto fail_gateway_test

echo.
echo ===== Workers 1/4 依存関係のインストール =====
if exist workers\package-lock.json (
  call npm ci --prefix workers --no-audit --no-fund
) else (
  call npm install --prefix workers --no-audit --no-fund
)
if errorlevel 1 goto fail_workers_install

echo.
echo ===== Workers 2/4 型チェック (npm run typecheck) =====
call npm --prefix workers run typecheck
if errorlevel 1 goto fail_workers_typecheck

echo.
echo ===== Workers 3/4 ビルド (npm run build) =====
call npm --prefix workers run build
if errorlevel 1 goto fail_workers_build

echo.
echo ===== Workers 4/4 テスト (npm test) =====
call npm --prefix workers test
if errorlevel 1 goto fail_workers_test

echo.
echo BUILD OK: p1228 のビルドとテストがすべて成功しました
echo   Gateway (Go):         build OK / test OK
echo   Workers (TypeScript): install OK / typecheck OK / build OK / test OK (成果物: workers\dist\)
exit /b 0

:fail_gateway_build
echo ERROR: ステップが失敗しました: Gateway ビルド (go build ./...) 1>&2
exit /b 1

:fail_gateway_test
echo ERROR: ステップが失敗しました: Gateway テスト (go test ./...) 1>&2
exit /b 1

:fail_workers_install
echo ERROR: ステップが失敗しました: Workers 依存関係のインストール 1>&2
exit /b 1

:fail_workers_typecheck
echo ERROR: ステップが失敗しました: Workers 型チェック (npm run typecheck) 1>&2
exit /b 1

:fail_workers_build
echo ERROR: ステップが失敗しました: Workers ビルド (npm run build) 1>&2
exit /b 1

:fail_workers_test
echo ERROR: ステップが失敗しました: Workers テスト (npm test) 1>&2
exit /b 1
