package com.gsb.gateway

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.VpnService
import android.os.Build
import android.os.ParcelFileDescriptor
import android.util.Log
import java.net.URLEncoder

/**
 * The phone's GSB-TUN: Android creates the VPN interface and routes, and
 * gsb-core (Rust, tun2proxy) terminates the traffic and sends it to the
 * university proxy with CONNECT. This app itself is excluded from the VPN,
 * so the core's own connections to the proxy leave over Wi-Fi directly (the
 * desktop needs a bypass route for that).
 */
class GsbVpnService : VpnService() {

    /** Shared with the UI, which polls it through the WebView bridge. */
    object State {
        @Volatile var running = false
        @Volatile var starting = false
        @Volatile var error = ""
        @Volatile var since = 0L
        @Volatile var proxy = ""
    }

    private var tun: ParcelFileDescriptor? = null
    private var worker: Thread? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_STOP -> stopTunnel()
            else -> {
                val p = Prefs(this)
                startTunnel(p.host, p.port, p.username, p.password, p.udpgw)
            }
        }
        return START_NOT_STICKY
    }

    override fun onRevoke() {
        // Another VPN took over or the user revoked it in Settings.
        stopTunnel()
        super.onRevoke()
    }

    override fun onDestroy() {
        stopTunnel()
        super.onDestroy()
    }

    private fun startTunnel(host: String, port: Int, user: String, pass: String, udpgw: String) {
        if (worker != null) return
        if (host.isBlank() || port <= 0) {
            State.error = "Proxy adresi eksik"
            stopSelf()
            return
        }
        State.starting = true
        State.error = ""
        goForeground(NOTIFICATION_ID, notification("Bağlanıyor: $host:$port"))

        val fd = try {
            Builder()
                .setSession("GSB Gateway")
                .setMtu(MTU)
                .addAddress("10.255.77.2", 24)
                .addRoute("0.0.0.0", 0)
                // Capture IPv6 too so it cannot bypass the proxy; the core drops it.
                .addAddress("fd00:6773:6274::2", 128)
                .addRoute("::", 0)
                .addDnsServer(TUN_DNS)
                .addDisallowedApplication(packageName)
                .apply { if (Build.VERSION.SDK_INT >= 29) { setMetered(false); setBlocking(false) } }
                .establish()
        } catch (e: Exception) {
            Log.e(TAG, "establish", e)
            null
        }
        if (fd == null) {
            fail("VPN arayüzü oluşturulamadı (izin verildi mi?)")
            return
        }
        tun = fd

        val auth = if (user.isNotEmpty()) "${enc(user)}:${enc(pass)}@" else ""
        val url = "http://$auth$host:$port"
        State.proxy = "$host:$port"
        worker = Thread({
            State.running = true
            State.starting = false
            State.since = System.currentTimeMillis()
            updateNotification("Bağlı: $host:$port")
            val code = Core.start(fd.fd, url, MTU, udpgw)
            Log.i(TAG, "core exited with $code")
            State.running = false
            if (code != 0) State.error = "Tünel durdu (kod $code)"
            cleanup()
        }, "gsb-core").also { it.start() }
    }

    private fun stopTunnel() {
        if (worker == null) {
            cleanup()
            return
        }
        Core.stop() // the worker thread returns and runs cleanup()
    }

    private fun cleanup() {
        worker = null
        try { tun?.close() } catch (_: Exception) {}
        tun = null
        State.running = false
        State.starting = false
        stopForeground(STOP_FOREGROUND_REMOVE)
        stopSelf()
    }

    private fun fail(msg: String) {
        State.error = msg
        cleanup()
    }

    private fun enc(s: String) = URLEncoder.encode(s, "UTF-8").replace("+", "%20")

    private fun notification(text: String): Notification {
        val nm = getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(NotificationChannel(CHANNEL, "Tünel durumu", NotificationManager.IMPORTANCE_LOW))
        val open = PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE)
        val stop = PendingIntent.getService(this, 1, Intent(this, GsbVpnService::class.java).setAction(ACTION_STOP), PendingIntent.FLAG_IMMUTABLE)
        return Notification.Builder(this, CHANNEL)
            .setSmallIcon(R.drawable.ic_shield)
            .setContentTitle("GSB Gateway")
            .setContentText(text)
            .setContentIntent(open)
            .setOngoing(true)
            .addAction(Notification.Action.Builder(null, "Bağlantıyı kes", stop).build())
            .build()
    }

    private fun goForeground(id: Int, n: Notification) {
        if (Build.VERSION.SDK_INT >= 34) {
            startForeground(id, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE)
        } else {
            startForeground(id, n)
        }
    }

    private fun updateNotification(text: String) {
        getSystemService(NotificationManager::class.java).notify(NOTIFICATION_ID, notification(text))
    }

    companion object {
        private const val TAG = "GsbVpn"
        private const val CHANNEL = "tunnel"
        private const val NOTIFICATION_ID = 1
        private const val MTU = 1500
        /** Must match TUN_DNS in core-mobile/src/lib.rs. */
        private const val TUN_DNS = "10.255.77.1"
        const val ACTION_STOP = "com.gsb.gateway.STOP"

        fun stop(ctx: Context) {
            ctx.startService(Intent(ctx, GsbVpnService::class.java).setAction(ACTION_STOP))
        }
    }
}
