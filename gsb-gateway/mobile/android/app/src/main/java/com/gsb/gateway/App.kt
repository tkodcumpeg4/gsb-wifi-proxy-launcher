package com.gsb.gateway

import android.app.Application

class App : Application() {
    override fun onCreate() {
        super.onCreate()
        // The Go bridge reads the current network from netinfo.json.
        NetInfo.watch(this)
    }
}
