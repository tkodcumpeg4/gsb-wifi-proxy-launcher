package com.gsb.gateway

import android.content.Context

/**
 * App-side settings. Proxy, credentials and relay live in the Go process's
 * config.json (edited in the panel); only what the app itself needs is here.
 */
class Prefs(ctx: Context) {
    private val sp = ctx.getSharedPreferences("gsb", Context.MODE_PRIVATE)

    /** Packages that bypass the VPN (bank apps and the like). */
    var excluded: Set<String>
        get() = sp.getStringSet("excluded", emptySet()) ?: emptySet()
        set(v) = sp.edit().putStringSet("excluded", v).apply()

    var acceptedNotice: Boolean
        get() = sp.getBoolean("notice", false)
        set(v) = sp.edit().putBoolean("notice", v).apply()
}
