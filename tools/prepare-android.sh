#!/usr/bin/env bash
# prepare-android.sh — 把核心构件落位到安卓壳工程（app/ 模块）。
# NexPort（基于 ClawProxyHub（AGPL-3.0）修改构建）。
#
# 说明：:app 模块直接消费 core/dist/gateway-core.aar 的内容——
#   - classes.jar        → app/libs/gateway-core-classes.jar（implementation files(...)）
#   - jni/<abi>/libgojni.so → app/src/main/jniLibs/<abi>/libgojni.so
#   这样做的而不是 implementation(files("<..>.aar"))，是因为 AGP 对本地 .aar 的
#   files() 导入在 bundle/AAB 场景不可靠（无法携带 variant 元数据），且自行持有
#   jniLibs 才能精确控制 useLegacyPackaging 与 ABI 目录命名。AAR 本体仍是规范产物。
#   - luahost / cloudflared 二进制 → jniLibs/<abi>/libluahost.so、libcloudflared.so
#     （useLegacyPackaging=true 时安装期解入 nativeLibraryDir，exec 三通道的唯一来源）
#   - 内置 Go 插件（build-plugins.sh 产物，官方源 index 全量 25 个 Go 插件 @0f52234，
#     Lua autoclaw 不内置）→ jniLibs/<abi>/libplugin_<名>_<abi>.so
#     （插件安卓化方案 ①：核心在 GOOS=android 下按 GOARCH 推导回查 nativeLibraryDir/
#     libplugin_<名>_<abi>.so，manifest/icon 由核心 plugmgr/builtin 内嵌，无需随 assets 分发）
#   - LICENSE / THIRD_PARTY_NOTICES.md → assets/（关于页 AGPL 合规展示）
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST="$ROOT/core/dist"
APP="$ROOT/app"

for abi_pair in "arm64-v8a:arm64" "x86_64:x64"; do
  abi="${abi_pair%%:*}"; tag="${abi_pair##*:}"
  # 插件 .so 的 ABI 段（GOARCH 口径，与 build-plugins.sh 产物一致）：arm64 / x86_64
  ptag=$([ "$abi" = "arm64-v8a" ] && echo arm64 || echo x86_64)
  mkdir -p "$APP/src/main/jniLibs/$abi"
  cp "$DIST/libluahost-android-$tag.so" "$APP/src/main/jniLibs/$abi/libluahost.so"
  cp "$DIST/cloudflared-android-$tag"  "$APP/src/main/jniLibs/$abi/libcloudflared.so"
  # 内置 Go 插件：dist/plugins/<abi>/libplugin_<名>_<abi>.so → jniLibs/<abi>/（同名）
  # 先清旧产物（v1.2.0 无 ABI 段的 libplugin_<名>.so）防双套混装
  rm -f "$APP/src/main/jniLibs/$abi"/libplugin_*.so
  ls "$DIST/plugins/$abi"/libplugin_*_${ptag}.so >/dev/null 2>&1 || {
    echo "缺内置插件产物: $DIST/plugins/$abi/libplugin_*_${ptag}.so（先跑 bash tools/build-plugins.sh）" >&2; exit 1;
  }
  for plugin_so in "$DIST/plugins/$abi"/libplugin_*_${ptag}.so; do
    cp "$plugin_so" "$APP/src/main/jniLibs/$abi/$(basename "$plugin_so")"
  done
done

tmp="$(mktemp -d)"
unzip -q -o "$DIST/gateway-core.aar" -d "$tmp"
mkdir -p "$APP/libs"
cp "$tmp/classes.jar" "$APP/libs/gateway-core-classes.jar"
cp "$tmp/jni/arm64-v8a/libgojni.so" "$APP/src/main/jniLibs/arm64-v8a/libgojni.so"
cp "$tmp/jni/x86_64/libgojni.so"    "$APP/src/main/jniLibs/x86_64/libgojni.so"
rm -rf "$tmp"

mkdir -p "$APP/src/main/assets"
cp "$ROOT/LICENSE" "$APP/src/main/assets/LICENSE"
cp "$ROOT/THIRD_PARTY_NOTICES.md" "$APP/src/main/assets/THIRD_PARTY_NOTICES.md"

# v1.3.0 ④：随包源码包（nexport-source.zip）自 APK assets 移除（导出入口已于 v1.2.0
# 移除）——AGPL §13 源码提供改为指向公开 fork 仓库 BuildConfig.FORK_SOURCE_URL
# （https://github.com/bilieebiliee1-design/ClawProxyHub，EULA「开源与署名」/关于页/
# THIRD_PARTY_NOTICES 文案同步）。如需为 Releases 制作源码附件，可手动运行：
#   python tools/make-source-zip.py "$ROOT"
# 该脚本不再由本流程自动调用，产物不得放回 app/src/main/assets/。

echo "PREPARE_OK"
ls -la "$APP/src/main/jniLibs/arm64-v8a" "$APP/src/main/jniLibs/x86_64" "$APP/libs"
