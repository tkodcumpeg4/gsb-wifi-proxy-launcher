package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"rsc.io/qr"
)

// Settings transfer between devices ("Telefona Aktar"): the desktop panel
// shows a QR code plus a 6-digit PIN; the phone app scans it and imports.
// The code is AES-GCM ciphertext under a key derived from the PIN with
// PBKDF2, so a photo of the screen alone does not reveal the password or
// the relay token, and the importer rejects it after transferTTL. The PIN
// is shown only next to the code, never encoded in it.

const (
	transferPrefix = "GSB1."
	transferTTL    = 10 * time.Minute
	transferIter   = 200_000 // ~0.3 s on a phone; makes guessing 10^6 PINs costly
)

// transferPayload is what moves between devices: the proxy and its login,
// saved logins for other proxies, proxies added by hand, the UDP relay and
// the WARP-Shield switches. Device-specific state (TUN, system proxy, Wi-Fi
// binding) stays behind.
type transferPayload struct {
	V                  int               `json:"v"`
	Expires            int64             `json:"exp"`
	ActiveProxyHost    string            `json:"host"`
	ActiveProxyPort    int               `json:"port"`
	ActiveProxyUni     string            `json:"uni"`
	Username           string            `json:"user"`
	Password           string            `json:"pass"`
	Creds              map[string]*Creds `json:"creds,omitempty"`
	CustomProxies      []CustomProxy     `json:"custom,omitempty"`
	RelayServer        string            `json:"relay,omitempty"`
	RelayToken         string            `json:"relay_token,omitempty"`
	RelayFingerprint   string            `json:"relay_fp,omitempty"`
	WarpShieldEnabled  bool              `json:"warp"`
	IPConnectMode      bool              `json:"ipconn"`
	TLSFragmentEnabled bool              `json:"tlsfrag"`
	TLSFragmentMode    string            `json:"tlsmode"`
}

func transferKey(pin string, salt []byte) ([]byte, error) {
	return pbkdf2.Key(sha256.New, pin, salt, transferIter, 32)
}

// exportSettings seals the current settings under a fresh random PIN.
func exportSettings() (code, pin string, expires time.Time, err error) {
	c := getConfig()
	expires = time.Now().Add(transferTTL)
	p := transferPayload{
		V: 1, Expires: expires.Unix(),
		ActiveProxyHost: c.ActiveProxyHost, ActiveProxyPort: c.ActiveProxyPort, ActiveProxyUni: c.ActiveProxyUni,
		Username: c.Username, Password: c.Password,
		CustomProxies: c.CustomProxies,
		RelayServer:   c.UDPGWServer, RelayToken: c.RelayToken, RelayFingerprint: c.RelayFingerprint,
		WarpShieldEnabled: c.WarpShieldEnabled, IPConnectMode: c.IPConnectMode,
		TLSFragmentEnabled: c.TLSFragmentEnabled, TLSFragmentMode: c.TLSFragmentMode,
	}
	cfgMu.RLock()
	p.Creds = make(map[string]*Creds, len(cfg.ProxyCredentials))
	for k, v := range cfg.ProxyCredentials {
		if v != nil && v.Username != "" {
			p.Creds[k] = &Creds{Username: v.Username, Password: v.Password}
		}
	}
	cfgMu.RUnlock()

	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", "", expires, err
	}
	pin = fmt.Sprintf("%06d", n.Int64())
	plain, err := json.Marshal(&p)
	if err != nil {
		return "", "", expires, err
	}
	salt := make([]byte, 16)
	if _, err = rand.Read(salt); err != nil {
		return "", "", expires, err
	}
	aead, err := transferAEAD(pin, salt)
	if err != nil {
		return "", "", expires, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", "", expires, err
	}
	blob := append(append(salt, nonce...), aead.Seal(nil, nonce, plain, []byte(transferPrefix))...)
	return transferPrefix + base64.RawURLEncoding.EncodeToString(blob), pin, expires, nil
}

var errTransferPIN = errors.New("PIN yanlış ya da kod bozuk")

func transferAEAD(pin string, salt []byte) (cipher.AEAD, error) {
	key, err := transferKey(pin, salt)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// openTransfer decrypts and validates a code; it does not change settings.
func openTransfer(code, pin string, now time.Time) (*transferPayload, error) {
	code = strings.TrimSpace(code)
	if !strings.HasPrefix(code, transferPrefix) {
		return nil, errors.New("Bu bir GSB Gateway aktarım kodu değil")
	}
	blob, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(code, transferPrefix))
	if err != nil || len(blob) < 16+12+16 {
		return nil, errTransferPIN
	}
	pin = strings.TrimSpace(pin)
	aead, err := transferAEAD(pin, blob[:16])
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, blob[16:28], blob[28:], []byte(transferPrefix))
	if err != nil {
		return nil, errTransferPIN
	}
	var p transferPayload
	if err := json.Unmarshal(plain, &p); err != nil || p.V != 1 {
		return nil, errors.New("Kod bu sürümle okunamıyor")
	}
	if now.Unix() > p.Expires {
		return nil, errors.New("Kodun süresi dolmuş; bilgisayarda yeni bir kod oluşturun")
	}
	return &p, nil
}

// importSettings applies a payload over the current settings. Saved logins
// and hand-added proxies are merged, not replaced.
func importSettings(p *transferPayload) {
	updateConfig(func(c *Config) {
		if p.ActiveProxyHost != "" {
			c.ActiveProxyHost, c.ActiveProxyPort, c.ActiveProxyUni = p.ActiveProxyHost, p.ActiveProxyPort, p.ActiveProxyUni
		}
		c.Username, c.Password = p.Username, p.Password
		if c.ProxyCredentials == nil {
			c.ProxyCredentials = map[string]*Creds{}
		}
		for k, v := range p.Creds {
			c.ProxyCredentials[k] = &Creds{Username: v.Username, Password: v.Password}
		}
		if p.Username != "" {
			c.ProxyCredentials[c.activeKey()] = &Creds{Username: p.Username, Password: p.Password}
		}
		for _, cp := range p.CustomProxies {
			dup := false
			for _, have := range c.CustomProxies {
				if have.Host == cp.Host && have.Port == cp.Port {
					dup = true
					break
				}
			}
			if !dup {
				c.CustomProxies = append(c.CustomProxies, cp)
			}
		}
		if p.RelayServer != "" {
			c.UDPGWServer, c.RelayFingerprint = p.RelayServer, p.RelayFingerprint
			if p.RelayToken != "" {
				c.RelayToken = p.RelayToken
			}
		}
		c.WarpShieldEnabled, c.IPConnectMode = p.WarpShieldEnabled, p.IPConnectMode
		c.TLSFragmentEnabled = p.TLSFragmentEnabled
		if p.TLSFragmentMode == tlsModeVersion || p.TLSFragmentMode == tlsModeSplit {
			c.TLSFragmentMode = p.TLSFragmentMode
		}
	})
}

// qrSVG renders text as a QR code (medium error correction) in an SVG data
// URL, one unit per module plus the 4-module quiet zone.
func qrSVG(text string) (string, error) {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return "", err
	}
	n := code.Size + 8
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges"><rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="`, n, n, n, n)
	for y := 0; y < code.Size; y++ {
		for x := 0; x < code.Size; x++ {
			if code.Black(x, y) {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x+4, y+4)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(b.String())), nil
}

func apiTransferExport(w http.ResponseWriter, r *http.Request) {
	code, pin, exp, err := exportSettings()
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	img, err := qrSVG(code)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": "QR oluşturulamadı: " + err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "code": code, "pin": pin, "expires": exp.Unix(), "qr": img})
}

func apiTransferImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
		PIN  string `json:"pin"`
	}
	readJSON(r, &req)
	p, err := openTransfer(req.Code, req.PIN, time.Now())
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	importSettings(p)
	writeJSON(w, 200, map[string]any{"ok": true, "proxy": fmt.Sprintf("%s:%d", p.ActiveProxyHost, p.ActiveProxyPort), "uni": p.ActiveProxyUni, "relay": p.RelayServer != ""})
}

// jsQR decodes the transfer QR from the phone camera in the WebView; it is
// served from here so scanning works without internet.
//
//go:embed web/jsqr.js
var embeddedJSQR []byte

func serveJSQR(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "max-age=86400")
	w.Write(embeddedJSQR)
}
