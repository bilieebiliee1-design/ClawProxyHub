package io.nexport.gateway.app

import android.content.Context
import android.content.res.Configuration
import android.content.res.Resources
import androidx.appcompat.app.AppCompatActivity

/**
 * BaseActivity — 全 Activity 基座（settingsSpec appearance_font_size）：
 * attachBaseContext 按「系统 fontScale × 设置系数」覆写 configuration.fontScale，
 * 全部 sp 文字随动。系统无障碍大字与设置项叠加生效。
 *
 * 基于 ClawProxyHub（AGPL-3.0）修改构建。
 */
abstract class BaseActivity : AppCompatActivity() {

    override fun attachBaseContext(newBase: Context) {
        // 系统级 fontScale 取系统资源配置（未被壳覆写过的原始值），避免重建时系数复利叠加
        val systemScale = Resources.getSystem().configuration.fontScale
        val factor = Prefs.fontFactor(Prefs.fontSizeIndex(newBase))
        val cfg = Configuration(newBase.resources.configuration)
        cfg.fontScale = systemScale * factor
        super.attachBaseContext(newBase.createConfigurationContext(cfg))
    }
}
