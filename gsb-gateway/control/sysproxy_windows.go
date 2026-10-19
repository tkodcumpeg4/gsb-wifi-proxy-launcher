package main

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	inetSettingsKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`
	proxyOverride   = "localhost;127.*;10.*;192.168.*;<local>"
	noProxyEnv      = "localhost,127.0.0.1,10.*,192.168.*"
)

var (
	wininet            = windows.NewLazySystemDLL("wininet.dll")
	procInternetSetOpt = wininet.NewProc("InternetSetOptionW")
	user32             = windows.NewLazySystemDLL("user32.dll")
	procSendMsgTimeout = user32.NewProc("SendMessageTimeoutW")
)

var proxyEnvVars = []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY"}

// setSystemProxy flips WinINet, user environment and WinHTTP proxy settings,
// exactly like set_windows_system_proxy in the Python build.
func setSystemProxy(enable bool) bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, inetSettingsKey, registry.SET_VALUE)
	if err != nil {
		stats.setError("Sistem proxy hatası: " + err.Error())
		return false
	}
	if enable {
		k.SetDWordValue("ProxyEnable", 1)
		k.SetStringValue("ProxyServer", localProxyAddr)
		k.SetStringValue("ProxyOverride", proxyOverride)
	} else {
		k.SetDWordValue("ProxyEnable", 0)
		k.SetStringValue("ProxyServer", "")
	}
	k.Close()
	procInternetSetOpt.Call(0, 39, 0, 0) // INTERNET_OPTION_SETTINGS_CHANGED
	procInternetSetOpt.Call(0, 37, 0, 0) // INTERNET_OPTION_REFRESH

	if ek, err := registry.OpenKey(registry.CURRENT_USER, "Environment", registry.SET_VALUE); err == nil {
		url := "http://" + localProxyAddr
		for _, v := range proxyEnvVars {
			if enable {
				val := url
				if v == "NO_PROXY" {
					val = noProxyEnv
				}
				ek.SetStringValue(v, val)
				os.Setenv(v, val)
			} else {
				ek.DeleteValue(v)
				os.Unsetenv(v)
			}
		}
		ek.Close()
		env, _ := windows.UTF16PtrFromString("Environment")
		var result uintptr
		// HWND_BROADCAST, WM_SETTINGCHANGE, SMTO_ABORTIFHUNG, 1000 ms
		procSendMsgTimeout.Call(0xFFFF, 0x1A, 0, uintptr(unsafe.Pointer(env)), 2, 1000, uintptr(unsafe.Pointer(&result)))
	}

	if enable {
		hiddenCmd("netsh", "winhttp", "set", "proxy", localProxyAddr, "localhost;127.*;10.*;192.168.*").Run()
	} else {
		hiddenCmd("netsh", "winhttp", "reset", "proxy").Run()
	}

	updateConfig(func(c *Config) { c.SystemProxyEnabled = enable })
	return true
}

type SysProxyStatus struct {
	Enabled bool   `json:"enabled"`
	Server  string `json:"server"`
}

func getSystemProxyStatus() SysProxyStatus {
	k, err := registry.OpenKey(registry.CURRENT_USER, inetSettingsKey, registry.QUERY_VALUE)
	if err != nil {
		return SysProxyStatus{}
	}
	defer k.Close()
	en, _, _ := k.GetIntegerValue("ProxyEnable")
	srv, _, _ := k.GetStringValue("ProxyServer")
	return SysProxyStatus{Enabled: en != 0, Server: srv}
}
