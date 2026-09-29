// Package version — 核心版本号，随发布手动维护，可经 ldflags 覆盖。
//
// 基于 ClawProxyHub（AGPL-3.0）修改构建：Core 语义保留（与上游 CHANGELOG 顶部对齐，
// 供插件 min_core_version 校验与握手），新增 Mobile 位（安卓端产品版本 versionName）。
package version

// Core 当前核心版本（语义保留自上游 ClawProxyHub v1.2.1）；
// 构建时 -ldflags "-X .../version.Core=x" 可覆盖。
// 1.2.8（2026-09-27，本地修订）：验收修复两处——①一键签到区分「跳过」：engine.runOne
// 对 success 且插件以 warning 级通知（需人工）或「跳过：」摘要前缀表达的情况，在批量进度
// 通道单列 status=skipped 并独立计数（DB 执行历史仍 success）；checkin done 事件增
// skipped 字段。②（面板侧，另见 Plugins.vue）市场重装按钮换 t-dialog。分组删除级联清理
// 路由已含（keys.go deleteGroup）。
// 1.2.7（2026-09-27，本地修订）：局域网监听安卓修复——targetSdk 34+ 对应用 UID 关闭
// netlink RTM_GETLINK（net.Interfaces 报 netlinkrib: permission denied，run-as 同 UID
// 复现），核心自检拿不到地址致 lan=off；新增 Options.LanIPHint（宿主 Kotlin 经
// java.net.NetworkInterface 枚举站点内 IPv4 经 bridge Config.LanIP 注入），自检失败时
// 兜底采用并在启动日志标注 source=host-hint。
// 同轮：dashboardQuota 聚合键由仅 instance_id 改为 (plugin_id, instance_id)——不同插件
// 共用默认实例时首行插件名会吞掉其余插件的账号/积分（面板「渠道概览」与首页账号概览
// 同源同修）。
// 同轮：deleteGroup 分组删除级联清理——仅指向该分组的模型路由整条删除（孤儿路由残留
// 会占用全局唯一路由名并迫使后续自动配置建 @插件名 后缀路由）、共享路由摘除条目、
// failover 悬空引用与 account_groups/group_proxies/key_routes 关联行一并清理。
// 1.2.6（2026-09-27，本地修订）：重品牌面板 dist 重新嵌入（任务历史时间 fmtDateTime
// 本地化渲染等），gateway-core.aar bind 重建（-androidapi 34）。
// 1.2.5（2026-09-27，本地修订）：v1.3.0 核心侧五块——①网关端口持久化（自动端口
// 首启随机落盘 gateway.port、复用、被占用重随机，端口变更经 PortChangeNotice/run-log/
// 站内通知三通道暴露）；②自动配置引擎（账号建档后自动建分组/模型路由/默认密钥，
// 跨插件同名模型确定性消歧 <模型名>@<插件名>，随登录响应回显端点+密钥+映射说明）；
// ③一键签到 POST /admin/tasks/checkin（全部启用规则立即执行 + NDJSON 逐任务进度）
// 与一键测活 POST /admin/probe/run（并行探测渠道×模型、控制变量、连接/首字/出字速度、
// 3 次退避重试）；④市场内置状态修正（installed 改磁盘真相 + android_reinstallable +
// reinstall-builtin 一键本地重装）与局域网监听（network.lan_enabled 默认开，仅 /v1
// 强制密钥 + /health，/admin 永不暴露，热切换）；⑤时区修复（bridge 注入设备偏移设
// time.Local + 任务历史返回前 In(Local) 规范化，修复安卓 UTC 落库晚 8 小时）。
// 1.2.4（2026-09-26，本地修订）：官方源 index 全量 10 个 Go 插件内置（abi 目录 +
// libplugin_<名>_<abi>.so jniLibs 命名，plugmgr/builtin 内嵌同源 manifest），市场
// 安卓适配标注（android_builtin/android_supported/android_unsupported_reason + 顶层
// android 标志，install-market 前置 422），插件源/市场索引并行抓取 + 短 TTL 缓存 +
// 连接级超时；重品牌面板 dist 重新内嵌（市场适配 UI）。
// 1.2.3（2026-09-26，本地修订）：Go 插件安卓化（内置 lobsterai/workbuddy/newapi 经
// nativeLibraryDir 启动，市场安装映射与卸载墓碑，见 core/plugmgr/builtin.go）；
// 隧道公网根路径品牌化落地页 + 可选面板暴露设置 tunnel.expose_admin（默认关）；
// 核心启动/隧道生命周期事件写入 run-logs（默认记录级别 error→info）；
// stop 序列释放 sqlite 句柄（Windows 文件锁）。
// 1.2.2（2026-09-25，本地修订）：修复隧道生命周期（Active 后误 cancel 致 cloudflared
// 被 SIGKILL）与安卓边缘 SRV 解析死路（--edge 预解析，见 core/tunnel/edge.go），
// 新增设置页「网关固定端口」配置项。
// 1.2.9（2026-09-29）：移植上游 fb6475c（gRPC 消息上限可经 env 配置，根治长会话 502）——
// sdk 层新增 CPH_GRPC_MAX_MSG_SIZE（默认 64MB，下限 4MB 回退），Serve 的 GRPCServer
// 闭包追加 MaxRecv/MaxSendMsgSize，plugmgr ClientConfig 追加 GRPCDialOptions
// （MaxCallRecv/MaxCallSendMsgSize），两端读同一变量（读取逻辑在 SDK 层，插件须重编生效）。
var Core = "1.2.9"

// Mobile 安卓端产品版本（NexPort versionName）；构建时同样可经 ldflags 覆盖。
// 1.4.3（2026-09-29）：随 APK v1.4.3（versionCode 8）同步——热修 zcode 对话 502
// （builtin/zcode 内嵌 system_prompt.json，随 libplugin_zcode 重编），AAR 随
// Mobile 位重编（版本链六处一致口径）。
// 1.4.2（2026-09-29）：随 APK v1.4.2（versionCode 7）同步——核心 AAR 换新（SDK
// v1.2.2 gRPC 消息上限修复 + 10 插件全量重编 + luahost 重编），关于页「核心版本」
// 行与隧道落地页徽标统一 1.4.2（验收④回归口径：AAR 内 Mobile 必须随产品版本同升）。
// 1.4.1（2026-09-28）：随 APK v1.4.1（versionCode 6）同步——源码以 mobile-port 分支
// 公开发布（fork 仓库），关于页署名/EULA 同步更新。
// 1.4.0（2026-09-27）：随 APK v1.4.0（versionCode 5）同步——首页/关于页版本行、
// bridge Version() 串、隧道落地页徽标统一口径（验收④回归：AAR 内 Mobile 滞留
// 1.3.0 致首页「版本: 1.3.0 (core …)」与 APK versionName 1.4.0 不一致）。
// 1.3.0（2026-09-26）：随 APK v1.3.0（versionCode 4）同步——首页版本行/隧道落地页
// 版本徽标与关于页、APK versionName 统一口径（验收回归：AAR 内 Mobile 滞留 1.1.0，
// bridge Version() 串三处不一致）。
// 1.1.0（2026-09-26）：随 APK v1.1.0（versionCode 2）全面改版同步。
var Mobile = "1.4.3"

// String 完整版本串："mobile (core x)" 形态，供 bridge.Version() 与关于页展示。
func String() string { return Mobile + " (core " + Core + ")" }
