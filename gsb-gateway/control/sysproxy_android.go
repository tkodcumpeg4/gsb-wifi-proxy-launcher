package main

// A system proxy setting would only cover HTTP-aware apps; on Android the VPN
// already carries every app, so the panel hides this switch.

func setSystemProxy(enable bool) bool {
	stats.setError("Android'de sistem proxy'si yerine VPN kullanılır")
	return false
}

type SysProxyStatus struct {
	Enabled bool   `json:"enabled"`
	Server  string `json:"server"`
}

func getSystemProxyStatus() SysProxyStatus { return SysProxyStatus{} }
