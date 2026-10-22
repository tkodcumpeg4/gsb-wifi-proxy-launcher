package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const (
	localProxyAddr = "127.0.0.1:8880"
	webUIAddr      = "127.0.0.1:8765"
)

// TLS ClientHello obfuscation modes. "version" reproduces the Python build
// (record-layer version rewritten to 03 04); "split" additionally cuts the
// ClientHello into two TLS records so the SNI never sits in a single record.
const (
	tlsModeVersion = "version"
	tlsModeSplit   = "split"
)

type Creds struct {
	Username    string `json:"username"`
	Password    string `json:"password,omitempty"`
	PasswordEnc string `json:"password_enc,omitempty"`
}

type CustomProxy struct {
	Uni  string `json:"uni"`
	Host string `json:"host"`
	Port int    `json:"port"`
}

type Config struct {
	ActiveProxyHost    string            `json:"active_proxy_host"`
	ActiveProxyPort    int               `json:"active_proxy_port"`
	ActiveProxyUni     string            `json:"active_proxy_uni"`
	Username           string            `json:"username"`
	Password           string            `json:"password,omitempty"`
	PasswordEnc        string            `json:"password_enc,omitempty"`
	ProxyCredentials   map[string]*Creds `json:"proxy_credentials"`
	WarpShieldEnabled  bool              `json:"warp_shield_enabled"`
	IPConnectMode      bool              `json:"ip_connect_mode"`
	TLSFragmentEnabled bool              `json:"tls_fragment_enabled"`
	TLSFragmentMode    string            `json:"tls_fragment_mode,omitempty"`
	BindToWifi         bool              `json:"bind_to_wifi"`
	SystemProxyEnabled bool              `json:"system_proxy_enabled"`
	TunEnabled         bool              `json:"tun_enabled"`
	ForceTun           bool              `json:"force_tun"`
	UDPGWServer        string            `json:"udpgw_server,omitempty"`
	CoreLog            string            `json:"core_log,omitempty"`
	RelayToken         string            `json:"relay_token,omitempty"`
	RelayTokenEnc      string            `json:"relay_token_enc,omitempty"`
	RelayFingerprint   string            `json:"relay_fingerprint,omitempty"`
	CustomProxies      []CustomProxy     `json:"custom_proxies"`
}

func defaultConfig() *Config {
	return &Config{
		ActiveProxyPort:    8080,
		ActiveProxyUni:     "Proxy seçilmedi",
		ProxyCredentials:   map[string]*Creds{},
		WarpShieldEnabled:  true,
		TLSFragmentEnabled: true,
		TLSFragmentMode:    tlsModeVersion,
		BindToWifi:         true,
		CustomProxies:      []CustomProxy{},
	}
}

var (
	// baseDir holds writable state (config.json, dns_cache.json, list.txt,
	// logs); resourceDir holds the files shipped next to the exe (gsb-core.exe,
	// wintun.dll, the bundled list.txt). They differ once state lives in AppData.
	baseDir     string
	resourceDir string

	cfgMu sync.RWMutex
	cfg   *Config
)

func dataPath(name string) string { return filepath.Join(baseDir, name) }

func resourcePath(name string) string { return filepath.Join(resourceDir, name) }

// seedDataDir copies bundled, user-editable files (list.txt) from the install
// folder into the writable data dir on first run, mirroring the Python build.
func seedDataDir() {
	os.MkdirAll(baseDir, 0o700)
	if resourceDir == baseDir {
		return
	}
	// list.txt: bundled proxy list, editable. config.json: migrate a legacy
	// config left next to the exe into AppData once (keeps the user's setup).
	for _, name := range []string{"list.txt", "config.json"} {
		dst := dataPath(name)
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		raw, err := os.ReadFile(resourcePath(name))
		if err != nil && name == "list.txt" && len(embeddedList) > 0 {
			raw, err = embeddedList, nil // single-exe build has no file next to it
		}
		if err == nil {
			perm := os.FileMode(0o644)
			if name == "config.json" {
				perm = 0o600
			}
			os.WriteFile(dst, raw, perm)
		}
	}
}

// loadConfig reads config.json, decrypting sealed passwords (secret_*.go). A legacy
// plaintext password is kept in memory and re-saved encrypted right away.
func loadConfig() {
	c := defaultConfig()
	migrate := false
	if raw, err := os.ReadFile(dataPath("config.json")); err == nil {
		if err := json.Unmarshal(raw, c); err != nil {
			fmt.Fprintf(os.Stderr, "[!] config.json okunamadı: %v\n", err)
		}
	}
	if c.ProxyCredentials == nil {
		c.ProxyCredentials = map[string]*Creds{}
	}
	if c.TLSFragmentMode == "" {
		c.TLSFragmentMode = tlsModeVersion
	}
	migrate = unsealPassword(&c.Password, &c.PasswordEnc) || migrate
	migrate = unsealPassword(&c.RelayToken, &c.RelayTokenEnc) || migrate
	for _, cr := range c.ProxyCredentials {
		if cr != nil {
			migrate = unsealPassword(&cr.Password, &cr.PasswordEnc) || migrate
		}
	}
	// The Python build forces these on every start; keep that contract.
	c.BindToWifi = true
	c.IPConnectMode = false

	cfgMu.Lock()
	cfg = c
	cfgMu.Unlock()
	if migrate {
		saveConfig()
	}
}

// unsealPassword fills *plain from *enc. It reports true when *plain came from
// a legacy plaintext field and therefore still needs to be written encrypted.
func unsealPassword(plain, enc *string) bool {
	if *enc != "" {
		if p, err := decryptSecret(*enc); err == nil {
			*plain = p
		}
		return false
	}
	return *plain != ""
}

func saveConfig() {
	cfgMu.RLock()
	out := *cfg
	out.ProxyCredentials = make(map[string]*Creds, len(cfg.ProxyCredentials))
	for k, v := range cfg.ProxyCredentials {
		if v == nil {
			continue
		}
		cp := *v
		cp.PasswordEnc, cp.Password = sealPassword(cp.Password), ""
		out.ProxyCredentials[k] = &cp
	}
	cfgMu.RUnlock()
	out.PasswordEnc, out.Password = sealPassword(out.Password), ""
	out.RelayTokenEnc, out.RelayToken = sealPassword(out.RelayToken), ""

	raw, err := json.MarshalIndent(&out, "", "  ")
	if err != nil {
		return
	}
	tmp := dataPath("config.json.tmp")
	if os.WriteFile(tmp, raw, 0o600) == nil {
		os.Rename(tmp, dataPath("config.json"))
	}
}

func sealPassword(p string) string {
	if p == "" {
		return ""
	}
	enc, err := encryptSecret(p)
	if err != nil {
		return ""
	}
	return enc
}

// getConfig returns a shallow copy safe for reading outside the lock.
func getConfig() Config {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	return *cfg
}

func updateConfig(fn func(c *Config)) {
	cfgMu.Lock()
	fn(cfg)
	cfgMu.Unlock()
	saveConfig()
}

func (c Config) activeKey() string {
	return fmt.Sprintf("%s:%d", c.ActiveProxyHost, c.ActiveProxyPort)
}

// credentialsFor mirrors get_Active_auth_header: per-proxy creds first, then globals.
func credentialsFor(host string, port int) (user, pass string) {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	if host == "" {
		host = cfg.ActiveProxyHost
	}
	if port == 0 {
		port = cfg.ActiveProxyPort
	}
	if cr := cfg.ProxyCredentials[fmt.Sprintf("%s:%d", host, port)]; cr != nil {
		user, pass = cr.Username, cr.Password
	}
	if user == "" {
		user = cfg.Username
	}
	if pass == "" {
		pass = cfg.Password
	}
	return
}
