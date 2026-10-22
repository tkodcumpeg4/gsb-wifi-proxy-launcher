package main

import (
	"sync"
	"time"
)

type WifiInfo struct {
	Connected   bool     `json:"connected"`
	IP          string   `json:"ip"`
	Gateway     string   `json:"gateway"`
	DNSSuffix   string   `json:"dns_suffix"`
	AdapterName string   `json:"adapter_name"`
	IfIndex     uint32   `json:"if_index"`
	DNSServers  []string `json:"dns_servers"`
}

var wifiCache struct {
	sync.Mutex
	at   time.Time
	info WifiInfo
}

// getWifiInfo returns the Wi-Fi adapter (GetAdaptersAddresses on Windows,
// sysfs/iproute2 on Linux). Cached for 3 s.
func getWifiInfo(force bool) WifiInfo {
	wifiCache.Lock()
	defer wifiCache.Unlock()
	if !force && time.Since(wifiCache.at) < 3*time.Second {
		return wifiCache.info
	}
	info := WifiInfo{AdapterName: "Wi-Fi"}
	if a := findWifiAdapter(); a != nil {
		info = *a
	}
	wifiCache.at, wifiCache.info = time.Now(), info
	return info
}
