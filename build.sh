#!/usr/bin/env bash
# 交叉编译 Win7 兼容版本（必须使用 Go 1.20.x，Go 1.21 起不再支持 Win7）
# 依赖：mingw-w64（brew install mingw-w64）
set -e
cd "$(dirname "$0")"

# 默认使用 gvm 安装的 go1.20.14，可用 GO=/path/to/go 覆盖
GO="${GO:-$HOME/.gvm/gos/go1.20.14/bin/go}"
export GOROOT="$("$GO" env GOROOT)"
case "$("$GO" version)" in
  *go1.20*) ;;
  *) echo "需要 Go 1.20.x，当前：$("$GO" version)"; exit 1 ;;
esac

# Homebrew 的 mingw 默认链接 UCRT（api-ms-win-crt-*.dll），未打 KB2999226 的 Win7 会缺 DLL；
# 这里强制外部链接并改用系统自带的 msvcrt.dll
LDFLAGS="-H windowsgui -s -w -linkmode external -extldflags=-mcrtdll=msvcrt-os"
# 可选：WEBHOOK=... ./build.sh 把默认的企业微信机器人地址编译进程序（也可以运行后在“设置”里填写）
if [ -n "$WEBHOOK" ]; then
  LDFLAGS="$LDFLAGS -X main.defaultWebhook=$WEBHOOK"
fi

echo "==> 64 位"
GOOS=windows GOARCH=amd64 CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc \
  "$GO" build -trimpath -ldflags "$LDFLAGS" -o dist/x64/watchdog.exe .

echo "==> 32 位"
GOOS=windows GOARCH=386 CGO_ENABLED=1 CC=i686-w64-mingw32-gcc \
  "$GO" build -trimpath -ldflags "$LDFLAGS" -o dist/x86/watchdog.exe .

for f in dist/*/watchdog.exe; do
  if x86_64-w64-mingw32-objdump -p "$f" | grep -q "api-ms-win-crt"; then
    echo "错误：$f 仍依赖 UCRT"; exit 1
  fi
done
ls -lh dist/*/watchdog.exe
for d in dist/x64 dist/x86; do
  [ -f "$d/sciter.dll" ] || echo "提示：$d 缺少 sciter.dll（Sciter 4.4.x 对应位数），请放到 exe 同目录"
done
