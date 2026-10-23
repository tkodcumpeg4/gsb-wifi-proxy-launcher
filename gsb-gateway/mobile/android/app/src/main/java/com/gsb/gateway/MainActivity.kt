package com.gsb.gateway

import android.Manifest
import android.annotation.SuppressLint
import android.app.Activity
import android.content.Intent
import android.content.pm.PackageManager
import android.net.VpnService
import android.os.Build
import android.os.Bundle
import android.view.WindowInsets
import android.webkit.JavascriptInterface
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import org.json.JSONObject

/**
 * One WebView showing assets/index.html; the page talks to the app through
 * the `GSB` JavaScript interface below. Only the bundled page is loaded.
 */
class MainActivity : Activity() {

    private lateinit var web: WebView
    private lateinit var prefs: Prefs

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        prefs = Prefs(this)
        web = WebView(this).apply {
            setBackgroundColor(0xFF080B14.toInt())
            settings.javaScriptEnabled = true
            settings.domStorageEnabled = true
            settings.allowFileAccess = false
            webViewClient = object : WebViewClient() {
                // Never navigate away from the bundled page.
                override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest) = true
            }
            addJavascriptInterface(Bridge(), "GSB")
            loadUrl("file:///android_asset/index.html")
        }
        // Android 15+ draws edge to edge: keep the page clear of the status
        // bar, navigation bar and keyboard.
        web.setOnApplyWindowInsetsListener { v, insets ->
            if (Build.VERSION.SDK_INT >= 30) {
                val b = insets.getInsets(WindowInsets.Type.systemBars() or WindowInsets.Type.ime())
                v.setPadding(b.left, b.top, b.right, b.bottom)
                WindowInsets.CONSUMED
            } else {
                @Suppress("DEPRECATION")
                v.setPadding(insets.systemWindowInsetLeft, insets.systemWindowInsetTop, insets.systemWindowInsetRight, insets.systemWindowInsetBottom)
                insets
            }
        }
        setContentView(web)
        if (Build.VERSION.SDK_INT >= 33 &&
            checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), REQ_NOTIFY)
        }
    }

    @Deprecated("Activity result API without AndroidX")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode == REQ_VPN) {
            if (resultCode == RESULT_OK) startService(Intent(this, GsbVpnService::class.java))
            else GsbVpnService.State.error = "VPN izni verilmedi"
        }
    }

    /** Asks for the one-time VPN permission, then starts the service. */
    private fun connect() {
        val ask = VpnService.prepare(this)
        if (ask != null) {
            @Suppress("DEPRECATION")
            startActivityForResult(ask, REQ_VPN)
        } else {
            startService(Intent(this, GsbVpnService::class.java))
        }
    }

    inner class Bridge {
        @JavascriptInterface
        fun settings(): String = JSONObject()
            .put("host", prefs.host)
            .put("port", prefs.port)
            .put("username", prefs.username)
            .put("has_password", prefs.password.isNotEmpty())
            .put("udpgw", prefs.udpgw)
            .put("notice", prefs.acceptedNotice)
            .toString()

        @JavascriptInterface
        fun status(): String {
            val s = GsbVpnService.State
            return JSONObject()
                .put("running", s.running)
                .put("starting", s.starting)
                .put("error", s.error)
                .put("since", s.since)
                .put("proxy", s.proxy)
                .put("tx", if (s.running) Core.tx() else 0)
                .put("rx", if (s.running) Core.rx() else 0)
                .toString()
        }

        /** Saves the form (an empty password keeps the saved one) and connects. */
        @JavascriptInterface
        fun connect(hostPort: String, username: String, password: String, udpgw: String): String {
            val hp = hostPort.trim()
            val i = hp.lastIndexOf(':')
            val port = if (i > 0) hp.substring(i + 1).toIntOrNull() else null
            if (i <= 0 || port == null || port !in 1..65535) return "Adres IP:Port biçiminde olmalı (ör. 193.140.97.32:8080)"
            prefs.host = hp.substring(0, i)
            prefs.port = port
            prefs.username = username.trim()
            if (password.isNotEmpty()) prefs.password = password
            prefs.udpgw = udpgw.trim()
            GsbVpnService.State.error = ""
            runOnUiThread { this@MainActivity.connect() }
            return ""
        }

        @JavascriptInterface
        fun disconnect() {
            GsbVpnService.stop(this@MainActivity)
        }

        @JavascriptInterface
        fun acceptNotice() {
            prefs.acceptedNotice = true
        }
    }

    companion object {
        private const val REQ_VPN = 1
        private const val REQ_NOTIFY = 2
    }
}
