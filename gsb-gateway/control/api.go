package main

import (
	_ "embed"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

//go:embed web/ui.html
var embeddedUI []byte

//go:embed web/mini.html
var embeddedMini []byte

func newAPI() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", serveUI)
	mux.HandleFunc("GET /index.html", serveUI)
	mux.HandleFunc("GET /mini", serveMini)
	mux.HandleFunc("GET /jsqr.js", serveJSQR)
	mux.HandleFunc("POST /api/transfer/export", apiTransferExport)
	mux.HandleFunc("POST /api/transfer/import", apiTransferImport)
	mux.HandleFunc("GET /api/status", apiStatus)
	mux.HandleFunc("POST /api/proxy/select", apiProxySelect)
	mux.HandleFunc("POST /api/proxy/add", apiProxyAdd)
	mux.HandleFunc("POST /api/proxy/scan", func(w http.ResponseWriter, r *http.Request) {
		go scanAllProxies()
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/test/8888", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "diag": run8888Test()})
	})
	mux.HandleFunc("POST /api/test/speed", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, runSpeedTest())
	})
	mux.HandleFunc("POST /api/warp/toggle", apiWarpToggle)
	mux.HandleFunc("POST /api/sysproxy/toggle", func(w http.ResponseWriter, r *http.Request) {
		var p struct{ Enable bool }
		readJSON(r, &p)
		ok := setSystemProxy(p.Enable)
		writeJSON(w, 200, map[string]any{"ok": ok, "sys_proxy": getSystemProxyStatus()})
	})
	mux.HandleFunc("POST /api/wifi/options", apiWifiOptions)
	mux.HandleFunc("POST /api/wifi/bind", apiWifiOptions)
	mux.HandleFunc("POST /api/wifi/connect", func(w http.ResponseWriter, r *http.Request) {
		out, err := connectGSBWifi()
		writeJSON(w, 200, map[string]any{"ok": err == nil, "output": out, "wifi": getWifiInfo(true)})
	})
	mux.HandleFunc("POST /api/tun/toggle", apiTunToggle)
	mux.HandleFunc("POST /api/tun/force", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Enable bool `json:"enable"`
		}
		readJSON(r, &p)
		setForceTun(p.Enable)
		writeJSON(w, 200, map[string]any{"ok": true, "force_tun": p.Enable})
	})
	mux.HandleFunc("POST /api/tun/udpgw", apiRelaySet)
	mux.HandleFunc("POST /api/relay/test", func(w http.ResponseWriter, r *http.Request) {
		ms, err := testRelay()
		if err != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "latency_ms": ms})
	})
	mux.HandleFunc("POST /api/open", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			URL string `json:"url"`
		}
		readJSON(r, &p)
		// Only open vetted https links in the default browser.
		if strings.HasPrefix(p.URL, "https://github.com/") {
			openURL(p.URL)
			writeJSON(w, 200, map[string]any{"ok": true})
			return
		}
		writeJSON(w, 400, map[string]any{"ok": false})
	})
	mux.HandleFunc("POST /api/app/quit", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true})
		go func() {
			time.Sleep(200 * time.Millisecond)
			shutdown(0)
		}()
	})
	mux.HandleFunc("POST /api/app/elevate", func(w http.ResponseWriter, r *http.Request) {
		if isElevated() {
			writeJSON(w, 200, map[string]any{"ok": true, "already": true})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
		go func() {
			time.Sleep(300 * time.Millisecond)
			if relaunchElevated("--tun") {
				shutdown(0)
			}
		}()
	})
	return localOnly(mux)
}

func apiTunToggle(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Enable bool `json:"enable"`
	}
	readJSON(r, &p)
	if !p.Enable {
		disableTUN()
		writeJSON(w, 200, map[string]any{"ok": true, "tun": tunStatus()})
		return
	}
	if err := startTUN(); err != nil {
		writeJSON(w, 200, map[string]any{
			"ok":         false,
			"error":      err.Error(),
			"need_admin": err == errNeedAdmin,
			"tun":        tunStatus(),
		})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "tun": tunStatus()})
}

// localOnly rejects requests from other origins or rebinding host names, so
// a random web page cannot drive the panel API through the browser.
func localOnly(next http.Handler) http.Handler {
	allowedHost := func(h string) bool {
		return h == webUIAddr || h == "localhost"+webUIAddr[strings.LastIndex(webUIAddr, ":"):]
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && !allowedHost(strings.TrimPrefix(o, "http://")) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func serveUI(w http.ResponseWriter, r *http.Request) {
	// Prefer a ui.html next to the exe (lets the panel be tweaked without a
	// rebuild); otherwise serve the copy embedded in the binary.
	content, err := os.ReadFile(resourcePath("ui.html"))
	if err != nil {
		content = embeddedUI
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(content)
}

// serveMini is the small tray preview page (mini.html), overridable from disk
// like ui.html.
func serveMini(w http.ResponseWriter, r *http.Request) {
	content, err := os.ReadFile(resourcePath("mini.html"))
	if err != nil {
		content = embeddedMini
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(content)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	body, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(body)
}

func readJSON(r *http.Request, v any) {
	json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(v)
}

func apiStatus(w http.ResponseWriter, r *http.Request) {
	c := getConfig()
	activeKey := c.activeKey()
	user, pass := credentialsFor("", 0)

	proxies := parseProxyList(false)
	cfgMu.RLock()
	for i := range proxies {
		p := &proxies[i]
		p.Scan = stats.scanResult(p.Key)
		p.IsActive = p.Key == activeKey
		cr := cfg.ProxyCredentials[p.Key]
		p.HasSavedCreds = cr != nil && cr.Username != ""
	}
	cfgMu.RUnlock()

	writeJSON(w, 200, map[string]any{
		"wifi": getWifiInfo(false),
		"config": map[string]any{
			"active_proxy_host":    c.ActiveProxyHost,
			"active_proxy_port":    c.ActiveProxyPort,
			"active_proxy_uni":     c.ActiveProxyUni,
			"active_proxy_ip":      cachedProxyIP(c.ActiveProxyHost),
			"username":             user,
			"has_password":         pass != "",
			"warp_shield_enabled":  c.WarpShieldEnabled,
			"ip_connect_mode":      c.IPConnectMode,
			"tls_fragment_enabled": c.TLSFragmentEnabled,
			"tls_fragment_mode":    c.TLSFragmentMode,
			"bind_to_wifi":         c.BindToWifi,
			"local_bridge":         localProxyAddr,
		},
		"sys_proxy": getSystemProxyStatus(),
		"tun":       tunStatus(),
		"is_admin":  isElevated(),
		"force_tun": c.ForceTun,
		"udpgw":     c.UDPGWServer,
		"relay": map[string]any{
			"server":      c.UDPGWServer,
			"has_token":   c.RelayToken != "",
			"fingerprint": c.RelayFingerprint,
		},
		"stats":   stats.snapshot(),
		"proxies": proxies,
	})
}

func apiProxySelect(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Host          string  `json:"host"`
		Port          any     `json:"port"`
		Uni           string  `json:"uni"`
		Username      *string `json:"username"`
		Password      *string `json:"password"`
		ClearPassword bool    `json:"clear_password"`
	}
	readJSON(r, &p)
	updateConfig(func(c *Config) {
		if h := strings.TrimSpace(p.Host); h != "" {
			c.ActiveProxyHost = h
			c.ActiveProxyPort = anyInt(p.Port, 8080)
			c.ActiveProxyUni = strings.TrimSpace(p.Uni)
			if c.ActiveProxyUni == "" {
				c.ActiveProxyUni = "Üniversite Proxy"
			}
		}
		key := c.activeKey()
		if p.Username != nil {
			u := strings.TrimSpace(*p.Username)
			c.Username = u
			cr := c.ProxyCredentials[key]
			if cr == nil {
				cr = &Creds{}
				c.ProxyCredentials[key] = cr
			}
			cr.Username = u
			if p.Password != nil && *p.Password != "" {
				c.Password, cr.Password = *p.Password, *p.Password
			} else if p.Password != nil && p.ClearPassword {
				c.Password, cr.Password = "", ""
			}
		}
	})
	diag := run8888Test()
	stats.setScanResult(getConfig().activeKey(), diag.ProxyCheck)
	writeJSON(w, 200, map[string]any{"ok": true, "diag": diag})
}

func apiProxyAdd(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Uni      string `json:"uni"`
		HostPort string `json:"host_port"`
	}
	readJSON(r, &p)
	uni := strings.TrimSpace(p.Uni)
	if uni == "" {
		uni = "Özel Üniversite Proxy"
	}
	m := hostPortRe.FindStringSubmatch(strings.TrimSpace(p.HostPort))
	if m == nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "Lütfen IP:Port formatında girin (Örn: 203.0.113.10:8080)"})
		return
	}
	host := m[1]
	port, _ := strconv.Atoi(m[2])
	updateConfig(func(c *Config) {
		c.CustomProxies = append([]CustomProxy{{Uni: uni, Host: host, Port: port}}, c.CustomProxies...)
	})
	if txt, err := os.ReadFile(dataPath("list.txt")); err == nil && !strings.Contains(string(txt), fmtHostPort(host, port)) {
		if f, err := os.OpenFile(dataPath("list.txt"), os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			f.WriteString("\n" + uni + "\n" + fmtHostPort(host, port) + "\n")
			f.Close()
		}
	}
	res := testSingleProxy(host, port, nil, nil, 3500*time.Millisecond)
	stats.setScanResult(fmtHostPort(host, port), res)
	writeJSON(w, 200, map[string]any{"ok": true, "result": res})
}

func apiWarpToggle(w http.ResponseWriter, r *http.Request) {
	var p map[string]any
	readJSON(r, &p)
	mode, _ := p["mode"].(string)
	if mode == "" {
		mode = "shield"
	}
	enable := true
	if v, ok := p["enable"].(bool); ok {
		enable = v
	}
	switch mode {
	case "shield":
		updateConfig(func(c *Config) {
			c.WarpShieldEnabled = enable
			if v, ok := p["ip_connect_mode"].(bool); ok {
				c.IPConnectMode = v
			}
			if v, ok := p["tls_fragment"].(bool); ok {
				c.TLSFragmentEnabled = v
			}
			if v, ok := p["tls_fragment_mode"].(string); ok && (v == tlsModeVersion || v == tlsModeSplit) {
				c.TLSFragmentMode = v
			}
		})
		writeJSON(w, 200, map[string]any{"ok": true, "warp_shield_enabled": enable})
	case "wiresock":
		ok, msg := toggleWiresock(enable)
		stats.mu.Lock()
		running := stats.wiresockRunning
		stats.mu.Unlock()
		writeJSON(w, 200, map[string]any{"ok": ok, "message": msg, "wiresock_running": running})
	default:
		writeJSON(w, 400, map[string]any{"ok": false, "error": "bilinmeyen mod"})
	}
}

func apiWifiOptions(w http.ResponseWriter, r *http.Request) {
	var p struct {
		BindToWifi *bool `json:"bind_to_wifi"`
		ConnectGSB bool  `json:"connect_gsb"`
	}
	readJSON(r, &p)
	if p.BindToWifi != nil {
		updateConfig(func(c *Config) { c.BindToWifi = *p.BindToWifi })
	}
	if p.ConnectGSB {
		connectGSBWifi()
	}
	writeJSON(w, 200, map[string]any{"ok": true, "wifi": getWifiInfo(true), "bind_to_wifi": getConfig().BindToWifi})
}

// apiRelaySet stores the relay address, token and certificate fingerprint.
// An empty server disables UDP relaying; an empty token keeps the saved one.
func apiRelaySet(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Server      string `json:"server"`
		Token       string `json:"token"`
		Fingerprint string `json:"fingerprint"`
	}
	readJSON(r, &p)
	server := strings.TrimSpace(p.Server)
	if server != "" {
		host, port, err := net.SplitHostPort(server)
		if err != nil || net.ParseIP(host) == nil || port == "" {
			writeJSON(w, 400, map[string]any{"ok": false, "error": "Röle adresi IP:port olmalı (örn. 203.0.113.10:443)"})
			return
		}
	}
	updateConfig(func(c *Config) {
		c.UDPGWServer = server
		if t := strings.TrimSpace(p.Token); t != "" {
			c.RelayToken = t
		}
		if f := strings.TrimSpace(p.Fingerprint); f != "" {
			c.RelayFingerprint = f
		}
	})
	writeJSON(w, 200, map[string]any{"ok": true, "note": "Sanal adaptör yeniden açıldığında geçerli olur"})
}

func anyInt(v any, def int) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case string:
		if n, err := strconv.Atoi(t); err == nil {
			return n
		}
	}
	return def
}
