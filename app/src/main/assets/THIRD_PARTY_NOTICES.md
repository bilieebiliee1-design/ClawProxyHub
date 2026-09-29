# THIRD_PARTY_NOTICES — 第三方组件与许可声明

NexPort 安卓端（gateway-mobile）基于 **ClawProxyHub**（AGPL-3.0，
https://github.com/ShadowSmallBaby/ClawProxyHub ）修改构建；本仓库整体按
**GNU Affero General Public License v3.0**（见根目录 `LICENSE`）授权发布。

本声明覆盖**发行包（AAR / APK / AAB / jniLibs）中实际包含或衍生的全部第三方组件**，
版本为发行构建时的解析/锁定版本（Go 侧以 `bridge/go.mod`（AAR 构建入口，`replace` 指向
core）与 `core/go.mod`、`core/luahost/go.mod` 为准；安卓侧以 AAB
`BUNDLE-METADATA/com.android.tools.build.libraries/dependencies.pb` 解析清单为准）。
以各上游仓库许可原文为准；全文可在对应源码仓库、Go module 缓存
（`$GOMODCACHE/<module>@<version>/LICENSE`）或 AndroidX 官方仓库获取。

## 一、Go 侧依赖（core / bridge / luahost / cloudflared）

| 组件 | 版本（发行构建解析） | 许可 | 用途 |
| --- | --- | --- | --- |
| github.com/ShadowSmallBaby/ClawProxyHub | v1.2.2（上游核心） | AGPL-3.0 | 内核 fork 基底 |
| github.com/hashicorp/go-plugin | v1.8.0 | MPL-2.0（Copyright IBM Corp.） | 插件子进程运行时 |
| google.golang.org/grpc | v1.83.2 | Apache-2.0 | 插件/宿主 RPC |
| google.golang.org/protobuf | v1.36.12 | BSD-3-Clause（Go 作者） | 契约序列化 |
| github.com/mattn/go-sqlite3 | v1.14.52 | MIT | SQLite 驱动（CGO） |
| github.com/golang-migrate/migrate/v4 | v4.20.1 | MIT | SQL 迁移（iofs 内嵌） |
| gorm.io/gorm ＋ gorm.io/driver/sqlite | v1.31.2 / v1.6.0 | MIT | ORM（WAL） |
| github.com/jinzhu/inflection | v1.0.0 | MIT | gorm 间接依赖（复数变形，随 libgojni 分发） |
| github.com/jinzhu/now | v1.1.5 | MIT | gorm 间接依赖（时间处理，随 libgojni 分发） |
| google.golang.org/genproto/googleapis/rpc | v0.0.0-20260630182238-925bb5da69e7 | Apache-2.0 | gRPC 间接依赖（status/codes，随 libgojni 分发） || github.com/yuin/gopher-lua | v1.1.1（core/luahost/go.mod） | MIT | Lua 插件运行时（白名单沙箱） |
| github.com/google/uuid | v1.6.0 | BSD-3-Clause（Google） | UUID |
| golang.org/x/crypto | v0.57.0 | BSD-3-Clause（Go 作者） | bcrypt 等 |
| golang.org/x/net | v0.59.0（bridge/go.mod pin——AAR 构建入口；core/go.mod 为 v0.58.0） | BSD-3-Clause（Go 作者） | HTTP/2 等 |
| golang.org/x/mobile | v0.0.0-20260908204917-8b95e45f8d3e | BSD-3-Clause（Go 作者） | gomobile/gobind 绑定 |
| cloudflared（github.com/cloudflare/cloudflared） | 2026.9.3（tools/build-cloudflared.sh:16 pin） | Apache-2.0 | 临时隧道（CGO 自建，非官方静态二进制） |

cloudflared 传递依赖中随二进制分发的代表性组件：quic-go（MIT）、
rs/zerolog（MIT）、urfave/cli（MIT）、coreos/go-systemd（Apache-2.0）、
prometheus/client_golang（Apache-2.0）、getsentry/sentry-go（MIT）、
klauspost/compress（BSD-3）、gopacket（BSD-3）。完整清单见 cloudflared 仓库
`go.mod` 与各依赖许可原文。

## 一之二、内置 Go 插件（APK 预打包，来源仓库 ClawProxyHubPlugins）

APK 内预打包的官方源 index 全量 10 个内置 Go 插件（cline 0.1.5 / commandcode 0.1.2 / ima 0.1.3 /
lobsterai 0.1.7 / mirasim 0.1.1 / newapi 0.1.4 / opencode 0.1.4 / todofor 0.1.1 / workbuddy 0.1.7 /
zcode 0.1.1，`core/dist/plugins/<abi>/libplugin_<名>_<abi>.so` → `app/src/main/jniLibs/<abi>/`，经
`tools/build-plugins.sh` 交叉编译）的**来源仓库**：

- **github.com/ShadowSmallBaby/ClawProxyHubPlugins**（https://github.com/ShadowSmallBaby/ClawProxyHubPlugins ）
  —— **AGPL-3.0**（仓库根 `LICENSE` 为 GNU Affero General Public License v3.0 全文，已本地核实）。
  插件源码以上游仓库 **ClawProxyHubPlugins**（本表上方链接）为提供渠道；随包源码包
  `nexport-source.zip` 已按合规复审 2026-09-28 裁定再次移除（v1.4.0 曾恢复，见下节变更记录）。
  构建脚本：gateway-mobile `tools/build-plugins.sh`；内嵌 manifest 见 core `plugmgr/builtin/<名>/`。

### 插件模块直接依赖（ClawProxyHubPlugins go.mod require，许可均经 go module 缓存本地核实）

| 组件 | 版本（发行构建解析） | 许可 | 用途 |
| --- | --- | --- | --- |
| github.com/ShadowSmallBaby/ClawProxyHub | v1.2.2（经 gateway-mobile `core/go.mod:6` 锚定解析） | AGPL-3.0 | 上游核心库（插件宿主 API/共享类型） |
| github.com/gorilla/websocket | v1.5.3 | BSD-2-Clause（Copyright 2013 The Gorilla WebSocket Authors；缓存 LICENSE 原文核实） | WebSocket 客户端 |
| github.com/warpdotdev/warp-proto-apis/apis/multi_agent | v0.0.0-20260917164411-f5c1878026bc | **AGPL-3.0**（上游仓库 LICENSE.md 为 GNU Affero General Public License v3，Copyright (C) 2020-2026 Denver Technologies, Inc.——经 GitHub API 于 2026-09-26 本地核实；该许可文件未随 go module 归档分发，归档内仅 go.mod/go.sum/.proto/生成代码，使用方需自上游仓库获取许可全文） | lobsterai 插件上游协议 stub |
| golang.org/x/crypto | v0.57.0 | BSD-3-Clause（Go 作者） | 加密原语 |
| google.golang.org/grpc | v1.83.2 | Apache-2.0 | 插件/宿主 RPC |
| google.golang.org/protobuf | v1.36.12 | BSD-3-Clause（Go 作者） | 契约序列化 |

### 插件模块间接依赖（第一节未列出者，均本地核实）

| 组件 | 版本 | 许可 | 备注 |
| --- | --- | --- | --- |
| github.com/hashicorp/yamux | v0.1.2 | MPL-2.0（Copyright 2014 HashiCorp, Inc.，缓存 LICENSE 原文核实） | go-plugin 传输多路复用 |
| github.com/oklog/run | v1.1.0 | Apache-2.0（缓存 LICENSE 原文核实） | go-plugin 进程组 |
| github.com/hashicorp/go-hclog | v1.6.3 | MIT（缓存 LICENSE 原文核实） | go-plugin 日志 |
| github.com/fatih/color | v1.13.0 | MIT（缓存 LICENSE.md 原文核实） | 终端着色 |
| github.com/mattn/go-colorable | v0.1.12 | MIT（缓存 LICENSE 原文核实） | Windows 终端 |
| github.com/mattn/go-isatty | v0.0.17 | MIT（缓存 LICENSE 原文核实） | TTY 检测 |
| github.com/golang/protobuf | v1.5.4 | BSD-3-Clause（Go 作者；缓存 LICENSE 原文核实） | grpc 传递 |
| golang.org/x/net / x/sys / x/text | v0.58.0 / v0.48.0 / v0.42.0 | BSD-3-Clause（Go 作者） | 传递 |

## 二、安卓侧依赖（:app 运行时类路径——发行包实际解析清单）

以下为 AAB `dependencies.pb` 记录的全部解析后组件：v1.1.0 构建共 72 条解析记录
（去重后 71 个 Maven 坐标，其中 1 条为工具链重复写入），版本即发行解析版本；
v1.3.0 起新增直接依赖（见变更记录），当次构建解析清单以发行 AAB 的
`BUNDLE-METADATA/com.android.tools.build.libraries/dependencies.pb` 为准。
以二进制打入 APK 的组件另见 APK `META-INF/*.version`（51 个，含本轮新增的
androidx.webkit_webkit）。AndroidX 全家、Material、Kotlin（stdlib/bom/jdk7/jdk8）、
kotlinx-coroutines、org.jetbrains:annotations、Guava listenablefuture、
error_prone_annotations 均 **Apache License 2.0**（各组件源码仓库 LICENSE 原文为准；
AndroidX 与 Material 为 androidx.dev / AOSP Apache-2.0，Kotlin 为 JetBrains Apache-2.0）。

### 直接依赖（app/build.gradle.kts）

| 组件 | 版本 | 许可 | 用途 |
| --- | --- | --- | --- |
| androidx.core:core-ktx | 1.13.1 | Apache-2.0 | KTX 扩展 |
| androidx.appcompat:appcompat | 1.7.0 | Apache-2.0 | AppCompatActivity 等 |
| com.google.android.material:material | 1.12.0 | Apache-2.0 | Material 主题/控件 |
| androidx.constraintlayout:constraintlayout | 2.1.4 | Apache-2.0 | 布局（传递引入） |
| androidx.fragment:fragment-ktx | 1.8.2 | Apache-2.0 | Fragment |
| androidx.viewpager2:viewpager2 | 1.1.0 | Apache-2.0 | （传递引入） |
| androidx.browser:browser | 1.8.0 | Apache-2.0 | Custom Tabs（外部链接） |
| androidx.webkit:webkit | 1.12.0 | Apache-2.0 | 面板对比度/主题同步（WebViewCompat.addDocumentStartJavaScript，v1.1.0 新增） |
| androidx.activity:activity-ktx | 1.9.3 | Apache-2.0 | 预测性返回（OnBackPressedCallback 回调链，v1.1.0 直接固定） |
| io.noties.markwon:core | 4.6.2 | Apache-2.0 | Markdown 渲染（协议/声明/LICENSE/THIRD_PARTY_NOTICES/首启 EULA；纯 TextView span、无 WebView，v1.2.0 新增） |
| io.noties.markwon:ext-tables | 4.6.2 | Apache-2.0 | Markdown 表格渲染（THIRD_PARTY_NOTICES 依赖表；v1.3.0 新增） |
| androidx.dynamicanimation:dynamicanimation | 1.0.0 | Apache-2.0 | 悬浮窗贴边/弹回 SpringAnimation（v1.3.0 起直接固定；此前经 Material 传递引入） |
| org.jetbrains.kotlin:kotlin-stdlib | 2.2.10 | Apache-2.0 | Kotlin 运行时（含 jdk7/jdk8 1.8.22、common 2.2.10、kotlin-bom 1.8.22） |
| org.jetbrains.kotlinx:kotlinx-coroutines-android / -core / -core-jvm / -bom | 1.6.4 | Apache-2.0 | 协程（APK 内 DebugProbesKt.bin 即其调试探针） |

### 传递依赖（随发行包打入；全部 Apache-2.0）

androidx.activity:activity:1.9.3（随 activity-ktx 1.9.3）；androidx.annotation:annotation:1.6.0、
annotation-jvm:1.6.0、annotation-experimental:1.4.0；androidx.appcompat-resources:1.7.0；
androidx.arch.core:core-common / core-runtime:2.2.0；androidx.cardview:1.0.0；
androidx.collection:collection / collection-ktx:1.1.0；androidx.concurrent:concurrent-futures:1.1.0；
androidx.constraintlayout-core:1.0.4；androidx.coordinatorlayout:1.1.0；androidx.core:core:1.13.1；
androidx.cursoradapter:1.0.0；androidx.customview:1.1.0、customview-poolingcontainer:1.0.0；
androidx.documentfile:1.0.0；androidx.drawerlayout:1.1.1；
androidx.emoji2:1.3.0、emoji2-views-helper:1.3.0；androidx.fragment:fragment:1.8.2；
androidx.interpolator:1.0.0；androidx.legacy:legacy-support-core-utils:1.0.0；
androidx.lifecycle:lifecycle-common / livedata / livedata-core / livedata-core-ktx /
lifecycle-process / runtime / runtime-ktx / viewmodel / viewmodel-ktx /
viewmodel-savedstate:2.6.2；androidx.loader:1.0.0；androidx.localbroadcastmanager:1.0.0；
androidx.print:1.0.0；androidx.profileinstaller:1.3.1；androidx.recyclerview:1.3.1；
androidx.resourceinspection:resourceinspection-annotation:1.0.1；androidx.savedstate:1.2.1、
savedstate-ktx:1.2.1；androidx.startup:startup-runtime:1.1.1；androidx.tracing:1.0.0；
androidx.transition:1.5.0；androidx.vectordrawable:1.1.0、vectordrawable-animated:1.1.0；
androidx.versionedparcelable:1.1.1；androidx.viewpager:1.0.0；
org.commonmark:commonmark:0.13.0 与 org.commonmark:commonmark-ext-gfm-tables:0.13.0
（随 io.noties.markwon:core / :ext-tables 解析，BSD-2-Clause，Copyright Atlassian Pty Ltd）；
io.noties.markwon 系自身传递 androidx.annotation:annotation:1.1.0（v1.1.0 已列 annotation 1.6.0 之上不重复计数）；
com.google.errorprone:error_prone_annotations:2.15.0；
com.google.guava:listenablefuture:1.0；org.jetbrains:annotations:13.0。

#### 安卓侧变更记录（合规复审追踪）

- **v1.4.2（2026-09-29）**：Go 侧依赖版本同步（本轮为版本表勘误轮，无许可/坐标变化）——
  §一 ClawProxyHub v1.2.1 → **v1.2.2**（core/go.mod:6 锚定；移植上游 fb6475c gRPC 消息
  上限修复，libgojni/libplugin_*/libluahost 均实编 v1.2.2，`go version -m` 实证）；
  §一之二 10 个内置插件版本号随 manifest 全量 bump（cline 0.1.5 / commandcode 0.1.2 /
  ima 0.1.3 / lobsterai 0.1.7 / mirasim 0.1.1 / newapi 0.1.4 / opencode 0.1.4 /
  todofor 0.1.1 / workbuddy 0.1.7 / zcode 0.1.1）；§一之二 插件模块 ClawProxyHub
  依赖 v1.2.0 → v1.2.2（经 core/go.mod 锚定解析，同上实证）。
- **v1.4.0（2026-09-28 复审修订）**：**再次移除随包源码包 `nexport-source.zip`**——
   2026-09-28 合规复审裁定恢复 v1.3.0 移除态（随包源码 zip 移除、源码提供渠道回到公开
   fork 仓库 https://github.com/bilieebiliee1-design/ClawProxyHub ）；关于页「导出源码包」
   入口同步移除。**仓库维护者须确保该仓库实际包含 gateway-mobile 对应源码**（2026-09-27 与
   2026-09-28 两次复审均实测仓库仅有桌面上游布局、无本 fork 源码——渠道有效性的前提），
   此为仓库侧持续义务。下条 2026-09-27 恢复随包提供的记录仅作历史。
- **v1.4.0（2026-09-27）**：**恢复随包源码包 `nexport-source.zip`**（v1.3.0 移除，
  本次依合规复审恢复——APK assets 随附，关于页提供「导出源码包」SAF 导出入口；包含
  gateway-mobile 全量源形态与 `nexport/third_party/ClawProxyHubPlugins/` 内置插件源码，
  不含二进制/签名材料）；新增依赖 androidx.browser / webkit / activity-ktx 等见上方各版本行；
  面板设置新增局域网监听（lan_enabled）开关。

- **v1.3.0（2026-09-26）**：新增直接依赖 io.noties.markwon:ext-tables:4.6.2（Markdown
  表格渲染；传递 org.commonmark:commonmark-ext-gfm-tables:0.13.0，BSD-2-Clause；
  Apache-2.0）；androidx.dynamicanimation:dynamicanimation:1.0.0 由 Material 传递
  依赖改为直接固定（悬浮窗 SpringAnimation）。随包源码包 `nexport-source.zip` 自
  APK assets 移除（§13 源码提供改为公开 fork 仓库）。

- **v1.2.0（2026-09-26）**：新增直接依赖 io.noties.markwon:core:4.6.2（Markdown 渲染；
  传递 org.commonmark:commonmark:0.13.0，BSD-2-Clause；Apache-2.0）。

- **v1.1.0（2026-09-26）**：新增直接依赖 androidx.webkit:webkit:1.12.0（面板主题/
  对比度 document-start 注入）；androidx.activity:activity(-ktx) 1.8.1 → **1.9.3**
  （直接固定 activity-ktx:1.9.3，activity 随之解析为 1.9.3）；补列
  kotlinx-coroutines-bom / -core-jvm:1.6.4（v1.0.0 时已解析但枚举遗漏）。
  解析记录 70（v1.0.0 声明口径）→ 72（dependencies.pb 实录，去重 71 坐标）。

## 三、面板（dashboard/，构建产物经 go:embed 内嵌）

| 组件 | 许可 |
| --- | --- |
| Vue 3 / Vue Router / pinia / vue-i18n | MIT |
| TDesign Vue Next ＋ tdesign-icons-vue-next | MIT |
| Vite / @vitejs/plugin-vue / vue-tsc / TypeScript | MIT / Apache-2.0（TypeScript） |
| echarts | Apache-2.0 |

## 四、其他

- SQLite（SQLite Public Domain）—— 经 mattn/go-sqlite3 静态编入。
- 安卓构建工具链（AGP/Gradle/NDK）属构建期工具，不在分发产物内。
- **随包源码包 `nexport-source.zip` 已再次移除**（v1.4.0 曾恢复；合规复审 2026-09-28
  裁定恢复 v1.3.0 移除态）：本 fork 完整对应源码以公开 fork 仓库
  https://github.com/bilieebiliee1-design/ClawProxyHub 为提供渠道（AGPL §13，见下节）；
  仓库内容的完整与同步由仓库维护者负责（见上「安卓侧变更记录」2026-09-28 条）。

## AGPL §13 源码提供

本 fork 的完整对应源码以公开 fork 仓库
**https://github.com/bilieebiliee1-design/ClawProxyHub**（= `BuildConfig.FORK_SOURCE_URL`）为提供渠道。
渠道沿革（两次合规复审口径相反，2026-09-28 复审裁定以本节为准）：v1.2.0 起随包附带源码包 →
v1.3.0（2026-09-26）移除并改称仅经该仓库提供 → 2026-09-27 复审核实**该仓库当时不含 NexPort 修改**
（仅有桌面上游布局），恢复随包源码包 `nexport-source.zip` → **2026-09-28 复审裁定恢复移除态**
（随包源码 zip 移除、源码凭证为 fork 仓库），关于页「导出源码包」入口同步移除。
**持续义务**：仓库维护者须使该仓库实际包含 gateway-mobile 对应源码（安卓壳 / core / bridge /
dashboard / tools 构建脚本）与 `nexport/third_party/ClawProxyHubPlugins/`（内置 Go 插件对应源码，
对应"一之二"节，`tools/build-plugins.sh` 为构建入口）——两次复审均实测该仓库尚缺本 fork 源码，
缺位期间 §13 渠道仅具形式。上游署名：「NexPort 基于 ClawProxyHub（AGPL-3.0）修改构建」；
内置插件基于 ClawProxyHubPlugins（AGPL-3.0）构建。
