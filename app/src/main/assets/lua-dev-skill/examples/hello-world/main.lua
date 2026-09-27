-- main.lua — Hello World 最小 Lua 插件模板（NexPort lua-dev-skill 收录）。
--
-- 展示 lua 插件的完整最小闭环，全部行为离线可跑（不请求任何上游）：
--   login  : 校验表单里的 api_key 非空 → 存为凭据 blob（JSON）
--   models : 返回一个内置模型 hello-model
--   chat   : 校验凭据 → 用统一事件流回显一句问候
-- 脚本约定：main.lua 必须 `return M`（一个 table），约定函数挂在其上；
-- 缺省的函数（本例未定义 handshake/refresh/profile）由 luahost 降级处理
-- （handshake 回退读 manifest.json；refresh/profile 为空操作）。

local M = {}

-- --------------------------------------------------------------------------
-- 小工具：沙箱只有 base/table/string/math 四个标准库，禁 os/io/print/load。
-- JSON、日志、随机等一律走 cph.* 宿主 API。
-- --------------------------------------------------------------------------

local function str(v)
  if type(v) == "string" then return v end
  return ""
end

local function tbl(v)
  if type(v) == "table" then return v end
  return {}
end

-- 凭据 blob 是不透明字节串，格式插件自定义；这里用 JSON：
--   { "api_key": "..." }
local function parse_cred(cred)
  local blob = str(tbl(cred).blob)
  if blob == "" then
    return nil, "missing credential"
  end
  local ok, data = pcall(cph.json.decode, blob)
  if not ok or type(data) ~= "table" or str(data.api_key) == "" then
    return nil, "invalid credential blob"
  end
  return data
end

-- --------------------------------------------------------------------------
-- 约定函数 1/3：login(req) → { blob, profile } 或 { error } 或 { next }
-- req = { method_id, form = {..}, state, instance_id }
-- --------------------------------------------------------------------------

function M.login(req)
  local api_key = str(tbl(req.form).api_key)
  if api_key == "" then
    return { error = { code = 400, message = "api_key 不能为空" } }
  end
  return {
    blob = cph.json.encode({ api_key = api_key }),
    profile = {
      display_name = "hello-user",
      healthy = true,
      quota = {},
    },
  }
end

-- --------------------------------------------------------------------------
-- 约定函数 2/3：models(cred) → { models = { { id, label?, context_window,
--   supports_tools, supports_stream }, ... } }
-- --------------------------------------------------------------------------

function M.models(cred)
  return {
    models = {
      {
        id = "hello-model",
        label = { zh = "你好模型", en = "Hello Model" },
        context_window = 8192,
        supports_tools = false,
        supports_stream = true,
      },
    },
  }
end

-- --------------------------------------------------------------------------
-- 约定函数 3/3：chat(req, stream) → 无返回值，用 stream 对象吐事件流。
-- req.messages[i] = { role, text, ... }；req.credential = { account_id, blob, ... }
-- 事件序列：message_start → content_delta*（→ reasoning_delta/tool_call_delta*）
--           → message_finish；失败用 stream.failed{code, message}。
-- --------------------------------------------------------------------------

function M.chat(req, stream)
  local cred, cerr = parse_cred(req.credential)
  if cred == nil then
    -- 401 = 凭据失效（核心会对该账号做刷新/标记过期处理）；其余 4xx/5xx 按需选码。
    stream.failed({ code = 401, message = cerr })
    return
  end

  -- 拼一句回显：取最后一条 user 消息的 text。
  local user_text = ""
  for _, m in ipairs(tbl(req.messages)) do
    if str(m.role) == "user" then
      user_text = str(m.text)
    end
  end

  stream.message_start({ model = str(req.model) })
  stream.content_delta({ text = "hello, " .. user_text })
  stream.message_finish({
    finish_reason = "stop",
    usage = { input_tokens = 1, output_tokens = 3 },
  })
end

return M
