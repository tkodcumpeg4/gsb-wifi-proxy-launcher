package main

import (
	"testing"
	"time"
)

func TestTransferRoundTrip(t *testing.T) {
	cfg = defaultConfig()
	cfg.ActiveProxyHost, cfg.ActiveProxyPort, cfg.ActiveProxyUni = "193.140.97.32", 8080, "Örnek Üniversite"
	cfg.Username, cfg.Password = "240603030", "gizli şifre"
	cfg.UDPGWServer, cfg.RelayToken, cfg.RelayFingerprint = "203.0.113.10:443", "tok", "fp"
	cfg.CustomProxies = []CustomProxy{{Uni: "Ek", Host: "10.1.1.1", Port: 3128}}

	code, pin, _, err := exportSettings()
	if err != nil {
		t.Fatal(err)
	}
	if len(pin) != 6 {
		t.Fatalf("pin %q", pin)
	}
	if _, err := openTransfer(code, "000000", time.Now()); err == nil && pin != "000000" {
		t.Fatal("wrong PIN accepted")
	}
	if _, err := openTransfer(code, pin, time.Now().Add(transferTTL+time.Minute)); err == nil {
		t.Fatal("expired code accepted")
	}
	p, err := openTransfer(code, pin, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if p.Password != "gizli şifre" || p.RelayToken != "tok" || p.ActiveProxyUni != "Örnek Üniversite" || len(p.CustomProxies) != 1 {
		t.Fatalf("payload %+v", p)
	}
	if _, err := qrSVG(code); err != nil {
		t.Fatalf("qr: %v (code is %d bytes)", err, len(code))
	}
}
