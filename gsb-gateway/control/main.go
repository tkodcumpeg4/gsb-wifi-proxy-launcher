// gsb-gateway is the control plane of GSB Gateway: the web panel on
// 127.0.0.1:8765, the HTTP/CONNECT bridge on 127.0.0.1:8880 and the OS
// integration (*_windows.go / *_linux.go). It replaces the Python "Turbo v2" build feature-for-feature
// (see ../docs/BEHAVIOR.md) and supervises the Rust data plane (../core),
// which provides the system-wide TUN mode.
package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	noBrowser := flag.Bool("no-browser", false, "başlangıçta paneli açma")
	noTray := flag.Bool("no-tray", false, "tray simgesi olmadan çalıştır (konsol/servis modu)")
	dir := flag.String("data-dir", "", "ayar/veri klasörü (varsayılan: Windows %APPDATA%\\GSB Gateway, Linux ~/.config/gsb-gateway)")
	withTun := flag.Bool("tun", false, "başlangıçta sanal adaptörü (TUN) aç")
	flag.Parse()

	resourceDir = exeDir()
	baseDir = *dir
	if baseDir == "" {
		baseDir = defaultDataDir()
	}
	baseDir, _ = filepath.Abs(baseDir)
	seedDataDir()
	loadDNSCache()
	loadConfig()
	loadUsage()
	go usagePersistLoop()

	// After a UAC relaunch the old instance may still hold the ports briefly.
	ln := listenRetry(webUIAddr, 10*time.Second)

	go poolWarmerLoop()
	go watchUplink()
	go runLocalBridge()
	go run8888Test()
	go scanAllProxies()

	sig := make(chan os.Signal, 1)
	// Console close / logoff / shutdown arrive as SIGTERM on Windows.
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		shutdown(0)
	}()

	if *withTun || getConfig().TunEnabled {
		go func() {
			time.Sleep(time.Second)
			if err := startTUN(); err != nil {
				fmt.Fprintf(os.Stderr, "[!] TUN başlatılamadı: %v\n", err)
			} else {
				fmt.Println("[+] Sanal adaptör (GSB-TUN) aktif: tüm sistem trafiği proxy üzerinden")
			}
		}()
	}

	srv := &http.Server{Handler: newAPI(), ReadHeaderTimeout: 10 * time.Second}
	fmt.Printf("[+] GSB Kontrol Merkezi arayüzü aktif: http://%s\n", webUIAddr)
	go func() {
		if err := srv.Serve(ln); err != nil {
			fmt.Fprintf(os.Stderr, "[!] Panel durdu: %v\n", err)
			shutdown(1)
		}
	}()
	if !*noBrowser {
		go func() { time.Sleep(800 * time.Millisecond); openPanel() }()
	}

	if *noTray {
		select {} // headless/console mode: run until signalled
	}
	runTray() // blocks on the main goroutine until the user quits
	shutdown(0)
}

func listenRetry(addr string, wait time.Duration) net.Listener {
	deadline := time.Now().Add(wait)
	for {
		ln, err := net.Listen("tcp4", addr)
		if err == nil {
			return ln
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(os.Stderr, "[!] %s açılamadı: %v\n", addr, err)
			os.Exit(1)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// shutdown tears down the TUN adapter (routes are restored by gsb-core)
// before exiting. The persisted tun_enabled flag is left untouched so the
// next start comes back in the same mode.
func shutdown(code int) {
	saveUsage()
	stopTUN()
	os.Exit(code)
}
