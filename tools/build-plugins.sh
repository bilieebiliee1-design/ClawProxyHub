#!/usr/bin/env bash
# build-plugins.sh — 官方 Go 插件安卓交叉编译（NexPort fork，插件安卓化方案 ①）。
#
# 【源码来源（2026-09-29 起正树自包含；2026-10-25 起 v1.5.1 上游命名包布局）】
# 默认从正树 core/plugmgr/builtin/<名>/ 构建：完整 .go 源码与 manifest.json /
# icon.png 同目录（上游 ClawProxyHubPlugins d4090ee 已落正树）。自上游 e1be07a 起
# 插件源由根目录 package main 改为命名包 plugin.go（New(version) 构造器），桌面
# 入口移至 cmd/main.go——正树 builtin/<名>/ 即上游命名包源，构建目标为
# ./plugmgr/builtin/<名>/cmd（package main 入口）。SDK 依赖统一在 core/go.mod：
# 上游 github.com/ShadowSmallBaby/ClawProxyHub v1.5.3-0…0ddc177（replace 钉死，
# 见该文件头注——插件模块 @d4090ee 的 go.mod 要求 SDK≥6805f65，其 Serve 已迁
# process.go 且 //go:build !android，MVS 静默顶版会破安卓构建）+ ClawProxyHubPlugins
# d4090ee 伪版本（供 newapi 0.1.7 的 shared/challenge）。
# PLUGINS_REPO=<插件仓> 不再改变构建来源，仅作防漂移校验（比对各插件 manifest 一致；
# 仓库须检出含 plugins-go/ 目录的版本，d4090ee 起 plugins/ 已更名 plugins-go/）。
#
# 清单来源：应用「官方」插件源 index.json（ClawProxyHubPlugins 仓库根 index.json，
# 全量 29 个 Go 插件 @d4090ee：27 更新批次（26 升版 + workbuddy 0.1.10 净化失效
# 修复 + newapi 0.1.7 挑战服务）+ cnb/logicc 0.1.0 新增内置（v1.5.1）；cnb 上游
# 无 icon.png（manifest 无 icon 字段），内嵌注册表 icon 可缺省。另有 Lua autoclaw
# 0.1.3，动态安装不受 noexec 限制，不内置）。版本取各插件 manifest.json（builtin
# 源码与二进制同源同版本，市场比对逻辑见 adminapi/marketplace.go）。
# 源码偏离（新增偏离必须同步本注，重同步上游 builtin/ 时逐项保留，否则静默回退）：
#   1) 各插件 cmd/main.go 入口适配层（上游 plugins-go/<名>/cmd/main.go 移植）：
#      ①去掉上游 //go:build !android 标签（GOOS=android 需 main 入口；SDK pin
#      0ddc177 下 sdk.Serve 定义于 sdk.go 无平台标签）；②import 改指正树命名包
#      io.nexport.gateway/core/plugmgr/builtin/<名>（上游模块路径在本仓不可达）。
#      版本注入契约不变：-X main.version → <名>.New(version)。
#   2) zcode/plugin.go 内嵌 system_prompt.json（安卓 nativeLibraryDir 只读，磁盘
#      读取必失败，v1.4.3 热修随 0.1.3 重涂；systemBlocks 内嵌优先、磁盘回退，
#      见该文件头注）；
#   3) newapi/challenge.go challengeClient：上游 Host.SettingsContext(ctx,name)
#      （v2.0 *Context 变体）改旧 SDK 同步 Settings(name)（nil 失败语义一致，见
#      该文件内注）。
#   （doubao 0.1.4 起无本地偏离：PR #1 三偏离已上游化（55a3a51 合并，0.1.4 基座
#      含 cookieHeader/loginCookieHeader/msToken），按上游原样同步。）
#
# 产出（双布局，字节同源）：
#   A. jniLibs 规范（prepare-android.sh 消费口径，ABI 段为 GOARCH 口径）：
#     core/dist/plugins/arm64-v8a/libplugin_<名>_arm64.so
#     core/dist/plugins/x86_64/libplugin_<名>_x86_64.so
#   B. ask 规范 <abi> 字面目录（arm64 / x64，libplugin_<名>_<abi>.so）：
#     core/dist/plugins/arm64/libplugin_<名>_arm64.so
#     core/dist/plugins/x64/libplugin_<名>_x64.so
#   SHA256SUMS.txt 覆盖双布局全部 so（v1.5.1 起 29 插件 × 2 ABI × 2 布局 = 116 条）。
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
#   bash tools/build-plugins.sh                     # 从正树 builtin 源码构建
#   PLUGINS_REPO=/path/to/ClawProxyHubPlugins bash tools/build-plugins.sh
#                                                   # 同上，另校验与插件仓 manifest 无漂移
set -euo pipefail
. "$(dirname "$0")/env.sh"

REPO="${PLUGINS_REPO:-}"
OUT="$DIST/plugins"
BUILTIN="$CORE/plugmgr/builtin"
# 官方源 index.json 实际清单（2026-10-25 快照 @d4090ee，29 条 Go = 27 更新（26 升版
# + workbuddy 0.1.10 净化失效修复）+ newapi 0.1.7 + cnb/logicc 0.1.0 新增；Lua
# autoclaw 0.1.3 不内置。更新 index 后同步此列表与 plugmgr/builtin_test.go）
PLUGINS="chatjimmy cline cnb codearts codebuff commandcode devin doubao gorkcli ima improvado joycode lobsterai logicc loomy mimo mirasim newapi notion opencode postman puter qoder raccoon todofor trae warp workbuddy zcode"

manifest_version() { awk -F'"' '/"version"/{print $4; exit}' "$1"; }

# ABI 目录（jniLibs 规范）与文件名 ABI 段（GOARCH 口径，与 plugmgr/builtin.go 解析一致）
rm -rf "$OUT/arm64-v8a" "$OUT/x86_64" "$OUT/arm64" "$OUT/x64"
rm -f "$OUT"/libplugin-*-android-*.so   # 清理 v1.2.0 旧扁平命名产物，避免双套混用
mkdir -p "$OUT/arm64-v8a" "$OUT/x86_64" "$OUT/arm64" "$OUT/x64"

for name in $PLUGINS; do
  mf="$BUILTIN/$name/manifest.json"
  [ -f "$mf" ] || { echo "缺 $mf（正树内置源码不全？）" >&2; exit 1; }
  [ -f "$BUILTIN/$name/plugin.go" ] || { echo "缺 $BUILTIN/$name/plugin.go（正树内置源码不全？）" >&2; exit 1; }
  [ -f "$BUILTIN/$name/cmd/main.go" ] || { echo "缺 $BUILTIN/$name/cmd/main.go（入口适配层缺失？）" >&2; exit 1; }
  ver="$(manifest_version "$mf")"
  [ -n "$ver" ] || { echo "$mf 无版本号" >&2; exit 1; }

  for arch in arm64 x64; do
    case "$arch" in
      arm64) cgo_env arm64; abidir="arm64-v8a"; abi="arm64"   ;;
      x64)   cgo_env amd64; abidir="x86_64";   abi="x86_64"   ;;
    esac
    out="$OUT/$abidir/libplugin_${name}_${abi}.so"
    # 上游 tools/pack 同款 ldflags，正树模块内构建（SDK 0ddc177 由 core/go.mod
    # replace 锚定；构建目标 = 命名包 + cmd 入口，e1be07a 布局）
    ( cd "$CORE" && "$GO" build -trimpath -ldflags "-s -w -X main.version=$ver" -o "$out" "./plugmgr/builtin/$name/cmd" )
    verify16k "$out" "$out.readelf" && rm -f "$out.readelf"
    echo "built $out (v$ver, $(du -h "$out" | cut -f1))"
    unset GOOS GOARCH CC
  done

  # ask 规范 <abi> 字面目录拷贝（字节同源；x64 目录内文件名 ABI 段取 x64 口径）
  cp "$OUT/arm64-v8a/libplugin_${name}_arm64.so" "$OUT/arm64/libplugin_${name}_arm64.so"
  cp "$OUT/x86_64/libplugin_${name}_x86_64.so"   "$OUT/x64/libplugin_${name}_x64.so"
  echo "builtin source built: $BUILTIN/$name (v$ver)"
done

# 防漂移校验：显式给定 PLUGINS_REPO 时，比对插件仓 manifest 与正树一致
# （d4090ee 起插件目录为 plugins-go/；doubao 0.1.4 起与上游一致，无例外口径）
if [ -n "$REPO" ]; then
  [ -d "$REPO/plugins-go" ] || { echo "PLUGINS_REPO 缺 plugins-go/（须检出 d4090ee 及以后）: $REPO" >&2; exit 1; }
  for name in $PLUGINS; do
    rmf="$REPO/plugins-go/$name/manifest.json"
    [ -f "$rmf" ] || { echo "PLUGINS_REPO 缺 $rmf" >&2; exit 1; }
    diff "$rmf" "$BUILTIN/$name/manifest.json" >/dev/null || { echo "manifest 漂移: $name（插件仓与正树不一致）" >&2; exit 1; }
  done
  echo "PLUGINS_REPO manifest 一致性校验通过（29/29）"
fi

# SHA256SUMS.txt 覆盖双布局全部产物（abi 相对路径），供组包侧校验
cd "$OUT"
find arm64-v8a x86_64 arm64 x64 -name 'libplugin_*.so' -type f | LC_ALL=C sort | xargs sha256sum > SHA256SUMS.txt
echo "sha256 manifest: $OUT/SHA256SUMS.txt（双布局 29×2×2=116 条）"

echo "PLUGIN_BUILD_OK"
ls "$OUT" "$OUT/arm64-v8a" "$OUT/x86_64" "$OUT/arm64" "$OUT/x64"
