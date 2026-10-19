package main

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// force_tun makes Windows prefer GSB-TUN for everything while the tunnel is up,
// so NLA-aware apps (and the "best interface" selection) never fall back to the
// bare GSB Wi-Fi — which has no usable internet without the proxy. It raises the
// uplink's IPv4 interface metric; the /32 bypass routes to the proxy and relay
// still win by prefix length, so the uplink keeps carrying them.
const forcedUplinkMetric = 9999

var uplinkMetric struct {
	sync.Mutex
	ifIndex  uint32
	original string // original InterfaceMetric, "" when not forced
}

func applyForceTun(ifIndex uint32) {
	uplinkMetric.Lock()
	defer uplinkMetric.Unlock()
	if uplinkMetric.original != "" {
		return
	}
	orig := getInterfaceMetric(ifIndex)
	if orig == "" {
		orig = "auto"
	}
	if setInterfaceMetric(ifIndex, strconv.Itoa(forcedUplinkMetric)) {
		uplinkMetric.ifIndex = ifIndex
		uplinkMetric.original = orig
	}
}

func clearForceTun() {
	uplinkMetric.Lock()
	defer uplinkMetric.Unlock()
	if uplinkMetric.original == "" {
		return
	}
	setInterfaceMetric(uplinkMetric.ifIndex, uplinkMetric.original)
	uplinkMetric.original = ""
}

// getInterfaceMetric returns the interface's current IPv4 InterfaceMetric, or
// "" if it is on automatic / cannot be read.
func getInterfaceMetric(ifIndex uint32) string {
	out, err := hiddenPwsh(fmt.Sprintf(
		"$i=Get-NetIPInterface -InterfaceIndex %d -AddressFamily IPv4 -ErrorAction Stop;"+
			"if($i.AutomaticMetric -eq 'Enabled'){'auto'}else{$i.InterfaceMetric}", ifIndex))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func setInterfaceMetric(ifIndex uint32, metric string) bool {
	var cmd string
	if metric == "auto" {
		cmd = fmt.Sprintf("Set-NetIPInterface -InterfaceIndex %d -AddressFamily IPv4 -AutomaticMetric Enabled", ifIndex)
	} else {
		cmd = fmt.Sprintf("Set-NetIPInterface -InterfaceIndex %d -AddressFamily IPv4 -AutomaticMetric Disabled -InterfaceMetric %s", ifIndex, metric)
	}
	_, err := hiddenPwsh(cmd)
	return err == nil
}

func hiddenPwsh(script string) (string, error) {
	out, err := hiddenCmd("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	return string(out), err
}
