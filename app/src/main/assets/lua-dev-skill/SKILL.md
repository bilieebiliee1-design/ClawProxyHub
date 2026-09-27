---
name: lua-plugin-dev
description: NexPort（ClawProxyHub fork）Lua 插件开发指南。当用户想为 NexPort/ClawProxyHub 网关写一个 Lua 插件（接入一个新的 AI 上游站点/服务，支持账号登录、模型目录、对话流式转发），或询问 .cphplugin 打包、manifest.json 字段、cph.* 宿主 API、protocol v2 握手契约、Lua 插件沙箱限制时使用本技能。产出：可直接安装的 .cphplugin（zip：manifest.json + main.lua）。
---

# Lua 插件开发（NexPort / ClawProxyHub）

本指南面向「让 AI 帮我写一个 Lua 插件」的场景：你的目标产物是一个 `.cphplugin` 包（zip），安装进 NexPort 核心后，用户即可在面板里添加账号、浏览模型，并通过统一网关对话。所有 API 名称、字段、协议细节均取自真实源码：

- 契约：`sdk/proto/cph.proto`（405 行，核心与插件唯一耦合点）
- Lua 宿主：`hosts/luahost/*.go`（Go 实现的通用插件，把契约 RPC 翻译成 Lua 调用；NexPort fork 中位于 `gateway-mobile/core/luahost/`，与上游逐文件比对仅模块导入路径不同）
- 官方示例：`examples/autoclaw/`（本目录收录，完整源码）、`examples/hello-world/`（最小可运行模板）

## 何时使用

- 用户说「给 NexPort / ClawProxyHub 写个插件」「接入 xx 站点」「做个签到/对话上游插件」且希望用 Lua（免编译、免打包二进制）。
- 用户拿着 `.cphplugin` 报错、询问 manifest 字段、`cph.http` 用法、握手失败原因。
- 不适用：Go 语言插件（独立二进制，走 `sdk.Serve`）；安卓上 Go 插件只能随 APK 内置，Lua 插件则是安卓上唯一可在线安装的插件形态。

## 前置条件

- 会 Lua（本宿主是 gopher-lua，Lua 5.1 语义）。
- 一个能访问的「上游」：OpenAI 兼容端点最简单（宿主自带 SSE 解析）。
- 测试环境任一：桌面核心（`go run ./cmd/nexcore` + 浏览器面板）或安卓 App 内嵌面板（127.0.0.1:<端口>）。上传安装走面板「插件」页。

## 架构 30 秒

```
NexPort 核心（Go）──gRPC/go-plugin──> luahost 进程（Go，--dir <插件目录>）
                                        └─ 沙箱 VM 执行 main.lua，约定函数挂在其返回的 table 上
main.lua ──cph.http.*──> 上游站点        （Lua 侧无裸 socket，出站只经宿主）
```

核心眼里 luahost 就是普通插件；main.lua 与核心不直接通信，一切经宿主翻译（`hosts/luahost/main.go`）。

## 插件目录结构

```
hello-world/
├── manifest.json      # 必须；插件清单（见下）
├── main.lua           # 必须；入口脚本，必须 `return M`（一个 table）
├── icon.png           # 可选；面板展示（manifest.icon 指向它）
└── lib/               # 可选；子模块，require("lib.<name>") 加载（只支持一层！）
    └── util.lua
```

## manifest.json 字段

字段来源：`hosts/luahost/manifest.go` 的 `scriptManifest`（与 `sdk/proto/cph.proto` 的 Manifest 对齐）。

| 字段 | 类型 | 说明 |
|---|---|---|
| `name` | string | 必须。插件 id，全局唯一（同名插件不能从其他来源重复安装） |
| `version` | string | 语义化版本，如 "0.1.0" |
| `author` | string | 作者 |
| `label` | map | 多语言展示名：`{"zh": "...", "en": "..."}`（面板 zh 优先） |
| `runtime` | string | **必须写 `"lua"`**（缺省视为 Go 插件，安装要求包内二进制） |
| `entry` | string | 入口脚本，固定 `"main.lua"` |
| `icon` | string | 包内图标文件名，如 `"icon.png"`（可选） |
| `protocol_version` | int | 写 `2`。核心安装校验：0 或 1..2 之内（`plugmgr/install.go`，`sdk.MinProtocolVersion=1`、`sdk.ProtocolVersion=2`） |
| `min_core_version` | string | 可选，最低核心版本 |
| `settings_schema` | object | 可选，JSON Schema 对象；面板「插件设置」动态渲染（宿主原样透传为字符串） |
| `instance_schema` | object | 可选，实例级附加字段 Schema（实例 = 插件下的一个站点/部署，核心固定提供 name + base_url） |
| `capabilities` | string[] | 声明能力，如 `["chat","models","login","refresh"]`（面板展示用；多实例插件需含 `"instances"`） |
| `auth_methods` | array | 授权方式定义（见下节） |
| `endpoints` | string[] | 可处理的对外端点方言：`chat_completions` / `messages` / `responses`；**空 = chat_completions + messages** |

## 契约 protocol v2：握手

核心与插件按 go-plugin 建立连接后先握手（`HandshakeRequest{core_version, protocol_version}`）。Lua 侧两种方式：

1. **manifest.json 回退（推荐起步）**：main.lua 不定义 `handshake` 时，宿主直接读 manifest.json 生成 Manifest，并把 `protocol_version` 回填为 2（`hosts/luahost/rpc.go` + `manifest.go`）。hello-world 模板即此方式。
2. **脚本声明（autoclaw 方式）**：定义 `plugin.handshake(req)`，`req = {protocol_version, core_version}`。可校验版本并返回完整 manifest；版本不匹配返回 `{error={code=1, message=...}}` 拒载：

```lua
function plugin.handshake(req)
  if req.protocol_version ~= 2 then
    return { error = { code = 1, message = "protocol mismatch" } }
  end
  return { manifest = { name = "...", version = "...", protocol_version = 2, ... } }
end
```

## main.lua 约定函数

脚本必须以 `return M` 结尾（M 是 table）；宿主把每个 RPC 翻译成对 M 上约定函数的调用（`hosts/luahost/rpc.go`）。缺失的函数安全降级：

| 约定函数 | 签名 | 缺省行为 |
|---|---|---|
| `handshake(req)` | → `{manifest=...}` 或 `{error=...}` | 回退读 manifest.json |
| `login(req)` | → `{blob, profile}` / `{next}` / `{error}` | 无则登录报错 501 |
| `models(cred)` | → `{models={...}}` | 空目录 |
| `chat(req, stream)` | 无返回值，用 stream 吐事件流 | 501 "script has no chat()" |
| `refresh(cred)` | → `{blob, profile}` / `{error}` | 空操作（无凭据变更） |
| `profile(cred)` | → AccountProfile table | 空档案 |

每次 RPC 从 VM 池取一个沙箱 VM（懒建、复用、并发时各自一份）；Go 侧 panic 被兜底转换为错误/chat 的 task_failed，不会崩宿主进程（`rpc.go` 顶层 recover）。

### login：请求与返回

`req = {method_id, form={字段名:值}, state, instance_id}`（`proto.go loginReqToTable`）。`state` 是多步登录状态（上一步插件签发、核心原样回传的字符串）。

返回三选一（可组合，`account.go loginResultFromTable`）：

```lua
-- 完成：凭据入库（blob 是不透明字节串，推荐 JSON）
return { blob = cph.json.encode({api_key = key}),
         profile = { display_name = "...", healthy = true, quota = {} } }

-- 未完成：指示下一步（浏览器登录 / 再填一表单）
return { next = { action = "open_url", url = u, prompt = {zh="登录后粘贴跳转地址"} } }
return { next = { action = "input_form", fields = {...}, state = "...", wait = false } }
-- next.wait = true：插件自动等回调，前端轮询提交直至完成

-- 业务错误
return { error = { code = 400, message = "..." } }
```

### models：模型目录

```lua
function M.models(cred)
  return { models = {
    { id = "my-model", label = {zh="我的模型"}, context_window = 128000,
      supports_tools = true, supports_stream = true },
  } }
end
```

`cred` 可能为空 table（核心无凭据查询目录时），脚本自行判空。

### chat：统一信封进，事件流出

入参 `req`（`proto.go chatReqToTable`，字段名与 cph.proto snake_case 对齐）：

- `model`：客户端请求的模型 id
- `messages`：数组，每项 `{role, text, tool_call_id?, tool_error?, tool_calls?=[{id,name,arguments}], parts?=[{type,text,media_type,data,url,signature}]}`（role: system/user/assistant/tool）
- `tools`：`[{name, description, parameters_schema}]`；`tool_choice`：`{type, tool_name}`
- `stream`、`temperature`、`max_tokens`、`source`（"messages"/"chat_completions"/"responses"）、`extra`（协议特有透传 map）
- `credential`：`{account_id, blob, instance_id, updated_at}`（可 nil）

`stream` 对象（`stream.go`，每方法收一个 table，无返回值）：

| 方法 | 参数 table |
|---|---|
| `stream.message_start` | `{model, usage?}` |
| `stream.content_delta` | `{text}` |
| `stream.reasoning_delta` | `{text, signature?}` |
| `stream.tool_call_delta` | `{id, name, arguments_delta}` |
| `stream.message_finish` | `{finish_reason, usage?, stop_sequence?}` |
| `stream.failed` | `{code, message}` |

`usage` 五字段（Anthropic 语义）：`input_tokens / output_tokens / cached_tokens / cache_creation_tokens / reasoning_tokens`。事件序列：`message_start → content_delta*（→ reasoning_delta / tool_call_delta*）→ message_finish`。

**状态码语义**（核心侧消费，写插件前务必知道）：
- `stream.failed{code=401}` → 网关对该账号做凭据恢复（刷新同账号→换号），插件不自行换票（autoclaw 注释原话）。
- `429` / `402` → 核心自动暂停账号（429 十分钟后自动恢复，402 需手动）。
- 脚本 `chat()` 抛错 → 宿主转 `task_failed` code 502。

### refresh / profile

```lua
function M.refresh(cred)
  local ok, err = do_refresh(cred)
  if not ok then
    -- 401 = 凭据终态失效（核心标记 expired）；上游故障用 502（保留账号原状）
    return { error = { code = 401, message = err } }
  end
  return { blob = cph.json.encode(new_cred), profile = {...} }  -- blob 变更核心代存
end
```

注意（`result.go` 实测边界）：Lua 侧 refresh **不能**产生站内通知（`RefreshResult.notification` 未从脚本返回值映射），profile 支持 `display_name / healthy / quota / credits_json`（`sections` 动态块未映射）。

## auth_methods 定义

声明在 manifest.json 的 `auth_methods` 数组（脚本 handshake 里同构），面板据此动态渲染登录表单，核心不硬编码任何一种（cph.proto AuthMethod 注释）：

```json
{
  "id": "api_key",                        // 方式 id，login(req).method_id 对应
  "label": {"zh": "API 密钥", "en": "API Key"},
  "capabilities": ["refreshable"],        // 可选：refreshable / auto_relogin / profile
  "callback": "",                         // 浏览器授权回调形态：auto / wait / auto_wait（可空）
  "fields": [
    {"name": "api_key", "label": {"zh": "密钥"}, "type": "password",
     "required": true, "placeholder": "sk-..."}
  ]
}
```

字段 `type` 取值：`text / password / textarea / file / phone`。完整多步登录（发码→输码）参考 autoclaw 的 `phone_otp` 方式（`main.lua` login 分支）。

## 宿主 API：cph.*（Lua 侧唯一出站/系统通道）

注册于 `hosts/luahost/cph.go` + `http.go` + `openai.go`。**只有这些**；存储/代理/设置/任务 API 均未对 Lua 暴露（见「能力边界」）。

### cph.http.request(opts) → resp

```lua
local ok, resp = pcall(cph.http.request, {
  method = "POST",                 -- 默认 GET
  url = "https://api.example.com/v1/x",
  headers = { ["Authorization"] = "Bearer " .. token },
  body = cph.json.encode(payload), -- string 直发；给 table 则自动 JSON 并补 Content-Type
  timeout = 60000,                 -- 毫秒；不写走宿主默认（连接池复用，整体 10 分钟兜底）
})
-- 传输失败（DNS/连接/TLS/超时/坏 URL）会 raise → 必须 pcall
-- HTTP 4xx/5xx 不是错误：正常返回，自行看 resp.status
-- resp = { body = "...", status = 200, headers = {...} }
```

### cph.http.stream(opts) → true | false, err

SSE 流式请求。opts 同上，另加 `stream = <stream 对象>` 与 `format = "openai"`：

- `format="openai"`：宿主解析 OpenAI 兼容 SSE（`data:` 帧、`[DONE]`），自动驱动 `stream.content_delta / reasoning_delta / tool_call_delta`，结束时**自动补发 message_finish**（finish_reason 缺省 "stop"，`sse.go`）——脚本只需先发 `message_start`。
- 4xx/5xx 回 `(false, "HTTP <code>: <body截断4KB>")`——autoclaw 据此解析状态码做重试：`string.match(err, "^HTTP (%d+)")`。

### cph.openai.chat_body(req) → body table

把统一信封转成 OpenAI chat completions 请求体（messages/tools 转换、stream=true 自动置位），脚本通常只改 `body.model` 再发上游（autoclaw chat 主路径）。

### 其他

```lua
cph.json.encode(t) / cph.json.decode(s)            -- decode 失败 raise，配 pcall
cph.hash.md5(s) / cph.hash.sha256(s)               -- 返回 hex 小写
cph.hash.hmac_sha256(data, key)                    -- 注意参数顺序：先 data 后 key
cph.hash.base64url_encode(s) / .base64url_decode(s)-- decode 兼容带 padding 与标准字母表
cph.time.now()                                     -- UnixMilli
cph.time.sleep(ms)                                 -- 阻塞当前 VM（重试退避用，别太久）
cph.random.uuid() / cph.random.hex(n)              -- uuid v4；n 字节 → 2n 个 hex 字符
cph.log.debug/info/warn/error(msg [, fields])      -- 结构化日志进核心运行日志页；fields 为 string map
```

## 沙箱边界（写代码前必读）

`hosts/luahost/sandbox.go`：

- **可用标准库**：`base / table / string / math`。
- **禁用**：`os / io / debug / package` 整库不可用；全局符号 `dofile / loadfile / load / loadstring / collectgarbage / print / module` 被移除（`print` 会污染 go-plugin 握手管道，日志一律 `cph.log.*`）。
- **require**：只允许 `require("lib.<name>")`，映射 `<插件目录>/lib/<name>.lua`，带模块缓存、段名仅限 `[A-Za-z0-9_]`（天然禁路径逃逸）。子模块共享同一沙箱与 `cph.*`。
- **打包注意**：安装解包会把 zip 内 `lib/` 下的 .lua **平铺到 lib/ 一层**（`plugmgr/install.go extractLuaScripts` 按 base name 落盘）——所以 `require` 只支持一层 `lib.<name>`，不要写 `lib.a.b` 多级模块。

## 能力边界（Lua 与 Go 插件的差异）

以下宿主服务存在于契约（`cph.proto` 的 ClawHost / ClawPlugin）但对 **Lua 宿主未接线**，脚本拿不到：

- `StoreGet / StorePut`（KV 状态存储）、`GetProxy`（出站代理）、`GetSettings`（插件/实例设置）
- 任务调度 RPC（`ListTaskCapabilities / RunTask`）——luahost 未实现，**Lua 插件目前不能注册定时任务**（账号级状态请存凭据 blob，随 refresh 流转）
- `cred.proxy` 不透传给 Lua（`proto.go credToTable` 只给 account_id / blob / instance_id / updated_at）

## 打包为 .cphplugin 并安装

包就是一个 zip，根目录放 `manifest.json` 与 `main.lua`（核心按**基名**识别这两者与 icon）：

```bash
cd hello-world
zip -X ../hello-world.cphplugin manifest.json main.lua   # 有 icon/lib 再一并加入
```

- Lua 包**不需要**任何二进制（核心用内置/共享 luahost 启动；安卓为 nativeLibraryDir 的 `libluahost.so --dir <插件目录>`）。
- 安装：面板「插件」→ 上传安装（`POST /admin/plugins/install-upload`，multipart 字段 `package`），或从插件市场源安装（`runtime:"lua"` 的条目不受安卓 noexec 限制，可在线装/升级）。
- 安装校验：manifest 必须有 `name` 且 `runtime=="lua"`；`protocol_version` 须为 0 或 1..2；同名插件已从其他来源安装会被拒绝。

## 本地测试

1. **脚本级（最快，本仓库开发方式）**：参考 `gateway-mobile/core/luahost/load_test.go` / `hello_world_test.go`——用 `newVM(dir, nil)` 加载插件目录，直接调 `Handshake / Login / ListModels / Chat` 断言事件流，全程无需网络。`go test .`（在 core/luahost 模块内）。
2. **端到端（桌面）**：`go run ./cmd/nexcore` 起核心 → 浏览器面板上传 .cphplugin → 添加账号 → 插件页确认 running → 对话测试。
3. **端到端（安卓）**：App 内嵌面板（127.0.0.1:<网关端口>）同上；出错看「运行日志」页——`cph.log.error` 的输出与脚本报错（`run main.lua: ...` / `panic in chat()`）都在那里。

## 常见坑

1. **忘写 `return M`**：宿主报 "main.lua must `return M` (a table)"，直接拒载（`vm.go`）。
2. **用 print / os / io / load**：沙箱里全是 nil，`attempt to call a nil value`。日志用 `cph.log.*`。
3. **cph.http.request 不 pcall**：网络失败 raise 中断整个 RPC；参照 autoclaw 的 `http_req` 包装。
4. **hmac 参数顺序**：是 `hmac_sha256(data, key)`，不是 (key, data)。
5. **stream 忘发 message_start / message_finish**：核心按事件流聚合输出；`format="openai"` 时 finish 由宿主补，自写 SSE 解析则必须自己发全。
6. **refresh 返回 401 之外的语义**：只有 401 会让核心标记凭据失效；上游抖动请回 502 保留账号原状。
7. **模块级状态当持久化**：VM 池并发时会新建 VM，脚本内 local 变量不保证跨请求存活；每账号状态一律放进凭据 blob。
8. **凭据 blob 塞私钥明文不加密**：blob 由核心整体加密存储、仅回传插件，格式自定——推荐 JSON 字符串（autoclaw 同款做法），解析用 `pcall(cph.json.decode, ...)`。
9. **endpoints 留空却只支持 responses**：空 = chat_completions + messages；只支持其他方言必须显式声明。
10. **多级 lib 模块**：`require("lib.a.b")` 源码里合法，但安装解包平铺后会失败——保持一层。
11. **长 sleep / 死循环**：chat 是流式 RPC，脚本阻塞多久客户端就等多久；重试退避参考 autoclaw（上限次数 + 线性退避 + 状态码白名单）。

## 官方示例索引

- `examples/autoclaw/` — 官方 Lua 插件（manifest + 808 行 main.lua + icon）：多步手机验证码登录、自持 token 刷新、内置模型目录、OpenAI 兼容流式转发、重试与限流判定、钱包余额 profile——「真实上游接入」的完整参照。
- `examples/hello-world/` — 最小可运行模板（manifest + main.lua）：api_key 登录 → 静态模型 → 回显对话，离线可跑，已被 `core/luahost/hello_world_test.go` 契约测试覆盖（加载、handshake 回退、login/models/chat/401 全链路）。

从这两份源码出发改造，是写新插件最可靠的路径。
