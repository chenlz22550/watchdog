#!/usr/bin/env bash
# 交叉编译 Win7 兼容版本（必须使用 Go 1.20.x，Go 1.21 起不再支持 Win7）
# 依赖：mingw-w64（macOS：brew install mingw-w64；Ubuntu：apt install gcc-mingw-w64 binutils-mingw-w64）
# 可选环境变量：
#   GO=/path/to/go     指定 Go 1.20.x（默认 gvm 的 go1.20.14，找不到时用 PATH 里的 go）
#   VERSION=v1.2.3     编译进程序的版本号
#   WEBHOOK=...        把默认的企业微信机器人地址编译进程序（也可以运行后在“设置”里填写）
set -e
cd "$(dirname "$0")"

GO="${GO:-$HOME/.gvm/gos/go1.20.14/bin/go}"
[ -x "$GO" ] || GO="$(command -v go)"
export GOROOT="$("$GO" env GOROOT)"
case "$("$GO" version)" in
  *go1.20*) ;;
  *) echo "需要 Go 1.20.x，当前：$("$GO" version)"; exit 1 ;;
esac

LDFLAGS="-H windowsgui -s -w -linkmode external"
if [ -n "$VERSION" ]; then
  LDFLAGS="$LDFLAGS -X main.version=$VERSION"
fi
if [ -n "$WEBHOOK" ]; then
  LDFLAGS="$LDFLAGS -X main.defaultWebhook=$WEBHOOK"
fi

# build <GOARCH> <mingw 前缀> <输出目录>
build() {
  local cc="$2-gcc" flags="$LDFLAGS"
  # Homebrew 的 mingw 默认链接 UCRT（api-ms-win-crt-*.dll），未打 KB2999226 的 Win7 会缺 DLL，
  # 编译器支持时强制改用系统自带的 msvcrt.dll；不支持的（如 Ubuntu 的 mingw）本来就默认 msvcrt
  local probe; probe="$(mktemp -d)"
  if echo 'int main(void){return 0;}' | "$cc" -x c - -mcrtdll=msvcrt-os -o "$probe/t.exe" 2>/dev/null; then
    flags="$flags -extldflags=-mcrtdll=msvcrt-os"
  fi
  rm -rf "$probe"
  GOOS=windows GOARCH="$1" CGO_ENABLED=1 CC="$cc" \
    "$GO" build -trimpath -ldflags "$flags" -o "$3/watchdog.exe" .
  if "$2-objdump" -p "$3/watchdog.exe" | grep -qi "api-ms-win-crt"; then
    echo "错误：$3/watchdog.exe 依赖 UCRT，Win7 上可能无法启动"; exit 1
  fi
}

echo "==> 64 位"
build amd64 x86_64-w64-mingw32 dist/x64
echo "==> 32 位"
build 386 i686-w64-mingw32 dist/x86

ls -lh dist/*/watchdog.exe
for d in dist/x64 dist/x86; do
  [ -f "$d/sciter.dll" ] || echo "提示：$d 缺少 sciter.dll（Sciter 4.4.x 对应位数），请放到 exe 同目录"
done
