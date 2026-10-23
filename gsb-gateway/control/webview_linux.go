//go:build linux && !android

package main

import (
	"os/exec"
	"syscall"
)

// On Linux the panel opens as a Chromium-family app window (its own profile
// under <data>/webview, no tabs or address bar) and falls back to the default
// browser. There is no embedded WebView: webkit2gtk would need cgo.
func openPanel() {
	url := "http://" + webUIAddr
	for _, browser := range []string{
		"chromium", "chromium-browser", "google-chrome", "google-chrome-stable",
		"microsoft-edge", "microsoft-edge-stable", "brave-browser", "vivaldi",
	} {
		path, err := exec.LookPath(browser)
		if err != nil {
			continue
		}
		c := exec.Command(path, "--app="+url, "--window-size=1180,820", "--user-data-dir="+dataPath("webview"))
		c.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // outlive a terminal close
		if c.Start() == nil {
			go c.Wait()
			return
		}
	}
	openURL(url)
}

// openTrayPopup: Linux tray hosts have no anchored popups, so a click opens
// the full panel.
func openTrayPopup() { openPanel() }
