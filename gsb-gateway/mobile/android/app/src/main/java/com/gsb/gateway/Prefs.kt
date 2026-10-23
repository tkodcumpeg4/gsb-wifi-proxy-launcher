package com.gsb.gateway

import android.content.Context

/**
 * Saved connection settings. Phase 0 keeps them in the app's private
 * SharedPreferences (readable only by this app, excluded from backups);
 * phase 2 moves the password into the Android Keystore.
 */
class Prefs(ctx: Context) {
    private val sp = ctx.getSharedPreferences("gsb", Context.MODE_PRIVATE)

    var host: String
        get() = sp.getString("host", "") ?: ""
        set(v) = sp.edit().putString("host", v).apply()
    var port: Int
        get() = sp.getInt("port", 8080)
        set(v) = sp.edit().putInt("port", v).apply()
    var username: String
        get() = sp.getString("user", "") ?: ""
        set(v) = sp.edit().putString("user", v).apply()
    var password: String
        get() = sp.getString("pass", "") ?: ""
        set(v) = sp.edit().putString("pass", v).apply()
    var udpgw: String
        get() = sp.getString("udpgw", "") ?: ""
        set(v) = sp.edit().putString("udpgw", v).apply()
    var acceptedNotice: Boolean
        get() = sp.getBoolean("notice", false)
        set(v) = sp.edit().putBoolean("notice", v).apply()
}
