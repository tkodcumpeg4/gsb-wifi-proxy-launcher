package com.gsb.gateway

import android.content.Context
import android.util.Log
import java.io.File
import java.net.HttpURLConnection
import java.net.InetSocketAddress
import java.net.Socket
import java.net.URL

/**
 * The Go control plane from the desktop build (bridge on 127.0.0.1:8880,
 * panel and API on 127.0.0.1:8765), run as a child process. It ships as
 * lib/<abi>/libgsbgateway.so so Android installs it executable. Being part
 * of this app's UID, it is excluded from the VPN like the app itself, so its
 * connections to the university proxy go straight out over Wi-Fi.
 */
object Gateway {
    const val PANEL = "http://127.0.0.1:8765"
    const val BRIDGE = "http://127.0.0.1:8880"
    private const val TAG = "Gateway"

    private val lock = Any()
    private var proc: Process? = null

    fun dataDir(ctx: Context) = File(ctx.filesDir, "gsb").apply { mkdirs() }

    /** Starts the process if needed and waits until the panel answers. Blocking. */
    fun ensure(ctx: Context): Boolean {
        synchronized(lock) {
            if (proc?.isAlive == true || reachable()) {
                return waitReady()
            }
            val bin = File(ctx.applicationInfo.nativeLibraryDir, "libgsbgateway.so")
            val data = dataDir(ctx)
            NetInfo.write(ctx)
            try {
                val pb = ProcessBuilder(bin.path, "--no-tray", "--no-browser", "--data-dir", data.path)
                    .redirectErrorStream(true)
                    .redirectOutput(File(data, "gateway.log"))
                pb.environment()["GSB_DATA_DIR"] = data.path
                pb.environment()["GSB_SECRET_KEY"] = KeyVault.dataKeyHex(ctx)
                proc = pb.start()
            } catch (e: Exception) {
                Log.e(TAG, "start", e)
                return false
            }
        }
        return waitReady()
    }

    private fun waitReady(): Boolean {
        repeat(75) {
            if (reachable()) return true
            if (proc?.isAlive == false) return false
            Thread.sleep(200)
        }
        return false
    }

    private fun reachable() = try {
        Socket().use { it.connect(InetSocketAddress("127.0.0.1", 8765), 300) }
        true
    } catch (_: Exception) {
        false
    }

    /** GET on the local API; null on any failure. */
    fun get(path: String): String? = try {
        val c = URL(PANEL + path).openConnection() as HttpURLConnection
        c.connectTimeout = 2000
        c.readTimeout = 4000
        c.inputStream.bufferedReader().use { it.readText() }.also { c.disconnect() }
    } catch (_: Exception) {
        null
    }

    fun stop() {
        synchronized(lock) {
            proc?.destroy()
            proc = null
        }
    }
}
