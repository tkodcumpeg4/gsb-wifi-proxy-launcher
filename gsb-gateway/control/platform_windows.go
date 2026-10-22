package main

import (
	_ "embed"
	"os"
	"os/exec"
	"path/filepath"
)

//go:embed tray.ico
var trayIcon []byte

// defaultDataDir is %APPDATA%\GSB Gateway, falling back to the exe folder.
func defaultDataDir() string {
	if ad := os.Getenv("APPDATA"); ad != "" {
		return filepath.Join(ad, "GSB Gateway")
	}
	return exeDir()
}

// openURL opens a link in the default browser.
func openURL(url string) {
	exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}
