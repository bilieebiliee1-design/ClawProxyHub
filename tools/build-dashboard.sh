#!/usr/bin/env bash
# build-dashboard.sh — 重品牌面板构建并落位 core/web/dist（go:embed）。
# NexPort（基于 ClawProxyHub（AGPL-3.0）修改构建）。
#
# 面板源码在 dashboard/（fork 自上游 web/，品牌已改 NexPort；上游署名保留在
# GithubIconBtn/VersionChip 与「关于」页链路）。pnpm 由 packageManager 锁定 11.17.0；
# 构建脚本审批走 pnpm-workspace.yaml（allowBuilds: esbuild）。
set -euo pipefail
. "$(dirname "$0")/env.sh"

PNPM="${PNPM:-/c/Users/15884/AppData/Roaming/npm/pnpm.cmd}"
cd "$ROOT/dashboard"

"$PNPM" install --frozen-lockfile --registry=https://registry.npmmirror.com
"$PNPM" run build-fast || "$PNPM" run build

rm -rf "$CORE/web/dist"
cp -r dist "$CORE/web/dist"
echo "dashboard embedded: $CORE/web/dist ($(du -sh "$CORE/web/dist" | cut -f1))"
