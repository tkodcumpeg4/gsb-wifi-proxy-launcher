package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

// TUN mode: gsb-core.exe (Rust) owns the Wintun adapter and sends every TCP
// flow to our bridge on 127.0.0.1:8880, so WARP-Shield applies system-wide.

type TunStatus struct {
	Running   bool   `json:"running"`
	Starting  bool   `json:"starting"`
	Adapter   string `json:"adapter"`
	TunIndex  int    `json:"tun_index"`
	UDP       bool   `json:"udp"`
	TxBytes   uint64 `json:"tx_bytes"`
	RxBytes   uint64 `json:"rx_bytes"`
	LastError string `json:"last_error"`
	Since     int64  `json:"since"`
	Uplink    string `json:"uplink"`
}

var tunCtl struct {
	sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	done   chan struct{}
	status TunStatus
	// uplinkKey identifies the interface/gateway the routes were built for.
	uplinkKey   string
	uplinkIfIdx uint32
}

var errNeedAdmin = errors.New("Sanal adaptör için yönetici yetkisi gerekli")

func isElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

func tunStatus() TunStatus {
	tunCtl.Lock()
	defer tunCtl.Unlock()
	return tunCtl.status
}

func coreBinaries() (core, wintun string, err error) {
	// Prefer the embedded copies extracted to <data>\bin (single-exe build).
	if c, w, e := extractBundled(); e == nil && fileExists(c) && fileExists(w) {
		return c, w, nil
	}
	// Fallback: binaries sitting next to the exe or in the data dir.
	for _, dir := range []string{baseDir, exeDir()} {
		c, w := filepath.Join(dir, "gsb-core.exe"), filepath.Join(dir, "wintun.dll")
		if fileExists(c) && fileExists(w) {
			return c, w, nil
		}
	}
	return "", "", errors.New("gsb-core.exe/wintun.dll çıkarılamadı veya bulunamadı")
}

func exeDir() string {
	exe, _ := os.Executable()
	return filepath.Dir(exe)
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func startTUN() error {
	if !isElevated() {
		return errNeedAdmin
	}
	tunCtl.Lock()
	if tunCtl.done != nil {
		tunCtl.Unlock()
		return nil
	}
	tunCtl.status = TunStatus{Starting: true}
	tunCtl.Unlock()

	fail := func(err error) error {
		tunCtl.Lock()
		tunCtl.status = TunStatus{LastError: err.Error()}
		tunCtl.Unlock()
		return err
	}

	core, wintun, err := coreBinaries()
	if err != nil {
		return fail(err)
	}
	_, _, proxyIP := activeProxy()
	if proxyIP == "" {
		return fail(errProxyDNS)
	}
	wifi := pickUplink(proxyIP)
	if wifi == nil || wifi.Gateway == "" || wifi.IfIndex == 0 {
		return fail(errors.New("proxy'ye giden ağ arayüzü veya ağ geçidi bulunamadı"))
	}

	args := []string{
		"--bridge", localProxyAddr,
		"--wifi-if", fmt.Sprint(wifi.IfIndex),
		"--wifi-gw", wifi.Gateway,
		"--bypass", proxyIP + "/32",
		"--wintun", wintun,
	}
	for _, d := range wifi.DNSServers {
		if hijackableDNS(d, wifi.IP) {
			args = append(args, "--dns-hijack", d)
		}
	}
	if gw := strings.TrimSpace(getConfig().UDPGWServer); gw != "" {
		args = append(args, "--udpgw", gw)
	}

	cmd := hiddenCmd(core, args...)
	if lvl := strings.TrimSpace(getConfig().CoreLog); lvl != "" {
		// e.g. "debug" or "warn,tun2proxy=debug" — written to gsb-core.log.
		cmd.Env = append(os.Environ(), "RUST_LOG="+lvl)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fail(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fail(err)
	}
	logf, _ := os.OpenFile(dataPath("gsb-core.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if logf != nil {
		cmd.Stderr = logf
	}
	if err := cmd.Start(); err != nil {
		return fail(err)
	}

	done := make(chan struct{})
	ready := make(chan error, 1)
	tunCtl.Lock()
	tunCtl.cmd, tunCtl.stdin, tunCtl.done = cmd, stdin, done
	tunCtl.Unlock()

	go readCoreEvents(stdout, ready)
	go func() {
		err := cmd.Wait()
		if logf != nil {
			logf.Close()
		}
		tunCtl.Lock()
		st := tunCtl.status
		if st.LastError == "" && err != nil && st.Running {
			st.LastError = "gsb-core beklenmedik şekilde kapandı: " + err.Error()
		}
		st.Running, st.Starting = false, false
		tunCtl.status = st
		tunCtl.cmd, tunCtl.stdin, tunCtl.done = nil, nil, nil
		tunCtl.Unlock()
		close(done)
		select {
		case ready <- errors.New("gsb-core başlatılamadı"):
		default:
		}
	}()

	select {
	case err := <-ready:
		if err != nil {
			<-done
			if st := tunStatus(); st.LastError != "" {
				return errors.New(st.LastError)
			}
			return fail(err)
		}
		tunCtl.Lock()
		tunCtl.status.Uplink = fmt.Sprintf("%s (%s → %s)", wifi.AdapterName, wifi.IP, wifi.Gateway)
		tunCtl.uplinkKey = uplinkKey(wifi)
		tunCtl.uplinkIfIdx = wifi.IfIndex
		tunCtl.Unlock()
		if getConfig().ForceTun {
			applyForceTun(wifi.IfIndex)
		}
		updateConfig(func(c *Config) { c.TunEnabled = true })
		return nil
	case <-time.After(20 * time.Second):
		stopTUN()
		return fail(errors.New("gsb-core 20 sn içinde hazır olmadı"))
	}
}

func readCoreEvents(r io.Reader, ready chan<- error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		var ev struct {
			Event    string `json:"event"`
			Message  string `json:"message"`
			TunName  string `json:"tun_name"`
			TunIndex int    `json:"tun_index"`
			UDP      bool   `json:"udp"`
			Tx       uint64 `json:"tx"`
			Rx       uint64 `json:"rx"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		tunCtl.Lock()
		switch ev.Event {
		case "ready":
			tunCtl.status = TunStatus{Running: true, Adapter: ev.TunName, TunIndex: ev.TunIndex, UDP: ev.UDP, Since: time.Now().Unix()}
			select {
			case ready <- nil:
			default:
			}
		case "traffic":
			tunCtl.status.TxBytes, tunCtl.status.RxBytes = ev.Tx, ev.Rx
		case "error":
			tunCtl.status.LastError = ev.Message
			stats.setError("TUN: " + ev.Message)
		}
		tunCtl.Unlock()
	}
}

// stopTUN asks gsb-core to restore routes and exit, killing it after 6 s.
func stopTUN() {
	clearForceTun()
	tunCtl.Lock()
	cmd, stdin, done := tunCtl.cmd, tunCtl.stdin, tunCtl.done
	tunCtl.Unlock()
	if done == nil {
		return
	}
	stdin.Write([]byte("stop\n"))
	stdin.Close()
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		cmd.Process.Kill()
		<-done
	}
}

func disableTUN() {
	stopTUN()
	updateConfig(func(c *Config) { c.TunEnabled = false })
}

// setForceTun stores the preference and applies/clears it live if TUN is up.
func setForceTun(enable bool) {
	updateConfig(func(c *Config) { c.ForceTun = enable })
	tunCtl.Lock()
	running, idx := tunCtl.status.Running, tunCtl.uplinkIfIdx
	tunCtl.Unlock()
	if !running {
		return
	}
	if enable {
		applyForceTun(idx)
	} else {
		clearForceTun()
	}
}

// relaunchElevated restarts GSB Gateway through UAC ("runas") and returns
// true if the elevated copy was started; the caller then exits.
func relaunchElevated(extra ...string) bool {
	exe, _ := os.Executable()
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	params, _ := windows.UTF16PtrFromString(strings.Join(append([]string{"--no-browser", "--data-dir", `"` + baseDir + `"`}, extra...), " "))
	dir, _ := windows.UTF16PtrFromString(exeDir())
	return windows.ShellExecute(0, verb, file, params, dir, windows.SW_SHOWNORMAL) == nil
}

// pickUplink prefers GSB Wi-Fi when bind_to_wifi is on; otherwise (or when
// Wi-Fi is down) it uses whichever adapter currently routes to the proxy.
func pickUplink(proxyIP string) *WifiInfo {
	if w := getWifiInfo(true); getConfig().BindToWifi && w.Connected && w.Gateway != "" {
		return &w
	}
	return findUplink(proxyIP)
}

func uplinkKey(w *WifiInfo) string {
	if w == nil {
		return ""
	}
	return fmt.Sprintf("%d|%s|%s", w.IfIndex, w.IP, w.Gateway)
}

// hijackableDNS reports whether a resolver address can safely be routed into
// the tunnel. Loopback resolvers (e.g. a local DNS proxy on 127.0.0.1) and
// link-local addresses must stay where they are, or the core's own
// connection to the bridge on 127.0.0.1 would loop.
func hijackableDNS(addr, uplinkIP string) bool {
	ip := net.ParseIP(addr)
	if ip == nil || ip.To4() == nil || addr == uplinkIP {
		return false
	}
	return !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified()
}

// watchUplink restarts TUN when the network it was built on goes away or
// changes (e.g. moving from Ethernet to GSB Wi-Fi, or a phone tether
// appearing). Without this the routes keep pointing at a dead gateway.
func watchUplink() {
	for range time.Tick(5 * time.Second) {
		tunCtl.Lock()
		running, key := tunCtl.status.Running, tunCtl.uplinkKey
		tunCtl.Unlock()
		if !running || key == "" {
			continue
		}
		_, _, proxyIP := activeProxy()
		if proxyIP == "" {
			continue
		}
		now := uplinkKey(pickUplink(proxyIP))
		if now == "" || now == key {
			continue
		}
		fmt.Printf("[*] Ağ değişti (%s → %s), sanal adaptör yeniden kuruluyor\n", key, now)
		stopTUN()
		if err := startTUN(); err != nil {
			stats.setError("TUN yeniden kurulamadı: " + err.Error())
		}
	}
}
