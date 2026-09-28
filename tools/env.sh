#!/usr/bin/env bash
# env.sh — 构建环境（本机绝对路径；新 shell 需 source 本文件）。
# NexPort（基于 ClawProxyHub（AGPL-3.0）修改构建）。
#
# 全部工具用绝对路径调用（gomobile/gobind 不在 PATH；会话不继承 ANDROID_* 环境变量）。

export GOROOT_BIN="/c/Users/15884/go-sdk/go/bin"
export GO="$GOROOT_BIN/go.exe"
export PATH="/c/Users/15884/go/bin:$GOROOT_BIN:$PATH"   # gomobile / gobind + go

export JAVA_HOME="D:\\Java\\jdk-21.0.9"
export ANDROID_HOME="C:\\Users\\15884\\AppData\\Local\\Android\\Sdk"
export ANDROID_NDK_HOME="C:\\Users\\15884\\AppData\\Local\\Android\\Sdk\\ndk\\29.0.14206865"
export NDK="$ANDROID_NDK_HOME"
export NDK_CLANG="$NDK/toolchains/llvm/prebuilt/windows-x86_64/bin/clang.exe"
export NDK_READELF="$NDK/toolchains/llvm/prebuilt/windows-x86_64/bin/llvm-readelf.exe"

export GOPROXY=goproxy.cn,direct

# 本仓库根与产物目录
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CORE="$ROOT/core"
DIST="$CORE/dist"

# CGO 交叉编译公共前缀（android API 26；NDK r29 默认 16KB LOAD 对齐）
cgo_env() {
  local arch="$1"   # arm64 | amd64
  case "$arch" in
    arm64)  export CC="$NDK_CLANG --target=aarch64-linux-android26"; export GOARCH=arm64 ;;
    amd64)  export CC="$NDK_CLANG --target=x86_64-linux-android26";  export GOARCH=amd64 ;;
    *) echo "unknown arch: $arch" >&2; return 1 ;;
  esac
  export GOOS=android CGO_ENABLED=1
}

# verify16k 校验 ELF 为 ET_DYN、bionic 动态链接且全部 LOAD 对齐 0x4000（16KB 页）。
# 注意：统计用单条 awk 完成——本机 shell 在 `set -euo pipefail` 下，命令替换内
# ≥3 段管道会静默杀掉脚本（两段没事）；awk 退出码恒 0 也避开 `grep -v` 零行
# 输出退出 1 的坑（对齐全合格时恰好 0 行，正踩中）。
verify16k() {
  local f="$1" out="${2:-}"
  if [ -z "$out" ]; then out="$(mktemp)"; fi  # 单参调用（build-luahost/build-cloudflared）兜底临时文件，向后兼容
  trap 'rm -f "$out"' RETURN
  "$NDK_READELF" -h "$f" > "$out" 2>&1
  local type; type="$(awk '/Type:/{print $2}' "$out" | head -1)"
  [ "$type" = "DYN" ] || { echo "FAIL $f: Type=$type (需 DYN/PIE)" >&2; return 1; }
  "$NDK_READELF" -l "$f" > "$out" 2>&1
  local bad; bad="$(awk '/ LOAD / && ! / 0x4000$/ {n++} END {print n+0}' "$out")"
  [ "$bad" -eq 0 ] || { echo "FAIL $f: 存在非 0x4000 对齐的 LOAD 段" >&2; return 1; }
  echo "OK16K $f"
}
