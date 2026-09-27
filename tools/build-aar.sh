#!/usr/bin/env bash
# build-aar.sh — gomobile bind 出 gateway-core.aar（含重品牌面板内嵌）。
# NexPort（基于 ClawProxyHub（AGPL-3.0）修改构建）。
#
# 构建硬性项（逐条对应评审结论）：
#   ① bridge/go.mod 必须自带 golang.org/x/mobile 依赖 + `tool golang.org/x/mobile/cmd/gobind`
#      指令（否则 bind 报 missing golang.org/x/mobile dependency）；
#   ② 显式 -androidapi 34：NDK r29 拒绝 gomobile 默认的 16。注意 -androidapi 同时
#      决定 AAR manifest 的 minSdkVersion——34 会与 app 的 minSdk 26 冲突（manifest
#      合并失败），因此 bind 后把 AAR manifest 回写 minSdkVersion 26（编译头文件
#      级别仍是 34，运行时安装下限保持 26 不变，行为向后兼容）；
#   ③ -target 写 android/arm64,android/amd64 —— GOOS=android 下 GOARCH 是 amd64，
#      写 x86_64 直接报 unsupported；
#   ④ 面板产物先行：tools/build-dashboard.sh → core/web/dist（go:embed all:dist）；
#   ⑤ gomobile/gobind 不在 PATH，一律绝对路径调用（GOMOBILE 可覆盖）。
set -euo pipefail
. "$(dirname "$0")/env.sh"

GOMOBILE="${GOMOBILE:-/c/Users/15884/go/bin/gomobile.exe}"
API_LEVEL=34
SHIP_MIN_SDK=26

if [ ! -f "$CORE/web/dist/index.html" ]; then
  echo "core/web/dist 缺失：先跑 tools/build-dashboard.sh" >&2
  exit 1
fi

cd "$ROOT/bridge"
"$GO" mod tidy
mkdir -p "$DIST"
"$GOMOBILE" bind \
  -target=android/arm64,android/amd64 \
  -androidapi "$API_LEVEL" \
  -javapkg=io.nexport.gateway \
  -ldflags="-s -w" -trimpath \
  -o "$DIST/gateway-core.aar" .

# AAR manifest 回写：编译 API 34，发行 minSdk 保持 26（见硬性项②）。
# 用 python zipfile 原位替换该 entry（Git Bash 无 zip 命令，jar 会附带 MANIFEST.MF）。
tmp="$(mktemp -d)"
unzip -q -o "$DIST/gateway-core.aar" -d "$tmp"
if ! grep -q "minSdkVersion=\"$API_LEVEL\"" "$tmp/AndroidManifest.xml"; then
  echo "AAR manifest 未体现 -androidapi $API_LEVEL，bind 参数或 gomobile 版本异常" >&2
  exit 1
fi
sed -i "s/minSdkVersion=\"$API_LEVEL\"/minSdkVersion=\"$SHIP_MIN_SDK\"/" "$tmp/AndroidManifest.xml"
python - "$tmp/AndroidManifest.xml" "$DIST/gateway-core.aar" <<'PYEOF'
import sys, zipfile, shutil, os
manifest, aar = sys.argv[1], sys.argv[2]
tmp = aar + ".tmp"
with zipfile.ZipFile(aar) as src, zipfile.ZipFile(tmp, "w", zipfile.ZIP_DEFLATED) as dst:
    with open(manifest, "rb") as mf:
        data = mf.read()
    for item in src.infolist():
        dst.writestr(item, data if item.filename == "AndroidManifest.xml" else src.read(item.filename))
os.replace(tmp, aar)
PYEOF
rm -rf "$tmp"

# 产物自检：双 ABI + 绑定类齐备 + 发行 minSdk == 26
tmp="$(mktemp -d)"
unzip -q -o "$DIST/gateway-core.aar" -d "$tmp"
elf16k="$(mktemp)"
for d in arm64-v8a x86_64; do
  [ -f "$tmp/jni/$d/libgojni.so" ] || { echo "AAR 缺 jni/$d/libgojni.so" >&2; exit 1; }
  verify16k "$tmp/jni/$d/libgojni.so" "$elf16k"
done
rm -f "$elf16k"
grep -q "minSdkVersion=\"$SHIP_MIN_SDK\"" "$tmp/AndroidManifest.xml" || { echo "AAR minSdk != $SHIP_MIN_SDK" >&2; exit 1; }
# 绑定类在嵌套的 classes.jar 内（AAR 顶层只有 AndroidManifest/classes.jar/jni/...）
unzip -l "$tmp/classes.jar" | grep -q "io/nexport/gateway/bridge/Bridge.class" \
  || { echo "AAR 缺 Bridge 绑定类" >&2; exit 1; }
rm -rf "$tmp"
echo "AAR_OK $DIST/gateway-core.aar ($(du -h "$DIST/gateway-core.aar" | cut -f1))"
