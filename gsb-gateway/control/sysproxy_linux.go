package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Linux has no single system proxy switch. setSystemProxy covers what desktop
// apps actually read: GNOME's gsettings (also used by Cinnamon, Budgie, MATE
// apps via GIO), KDE's kioslaverc, and HTTP_PROXY & co. through a systemd
// environment.d file, which apps started after the next login inherit.

const noProxyEnv = "localhost,127.0.0.1,10.*,192.168.*"

var proxyEnvVars = []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY"}

func proxyEnvFile() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "environment.d", "90-gsb-gateway-proxy.conf")
}

func setSystemProxy(enable bool) bool {
	host, port, _ := strings.Cut(localProxyAddr, ":")
	ok := false

	if _, err := exec.LookPath("gsettings"); err == nil {
		if enable {
			for _, scheme := range []string{"http", "https"} {
				gsettings("set", "org.gnome.system.proxy."+scheme, "host", host)
				gsettings("set", "org.gnome.system.proxy."+scheme, "port", port)
			}
			gsettings("set", "org.gnome.system.proxy", "ignore-hosts", "['localhost', '127.0.0.0/8', '10.0.0.0/8', '192.168.0.0/16', '::1']")
			ok = gsettings("set", "org.gnome.system.proxy", "mode", "manual") == nil
		} else {
			ok = gsettings("set", "org.gnome.system.proxy", "mode", "none") == nil
		}
	}

	for _, kw := range []string{"kwriteconfig6", "kwriteconfig5"} {
		if _, err := exec.LookPath(kw); err != nil {
			continue
		}
		set := func(key, val string) error {
			return exec.Command(kw, "--file", "kioslaverc", "--group", "Proxy Settings", "--key", key, val).Run()
		}
		if enable {
			set("httpProxy", "http://"+host+" "+port)
			set("httpsProxy", "http://"+host+" "+port)
			set("NoProxyFor", noProxyEnv)
			ok = set("ProxyType", "1") == nil || ok
		} else {
			ok = set("ProxyType", "0") == nil || ok
		}
		break
	}

	url := "http://" + localProxyAddr
	if f := proxyEnvFile(); f != "" {
		if enable {
			var b strings.Builder
			for _, v := range proxyEnvVars {
				val := url
				if v == "NO_PROXY" {
					val = noProxyEnv
				}
				b.WriteString(v + "=" + val + "\n" + strings.ToLower(v) + "=" + val + "\n")
			}
			os.MkdirAll(filepath.Dir(f), 0o755)
			ok = os.WriteFile(f, []byte(b.String()), 0o644) == nil || ok
		} else {
			os.Remove(f)
			ok = true
		}
	}
	for _, v := range proxyEnvVars {
		if enable {
			val := url
			if v == "NO_PROXY" {
				val = noProxyEnv
			}
			os.Setenv(v, val)
		} else {
			os.Unsetenv(v)
		}
	}

	if !ok {
		stats.setError("Sistem proxy hatası: gsettings/kwriteconfig bulunamadı")
		return false
	}
	updateConfig(func(c *Config) { c.SystemProxyEnabled = enable })
	return true
}

func gsettings(args ...string) error {
	return exec.Command("gsettings", args...).Run()
}

type SysProxyStatus struct {
	Enabled bool   `json:"enabled"`
	Server  string `json:"server"`
}

// getSystemProxyStatus reads GNOME's setting when available, else the flag
// this app last stored.
func getSystemProxyStatus() SysProxyStatus {
	if out, err := exec.Command("gsettings", "get", "org.gnome.system.proxy", "mode").Output(); err == nil {
		if strings.Trim(strings.TrimSpace(string(out)), "'") != "manual" {
			return SysProxyStatus{}
		}
		h, _ := exec.Command("gsettings", "get", "org.gnome.system.proxy.http", "host").Output()
		p, _ := exec.Command("gsettings", "get", "org.gnome.system.proxy.http", "port").Output()
		return SysProxyStatus{Enabled: true, Server: strings.Trim(strings.TrimSpace(string(h)), "'") + ":" + strings.TrimSpace(string(p))}
	}
	if getConfig().SystemProxyEnabled {
		return SysProxyStatus{Enabled: true, Server: localProxyAddr}
	}
	return SysProxyStatus{}
}
