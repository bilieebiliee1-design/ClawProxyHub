#!/usr/bin/env python3
"""make-source-zip.py — 打包本 fork 完整对应源码 → app/src/main/assets/nexport-source.zip。

AGPL-3.0 合规（§6 传达 / §13 网络交互提供源码）：发行包（APK/AAB）内随附完整对应源码，
关于页可导出。仅收录源形态与构建脚本；排除生成物与二进制（它们由所含脚本再生）：

  排除：build/、.gradle/、node_modules/、各处 dist 生成物、core/dist 二进制产物、
        app/libs/、app/src/main/jniLibs/（.so 二进制）、app/src/main/assets/nexport-source.zip（自身）、
        keystore/（签名私钥，绝不入源码包）、local.properties（机器相关）、*.log。

用法：python tools/make-source-zip.py [仓库根]
"""
import os
import sys
import zipfile

EXCLUDE_DIRS = {
    ".git", ".gradle", "build", "node_modules", "keystore",
    "dist",          # dashboard/dist、core/web/dist（生成物；由源码+脚本再生）
    "libs",          # app/libs（AAR 提取的 classes.jar，生成物）
    "jniLibs",       # .so 二进制（由 tools/*.sh 再生）
    "qa", "qa2", "qa3", "qa4", "qa5", "qa6", "qa7", "qa14",
    # 测试截图/UI dump 等过程产物，非对应源码（qa4-qa14 补录：合规复审 2026-09-27）
}

# 内置 Go 插件的完整对应源码树（AGPL §13；合规复审 2026-09-26 补录）：
# 来源仓库与 gateway-mobile 同级（../ClawProxyHubPlugins，构建入口 tools/build-plugins.sh
# 的 PLUGINS_REPO 默认路径）。收录进 zip 的 nexport/third_party/ClawProxyHubPlugins/，
# 排除其构建产物（dist/ 的 .cphplugin 与索引）与 .git。
EXTRA_TREES = [
    # (zip 内目标目录, 来源目录, 额外排除的目录名)
    # __file__ = <仓库根>/tools/make-source-zip.py → 仓库根上一级 = BBS 工作区根
    ("third_party/ClawProxyHubPlugins",
     os.path.join(os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__)))),
                  "ClawProxyHubPlugins"),
     {"dist"}),
]
EXCLUDE_FILES_PREFIX = ("nexport-source.zip",)  # 防自包含
EXCLUDE_FILES = {"local.properties"}
EXCLUDE_EXT = (".log", ".exe", ".so", ".aar", ".jks")

def included(rel: str, name: str, is_dir: bool) -> bool:
    if is_dir:
        return name not in EXCLUDE_DIRS and not name.startswith(".")
    if name in EXCLUDE_FILES or name.startswith(EXCLUDE_FILES_PREFIX):
        return False
    if name.endswith(EXCLUDE_EXT):
        return False
    return True

def main(root: str) -> int:
    root = os.path.abspath(root)
    out = os.path.join(root, "app", "src", "main", "assets")
    os.makedirs(out, exist_ok=True)
    dest = os.path.join(out, "nexport-source.zip")
    count = 0
    with zipfile.ZipFile(dest, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as z:
        for dirpath, dirnames, filenames in os.walk(root):
            rel_dir = os.path.relpath(dirpath, root).replace("\\", "/")
            # 剪枝：跳过排除目录
            dirnames[:] = [d for d in dirnames if included(rel_dir, d, True)]
            base = "" if rel_dir == "." else rel_dir + "/"
            for fn in sorted(filenames):
                if not included(base, fn, False):
                    continue
                full = os.path.join(dirpath, fn)
                arc = "nexport/" + base + fn
                z.write(full, arc)
                count += 1
        # 内置 Go 插件源码树（third_party/；AGPL §13 合规复审 2026-09-26 补录）
        for rel_target, src_root, extra_excludes in EXTRA_TREES:
            if not os.path.isdir(src_root):
                print(f"WARN: extra tree missing: {src_root}（内置插件源码未随源码包，AGPL §13 完整性受损）",
                      file=sys.stderr)
                continue
            for dirpath, dirnames, filenames in os.walk(src_root):
                rel_dir = os.path.relpath(dirpath, src_root).replace("\\", "/")
                dirnames[:] = [d for d in dirnames
                               if d not in extra_excludes and d not in EXCLUDE_DIRS
                               and not d.startswith(".")]
                base = "" if rel_dir == "." else rel_dir + "/"
                for fn in sorted(filenames):
                    if not included(base, fn, False):
                        continue
                    z.write(os.path.join(dirpath, fn), "nexport/" + rel_target + "/" + base + fn)
                    count += 1

    size = os.path.getsize(dest)
    print(f"SOURCE_ZIP_OK {dest} files={count} bytes={size}")
    return 0

if __name__ == "__main__":
    sys.exit(main(sys.argv[1] if len(sys.argv) > 1 else
                  os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")))
