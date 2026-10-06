package io.nexport.gateway.app

import android.content.Context
import android.content.SharedPreferences

/**
 * Prefs — 壳应用本地设置。
 *
 * 安全设计：
 *  - EULA/引导完成标记、开机自启（默认关）为普通明文项（无敏感性）；
 *  - 管理员凭据（面板自动登录用）经 AndroidKeyStore AES-GCM 加密后落盘，
 *    密钥不可导出（见 KeyEnvelope）。
 *
 * v1.1.0 新增设置项（settingsSpec）：
 *  - appearance_theme      system/light/dark（默认 system，经 AppCompatDelegate 生效）
 *  - appearance_font_size  0..3（系数 0.9/1.0/1.2/1.4，BaseActivity 覆写 fontScale）
 *  - appearance_predictive_back  默认开（壳内返回点是否走预测式回调）
 *  - general_language      system/zh/en（AppCompatDelegate.setApplicationLocales）
 *  - network_gateway_port_mode/value  auto（随机，默认）/fixed（1024–65535）
 *  - tunnel_auto_reconnect 默认开（30s/1m/2m/5m 退避至多 5 次）
 *  - notif_banner_dismissed 首页通知权限引导卡「不再打扰」标记
 *
 * 基于 ClawProxyHub（AGPL-3.0）修改构建。
 */
object Prefs {
    private const val FILE = "nexport_prefs"

    private fun sp(ctx: Context): SharedPreferences =
        ctx.getSharedPreferences(FILE, Context.MODE_PRIVATE)

    fun eulaAccepted(ctx: Context): Boolean = sp(ctx).getBoolean("eula_accepted", false)
    fun setEulaAccepted(ctx: Context, v: Boolean) = sp(ctx).edit().putBoolean("eula_accepted", v).apply()

    fun onboardingDone(ctx: Context): Boolean = sp(ctx).getBoolean("onboarding_done", false)
    fun setOnboardingDone(ctx: Context, v: Boolean) = sp(ctx).edit().putBoolean("onboarding_done", v).apply()

    // v1.5.0 修复②：面板 WebView HTTP 缓存清理水位（应用升级后首次创建 WebView 时
    // 清缓存并记版本，防止 WebView 缓存旧 dist 的 index.html 致新路由导航静默失效）
    fun panelCacheClearedVersion(ctx: Context): Int = sp(ctx).getInt("panel_cache_cleared_version", 0)
    fun setPanelCacheClearedVersion(ctx: Context, v: Int) =
        sp(ctx).edit().putInt("panel_cache_cleared_version", v).apply()

    /** 开机自启：默认关（架构方案 onboardingPlan/featureParity）。 */
    fun autoStart(ctx: Context): Boolean = sp(ctx).getBoolean("autostart", false)
    fun setAutoStart(ctx: Context, v: Boolean) = sp(ctx).edit().putBoolean("autostart", v).apply()

    /** 前台服务（后台常驻）：默认开（核心常驻是本应用的基本形态）。 */
    fun keepForeground(ctx: Context): Boolean = sp(ctx).getBoolean("keep_foreground", true)
    fun setKeepForeground(ctx: Context, v: Boolean) = sp(ctx).edit().putBoolean("keep_foreground", v).apply()

    /** 管理员凭据：Keystore 信封加密（base64(iv+ct)）。 */
    fun saveAdmin(ctx: Context, user: String, pass: String) {
        val blob = KeyEnvelope.encryptPrefs((user + "\n" + pass).toByteArray(Charsets.UTF_8))
        sp(ctx).edit().putString("admin_user", user).putString("admin_blob", blob).apply()
        markAdminPassCached(pass) // 进程内缓存同步更新（下次 adminPass 免解密）
    }

    fun adminUser(ctx: Context): String? = sp(ctx).getString("admin_user", null)

    // ---- adminPass 进程内解密缓存（perf 修复轮） ----
    // KeyEnvelope.decryptPrefs 走 AndroidKeyStore AES-GCM，单次代价高且曾在主线程被
    // 连调两次（升级后首启 ANR 最强候选，见 PanelFragment.ensurePanelToken 修复注释）。
    // 解密成功一次后进程内复用；写入（saveAdmin）/清除（clearAdmin）与核心会话切换
    // （AdminApi.reset → invalidateAdminPassCache）时失效重读。@Volatile 双字段 +
    // 重复解密的良性竞态可接受（结果幂等）。解密失败不缓存，保留每次调用重试语义。

    @Volatile private var adminPassCache: String? = null
    @Volatile private var adminPassCached = false

    private fun markAdminPassCached(pass: String?) {
        adminPassCache = pass
        adminPassCached = true
    }

    /** 凭据解密缓存失效（下次 adminPass 重新读 SP + Keystore 解密）。 */
    fun invalidateAdminPassCache() {
        adminPassCache = null
        adminPassCached = false
    }

    fun adminPass(ctx: Context): String? {
        if (adminPassCached) return adminPassCache
        val blob = sp(ctx).getString("admin_blob", null) ?: run {
            markAdminPassCached(null) // 未存凭据：缓存空结果，后续调用零 SP/Keystore 代价
            return null
        }
        val pass = try {
            val raw = KeyEnvelope.decryptPrefs(blob)
            val s = raw?.let { String(it, Charsets.UTF_8) }
            val i = s?.indexOf('\n') ?: -1
            if (s == null || i < 0) null else s.substring(i + 1)
        } catch (_: Exception) {
            null
        }
        if (pass != null) markAdminPassCached(pass)
        return pass
    }

    /** 清除本机凭据记忆（settingsSpec security_forget_credentials）。 */
    fun clearAdmin(ctx: Context) {
        sp(ctx).edit().remove("admin_user").remove("admin_blob").apply()
        invalidateAdminPassCache()
    }

    // ---- v1.1.0 设置项 ----

    fun themeMode(ctx: Context): String = sp(ctx).getString("appearance_theme", "system") ?: "system"
    fun setThemeMode(ctx: Context, v: String) = sp(ctx).edit().putString("appearance_theme", v).apply()

    /** 字体大小档位 0..3；系数 0.9/1.0/1.2/1.4（随系统字体缩放叠加）。 */
    fun fontSizeIndex(ctx: Context): Int = sp(ctx).getInt("appearance_font_size", 1)
    fun setFontSizeIndex(ctx: Context, v: Int) = sp(ctx).edit().putInt("appearance_font_size", v).apply()
    fun fontFactor(index: Int): Float =
        floatArrayOf(0.9f, 1.0f, 1.2f, 1.4f)[index.coerceIn(0, 3)]

    /** 预测性返回：默认开。开关仅控制壳内返回点是否注册预测式回调（见 accessibilitySpec）。 */
    fun predictiveBack(ctx: Context): Boolean = sp(ctx).getBoolean("appearance_predictive_back", true)
    fun setPredictiveBack(ctx: Context, v: Boolean) = sp(ctx).edit().putBoolean("appearance_predictive_back", v).apply()

    /** 语言：system/zh/en（跟随系统 = 空 LocaleList）。 */
    fun language(ctx: Context): String = sp(ctx).getString("general_language", "system") ?: "system"
    fun setLanguage(ctx: Context, v: String) = sp(ctx).edit().putString("general_language", v).apply()

    /** 网关端口：auto（0 = 随机，核心默认行为）/ fixed（1024–65535）。 */
    fun gatewayPortFixed(ctx: Context): Boolean =
        sp(ctx).getString("network_gateway_port_mode", "auto") == "fixed"
    fun setGatewayPortFixed(ctx: Context, v: Boolean) =
        sp(ctx).edit().putString("network_gateway_port_mode", if (v) "fixed" else "auto").apply()

    fun gatewayPortValue(ctx: Context): Int = sp(ctx).getInt("network_gateway_port_value", 17777)
    fun setGatewayPortValue(ctx: Context, v: Int) = sp(ctx).edit().putInt("network_gateway_port_value", v).apply()

    /** 隧道自动重连：默认开。 */
    fun tunnelAutoReconnect(ctx: Context): Boolean = sp(ctx).getBoolean("tunnel_auto_reconnect", true)
    fun setTunnelAutoReconnect(ctx: Context, v: Boolean) = sp(ctx).edit().putBoolean("tunnel_auto_reconnect", v).apply()

    /** 首页通知权限引导卡「暂不/拒绝」标记：置位后不再打扰。 */
    fun notifBannerDismissed(ctx: Context): Boolean = sp(ctx).getBoolean("notif_banner_dismissed", false)
    fun setNotifBannerDismissed(ctx: Context, v: Boolean) = sp(ctx).edit().putBoolean("notif_banner_dismissed", v).apply()

    // ---- v1.2.0 悬浮窗保活（floatingWindowSpec） ----

    /** 悬浮窗保活开关：默认关（写法同 tunnelAutoReconnect）。 */
    fun floatingKeepAlive(ctx: Context): Boolean = sp(ctx).getBoolean("floating_keep_alive", false)
    fun setFloatingKeepAlive(ctx: Context, v: Boolean) = sp(ctx).edit().putBoolean("floating_keep_alive", v).apply()

    /** 悬浮窗拖动位置记忆（Int.MIN_VALUE = 未记录，首次居中显示）。 */
    fun floatingPosX(ctx: Context): Int = sp(ctx).getInt("floating_pos_x", Int.MIN_VALUE)
    fun floatingPosY(ctx: Context): Int = sp(ctx).getInt("floating_pos_y", Int.MIN_VALUE)
    fun setFloatingPos(ctx: Context, x: Int, y: Int) =
        sp(ctx).edit().putInt("floating_pos_x", x).putInt("floating_pos_y", y).apply()
}
