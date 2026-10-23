package com.gsb.gateway

import android.app.PendingIntent
import android.content.Intent
import android.net.VpnService
import android.os.Build
import android.service.quicksettings.Tile
import android.service.quicksettings.TileService

/** Quick Settings tile: connect / disconnect without opening the app. */
class VpnTileService : TileService() {

    override fun onStartListening() = render()

    override fun onClick() {
        val s = GsbVpnService.State
        when {
            s.running || s.starting -> GsbVpnService.stop(this)
            // First use needs the system VPN consent, which only an activity can ask for.
            VpnService.prepare(this) != null -> openApp()
            else -> GsbVpnService.start(this)
        }
        render()
    }

    private fun openApp() {
        val intent = Intent(this, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        if (Build.VERSION.SDK_INT >= 34) {
            startActivityAndCollapse(PendingIntent.getActivity(this, 0, intent, PendingIntent.FLAG_IMMUTABLE))
        } else {
            @Suppress("DEPRECATION", "StartActivityAndCollapseDeprecated")
            startActivityAndCollapse(intent)
        }
    }

    private fun render() {
        val t = qsTile ?: return
        val s = GsbVpnService.State
        t.state = if (s.running || s.starting) Tile.STATE_ACTIVE else Tile.STATE_INACTIVE
        if (Build.VERSION.SDK_INT >= 29) {
            t.subtitle = when {
                s.running -> "Bağlı"
                s.starting -> "Bağlanıyor"
                else -> "Kapalı"
            }
        }
        t.updateTile()
    }
}
