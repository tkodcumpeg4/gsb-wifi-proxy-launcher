package main

import (
	"runtime"
	"sync"
	"time"
	"unsafe"

	webview "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

// The tray preview: a small borderless WebView2 window (mini.html) anchored
// to the bottom-right of the work area, next to the tray. It hides when it
// loses focus, like the Wi-Fi / volume flyouts, and is kept alive while hidden
// so the next click shows it instantly. "Detaylı Paneli Aç" opens the full
// panel window (openPanel).
var mini struct {
	mu       sync.Mutex
	wv       webview.WebView
	hwnd     windows.HWND
	active   bool // window exists or is being created
	visible  bool
	hiddenAt time.Time
}

const (
	miniWidth  = 360 // DIPs
	miniHeight = 540
	miniMargin = 12
)

func openTrayPopup() {
	mini.mu.Lock()
	if !mini.active {
		mini.active = true
		mini.mu.Unlock()
		go runMiniWindow()
		return
	}
	wv, visible, hiddenAt := mini.wv, mini.visible, mini.hiddenAt
	mini.mu.Unlock()
	if wv == nil {
		return // still being created
	}
	// Clicking the tray icon while the flyout is open first blurs it (which
	// hides it); that same click must not show it again.
	if visible || time.Since(hiddenAt) < 400*time.Millisecond {
		wv.Dispatch(hideMini)
		return
	}
	wv.Dispatch(showMini)
}

func runMiniWindow() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer func() {
		failed := recover() != nil
		mini.mu.Lock()
		mini.wv, mini.hwnd, mini.active, mini.visible = nil, 0, false, false
		mini.mu.Unlock()
		if failed {
			openPanel() // no WebView2: the panel has its own browser fallback
		}
	}()

	wv := webview.NewWithOptions(webview.WebViewOptions{
		AutoFocus: true,
		DataPath:  dataPath("webview"),
		WindowOptions: webview.WindowOptions{
			Title:  "GSB Proxy",
			Width:  miniWidth,
			Height: miniHeight,
			IconId: 1,
		},
	})
	if wv == nil {
		panic("webview init failed")
	}
	hwnd := windows.HWND(wv.Window())
	makeFlyout(hwnd)
	wv.Bind("gsbHide", hideMini)
	wv.Bind("gsbOpenPanel", func() {
		hideMini()
		openPanel()
	})

	mini.mu.Lock()
	mini.wv, mini.hwnd = wv, hwnd
	mini.mu.Unlock()

	wv.Navigate("http://" + webUIAddr + "/mini")
	showMini()
	wv.Run() // until the window is destroyed (e.g. Alt+F4)
	wv.Destroy()
}

// makeFlyout strips the caption and taskbar button and asks DWM for rounded
// corners (Windows 11; ignored on 10).
func makeFlyout(hwnd windows.HWND) {
	var gwlStyle, gwlExStyle int32 = -16, -20 // negative indexes, passed sign-extended
	const (
		wsPopup        = 0x80000000
		wsClipChildren = 0x02000000
		wsExToolWindow = 0x00000080
		wsExTopmost    = 0x00000008
	)
	procSetWindowLongPtr.Call(uintptr(hwnd), uintptr(gwlStyle), wsPopup|wsClipChildren)
	procSetWindowLongPtr.Call(uintptr(hwnd), uintptr(gwlExStyle), wsExToolWindow|wsExTopmost)
	const dwmwaWindowCornerPreference, dwmwcpRound = 33, 2
	pref := uint32(dwmwcpRound)
	procDwmSetWindowAttribute.Call(uintptr(hwnd), dwmwaWindowCornerPreference, uintptr(unsafe.Pointer(&pref)), 4)
}

// showMini runs on the window's UI thread.
func showMini() {
	mini.mu.Lock()
	hwnd, wv := mini.hwnd, mini.wv
	mini.mu.Unlock()
	if hwnd == 0 {
		return
	}
	dpi := uint32(96)
	if r, _, _ := procGetDpiForWindow.Call(uintptr(hwnd)); r != 0 {
		dpi = uint32(r)
	}
	scale := func(v int) int32 { return int32(v * int(dpi) / 96) }
	w, h, m := scale(miniWidth), scale(miniHeight), scale(miniMargin)
	var wa windows.Rect
	const spiGetWorkArea = 0x0030
	procSystemParametersInfo.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&wa)), 0)
	const (
		hwndTopmost    = ^uintptr(0) // (HWND)-1
		swpFrameChange = 0x0020
		swpShowWindow  = 0x0040
	)
	procSetWindowPos.Call(uintptr(hwnd), hwndTopmost,
		uintptr(wa.Right-w-m), uintptr(wa.Bottom-h-m), uintptr(w), uintptr(h),
		swpFrameChange|swpShowWindow)
	procSetForeground.Call(uintptr(hwnd))
	if wv != nil { // AutoFocus moves focus into the page on activation, so blur fires on deactivation
		wv.Eval("typeof poll === 'function' && poll()")
	}
	mini.mu.Lock()
	mini.visible = true
	mini.mu.Unlock()
}

// hideMini runs on the window's UI thread (bound to JS as gsbHide).
func hideMini() {
	mini.mu.Lock()
	defer mini.mu.Unlock()
	if mini.hwnd == 0 || !mini.visible {
		return
	}
	const swHide = 0
	procShowWindow.Call(uintptr(mini.hwnd), swHide)
	mini.visible, mini.hiddenAt = false, time.Now()
}

var (
	procSetWindowLongPtr      = user32win.NewProc("SetWindowLongPtrW")
	procSetWindowPos          = user32win.NewProc("SetWindowPos")
	procGetDpiForWindow       = user32win.NewProc("GetDpiForWindow")
	procSystemParametersInfo  = user32win.NewProc("SystemParametersInfoW")
	procDwmSetWindowAttribute = windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmSetWindowAttribute")
)
