# ClawProxyHub

Claw 类客户端（LobsterAI / WorkBuddy 等）的统一管理反代网关：核心提供网关、路由、账号、分组、代理、密钥、任务调度与仪表盘，具体客户端实现以**插件**形式接入，插件源码与发布在独立仓库 [ClawProxyHubPlugins](https://github.com/ShadowSmallBaby/ClawProxyHubPlugins)。

## 架构

```
客户端（Claude Code / Codex CLI / Cherry Studio ...）
   │  /v1/messages · /v1/chat/completions · /v1/responses
   ▼
┌─ ClawProxyHub 核心（单二进制，内嵌仪表盘）───────────────────┐
│  网关：三协议归一化 → 统一信封（自动协议转换）               │
│  实例：插件 → 实例（站点地址 + 站点级配置）→ 账号 / 分组     │
│  路由：对外模型别名 → 分组（权重+真实模型映射）→ 账号        │
│       策略：round_robin / random / least_used / sticky       │
│  账号：多步登录 / 刷新 / 401 自动续期与换号 / 账号或分组代理 │
│  任务：interval / daily / once 调度（签到等维护任务）        │
│  存储：SQLite（golang-migrate 启动自动迁移）                 │
│  市场：多源索引 → 下载 .cphplugin → 校验 → 安装              │
│  运行时：Go 插件二进制 / Lua 插件（内置 LuaHost 沙箱）        │
└────────────┬─────────────────────────────────────────────────┘
             │ hashicorp/go-plugin（子进程 gRPC，契约 protocol v2）
   ┌─────────┴─────────┐
   ▼                   ▼
 lobsterai 插件     autoclaw 插件    （← ClawProxyHubPlugins 仓库构建发布，Go/Lua 双运行时）
```

## 快速开始

### 本地运行

```bash
# 仪表盘（go:embed 嵌入，需先构建）
cd web && pnpm install && pnpm build && cd ..

go build -o cph ./cmd/cph
./cph
```

浏览器打开 `http://127.0.0.1:8080` → 首次进入引导页创建管理员账号 → 「插件」页从插件市场安装 lobsterai / workbuddy（离线环境可上传 `.cphplugin` 包）。

### Docker

```bash
docker compose up -d   # 管理密码在 docker-compose.yml 中配置
```

镜像只含核心；插件在仪表盘安装后落在 `data/` 卷中持久化。

### 使用流程

1. **装插件**：「插件」→ 插件市场 → 安装（GitHub 不通时可在「插件源」加自建源，或上传离线包）
2. **（多实例插件）建实例**：「实例」→ 选插件 → 填站点地址与站点级配置（单例插件首次使用自动落默认实例）
3. **添加账号**：「账号」→ 添加 → 选择插件（多实例插件再选实例）与授权方式
   - lobsterai：浏览器 OAuth / 凭据文件导入
   - workbuddy：手机验证码 / 浏览器授权 / 凭据文件导入
   - newapi：API 密钥 / 密码 / 凭据文件
4. **建分组**：把同实例账号划入分组（分组 = 单实例账号池）
5. **（可选）绑代理**：账号或分组绑定出站代理（账号级优先），上游流量经代理
6. **建路由**：对外模型别名（如 `deepseek-flash`）→ 分组 + 真实模型 + 权重
7. **建密钥**：客户端调用凭据（明文只显示一次），可限定路由范围
8. **接入客户端**：

```bash
curl http://127.0.0.1:8080/v1/chat/completions \
  -H "Authorization: Bearer cph-xxxx" \
  -d '{"model":"deepseek-flash","messages":[{"role":"user","content":"你好"}]}'
```

Claude Code 等客户端把 base URL 指向 `http://127.0.0.1:8080`，任意协议入口自动转换。

## 配置（环境变量）

见 [.env.example](.env.example)。核心项：`CPH_ADDR`、`CPH_DATA_DIR`、`CPH_ADMIN_USERNAME/PASSWORD`（仅首启引导，之后以数据库为准）、`CPH_MARKETPLACE_URL`（自建市场索引）。

## 插件开发

插件是独立 Go 二进制，引用本仓库的 `sdk` 模块，实现 `pb.ClawPluginServer` 后一行启动：

```go
import "github.com/ShadowSmallBaby/ClawProxyHub/sdk"

func main() { sdk.Serve(&myPlugin{}) }
```

- 契约：`sdk/proto/cph.proto`（Handshake / Login 多步登录 / Refresh / ListModels / Chat 统一信封 / RunTask）
- 通用上游适配：`sdk/openaiup`（OpenAI 方言）、`sdk/anthropicup`（Anthropic 方言）、`sdk/responsesup`（Responses 方言）；SSE 分帧助手 `sdk/sse`
- 宿主回调（日志 / 存储 / 代理查询）：实现 `sdk.HostAware` 接收 `*sdk.Host`
- 参考实现：`examples/stub`（演示插件）与 [ClawProxyHubPlugins](https://github.com/ShadowSmallBaby/ClawProxyHubPlugins) 中的正式插件；不想写 Go 可用核心内置 LuaHost 写 **Lua 插件**（零编译，见插件仓库 AGENTS.md §11）
- 包格式 `.cphplugin`（zip 容器）：含 `manifest.json`（name / version / author / protocol_version / icon）+ `plugin-<os>-<arch>[.exe]`（Lua 插件为平台无关的 `main.lua`）；打包器与发布流程见插件仓库

## 项目结构

```
cmd/cph          核心入口
internal/        网关 / 路由 / 账号 / 任务 / 插件管理 / 管理 API（database/migrations 为 SQL 迁移）
sdk/             插件开发工具包（契约生成代码 + 上游适配器 + SSE/传输层）
hosts/luahost    Lua 插件运行时（独立 module，核心内置加载）
examples/stub    演示插件（开发参照）
web/             仪表盘（Vue3 + TDesign，go:embed 嵌入）
```

## 社区

Linux DO: [学AI上L站](https://linux.do)

## 许可证

本项目基于 [AGPL-3.0](LICENSE) 协议开源。

---

## NexPort 安卓版（mobile-port）

NexPort 是本项目（ClawProxyHub）的**安卓移植版**：同一个 Go 核心（网关、路由、账号、
分组、密钥、任务调度、插件市场）搬进手机，应用内一键完成核心启动、插件安装、临时
隧道与后台保活，面板内嵌 WebView，开箱即用；两端共享同一核心与同一套插件生态。

- **安装包（APK）**：本仓库 [Releases](https://github.com/bilieebiliee1-design/ClawProxyHub/releases) 提供（当前 v1.4.6，附 APK/AAB 与校验和）；也可从 [`mobile-port` 分支](https://github.com/bilieebiliee1-design/ClawProxyHub/tree/mobile-port)自行构建。
- **完整源码**：本仓库 [`mobile-port` 分支](https://github.com/bilieebiliee1-design/ClawProxyHub/tree/mobile-port)（含构建脚本与内置插件对应源码，AGPL-3.0）

### 为什么已有桌面端还要做移动端

桌面端确实更适合重度使用；移动端解决的是另一件事——网关随手机走、不必为一台网关
常开一台电脑。核心启动/插件安装/临时隧道/保活都在应用内一键完成，账号建档后还有
自动配置引擎直接生成分组、路由和默认密钥，局域网内设备可直接把 base URL 指到手机。

### 为什么选择插件化

一体化对接方式下用户只能被动等作者更新；插件化把能力开放出来——你可以为自家在用的
站点写一个 Lua 插件（免编译，打包成 `.cphplugin` 即装）。应用已在关于页内置
**lua-plugin-dev 开发指南**（SKILL.md + 官方 autoclaw 示例 + hello-world 模板），
把技能装进你自己的 AI agent 就能让 AI 帮忙写。写出好用的插件欢迎分享到
QQ 群 **1124936153**。

### 欢迎二改，但请依规

欢迎 fork 二改，但必须依规（AGPL-3.0）：保留 LICENSE 全文与版权声明；保留对上游
[ShadowSmallBaby/ClawProxyHub](https://github.com/ShadowSmallBaby/ClawProxyHub) 与本仓库的署名；
以 AGPL-3.0 同样开放你修改后的完整源码；应用内关于页署名不可移除。觉得有帮助的话，
欢迎给[本仓库](https://github.com/bilieebiliee1-design/ClawProxyHub)与
[上游仓库](https://github.com/ShadowSmallBaby/ClawProxyHub)点个 Star。

### 赞赏

![赞赏码](https://raw.githubusercontent.com/bilieebiliee1-design/ClawProxyHub/mobile-port/docs/reward-qr.jpg)

赞赏是为了获得更多持续维护的动力，如果收获足够的鼓励就可以一直为爱发电。赞赏后可进群
（QQ 1124936153）联系作者进入 **VIP 会员群**：相关反馈会优先满足、获得持久的技术支持。
不赞赏也完全可以正常使用全部功能。

构建方法与更多细节见 [`mobile-port` 分支 README](https://github.com/bilieebiliee1-design/ClawProxyHub/blob/mobile-port/README.md)。
