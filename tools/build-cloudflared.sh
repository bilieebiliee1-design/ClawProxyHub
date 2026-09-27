#!/usr/bin/env bash
# build-cloudflared.sh — 源码构建 CGO 版 cloudflared（安卓双架构）。
# NexPort（基于 ClawProxyHub（AGPL-3.0）修改构建）。
#
# 【为什么必须自建】官方发布版 cloudflared-linux-arm64 为完全静态 ET_EXEC 非 PIE、
# 纯 Go 解析器：安卓上即使 exec 成功也无法解析任何主机名（Go net/conf.go 明示
# android 必须 cgo resolver；纯 Go 回退 127.0.0.1:53 全灭）。本脚本以
# CGO_ENABLED=1 + NDK clang（动态链接 bionic，DNS 走 getaddrinfo）构建，
# 产物为 PIE ET_DYN、16KB LOAD 对齐，改名 libcloudflared.so 入 APK jniLibs
# （需 packaging { jniLibs { useLegacyPackaging = true } } 才会解出实体文件供 exec）。
#
# 用法：tools/build-cloudflared.sh [版本]   # 默认 2026.9.3
set -euo pipefail
. "$(dirname "$0")/env.sh"

VERSION="${1:-2026.9.3}"
SRC="${CFD_SRC:-/tmp/cfd-src}"

if [ ! -d "$SRC" ]; then
  git clone --depth 1 --branch "$VERSION" https://github.com/cloudflare/cloudflared "$SRC"
fi
cd "$SRC"
git log --oneline -1

for arch in arm64 amd64; do
  out="$DIST/cloudflared-android-$arch"
  cgo_env "$arch"
  "$GO" build -trimpath -ldflags "-s -w" -o "$out" ./cmd/cloudflared
  verify16k "$out"
  echo "built $out ($(du -h "$out" | cut -f1))"
done
echo CLOUDFLARED_OK
