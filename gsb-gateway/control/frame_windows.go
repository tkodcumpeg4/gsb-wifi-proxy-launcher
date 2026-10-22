package main

import (
	"sync"
	"unsafe"

	webview "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

// The panel window draws its own title bar: the native caption is removed by
// answering WM_NCCALCSIZE with the whole window as client area, while the
// WS_OVERLAPPEDWINDOW style stays, so DWM keeps the shadow, rounded corners,
// Aero Snap and the minimize/maximize animations. The page (ui.html) draws
// the window buttons and calls the bindings below to drag, resize, minimize,
// maximize and close.

var frame struct {
	sync.Mutex
	oldProc map[windows.HWND]uintptr
}

var frameProc = windows.NewCallback(frameWndProc)

func makeFrameless(wv webview.WebView, hwnd windows.HWND) {
	const gwlpWndProc = -4
	idx := int32(gwlpWndProc)
	frame.Lock()
	if frame.oldProc == nil {
		frame.oldProc = map[windows.HWND]uintptr{}
	}
	old, _, _ := procSetWindowLongPtr.Call(uintptr(hwnd), uintptr(idx), frameProc)
	frame.oldProc[hwnd] = old
	frame.Unlock()

	const swpNoSize, swpNoMove, swpNoZOrder, swpFrameChanged = 0x1, 0x2, 0x4, 0x20
	procSetWindowPos.Call(uintptr(hwnd), 0, 0, 0, 0, 0, swpNoSize|swpNoMove|swpNoZOrder|swpFrameChanged)

	// Hit-test codes for WM_NCLBUTTONDOWN, by the edge names the page sends.
	edges := map[string]uintptr{
		"caption": 2, "w": 10, "e": 11, "n": 12, "nw": 13, "ne": 14, "s": 15, "sw": 16, "se": 17,
	}
	const wmNCLButtonDown, wmClose, wmSysCommand = 0x00A1, 0x0010, 0x0112
	const scMinimize, scMaximize, scRestore = 0xF020, 0xF030, 0xF120
	// Bindings run on the window's UI thread. Releasing the WebView's mouse
	// capture and posting a non-client button-down hands the gesture to the
	// system move/size loop, exactly as if the native frame had been grabbed.
	wv.Bind("gsbWinDrag", func(edge string) {
		ht, ok := edges[edge]
		if !ok {
			return
		}
		procReleaseCapture.Call()
		procSendMessage.Call(uintptr(hwnd), wmNCLButtonDown, ht, 0)
	})
	wv.Bind("gsbWinMinimize", func() {
		procSendMessage.Call(uintptr(hwnd), wmSysCommand, scMinimize, 0)
	})
	wv.Bind("gsbWinToggleMax", func() bool {
		if isZoomed(hwnd) {
			procSendMessage.Call(uintptr(hwnd), wmSysCommand, scRestore, 0)
			return false
		}
		procSendMessage.Call(uintptr(hwnd), wmSysCommand, scMaximize, 0)
		return true
	})
	wv.Bind("gsbWinIsMax", func() bool { return isZoomed(hwnd) })
	wv.Bind("gsbWinClose", func() {
		procPostMessage.Call(uintptr(hwnd), wmClose, 0, 0)
	})
	wv.Init("window.__gsbFrame = true;")
}

func isZoomed(hwnd windows.HWND) bool {
	r, _, _ := procIsZoomed.Call(uintptr(hwnd))
	return r != 0
}

func frameWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	const wmNCCalcSize, wmNCDestroy = 0x0083, 0x0082
	frame.Lock()
	old := frame.oldProc[windows.HWND(hwnd)]
	if msg == wmNCDestroy {
		delete(frame.oldProc, windows.HWND(hwnd))
	}
	frame.Unlock()

	if msg == wmNCCalcSize && wparam != 0 {
		// rgrc[0] is the proposed window rect; leaving it untouched makes the
		// whole window client area. A maximized window is larger than the
		// monitor by its resize border, so pull the client rect back in.
		if isZoomed(windows.HWND(hwnd)) {
			// lparam is an OS-owned NCCALCSIZE_PARAMS*, not Go memory.
			r := *(**windows.Rect)(unsafe.Pointer(&lparam))
			dpi, _, _ := procGetDpiForWindow.Call(hwnd)
			if dpi == 0 {
				dpi = 96
			}
			const smCxFrame, smCyFrame, smCxPaddedBorder = 32, 33, 92
			pad, _, _ := procGetSystemMetricsForDpi.Call(smCxPaddedBorder, dpi)
			cx, _, _ := procGetSystemMetricsForDpi.Call(smCxFrame, dpi)
			cy, _, _ := procGetSystemMetricsForDpi.Call(smCyFrame, dpi)
			bx, by := int32(cx+pad), int32(cy+pad)
			r.Left += bx
			r.Right -= bx
			r.Top += by
			r.Bottom -= by
		}
		return 0
	}
	ret, _, _ := procCallWindowProc.Call(old, hwnd, msg, wparam, lparam)
	return ret
}

var (
	procCallWindowProc         = user32win.NewProc("CallWindowProcW")
	procReleaseCapture         = user32win.NewProc("ReleaseCapture")
	procSendMessage            = user32win.NewProc("SendMessageW")
	procPostMessage            = user32win.NewProc("PostMessageW")
	procIsZoomed               = user32win.NewProc("IsZoomed")
	procGetSystemMetricsForDpi = user32win.NewProc("GetSystemMetricsForDpi")
)
