package main

import (
	"os"
	"os/exec"
	"sync"
	"time"
)

const wiresockExe = `C:\Program Files\WireSock Secure Connect\bin\wiresock-client.exe`

var wiresock struct {
	sync.Mutex
	cmd  *exec.Cmd
	done chan struct{}
}

func wiresockAlive() bool {
	if wiresock.done == nil {
		return false
	}
	select {
	case <-wiresock.done:
		return false
	default:
		return true
	}
}

func setWiresockRunning(v bool) {
	stats.mu.Lock()
	stats.wiresockRunning = v
	stats.mu.Unlock()
}

// toggleWiresock starts/stops wiresock-client with warp_wg.conf.
func toggleWiresock(enable bool) (bool, string) {
	if _, err := os.Stat(wiresockExe); err != nil {
		return false, "WireSock kurulu değil (WARP-Shield dahili mod aktif)."
	}
	conf := dataPath("warp_wg.conf")
	if _, err := os.Stat(conf); err != nil {
		return false, "warp_wg.conf dosyası bulunamadı."
	}
	wiresock.Lock()
	defer wiresock.Unlock()

	if enable {
		if wiresockAlive() {
			return true, "WireSock WARP zaten çalışıyor."
		}
		cmd := hiddenCmd(wiresockExe, "run", "-config", conf, "-log-level", "none")
		if err := cmd.Start(); err != nil {
			return false, err.Error()
		}
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done); setWiresockRunning(false) }()
		wiresock.cmd, wiresock.done = cmd, done
		time.Sleep(800 * time.Millisecond)
		running := wiresockAlive()
		setWiresockRunning(running)
		if running {
			return true, "WireSock Cloudflare WARP tüneli başlatıldı."
		}
		return false, "WireSock başlatılamadı (GSB Wi-Fi UDP portlarını engellediği için TCP WARP-Shield modu aktiftir)."
	}

	if wiresockAlive() {
		wiresock.cmd.Process.Kill()
	}
	hiddenCmd("taskkill", "/F", "/IM", "wiresock-client.exe").Run()
	setWiresockRunning(false)
	return true, "WireSock Cloudflare WARP durduruldu."
}
