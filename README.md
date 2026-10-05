# NexPort — 安卓端统一 AI 网关（核心仓库）

> **README 双变体说明（勿互相覆盖）**：本文件是开发工作树的构建/改造要点短版；
> [mobile-port 分支 README](https://github.com/bilieebiliee1-design/ClawProxyHub/blob/mobile-port/README.md)
> 是面向用户的扩写版（获取安装包/系统要求/为什么插件化等），为**发布真相源**。
> 两份内容定位不同、长期并存；同步源码分支时 README 一律在分支上单独修订，
> 不得用本文件整文件替换（单一化合并待下轮处理）。

> **NexPort 基于 [ClawProxyHub](https://github.com/ShadowSmallBaby/ClawProxyHub)（AGPL-3.0）修改构建。**
> 全部源码（含构建脚本）随本仓库发布；许可见根目录 `LICENSE`（AGPL-3.0）与
> `THIRD_PARTY_NOTICES.md`。

## 系统要求与设备兼容性

- **仅支持 64 位设备（`arm64-v8a`）**，Android 8.0（API 26）及以上；不提供
  armeabi-v7a（32 位）产物——32 位设备安装直接报
  `INSTALL_FAILED_NO_MATCHING_ABIS`（或商店/文件管理器提示「不兼容」），属预期
  排除而非故障。2017–2019 年低端机型存在相当比例 32 位用户空间，安装前可用
  `adb shell getprop ro.product.cpu.abilist` 确认（输出含 `arm64-v8a` 即为
  64 位）。不做 32 位产物的取舍：28 个 native 库全量重编、构建矩阵翻倍、APK
  再增约 150MB，收益仅为已趋淘汰的机型群，以本文档化排除替代。
- x86_64 构建仅供模拟器/开发调试，正式渠道以 arm64-v8a 为准（APK 按 ABI 拆分：
  arm64 专用 / x86_64 专用 / universal 兜底）。

## 目录

```
gateway-mobile/
├── core/      Go 核心库（io.nexport.gateway/core）——上游内核 fork＋安卓化改造
│   ├── app/         生命周期：Start/Stop/Restart（替代桌面 main.run）
│   ├── bridge 入口在 ../bridge/      ↓
│   ├── conf/  account/  adminapi/  database/  gateway/  plugmgr/  router/
│   ├── task/  setting/  event/  fingerprint/  janitor/  model/  runlog/
│   ├── logsink/   日志统一出口（安卓不可依赖 stdout）
│   ├── tunnel/    cloudflared 临时隧道（第二监听器仅 /v1 + /health）
│   ├── sdk/       插件契约（gRPC/protobuf，与上游线格式兼容）
│   ├── luahost/   Lua 插件宿主（独立 module，CGO 构建）
│   ├── web/       go:embed all:dist —— 重品牌面板产物
│   ├── cmd/nexcore/  桌面冒烟入口（安卓不经过此）
│   └── dist/      ★ 产物：gateway-core.aar / libluahost-*.so / cloudflared-android-*
├── bridge/    gomobile bind 包（io.nexport.gateway/bridge）
├── dashboard/ 面板源码（fork 自上游 web/，品牌 NexPort；pnpm build → core/web/dist）
├── app/       安卓壳工程（Kotlin 模块 :app；android/ 为对接说明）
├── android/   壳工程对接说明（模块本体在 app/）
├── tools/     构建脚本（env.sh + build-{dashboard,aar,luahost,cloudflared,android}.sh + prepare-android.sh）
├── LICENSE    AGPL-3.0 全文
└── THIRD_PARTY_NOTICES.md
```

## 核心与桌面版的差异（移植改造点）

1. **生命周期库化**：`app.Start(ctx, Options) (gatewayPort, tunnelPort, err)`——
   两监听器一律 `127.0.0.1:0` 先 bind 再回报实际端口；去 signal/os.Exit；`Restart()`
   供备份恢复闭环（restore/ 换入需重启）。
2. **隧道第二监听器**：仅挂 `gw.Handler()`（/v1/* + /health）；/admin、面板、图标
   端点拓扑不可达。隧道由 `tunnel.Manager` 管理（TryCloudflare，QUIC 失败自动
   `--protocol http2` 重试一次），默认关。
3. **日志统一出口**：`logsink`（database gorm logger、gateway、plugin host/manager、
   task、janitor、sdk 等 12+ 处原 stdout/stderr 全部改道）；bridge 节流后回调 Kotlin。
4. **TMPDIR 修复**：`app.Start` 前置设 TMPDIR，且 adminapi 三处 `os.CreateTemp`
   改走显式缓存目录（市场下载/离线上传/备份导出）。
5. **secret.key 双格式**：桌面裸 32 字节（备份换入场景）与安卓 Keystore 信封
   （`NXPORT-KEY-ENVELOPE v1` 头）兼容；信封缺注入时快速失败，绝不静默重生成。
6. **插件安卓分支**：lua 经 `nativeLibraryDir/libluahost.so --dir <插件目录>` 启动
   （不写 data/hosts）；Go 插件回查 `nativeLibraryDir/libplugin_<name>.so`（仅 APK
   预打包）；市场安装仅放行 runtime=lua 包，Go 包明确拒绝。
7. **服务端密码策略加强**：/admin/setup 要求 ≥10 位含大小写与数字（原生引导同规）。
8. **版本位**：`core/version/version.go` 单一事实源——`version.Core`（语义保留，随上游
   对齐，当前 1.2.9）+ `version.Mobile`（随 APK versionName/versionCode 同步，当前
   1.4.11 / versionCode 16）。

## 构建（Windows 主机，Git Bash）

```bash
source tools/env.sh
tools/build-dashboard.sh     # 面板：pnpm install+build → core/web/dist
tools/build-aar.sh           # gomobile bind → core/dist/gateway-core.aar
tools/build-luahost.sh       # CGO → libluahost-android-{arm64,x64}.so
tools/build-cloudflared.sh   # CGO → cloudflared-android-{arm64,x64}
tools/build-android.sh       # 安卓壳：prepare-android.sh + release APK/AAB
```

核心产物落在 `core/dist/`；安卓壳产物落在
`app/build/outputs/apk/release/app-release.apk` 与
`app/build/outputs/bundle/release/app-release.aab`（签名 keystore/ 与其 properties
见 `android/README.md`）。对接要点（jniLibs useLegacyPackaging、FGS、WebView 两桥、
Keystore 信封）见 `android/README.md`。

## 冒烟（桌面）

```bash
go run ./cmd/nexcore   # env CPH_DATA_DIR=./data；curl 127.0.0.1:<port>/health
```

## v1.3.0 核心侧改造（NexPort fork 本地修订）

> 以下五块均带单元/端到端回归（`core/app`、`core/adminapi`、`core/task`、`core/plugmgr`）。

### ① 网关端口持久化（`app/port.go` + `app/app.go`）

- **自动端口（Options.GatewayPort = 0，默认）**：首次启动随机绑定并把端口写入
  `<DataDir>/gateway.port`；之后每次启动**优先复用**该端口；仅当绑定失败（被占用等）
  才重新随机，并产生「端口变更（旧→新）」记录。持久化用文件而非 settings 表：监听器
  有意先于建库绑定（启动失败快速、不占资源），此刻 DB 尚未打开。
- **端口变更通知三通道**：`bridge.PortChangeNotice()`（Start 成功后查询一次，应用 UI
  弹提示）+ run-logs（面板运行日志页）+ 站内通知（面板铃铛）。
- **与「固定端口/自动」协同**：固定端口（1-65535）语义不变——被占用 Start 直接报错，
  不静默换口；固定端口绑定成功后同样落盘，用户切回「自动」时从最近生效端口继续。

### ② 自动配置引擎（`adminapi/autocfg.go`，开箱即用）

触发时机：账号建档成功（`POST /admin/accounts/login` 返回 `done:true`）后同步执行
（纯 DB 操作，毫秒级；模型目录已在建档时同步落库），结果随响应 `auto_config` 字段
回显，应用据此展示「本地/局域网端点 + 密钥 + 模型映射说明」。

1. **分组**：为插件的每个实例建一个分组并纳入其全部账号（同插件多账号同一分组；
   分组模型限定同实例，多实例插件按实例各建一组）。已有该插件+实例分组则复用并入。
2. **模型路由**：模型名**原样映射**——对外路由名 = 客户端请求的 model = 分组内真实
   模型 id（单分组权重 100）。
3. **默认 API 密钥**：库内无任何密钥时生成一把（与面板「API 密钥」同表同格式，列表/
   回显/删除全同步）；已有则复用第一把并在响应中回显明文（仅此刻，与 reveal 同源）。

**跨插件同名模型消歧（确定性策略）**：路由名全局唯一，事件顺序即优先级——先配置的
插件保留原名路由，后来者一律建 `<模型名>@<插件名>`（插件名唯一，结果确定可预测）；
同名路由已指向本插件分组时视为已配置（幂等跳过）。真实上游模型名在两种路由里都
保持原名，消歧只作用于对外路由名。该策略同时写入本节与 `autocfg.go` 文件头注释。

### ③ 一键签到与一键测活（`task/engine.go`、`adminapi/checkin.go`、`adminapi/probe.go`）

- **一键签到** `POST /admin/tasks/checkin`：立即执行**全部启用**的调度规则（停用规则
  按用户意图跳过），NDJSON 进度流逐任务播报 `{total}` → `running:true` →
  `{status,summary,error}` → `{done,success,failed}`。执行内核与调度触发完全同源
  （执行历史落 task_runs、通知落站内、凭据变更回写）；与调度 tick 互斥。
- **一键测活** `POST /admin/probe/run?concurrency=4`：并行探测所有渠道（可调度账号）×
  其模型目录，**控制变量法**——统一 "ping" 提示、max_tokens=16、temperature=0、
  stream=true、同一首帧超时，唯一变量是渠道与模型；探测直连插件 Chat、不落
  request_logs（不污染仪表盘统计）。指标：`connect_ms`（连接时间：首事件）、
  `first_token_ms`（首字时间）、`total_ms`、`tokens_per_sec`（出字速度 = 输出
  token ÷（total−first_token），上游未回 usage 按字符/3.5 估算）。失败**最多 3 次
  退避重试**（1s/2s/4s），NDJSON 实时输出 start/retry/result/done。

### ④ 市场内置状态修正 + 局域网监听（`adminapi/marketplace.go`、`plugmgr/builtin.go`、`app/lan.go`）

- **市场内置状态**：安装状态判定源从「运行中实例」改为「落盘 manifest」（停止的
  内置插件不再误判未安装）；新增 `android_reinstallable` 字段——内置注册表命中但
  落盘目录缺失（被卸载，墓碑在位）时置位，前端显示「已内置，未安装」并提供
  **一键本地重装**：`POST /admin/plugins/{name}/reinstall-builtin`（清墓碑 → 重新
  落盘 manifest/icon → 启动并恢复自启；二进制来自 APK nativeLibraryDir，无网络依赖）。
- **局域网监听**：新设置 `network.lan_enabled`（**默认开**，`PUT /admin/settings`
  的 `lan_enabled`，热切换无需重启）。开启后核心额外绑定**检测到的局域网 IPv4**
  （`lanListenIP`：wlan 优先、私网优先、绝不绑 0.0.0.0 通配）同一网关端口，专用 mux
  **仅暴露 /v1（复用网关 handler，逐请求强制 API 密钥）与 /health**；/admin 与面板
  在该 mux 上无注册 → 404 不暴露存在性。运行时事实经 `bridge.LanEndpoint()` 与
  `/admin/system/info`（`gateway_port`/`port_change`/`lan_enabled`/`lan_endpoint`）暴露。

### ⑤ 任务执行历史时区修复（`app/app.go` TZOffsetSeconds + `adminapi/tasks.go`）

根因：安卓上 Go 运行时加载不到 `/etc/localtime`，`time.Local` 回退 UTC，落库与
返回时间比设备本地差 8 小时。修复两段：

1. **入口注入**：应用经 bridge `Config.TZOffsetSeconds`（设备偏移秒）传入，app.Start
   设 `time.Local = time.FixedZone(...)`，新记录落库/序列化自带正确偏移；
2. **存量兜底**：任务执行历史（task_runs started_at/finished_at）与规则
   next_run_at/last_run_at 返回前统一 `In(Local)` 规范化——不改时刻、只换标注，
   面板按 RFC3339 截串展示（`Tasks.vue` slice(0,19)）即为本机时间。
