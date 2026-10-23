package com.gsb.gateway

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.VpnService
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.os.ParcelFileDescriptor
import android.service.quicksettings.TileService
import android.util.Log
import org.json.JSONObject

/**
 * The phone's GSB-TUN. Android creates the VPN interface and routes;
 * gsb-core (Rust, tun2proxy) terminates the traffic and sends it with
 * CONNECT to the Go bridge on 127.0.0.1:8880, which applies WARP-Shield and
 * talks to the university proxy, exactly as on the desktop. This app and its
 * Go child process are excluded from the VPN, so their own connections leave
 * over Wi-Fi directly (the desktop needs a bypass route for that).
 *
 * Started from the app, the Quick Settings tile, or by Android itself for
 * "Always-on VPN".
 */
class GsbVpnService : VpnService() {

    /** Shared with the UI and the tile. */
    object State {
        @Volatile var running = false
        @Volatile var starting = false
        @Volatile var udp = false
        @Volatile var error = ""
        @Volatile var since = 0L
        @Volatile var proxy = ""
    }

    private var tun: ParcelFileDescriptor? = null
    private var worker: Thread? = null
    private val main = Handler(Looper.getMainLooper())
    private var lastBytes = 0L
    private var lastTick = 0L

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            stopTunnel()
        } else {
            // ACTION_START, or android.net.VpnService / null for Always-on.
            startTunnel()
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

    private fun startTunnel() {
        if (worker != null) return
        State.starting = true
        State.error = ""
        goForeground(notification("Bağlanıyor…"))
        refreshTile()
        worker = Thread({ runTunnel() }, "gsb-core").also { it.start() }
    }

    /** Worker thread: bridge up, interface up, then gsb-core until stopped. */
    private fun runTunnel() {
        if (!Gateway.ensure(this)) {
            fail("Köprü (gsb-gateway) başlatılamadı")
            return
        }
        val status = Gateway.get("/api/status")?.let { runCatching { JSONObject(it) }.getOrNull() }
        val cfg = status?.optJSONObject("config")
        val host = cfg?.optString("active_proxy_host").orEmpty()
        if (host.isEmpty()) {
            fail("Önce panelde bir proxy seçip giriş bilgilerini kaydedin")
            return
        }
        val udpgw = status?.optString("udpgw").orEmpty()
        State.proxy = "$host:${cfg?.optInt("active_proxy_port", 8080)}"
        State.udp = udpgw.isNotEmpty()

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
                .apply {
                    addDisallowedApplication(packageName)
                    Prefs(this@GsbVpnService).excluded.forEach { pkg -> runCatching { addDisallowedApplication(pkg) } }
                    setConfigureIntent(PendingIntent.getActivity(this@GsbVpnService, 0, Intent(this@GsbVpnService, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE))
                    if (Build.VERSION.SDK_INT >= 29) { setMetered(false); setBlocking(false) }
                }
                .establish()
        } catch (e: Exception) {
            Log.e(TAG, "establish", e)
            null
        }
        if (fd == null) {
            fail("VPN arayüzü oluşturulamadı (VPN izni verildi mi?)")
            return
        }
        tun = fd

        State.running = true
        State.starting = false
        State.since = System.currentTimeMillis()
        lastBytes = 0
        lastTick = 0
        main.post(ticker)
        refreshTile()
        val code = Core.start(fd.fd, Gateway.BRIDGE, MTU, udpgw)
        Log.i(TAG, "core exited with $code")
        if (code != 0) State.error = "Tünel durdu (kod $code)"
        cleanup()
    }

    private fun stopTunnel() {
        if (worker == null) {
            cleanup()
            return
        }
        Core.stop() // runTunnel returns and runs cleanup()
    }

    private fun cleanup() {
        main.removeCallbacks(ticker)
        worker = null
        try { tun?.close() } catch (_: Exception) {}
        tun = null
        State.running = false
        State.starting = false
        refreshTile()
        stopForeground(STOP_FOREGROUND_REMOVE)
        stopSelf()
    }

    private fun fail(msg: String) {
        State.error = msg
        cleanup()
    }

    /** Live speed in the notification, every 2 s. */
    private val ticker = object : Runnable {
        override fun run() {
            if (!State.running) return
            val now = System.currentTimeMillis()
            val total = Core.tx() + Core.rx()
            val rate = if (lastTick > 0) (total - lastBytes) * 1000 / maxOf(1, now - lastTick) else 0
            lastBytes = total
            lastTick = now
            getSystemService(NotificationManager::class.java)
                .notify(NOTIFICATION_ID, notification("${State.proxy} · ${fmt(rate)}/s · ${fmt(total)}"))
            main.postDelayed(this, 2000)
        }
    }

    private fun fmt(n: Long) = when {
        n < 1024 * 1024 -> "%.1f KB".format(n / 1024.0)
        n < 1024L * 1024 * 1024 -> "%.1f MB".format(n / 1048576.0)
        else -> "%.2f GB".format(n / 1073741824.0)
    }

    private fun notification(text: String): Notification {
        val nm = getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(NotificationChannel(CHANNEL, "Tünel durumu", NotificationManager.IMPORTANCE_LOW))
        val open = PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE)
        val stop = PendingIntent.getService(this, 1, Intent(this, GsbVpnService::class.java).setAction(ACTION_STOP), PendingIntent.FLAG_IMMUTABLE)
        return Notification.Builder(this, CHANNEL)
            .setSmallIcon(R.drawable.ic_shield)
            .setContentTitle(if (State.running) "GSB Gateway · bağlı" else "GSB Gateway")
            .setContentText(text)
            .setContentIntent(open)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .addAction(Notification.Action.Builder(null, "Bağlantıyı kes", stop).build())
            .build()
    }

    private fun goForeground(n: Notification) {
        if (Build.VERSION.SDK_INT >= 34) {
            startForeground(NOTIFICATION_ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE)
        } else {
            startForeground(NOTIFICATION_ID, n)
        }
    }

    private fun refreshTile() {
        TileService.requestListeningState(this, ComponentName(this, VpnTileService::class.java))
    }

    companion object {
        private const val TAG = "GsbVpn"
        private const val CHANNEL = "tunnel"
        private const val NOTIFICATION_ID = 1
        private const val MTU = 1500
        /** Must match TUN_DNS in core-mobile/src/lib.rs. */
        private const val TUN_DNS = "10.255.77.1"
        const val ACTION_START = "com.gsb.gateway.START"
        const val ACTION_STOP = "com.gsb.gateway.STOP"

        fun start(ctx: Context) {
            ctx.startForegroundService(Intent(ctx, GsbVpnService::class.java).setAction(ACTION_START))
        }

        fun stop(ctx: Context) {
            ctx.startService(Intent(ctx, GsbVpnService::class.java).setAction(ACTION_STOP))
        }
    }
}
