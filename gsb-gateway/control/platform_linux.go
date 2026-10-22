package main

import (
	_ "embed"
	"os"
	"os/exec"
	"path/filepath"
)

// The StatusNotifierItem tray on Linux decodes PNG, not ICO.
//
//go:embed tray.png
var trayIcon []byte

// defaultDataDir is $XDG_CONFIG_HOME/gsb-gateway (~/.config/gsb-gateway),
// falling back to the exe folder.
func defaultDataDir() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "gsb-gateway")
	}
	return exeDir()
}

// openURL opens a link in the default browser.
func openURL(url string) {
	exec.Command("xdg-open", url).Start()
}
