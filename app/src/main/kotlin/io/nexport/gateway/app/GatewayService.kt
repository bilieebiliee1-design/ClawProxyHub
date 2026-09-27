package io.nexport.gateway.app

import io.nexport.gateway.R

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import androidx.core.app.NotificationCompat
import androidx.core.app.ServiceCompat

/**
 * GatewayService — 前台服务（specialUse 单路，承载常驻网关与任务调度）。
 *
 * Android 15 行为变更（评审高危④）：dataSync 有 24h 内 6h 配额且 BOOT_COMPLETED
 * 不得拉起——常驻网关 + 任务调度约 6 小时即断，故仅用 specialUse：
 *  - 声明 FOREGROUND_SERVICE_SPECIAL_USE 权限 + PROPERTY_SPECIAL_USE_FGS_SUBTYPE
 *    用途说明（AndroidManifest，Play 审核要点）；
 *  - 隧道 Active 时通知展示临时域名 + 一键断开；应用退出即回收（stopAll）。
 * 不长期持有 WakeLock，接受 Doze 下 interval/daily 任务漂移（onboarding 已明示）。
 *
 * 基于 ClawProxyHub（AGPL-3.0）修改构建。
 */
class GatewayService : Service() {

    companion object {
        const val CHANNEL_ID = "gateway"
        const val NOTIF_ID = 41

        const val ACTION_START = "io.nexport.gateway.action.START"
        const val ACTION_DISCONNECT_TUNNEL = "io.nexport.gateway.action.DISCONNECT_TUNNEL"
        const val ACTION_STOP = "io.nexport.gateway.action.STOP"

        @Volatile var isRunning = false; private set

        fun start(context: Context) {
            val i = Intent(context, GatewayService::class.java).setAction(ACTION_START)
            androidx.core.content.ContextCompat.startForegroundService(context, i)
        }

        fun stop(context: Context) {
            context.stopService(Intent(context, GatewayService::class.java))
        }

        fun createChannel(context: Context) {
            val ch = NotificationChannel(
                CHANNEL_ID,
                context.getString(R.string.notif_channel_gateway),
                NotificationManager.IMPORTANCE_LOW
            ).apply { description = context.getString(R.string.notif_channel_gateway_desc) }
            (context.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager)
                .createNotificationChannel(ch)
        }

        /** 隧道/核心状态变化时刷新通知（服务未运行则跳过）。 */
        fun refresh(context: Context) {
            if (!isRunning) return
            val nm = context.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
            nm.notify(NOTIF_ID, buildNotification(context))
        }

        private fun buildNotification(context: Context): Notification {
            val contentIntent = PendingIntent.getActivity(
                context, 0,
                Intent(context, MainActivity::class.java),
                PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
            )
            val b = NotificationCompat.Builder(context, CHANNEL_ID)
                .setSmallIcon(R.drawable.ic_stat_nexport)
                .setContentIntent(contentIntent)
                .setOngoing(true)
                .setOnlyAlertOnce(true)
            when {
                CoreController.state == CoreController.State.RUNNING &&
                    CoreController.tunnelState == "active" && CoreController.tunnelUrl.isNotEmpty() -> {
                    b.setContentTitle(context.getString(R.string.notif_tunnel_active, CoreController.tunnelUrl))
                    b.addAction(NotificationCompat.Action(
                        0,
                        context.getString(R.string.notif_action_disconnect),
                        PendingIntent.getService(
                            context, 1,
                            Intent(context, GatewayService::class.java).setAction(ACTION_DISCONNECT_TUNNEL),
                            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
                    ))
                }
                CoreController.state == CoreController.State.RUNNING ->
                    b.setContentTitle(context.getString(R.string.notif_core_running, CoreController.gatewayPort))
                else -> b.setContentTitle(context.getString(R.string.notif_core_starting))
            }
            b.addAction(NotificationCompat.Action(
                0,
                context.getString(R.string.notif_action_stop),
                PendingIntent.getService(
                    context, 2,
                    Intent(context, GatewayService::class.java).setAction(ACTION_STOP),
                    PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
            ))
            return b.build()
        }
    }

    private val listener = object : CoreController.Listener {
        override fun onCoreStateChanged(state: CoreController.State, error: String?) {
            refresh(this@GatewayService)
        }

        override fun onTunnelEvent(state: String, url: String, message: String) {
            refresh(this@GatewayService)
        }
    }

    override fun onCreate() {
        super.onCreate()
        CoreController.addListener(listener)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_STOP -> {
                CoreController.stopTunnel()
                CoreController.stop()
                stopSelf()
                return START_NOT_STICKY
            }
            ACTION_DISCONNECT_TUNNEL -> {
                CoreController.stopTunnel()
                return START_STICKY
            }
            else -> {
                ServiceCompat.startForeground(
                    this, NOTIF_ID, buildNotification(this),
                    if (Build.VERSION.SDK_INT >= 29) ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE else 0
                )
                isRunning = true
                // 服务即核心载体：确保核心在跑（含开机自启路径）
                CoreController.start()
            }
        }
        return START_STICKY
    }

    override fun onDestroy() {
        isRunning = false
        CoreController.removeListener(listener)
        super.onDestroy()
    }

    override fun onBind(intent: Intent?): IBinder? = null
}
