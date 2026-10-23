package com.gsb.gateway

import android.content.Context
import android.net.ConnectivityManager
import android.net.LinkProperties
import android.net.Network
import android.net.NetworkCapabilities
import android.os.Build
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.net.Inet4Address
import java.net.NetworkInterface

/**
 * Writes the default network's details to <data>/netinfo.json for the Go
 * process (wifi_android.go), which cannot read routes itself on Android.
 * This app is excluded from its own VPN, so "default network" here is the
 * real Wi-Fi or mobile data network underneath.
 */
object NetInfo {
    @Volatile var privateDnsStrict = false
        private set

    fun write(ctx: Context) {
        val cm = ctx.getSystemService(ConnectivityManager::class.java)
        val net = cm.activeNetwork
        val lp = net?.let { cm.getLinkProperties(it) }
        val nc = net?.let { cm.getNetworkCapabilities(it) }
        val json = JSONObject().put("connected", false)
        if (lp != null && nc != null) fill(json, lp, nc)
        val f = File(Gateway.dataDir(ctx), "netinfo.json")
        val tmp = File(f.path + ".tmp")
        tmp.writeText(json.toString())
        tmp.renameTo(f)
    }

    private fun fill(json: JSONObject, lp: LinkProperties, nc: NetworkCapabilities) {
        val ip = lp.linkAddresses.map { it.address }.firstOrNull { it is Inet4Address }
        val gw = lp.routes.firstOrNull { it.isDefaultRoute && it.gateway is Inet4Address }?.gateway
        val dns = JSONArray()
        lp.dnsServers.filterIsInstance<Inet4Address>().forEach { dns.put(it.hostAddress) }
        val ifName = lp.interfaceName ?: ""
        privateDnsStrict = Build.VERSION.SDK_INT >= 28 && lp.isPrivateDnsActive && lp.privateDnsServerName != null
        json.put("connected", ip != null)
            .put("ip", ip?.hostAddress ?: "")
            .put("gateway", gw?.hostAddress ?: "")
            .put("dns_suffix", lp.domains ?: "")
            .put("adapter_name", ifName)
            .put("if_index", runCatching { NetworkInterface.getByName(ifName)?.index ?: 0 }.getOrDefault(0))
            .put("dns_servers", dns)
            .put("wifi", nc.hasTransport(NetworkCapabilities.TRANSPORT_WIFI))
    }

    /** Keeps netinfo.json current for the life of the process. */
    fun watch(ctx: Context) {
        val app = ctx.applicationContext
        app.getSystemService(ConnectivityManager::class.java).registerDefaultNetworkCallback(
            object : ConnectivityManager.NetworkCallback() {
                override fun onAvailable(network: Network) = update()
                override fun onLost(network: Network) = update()
                override fun onLinkPropertiesChanged(network: Network, lp: LinkProperties) = update()
                private fun update() { runCatching { write(app) } }
            },
        )
    }
}
