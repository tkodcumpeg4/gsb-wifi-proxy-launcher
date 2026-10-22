package main

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func findWifiAdapter() *WifiInfo {
	return findAdapter(func(a *windows.IpAdapterAddresses, name, desc, suffix string) bool {
		return a.IfType == windows.IF_TYPE_IEEE80211 ||
			strings.Contains(name, "Wi-Fi") ||
			strings.Contains(strings.ToLower(desc), "wireless") ||
			strings.Contains(strings.ToLower(suffix), "kykwifi")
	})
}

// findUplink returns the adapter Windows would use to reach dst (Wi-Fi on
// GSB, but also Ethernet), ignoring our own GSB-TUN adapter.
func findUplink(dst string) *WifiInfo {
	ip := net.ParseIP(dst).To4()
	if ip == nil {
		return nil
	}
	sa := &windows.SockaddrInet4{}
	copy(sa.Addr[:], ip)
	var idx uint32
	if windows.GetBestInterfaceEx(sa, &idx) != nil {
		return nil
	}
	return findAdapter(func(a *windows.IpAdapterAddresses, name, _, _ string) bool {
		return a.IfIndex == idx && name != "GSB-TUN"
	})
}

func findAdapter(match func(a *windows.IpAdapterAddresses, name, desc, suffix string) bool) *WifiInfo {
	size := uint32(16 * 1024)
	var buf []byte
	for range 3 {
		buf = make([]byte, size)
		err := windows.GetAdaptersAddresses(windows.AF_INET, windows.GAA_FLAG_INCLUDE_GATEWAYS, 0,
			(*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])), &size)
		if err == nil {
			break
		}
		if err != windows.ERROR_BUFFER_OVERFLOW {
			return nil
		}
		buf = nil
	}
	if buf == nil {
		return nil
	}
	for a := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])); a != nil; a = a.Next {
		if a.OperStatus != windows.IfOperStatusUp {
			continue
		}
		name := windows.UTF16PtrToString(a.FriendlyName)
		desc := windows.UTF16PtrToString(a.Description)
		suffix := windows.UTF16PtrToString(a.DnsSuffix)
		if !match(a, name, desc, suffix) {
			continue
		}
		ip := firstIPv4(a.FirstUnicastAddress)
		if ip == nil {
			continue
		}
		info := &WifiInfo{
			Connected:   true,
			IP:          ip.String(),
			DNSSuffix:   suffix,
			AdapterName: name,
			IfIndex:     a.IfIndex,
		}
		for d := a.FirstDnsServerAddress; d != nil; d = d.Next {
			if dip := sockaddrIPv4(d.Address); dip != nil {
				info.DNSServers = append(info.DNSServers, dip.String())
			}
		}
		for g := a.FirstGatewayAddress; g != nil; g = g.Next {
			if gip := sockaddrIPv4(g.Address); gip != nil {
				info.Gateway = gip.String()
				break
			}
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

func firstIPv4(u *windows.IpAdapterUnicastAddress) net.IP {
	for ; u != nil; u = u.Next {
		if ip := sockaddrIPv4(u.Address); ip != nil {
			return ip
		}
	}
	return nil
}

func sockaddrIPv4(sa windows.SocketAddress) net.IP {
	if sa.Sockaddr == nil || sa.Sockaddr.Addr.Family != windows.AF_INET {
		return nil
	}
	in := (*windows.RawSockaddrInet4)(unsafe.Pointer(sa.Sockaddr))
	return net.IPv4(in.Addr[0], in.Addr[1], in.Addr[2], in.Addr[3])
}

// connectGSBWifi runs `netsh wlan connect name=GSBWIFI` and returns its output.
func connectGSBWifi() (string, error) {
	out, err := hiddenCmd("netsh", "wlan", "connect", "name=GSBWIFI").CombinedOutput()
	time.Sleep(1500 * time.Millisecond)
	return decodeOEM(out), err
}

func hiddenCmd(name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	return c
}

// decodeOEM converts console output (OEM code page, e.g. 857 on Turkish
// Windows) to UTF-8 so netsh messages render correctly in the panel.
func decodeOEM(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	cp := getOEMCP()
	n, err := windows.MultiByteToWideChar(cp, 0, &b[0], int32(len(b)), nil, 0)
	if err != nil || n == 0 {
		return string(b)
	}
	w := make([]uint16, n)
	windows.MultiByteToWideChar(cp, 0, &b[0], int32(len(b)), &w[0], n)
	return strings.TrimSpace(windows.UTF16ToString(w))
}

var procGetOEMCP = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetOEMCP")

func getOEMCP() uint32 {
	r, _, _ := procGetOEMCP.Call()
	return uint32(r)
}
