package com.gsb.gateway

import android.Manifest
import android.annotation.SuppressLint
import android.app.Activity
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.net.VpnService
import android.os.Build
import android.os.Bundle
import android.view.WindowInsets
import android.webkit.JavascriptInterface
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.FrameLayout
import org.json.JSONArray
import org.json.JSONObject

/**
 * One WebView. It opens on assets/index.html (first-run notice, starting the
 * Go bridge), then shows the bridge's own pages: /mini as the home screen and
 * / (the desktop panel) for details. The pages detect `window.GSB` below and
 * switch the VPN, app exclusions and links through the app.
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
                override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
                    val url = request.url.toString()
                    if (url.startsWith(Gateway.PANEL) || url.startsWith("file:///android_asset/")) return false
                    openExternal(url)
                    return true
                }
            }
            addJavascriptInterface(Bridge(), "GSB")
            loadUrl("file:///android_asset/index.html")
        }
        // Android 15+ draws edge to edge: keep the page clear of the status
        // bar, navigation bar and keyboard. The padding goes on a container,
        // because a WebView does not inset its own content.
        val root = FrameLayout(this).apply {
            setBackgroundColor(0xFF080B14.toInt())
            addView(web, FrameLayout.LayoutParams(FrameLayout.LayoutParams.MATCH_PARENT, FrameLayout.LayoutParams.MATCH_PARENT))
        }
        root.setOnApplyWindowInsetsListener { v, insets ->
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
        setContentView(root)
        if (Build.VERSION.SDK_INT >= 33 &&
            checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), REQ_NOTIFY)
        }
    }

    @Deprecated("Back handling without AndroidX")
    override fun onBackPressed() {
        val url = web.url.orEmpty()
        // From the detailed panel, back returns to the home screen.
        if (url.startsWith(Gateway.PANEL) && !url.endsWith("/mini")) {
            web.loadUrl("${Gateway.PANEL}/mini")
            return
        }
        // On the home screen, an open sheet (app list) closes first.
        web.evaluateJavascript("window.gsbBack ? gsbBack() : false") { handled ->
            @Suppress("DEPRECATION")
            if (handled != "true") super.onBackPressed()
        }
    }

    @Deprecated("Activity result API without AndroidX")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode == REQ_VPN) {
            if (resultCode == RESULT_OK) GsbVpnService.start(this)
            else GsbVpnService.State.error = "VPN izni verilmedi"
        }
    }

    private fun openExternal(url: String) {
        if (!url.startsWith("https://")) return
        runCatching { startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url))) }
    }

    inner class Bridge {
        @JavascriptInterface
        fun notice(): Boolean = prefs.acceptedNotice

        @JavascriptInterface
        fun acceptNotice() {
            prefs.acceptedNotice = true
        }

        /** Starts the Go bridge, then shows the home screen. Called by index.html. */
        @JavascriptInterface
        fun boot() {
            Thread {
                val ok = Gateway.ensure(this@MainActivity)
                runOnUiThread {
                    if (ok) web.loadUrl("${Gateway.PANEL}/mini")
                    else web.evaluateJavascript("bootFailed()", null)
                }
            }.start()
        }

        @JavascriptInterface
        fun status(): String {
            val s = GsbVpnService.State
            return JSONObject()
                .put("running", s.running)
                .put("starting", s.starting)
                .put("udp", s.udp)
                .put("error", s.error)
                .put("since", s.since)
                .put("proxy", s.proxy)
                .put("tx", if (s.running) Core.tx() else 0)
                .put("rx", if (s.running) Core.rx() else 0)
                .put("excluded", prefs.excluded.size)
                .put("private_dns_strict", NetInfo.privateDnsStrict)
                .toString()
        }

        /** Connects or disconnects the VPN; returns an error message or "". */
        @JavascriptInterface
        fun vpn(enable: Boolean): String {
            GsbVpnService.State.error = ""
            if (!enable) {
                GsbVpnService.stop(this@MainActivity)
                return ""
            }
            runOnUiThread {
                val ask = VpnService.prepare(this@MainActivity)
                if (ask != null) {
                    @Suppress("DEPRECATION")
                    startActivityForResult(ask, REQ_VPN)
                } else {
                    GsbVpnService.start(this@MainActivity)
                }
            }
            return ""
        }

        @JavascriptInterface
        fun openPanel() = runOnUiThread { web.loadUrl("${Gateway.PANEL}/") }

        @JavascriptInterface
        fun openMini() = runOnUiThread { web.loadUrl("${Gateway.PANEL}/mini") }

        @JavascriptInterface
        fun openUrl(url: String) = runOnUiThread { openExternal(url) }

        /** Launchable apps with their exclusion state, sorted by name. */
        @JavascriptInterface
        fun apps(): String {
            val pm = packageManager
            val excluded = prefs.excluded
            val launcher = Intent(Intent.ACTION_MAIN).addCategory(Intent.CATEGORY_LAUNCHER)
            val list = pm.queryIntentActivities(launcher, 0)
                .map { it.activityInfo.packageName to it.loadLabel(pm).toString() }
                .filter { it.first != packageName }
                .distinctBy { it.first }
                .sortedBy { it.second.lowercase() }
            val out = JSONArray()
            list.forEach { (pkg, label) ->
                out.put(JSONObject().put("pkg", pkg).put("label", label).put("excluded", pkg in excluded))
            }
            return out.toString()
        }

        /** Takes effect on the next connect; reconnects now if the VPN is up. */
        @JavascriptInterface
        fun setExcluded(pkg: String, excluded: Boolean) {
            prefs.excluded = if (excluded) prefs.excluded + pkg else prefs.excluded - pkg
            if (GsbVpnService.State.running) {
                GsbVpnService.stop(this@MainActivity)
                web.postDelayed({ GsbVpnService.start(this@MainActivity) }, 1500)
            }
        }
    }

    companion object {
        private const val REQ_VPN = 1
        private const val REQ_NOTIFY = 2
    }
}
