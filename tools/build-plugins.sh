#!/usr/bin/env bash
# build-plugins.sh — 官方 Go 插件安卓交叉编译（NexPort fork，插件安卓化方案 ①）。
#
# 清单来源：应用「官方」插件源 index.json（ClawProxyHubPlugins 仓库根 index.json，
# 全量 10 个，均为 Go 插件；仓库另有 chatjimmy/codebuff 等 11 个未发布项 + Lua
# autoclaw，未上 index 不内置）。与上游 pack 流程一致，版本取各插件 manifest.json
# （仓库内版本可能领先 index 发布版本，如 lobsterai 源码 0.1.5 / index 0.1.4——
# 内置二进制以源码 manifest 为准，市场比对逻辑见 adminapi/marketplace.go）。
#
# 产出（jniLibs 规范命名 libplugin_<名>_<abi>.so，按 ABI 分目录，prepare-android.sh
# 原样拷入 app/src/main/jniLibs/<abi>/，nativeLibraryDir 内文件名保持一致）：
#   core/dist/plugins/arm64-v8a/libplugin_<名>_arm64.so
#   core/dist/plugins/x86_64/libplugin_<名>_x86_64.so
# 同时把 manifest.json + icon.png 同步进 core/plugmgr/builtin/<名>/
# （核心内嵌的内置插件描述，与二进制同版本同源）。
#
# 【必须 CGO_ENABLED=1 + NDK clang，不能照搬上游 pack 的纯 Go】与 build-luahost.sh
# 同理：安卓上 Go 内建 resolver 无 /etc/resolv.conf → DNS 死路（见 tunnel/edge.go 头注），
# 插件要访问上游 HTTPS 服务，必须动态链接 bionic（getaddrinfo）。产物 PIE、
# 16KB LOAD 对齐（NDK r29 默认），verify16k 逐个校验。
# 注意：ask 里"CGO_ENABLED=0 静态二进制"的提法在此不可用——纯 Go 静态产物在安卓上
# 无法解析任何域名（netgo 无 /etc/resolv.conf），会让全部插件运行期瘫痪，故沿用
# v1.2.0 三个内置插件已上真机验证的 CGO+bionic 方案；-trimpath / -ldflags "-s -w"
# / 版本注入与 ask 要求保持一致。
#
# 用法：
#   bash tools/build-plugins.sh                     # 用默认仓库路径构建
#   PLUGINS_REPO=/path/to/ClawProxyHubPlugins bash tools/build-plugins.sh
set -euo pipefail
. "$(dirname "$0")/env.sh"

REPO="${PLUGINS_REPO:-$(dirname "$ROOT")/ClawProxyHubPlugins}"
OUT="$DIST/plugins"
BUILTIN="$CORE/plugmgr/builtin"
# 官方源 index.json 实际清单（2026-09-26 快照，10 条，全 Go；更新 index 后同步此列表）
PLUGINS="cline commandcode ima lobsterai mirasim newapi opencode todofor workbuddy zcode"

[ -d "$REPO/plugins" ] || { echo "ClawProxyHubPlugins 仓库不存在: $REPO（先 git clone，或用 PLUGINS_REPO= 指定）" >&2; exit 1; }

manifest_version() { awk -F'"' '/"version"/{print $4; exit}' "$1"; }

# ABI 目录（jniLibs 规范）与文件名 ABI 段（GOARCH 口径，与 plugmgr/builtin.go 解析一致）
rm -rf "$OUT/arm64-v8a" "$OUT/x86_64"
rm -f "$OUT"/libplugin-*-android-*.so   # 清理 v1.2.0 旧扁平命名产物，避免双套混用
mkdir -p "$OUT/arm64-v8a" "$OUT/x86_64"
cd "$REPO"

for name in $PLUGINS; do
  mf="plugins/$name/manifest.json"
  [ -f "$mf" ] || { echo "缺 $mf（index 清单与仓库不同步？）" >&2; exit 1; }
  ver="$(manifest_version "$mf")"
  [ -n "$ver" ] || { echo "$mf 无版本号" >&2; exit 1; }

  for arch in arm64 x64; do
    case "$arch" in
      arm64) cgo_env arm64; abidir="arm64-v8a"; abi="arm64"   ;;
      x64)   cgo_env amd64; abidir="x86_64";   abi="x86_64"   ;;
    esac
    out="$OUT/$abidir/libplugin_${name}_${abi}.so"
    # 上游 tools/pack 同款 ldflags + CGO 安卓交叉编译（cgo_env 设置 GOOS/GOARCH/CC）
    "$GO" build -trimpath -ldflags "-s -w -X main.version=$ver" -o "$out" "./plugins/$name"
    verify16k "$out" "$out.readelf" && rm -f "$out.readelf"
    echo "built $out (v$ver, $(du -h "$out" | cut -f1))"
    unset GOOS GOARCH CC
  done

  # 内嵌描述与二进制同源同步（版本必然一致；builtin_test.go 校验三件套）
  mkdir -p "$BUILTIN/$name"
  cp "$mf" "$BUILTIN/$name/manifest.json"
  cp "plugins/$name/icon.png" "$BUILTIN/$name/icon.png"
  echo "builtin assets synced: $BUILTIN/$name (v$ver)"
done

# SHA256SUMS.txt 覆盖新布局全部产物（abi 相对路径），供组包侧校验
cd "$OUT"
find arm64-v8a x86_64 -name 'libplugin_*.so' -type f | LC_ALL=C sort | xargs sha256sum > SHA256SUMS.txt
echo "sha256 manifest: $OUT/SHA256SUMS.txt"

echo "PLUGIN_BUILD_OK"
ls -la "$OUT" "$OUT/arm64-v8a" "$OUT/x86_64"
