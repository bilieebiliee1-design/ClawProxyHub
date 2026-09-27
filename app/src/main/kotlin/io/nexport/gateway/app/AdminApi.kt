package io.nexport.gateway.app

import org.json.JSONObject
import java.io.IOException
import java.net.HttpURLConnection
import java.net.URL

/**
 * AdminApi — 极小的 /admin HTTP 客户端（原生引导与状态卡用）。
 *
 * 基于 ClawProxyHub（AGPL-3.0）修改构建。覆盖引导与首页状态所需端点（core/adminapi 目录已核实）：
 *  - GET  /admin/setup-status → {"initialized": bool}（免鉴权）
 *  - POST /admin/setup        → {"initialized": true}（仅表空时可用；服务端校验 ≥10 位含大小写数字）
 *  - POST /admin/login        → {"token","role"}
 *  - GET  /admin/plugins      → {"plugins":[...]}（状态卡在跑插件 + v1.4.0 ③ 供应商板块）
 *  - GET  /admin/keys         → {"keys":[...]}（v1.4.0 ② API 密钥脱敏展示，与面板同步）
 *  - GET  /admin/keys/{id}/reveal → {"key":...}（v1.4.0 ② 显隐/复制）
 *  - GET  /admin/accounts     → {"accounts":[...]}（v1.4.0 ⑤ 开箱即用卡判定）
 *  - POST /admin/tasks/checkin（v1.4.0 ④ 一键签到，NDJSON 进度流）
 *  - POST /admin/probe/run（v1.4.0 ④ 一键测活，NDJSON 进度流）
 * 其余管理能力一律不重复实现（走 WebView 面板）。
 *
 * 全部方法须在工作线程调用（HttpURLConnection 阻塞 IO）。
 */
object AdminApi {
    @Volatile private var token: String? = null

    fun reset() { token = null }
    fun hasToken(): Boolean = token != null

    /** 当前缓存的 JWT（面板 token 自动注入 panelLoginSpec A 用；null = 未登录）。 */
    fun currentToken(): String? = token

    private fun baseUrl(): String = "http://127.0.0.1:${CoreController.gatewayPort}"

    private fun call(path: String, method: String, body: String?, auth: Boolean): Pair<Int, String> {
        val conn = URL(baseUrl() + path).openConnection() as HttpURLConnection
        try {
            conn.requestMethod = method
            conn.connectTimeout = 5000
            conn.readTimeout = 15000
            if (auth && token != null) conn.setRequestProperty("Authorization", "Bearer $token")
            if (body != null) {
                conn.setRequestProperty("Content-Type", "application/json")
                conn.doOutput = true
                conn.outputStream.use { it.write(body.toByteArray(Charsets.UTF_8)) }
            }
            val code = conn.responseCode
            val stream = if (code in 200..299) conn.inputStream else conn.errorStream
            val text = stream?.bufferedReader(Charsets.UTF_8)?.use { it.readText() } ?: ""
            return code to text
        } finally {
            conn.disconnect()
        }
    }

    private fun errOf(text: String, code: Int): String = try {
        JSONObject(text).optString("error").ifEmpty { "HTTP $code" }
    } catch (_: Exception) {
        text.ifBlank { "HTTP $code" }
    }

    /** 首启引导是否已完成（users 表非空）。null = 网络失败。 */
    fun setupInitialized(): Boolean? = try {
        val (code, text) = call("/admin/setup-status", "GET", null, false)
        if (code == 200) JSONObject(text).optBoolean("initialized") else null
    } catch (_: Exception) { null }

    /** 创建管理员账号（引导 Step2 核心就绪后调用）。 */
    fun setup(user: String, pass: String): Result<Unit> = try {
        val body = JSONObject().put("username", user).put("password", pass).toString()
        val (code, text) = call("/admin/setup", "POST", body, false)
        if (code == 200) Result.success(Unit)
        else Result.failure(IOException(errOf(text, code)))
    } catch (t: Throwable) { Result.failure(t) }

    /** 登录并缓存 token（状态卡拉数据用；凭据来自 Keystore 加密存储）。 */
    fun login(user: String, pass: String): Result<Unit> = try {
        val body = JSONObject().put("username", user).put("password", pass).toString()
        val (code, text) = call("/admin/login", "POST", body, false)
        if (code == 200) {
            token = JSONObject(text).optString("token")
            Result.success(Unit)
        } else Result.failure(IOException(errOf(text, code)))
    } catch (t: Throwable) { Result.failure(t) }

    /** 自动登录（存有凭据时）。 */
    fun tryAutoLogin(): Boolean {
        val user = Prefs.adminUser(appCtx()) ?: return false
        val pass = Prefs.adminPass(appCtx()) ?: return false
        return login(user, pass).isSuccess
    }

    /**
     * 修改管理员密码（POST /admin/password，端点已核实：core/adminapi/server.go:98 注册、
     * auth.go changePassword 仅校验 len≥6——壳侧四规则由调用方强制）。
     * 401 时先用存储凭据续 token 重试一次；失败携带服务端 error 原文。
     * 凭据解封失败（无法续 token）返回明确失败，由 UI 引导手动登录面板（不静默失败）。
     */
    fun changePassword(oldPassword: String, newPassword: String): Result<Unit> {
        return try {
            var code: Int
            var text: String
            run {
                val r = call(
                    "/admin/password", "POST",
                    JSONObject().put("old_password", oldPassword).put("password", newPassword).toString(),
                    true)
                code = r.first; text = r.second
            }
            if (code == 401) {
                // 续 token：用本机存储凭据重新登录后重试；解封失败则明确报错
                if (tryAutoLogin()) {
                    val retry = call(
                        "/admin/password", "POST",
                        JSONObject().put("old_password", oldPassword).put("password", newPassword).toString(),
                        true)
                    code = retry.first; text = retry.second
                } else {
                    return Result.failure(IOException("credentials unavailable"))
                }
            }
            if (code == 200) Result.success(Unit)
            else Result.failure(IOException(errOf(text, code)))
        } catch (t: Throwable) { Result.failure(t) }
    }

    /** 在跑插件数（total, running），失败返回 null。 */
    fun pluginCounts(): Pair<Int, Int>? {
        val arr = pluginArray() ?: return null
        var running = 0
        for (i in 0 until arr.length()) if (arr.getJSONObject(i).optBoolean("running")) running++
        return arr.length() to running
    }

    /**
     * /admin/plugins 的插件数组（v1.4.0 ③ 供应商板块 + 状态卡共用）。
     * 元素字段：{name, label, icon("/assets/plugins/<name>/icon" 或空), running, ...}。
     * 修复既有解析缺陷：响应为 {"plugins":[...]} 包裹对象，原 JSONArray(text) 直解恒抛
     * 异常致「在跑插件 x/y」从不显示；此处兼容对象包裹与裸数组两种形态。
     */
    fun pluginArray(): org.json.JSONArray? {
        return try {
            val (code, text) = call("/admin/plugins", "GET", null, true)
            if (code == 401 && tryAutoLogin()) {
                return pluginArray()
            }
            if (code != 200) null else unwrapArray(text, "plugins")
        } catch (_: Exception) {
            null
        }
    }

    /** /admin/keys 的密钥数组（v1.4.0 ② API 密钥同步展示）。响应为 {"keys":[...]}。 */
    fun keyArray(): org.json.JSONArray? {
        return try {
            val (code, text) = call("/admin/keys", "GET", null, true)
            if (code == 401 && tryAutoLogin()) {
                return keyArray()
            }
            if (code != 200) null else unwrapArray(text, "keys")
        } catch (_: Exception) {
            null
        }
    }

    /** /admin/accounts 的账号数组（v1.4.0 ⑤ 开箱即用卡判定「已配置账号」）。 */
    fun accountArray(): org.json.JSONArray? {
        return try {
            val (code, text) = call("/admin/accounts", "GET", null, true)
            if (code == 401 && tryAutoLogin()) {
                return accountArray()
            }
            if (code != 200) null else unwrapArray(text, "accounts")
        } catch (_: Exception) {
            null
        }
    }

    /**
     * GET /admin/stats/quota → {"plugins":[{plugin,label,instance,accounts,with_quota,
     * quota{credits,total_credits,used_credits}}]}——面板概览页「渠道概览」同一管理接口
     * （tasks.go dashboardQuota：credits_json 快照按 插件·实例 聚合 + profile.quota 兜底）。
     * v1.4.0 追加①：首页「账号概览」板块数据源。
     */
    fun quotaArray(): org.json.JSONArray? {
        return try {
            val (code, text) = call("/admin/stats/quota", "GET", null, true)
            if (code == 401 && tryAutoLogin()) {
                return quotaArray()
            }
            if (code != 200) null else unwrapArray(text, "plugins")
        } catch (_: Exception) {
            null
        }
    }

    /** GET /admin/keys/{id}/reveal → 密钥明文（401 自动续 token；存量哈希 409 不回显）。 */
    fun revealKey(id: Long): Result<String> = try {
        val (code, text) = call("/admin/keys/$id/reveal", "GET", null, true)
        when {
            code == 401 && tryAutoLogin() -> revealKey(id)
            code != 200 -> Result.failure(IOException(errOf(text, code)))
            else -> Result.success(JSONObject(text).optString("key"))
        }
    } catch (t: Throwable) { Result.failure(t) }

    /**
     * GET /admin/system/info → lan_enabled（面板「设置 → 网络」局域网监听开关的当前值；
     * 合规复审 MEDIUM：面板新增该开关后，首页局域网端点展示须跟随其状态——关闭时不再
     * 用本机检测兜底拼接端点）。失败返回 null（未知态，UI 按原逻辑兜底）。
     */
    fun systemLanEnabled(): Boolean? {
        return try {
            val (code, text) = call("/admin/system/info", "GET", null, true)
            if (code == 401 && tryAutoLogin()) {
                return systemLanEnabled()
            }
            if (code != 200) null else JSONObject(text).optBoolean("lan_enabled")
        } catch (_: Exception) {
            null
        }
    }

    /** 兼容 {"key":[...]} 包裹对象与裸数组两种响应形态。 */
    private fun unwrapArray(text: String, field: String): org.json.JSONArray? {
        val t = text.trim()
        if (t.startsWith("[")) return org.json.JSONArray(t)
        val o = JSONObject(t)
        return o.optJSONArray(field) ?: o.optJSONArray("data")
    }

    /**
     * NDJSON 进度流 POST（v1.4.0 ④ 一键签到 /admin/tasks/checkin 与一键测活
     * /admin/probe/run）：与市场安装进度同通道形态，逐行 JSON 事件经 onEvent 回调
     * （工作线程回调，调用方自行 post 主线程）。流断开（进度通道关闭）= 核心侧 ctx
     * 取消、立即停止后续任务。auth=true 且 401 时先续 token 重试一次。
     * readTimeout=0：任务执行期间逐任务事件间隔可能很长（签到单任务数分钟），不可设
     * 读超时；中断靠调用方断开连接。
     */
    fun streamPost(path: String, body: String?, onEvent: (JSONObject) -> Unit): Result<Unit> {
        var conn: HttpURLConnection? = null
        return try {
            val c = URL(baseUrl() + path).openConnection() as HttpURLConnection
            conn = c
            c.requestMethod = "POST"
            c.connectTimeout = 5000
            c.readTimeout = 0
            if (token != null) c.setRequestProperty("Authorization", "Bearer $token")
            if (body != null) {
                c.setRequestProperty("Content-Type", "application/json")
                c.doOutput = true
                c.outputStream.use { it.write(body.toByteArray(Charsets.UTF_8)) }
            }
            val code = c.responseCode
            if (code == 401 && tryAutoLogin()) {
                c.disconnect()
                return streamPost(path, body, onEvent)
            }
            if (code !in 200..299) {
                val err = c.errorStream?.bufferedReader(Charsets.UTF_8)?.use { it.readText() } ?: ""
                return Result.failure(IOException(errOf(err, code)))
            }
            c.inputStream.bufferedReader(Charsets.UTF_8).use { reader ->
                while (true) {
                    val line = reader.readLine() ?: break
                    if (line.isBlank()) continue
                    try { onEvent(JSONObject(line)) } catch (_: Exception) { /* 坏行跳过 */ }
                }
            }
            Result.success(Unit)
        } catch (t: Throwable) {
            // 中途断流：调用方按「已中止」处理（核心 ctx 取消语义）
            Result.failure(t)
        } finally {
            conn?.disconnect()
        }
    }

    private fun appCtx(): android.content.Context = CoreControllerAppHolder.ctx
}

/** AdminApi 的上下文持有（Application.onCreate 注入）。 */
object CoreControllerAppHolder {
    @Volatile lateinit var ctx: android.content.Context
}
