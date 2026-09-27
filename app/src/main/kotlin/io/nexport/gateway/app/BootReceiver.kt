package io.nexport.gateway.app

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

/**
 * BootReceiver — 开机自启（可选项，默认关）。
 *
 * Android 15 约束：BOOT_COMPLETED 不能拉起 dataSync 类前台服务；本应用单路
 * specialUse 不在受限列表内，可安全由开机广播拉起（架构方案 featureParity 已核实）。
 * 仅在用户已接受 EULA 并显式开启自启时生效。
 *
 * 基于 ClawProxyHub（AGPL-3.0）修改构建。
 */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED) return
        if (!Prefs.eulaAccepted(context) || !Prefs.autoStart(context)) return
        GatewayService.start(context)
    }
}
