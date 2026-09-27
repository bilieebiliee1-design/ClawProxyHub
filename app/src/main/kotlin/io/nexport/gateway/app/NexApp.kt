package io.nexport.gateway.app

import android.app.Application
import androidx.appcompat.app.AppCompatDelegate

/**
 * NexApp — 应用入口：初始化核心控制器、通知渠道与外观主题（appearance_theme：
 * system/light/dark 经 AppCompatDelegate.setDefaultNightMode 覆盖 DayNight）。
 *
 * 基于 ClawProxyHub（AGPL-3.0）修改构建。
 */
class NexApp : Application() {
    override fun onCreate() {
        super.onCreate()
        CoreControllerAppHolder.ctx = this
        CoreController.init(this)
        GatewayService.createChannel(this)
        // 主题设置项经 DayNight 覆盖生效（values-night 色板自动跟随）
        AppCompatDelegate.setDefaultNightMode(
            when (Prefs.themeMode(this)) {
                "light" -> AppCompatDelegate.MODE_NIGHT_NO
                "dark" -> AppCompatDelegate.MODE_NIGHT_YES
                else -> AppCompatDelegate.MODE_NIGHT_FOLLOW_SYSTEM
            })
    }
}
