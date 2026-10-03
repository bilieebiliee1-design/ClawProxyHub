#!/usr/bin/env bash
# build-android.sh — 一键构建安卓壳（release APK + AAB）。
# NexPort（基于 ClawProxyHub（AGPL-3.0）修改构建）。
#
# 前置：JDK 21 / Android SDK（platforms;android-36 + build-tools;36.0.0）/ Gradle 9.6.1；
#       核心构件已由 build-{dashboard,aar,luahost,cloudflared}.sh 产出到 core/dist/。
# 产物（硬性位置）：
#   app/build/outputs/apk/release/app-release.apk
#   app/build/outputs/bundle/release/app-release.aab
# 签名：keystore/keystore.properties（storeFile 相对仓库根；含密码，勿入库）。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

export JAVA_HOME="${JAVA_HOME:-D:\\Java\\jdk-21.0.9}"
export ANDROID_HOME="${ANDROID_HOME:-C:\\Users\\15884\\AppData\\Local\\Android\\Sdk}"
GRADLE="${GRADLE:-D:/Gradle/gradle-9.6.1/bin/gradle.bat}"

# 1) 核心构件落位（classes.jar / libgojni.so / libluahost.so / libcloudflared.so / 许可文件）
bash tools/prepare-android.sh

# 2) release APK + AAB（AGP 9.4.0，Gradle 9.6.1；packaging.jniLibs.useLegacyPackaging=true）
"$GRADLE" --console=plain :app:assembleRelease :app:bundleRelease

# 3) 规范产物名（v1.4.7 起 splits.abi 启用：universal 包补挂头注承诺的硬性位置
#    app-release.apk——splits 后 AGP 产 app-universal-release.apk，模拟器验收与
#    发行打包依赖该固定路径；universal=双 ABI 全量，与该路径历史口径一致）
cp app/build/outputs/apk/release/app-universal-release.apk app/build/outputs/apk/release/app-release.apk

ls -la app/build/outputs/apk/release/ app/build/outputs/bundle/release/
