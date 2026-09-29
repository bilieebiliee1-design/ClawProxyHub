#!/usr/bin/env bash
# rebuild-zcode-hotfix.sh — v1.4.3 热修：单插件重编 libplugin_zcode 双 ABI。
# 与 build-plugins.sh 完全同款命令（cgo_env/ldflags/verify16k/双布局/SHA256SUMS）。
set -euo pipefail
. "$(dirname "$0")/env.sh"

OUT="$DIST/plugins"
name="zcode"
mf="$CORE/plugmgr/builtin/$name/manifest.json"
ver="$(awk -F'"' '/"version"/{print $4; exit}' "$mf")"
[ -n "$ver" ] || { echo "$mf 无版本号" >&2; exit 1; }
echo "manifest version: $ver"

for arch in arm64 x64; do
  case "$arch" in
    arm64) cgo_env arm64; abidir="arm64-v8a"; abi="arm64" ;;
    x64)   cgo_env amd64; abidir="x86_64";   abi="x86_64" ;;
  esac
  out="$OUT/$abidir/libplugin_${name}_${abi}.so"
  ( cd "$CORE" && "$GO" build -trimpath -ldflags "-s -w -X main.version=$ver" -o "$out" "./plugmgr/builtin/$name" )
  verify16k "$out" "$out.readelf" && rm -f "$out.readelf"
  cp "$out" "$OUT/${arch}/libplugin_${name}_${arch}.so"
  echo "built $out (v$ver)"
  unset GOOS GOARCH CC
done

cd "$OUT"
find arm64-v8a x86_64 arm64 x64 -name 'libplugin_*.so' -type f | LC_ALL=C sort | xargs sha256sum > SHA256SUMS.txt
echo "PLUGIN_ZCODE_OK (sha256 manifest regenerated: $OUT/SHA256SUMS.txt)"
