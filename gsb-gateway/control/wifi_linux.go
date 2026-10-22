package main

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Linux counterpart of the GetAdaptersAddresses code: interfaces come from the
// net package, the wireless flag from sysfs, gateways from /proc/net/route and
// DNS servers from systemd-resolved (falling back to /etc/resolv.conf).

func findWifiAdapter() *WifiInfo {
	return findAdapter(func(ifc net.Interface) bool {
		return isWireless(ifc.Name) || strings.Contains(strings.ToLower(linkDomain(ifc.Name)), "kykwifi")
	}, "")
}

// findUplink returns the interface the kernel would use to reach dst (Wi-Fi
// on GSB, but also Ethernet), ignoring our own GSB-TUN adapter.
func findUplink(dst string) *WifiInfo {
	if net.ParseIP(dst).To4() == nil {
		return nil
	}
	out, err := exec.Command("ip", "-4", "route", "get", dst).Output()
	if err != nil {
		return nil
	}
	dev, via := routeField(string(out), "dev"), routeField(string(out), "via")
	if dev == "" || dev == "GSB-TUN" {
		return nil
	}
	return findAdapter(func(ifc net.Interface) bool { return ifc.Name == dev }, via)
}

func isWireless(name string) bool {
	for _, p := range []string{"/sys/class/net/" + name + "/wireless", "/sys/class/net/" + name + "/phy80211"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

func findAdapter(match func(ifc net.Interface) bool, gateway string) *WifiInfo {
	ifcs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, ifc := range ifcs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || ifc.Name == "GSB-TUN" {
			continue
		}
		if !match(ifc) {
			continue
		}
		ip := firstIPv4(ifc)
		if ip == nil {
			continue
		}
		info := &WifiInfo{
			Connected:   true,
			IP:          ip.String(),
			Gateway:     gateway,
			DNSSuffix:   linkDomain(ifc.Name),
			AdapterName: ifc.Name,
			IfIndex:     uint32(ifc.Index),
			DNSServers:  linkDNS(ifc.Name),
		}
		if info.Gateway == "" {
			info.Gateway = defaultGateway(ifc.Name)
		}
		if info.Gateway == "" && strings.HasPrefix(info.IP, "10.50.") {
			// GSB hands out /21 subnets; the gateway is the first host.
			v4 := ip.To4()
			info.Gateway = fmt.Sprintf("%d.%d.%d.1", v4[0], v4[1], v4[2]&248)
		}
		return info
	}
	return nil
}

func firstIPv4(ifc net.Interface) net.IP {
	addrs, _ := ifc.Addrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
			return n.IP.To4()
		}
	}
	return nil
}

// defaultGateway reads the default route of ifname from /proc/net/route,
// whose addresses are little-endian hex.
func defaultGateway(ifname string) string {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) < 3 || fs[0] != ifname || fs[1] != "00000000" {
			continue
		}
		b, err := hex.DecodeString(fs[2])
		if err != nil || len(b) != 4 {
			continue
		}
		gw := make(net.IP, 4)
		binary.BigEndian.PutUint32(gw, binary.LittleEndian.Uint32(b))
		if !gw.IsUnspecified() {
			return gw.String()
		}
	}
	return ""
}

var ipv4Re = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)

// linkDNS asks systemd-resolved for the link's servers; without resolved it
// falls back to the global nameservers in /etc/resolv.conf.
func linkDNS(ifname string) []string {
	if out, err := exec.Command("resolvectl", "dns", ifname).Output(); err == nil {
		if _, list, ok := strings.Cut(string(out), ":"); ok {
			if dns := ipv4Re.FindAllString(list, -1); len(dns) > 0 {
				return dns
			}
		}
	}
	var dns []string
	for _, l := range resolvConfLines("nameserver") {
		if net.ParseIP(l).To4() != nil {
			dns = append(dns, l)
		}
	}
	return dns
}

func linkDomain(ifname string) string {
	if out, err := exec.Command("resolvectl", "domain", ifname).Output(); err == nil {
		if _, list, ok := strings.Cut(string(out), ":"); ok {
			for _, d := range strings.Fields(list) {
				if !strings.HasPrefix(d, "~") {
					return d
				}
			}
			return ""
		}
	}
	if s := resolvConfLines("search"); len(s) > 0 {
		return s[0]
	}
	return ""
}

func resolvConfLines(key string) []string {
	raw, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return nil
	}
	var vals []string
	for _, l := range strings.Split(string(raw), "\n") {
		fs := strings.Fields(l)
		if len(fs) >= 2 && fs[0] == key {
			vals = append(vals, fs[1:]...)
		}
	}
	return vals
}

// routeField returns the word after key in `ip route get` output.
func routeField(out, key string) string {
	fs := strings.Fields(out)
	for i := 0; i+1 < len(fs); i++ {
		if fs[i] == key {
			return fs[i+1]
		}
	}
	return ""
}

// connectGSBWifi brings up the GSBWIFI network through NetworkManager.
func connectGSBWifi() (string, error) {
	out, err := exec.Command("nmcli", "connection", "up", "id", "GSBWIFI").CombinedOutput()
	if err != nil {
		// No saved profile yet: connect to the open SSID directly.
		out, err = exec.Command("nmcli", "device", "wifi", "connect", "GSBWIFI").CombinedOutput()
	}
	time.Sleep(1500 * time.Millisecond)
	return strings.TrimSpace(string(out)), err
}

// hiddenCmd exists for the Windows build (no console window); on Linux it is
// a plain exec.Command.
func hiddenCmd(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}
