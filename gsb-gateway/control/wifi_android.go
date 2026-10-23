package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
)

// Android apps cannot read /proc/net/route or query routes over netlink, so
// the app (NetInfo.kt) writes what ConnectivityManager reports for the
// default network to <data>/netinfo.json whenever it changes.

type androidNetInfo struct {
	WifiInfo
	Wifi bool `json:"wifi"`
}

func readNetInfo() *androidNetInfo {
	raw, err := os.ReadFile(dataPath("netinfo.json"))
	if err != nil {
		return nil
	}
	var n androidNetInfo
	if json.Unmarshal(raw, &n) != nil || !n.Connected || n.IP == "" {
		return nil
	}
	return &n
}

func findWifiAdapter() *WifiInfo {
	if n := readNetInfo(); n != nil && n.Wifi {
		return &n.WifiInfo
	}
	return nil
}

// findUplink returns the phone's current default network (Wi-Fi or mobile
// data); this process is outside the VPN, so that is where its sockets go.
func findUplink(dst string) *WifiInfo {
	if n := readNetInfo(); n != nil {
		return &n.WifiInfo
	}
	return nil
}

func connectGSBWifi() (string, error) {
	return "", errors.New("Android'de Wi-Fi ayarlarından GSBWIFI ağına bağlanın")
}

func hiddenCmd(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}
