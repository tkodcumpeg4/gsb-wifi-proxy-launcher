package main

import (
	_ "embed"
	"time"

	"fyne.io/systray"
)

//go:embed tray.ico
var trayIcon []byte

// runTray shows the system-tray icon and menu and blocks until the user quits.
// Left-click opens the panel as an app window; right-click shows the menu.
func runTray() {
	systray.Run(onTrayReady, func() {})
}

func onTrayReady() {
	systray.SetIcon(trayIcon)
	systray.SetTooltip("GSB Gateway")
	systray.SetOnTapped(func() { openPanel() })

	mOpen := systray.AddMenuItem("Paneli Aç", "Ayar penceresini aç")
	systray.AddSeparator()
	mTun := systray.AddMenuItemCheckbox("Sanal Adaptör (TUN)", "Tüm trafiği proxy üzerinden yönlendir", false)
	mForce := systray.AddMenuItemCheckbox("Trafiği TUN'a Zorla", "Tünel açıkken Wi-Fi'ye düşmeyi engelle", false)
	mSys := systray.AddMenuItemCheckbox("Sistem Proxy", "Tarayıcılar için sistem proxy'si", false)
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Çıkış", "GSB Gateway'i kapat")

	// Reflect live state on the menu every few seconds.
	go func() {
		for {
			t := tunStatus()
			setCheck(mTun, t.Running)
			setCheck(mForce, getConfig().ForceTun)
			setCheck(mSys, getSystemProxyStatus().Enabled)
			if t.Running {
				mTun.SetTitle("Sanal Adaptör (TUN): Açık")
			} else {
				mTun.SetTitle("Sanal Adaptör (TUN)")
			}
			time.Sleep(3 * time.Second)
		}
	}()

	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				openPanel()
			case <-mTun.ClickedCh:
				if tunStatus().Running {
					disableTUN()
				} else if err := startTUN(); err == errNeedAdmin {
					// TUN needs admin: relaunch elevated and hand over.
					if relaunchElevated("--tun") {
						shutdown(0)
					}
				}
			case <-mForce.ClickedCh:
				setForceTun(!getConfig().ForceTun)
			case <-mSys.ClickedCh:
				setSystemProxy(!getSystemProxyStatus().Enabled)
			case <-mQuit.ClickedCh:
				systray.Quit()
				shutdown(0)
			}
		}
	}()
}

func setCheck(m *systray.MenuItem, on bool) {
	if on && !m.Checked() {
		m.Check()
	} else if !on && m.Checked() {
		m.Uncheck()
	}
}
