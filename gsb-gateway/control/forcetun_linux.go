package main

// force_tun exists for Windows, where NLA and "best interface" selection can
// send traffic out of the bare Wi-Fi while GSB-TUN is up. Linux routes purely
// by the table: gsb-core's 0.0.0.0/1 + 128.0.0.0/1 routes already beat the
// uplink's default route for every socket, so there is nothing to force.

func applyForceTun(ifIndex uint32) {}

func clearForceTun() {}
