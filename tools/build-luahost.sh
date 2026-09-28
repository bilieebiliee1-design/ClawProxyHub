#!/usr/bin/env bash
# build-luahost.sh — CGO 构建 luahost（安卓双架构 libluahost.so，入 jniLibs）。
# NexPort（基于 ClawProxyHub（AGPL-3.0）修改构建）。
#
# 【必须覆盖上游的 CGO_ENABLED=0】上游 Dockerfile 以纯 Go 构建 luahost，照搬到安卓
# 则 DNS 必死（net/conf.go：android 必须 cgo resolver）。本脚本强制 CGO_ENABLED=1
# ＋ NDK clang：产物 PIE、动态链接 bionic（getaddrinfo 可用）、16KB LOAD 对齐。
#
# 落位：APK 打包侧把两个 .so 放进 jniLibs/arm64-v8a 与 jniLibs/x86_64（命名保持
# libluahost.so）；核心 plugmgr 在 GOOS=android 下经 nativeLibraryDir/libluahost.so
# 启动（见 core/plugmgr/luahost.go 安卓分支）。
set -euo pipefail
. "$(dirname "$0")/env.sh"

cd "$CORE/luahost"
for arch in arm64 amd64; do
  # 产物命名与 prepare-android.sh 消费口径一致：x86 架构用 x64 段（GOARCH 仍为 amd64）
  tag=$([ "$arch" = arm64 ] && echo arm64 || echo x64)
  out="$DIST/libluahost-android-$tag.so"
  cgo_env "$arch"
  "$GO" build -trimpath -ldflags "-s -w" -o "$out" .
  verify16k "$out"
  echo "built $out ($(du -h "$out" | cut -f1))"
done
echo LUAHOST_OK
