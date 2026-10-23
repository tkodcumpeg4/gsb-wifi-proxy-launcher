package com.gsb.gateway

/** JNI bindings to libgsbcore.so (gsb-gateway/core-mobile). */
object Core {
    init {
        System.loadLibrary("gsbcore")
    }

    /**
     * Runs the tunnel on [tunFd] and blocks until [stop] (returns 0) or a
     * failure (negative). [proxyUrl] is `http://user:pass@host:port` with
     * URL-encoded credentials; [udpgw] is `ip:port` or empty.
     */
    external fun start(tunFd: Int, proxyUrl: String, mtu: Int, udpgw: String): Int

    external fun stop(): Int

    /** Bytes sent / received through the tunnel since [start]. */
    external fun tx(): Long

    external fun rx(): Long
}
