package main

import (
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	webview "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

const panelWindowTitle = "GSB Proxy Kontrol Paneli"

// The panel is shown in a single embedded WebView2 window owned by this
// process, so it behaves like a native app and never multiplies: a second
// request just brings the existing window to the front. If the WebView2
// runtime is missing, it falls back to an Edge/Chrome app window.
var panel struct {
	mu     sync.Mutex
	wv     webview.WebView
	hwnd   windows.HWND
	active bool
}

func openPanel() {
	panel.mu.Lock()
	if panel.active {
		// A window is open or still being created — never spawn a second one;
		// bring the existing one forward once it is ready.
		wv, hwnd := panel.wv, panel.hwnd
		panel.mu.Unlock()
		if wv != nil {
			wv.Dispatch(func() { focusWindow(hwnd) })
		}
		return
	}
	panel.active = true
	panel.mu.Unlock()
	go runPanelWindow()
}

func runPanelWindow() {
	// WebView2's window and message loop must live on one dedicated OS thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	defer func() {
		closing := recover() != nil
		panel.mu.Lock()
		panel.wv, panel.hwnd, panel.active = nil, 0, false
		panel.mu.Unlock()
		if closing {
			// No WebView2 runtime (or it failed): fall back to a browser window.
			openPanelBrowser()
		}
	}()

	wv := webview.NewWithOptions(webview.WebViewOptions{
		AutoFocus: true,
		DataPath:  dataPath("webview"),
		WindowOptions: webview.WindowOptions{
			Title:  panelWindowTitle,
			Width:  1180,
			Height: 820,
			Center: true,
			IconId: 1, // app icon from rsrc_windows.syso
		},
	})
	if wv == nil {
		panic("webview init failed")
	}
	hwnd := windows.HWND(wv.Window())
	darkTitleBar(hwnd)
	makeFrameless(wv, hwnd) // before Navigate: it installs the page bindings
	panel.mu.Lock()
	panel.wv, panel.hwnd = wv, hwnd
	panel.mu.Unlock()

	wv.Navigate("http://" + webUIAddr)
	wv.Run() // blocks until the window is closed
	wv.Destroy()
}

var (
	user32win         = windows.NewLazySystemDLL("user32.dll")
	procShowWindow    = user32win.NewProc("ShowWindow")
	procSetForeground = user32win.NewProc("SetForegroundWindow")
)

// darkTitleBar paints the caption in the panel's background (#080b14) so the
// window chrome blends into the page. Windows 11 honours the colours; Windows
// 10 (1809+) still gets the dark-mode caption.
func darkTitleBar(h windows.HWND) {
	const (
		dwmwaUseImmersiveDarkMode = 20
		dwmwaBorderColor          = 34
		dwmwaCaptionColor         = 35
		dwmwaTextColor            = 36
	)
	set := func(attr uintptr, v uint32) {
		procDwmSetWindowAttribute.Call(uintptr(h), attr, uintptr(unsafe.Pointer(&v)), 4)
	}
	set(dwmwaUseImmersiveDarkMode, 1)
	set(dwmwaCaptionColor, 0x00140b08) // COLORREF is 0x00BBGGRR
	set(dwmwaBorderColor, 0x00140b08)
	set(dwmwaTextColor, 0x00f2d7cf) // --fg2 #cfd7f2
}

func focusWindow(h windows.HWND) {
	if h == 0 {
		return
	}
	const swRestore = 9
	procShowWindow.Call(uintptr(h), swRestore)
	procSetForeground.Call(uintptr(h))
}

// openPanelBrowser is the fallback when WebView2 is unavailable.
func openPanelBrowser() {
	url := "http://" + webUIAddr
	profile := dataPath("webview")
	for _, browser := range []string{
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
	} {
		if fileExists(browser) {
			c := exec.Command(browser, "--app="+url, "--window-size=1180,820", "--user-data-dir="+profile)
			c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS}
			if c.Start() == nil {
				return
			}
		}
	}
	exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}
