package main

import "os"

// The app starts this binary with --data-dir set to its private files
// directory; GSB_DATA_DIR is the fallback.
func defaultDataDir() string {
	if d := os.Getenv("GSB_DATA_DIR"); d != "" {
		return d
	}
	return exeDir()
}

// openURL is handled by the app (window.GSB.openUrl); nothing to do here.
func openURL(url string) {}

// The panel lives in the app's WebView; there is no tray or browser window.
func openPanel()     {}
func openTrayPopup() {}

func runTray() { select {} }
