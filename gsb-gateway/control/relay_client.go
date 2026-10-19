package main

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// UDP relay (gsb-relay) client side. gsb-core sends udpgw frames to the
// configured relay address via a CONNECT to this bridge; we open that tunnel
// through the university proxy, wrap it in TLS pinned to the relay's
// certificate fingerprint, authenticate with the shared token and then pipe.

const relayTokenLen = 32

// isRelayTarget reports whether a CONNECT target is the configured relay.
func isRelayTarget(target string) bool {
	r := strings.TrimSpace(getConfig().UDPGWServer)
	return r != "" && r == target
}

func relayCredentials() (token []byte, fp string, err error) {
	c := getConfig()
	token, err = hex.DecodeString(strings.TrimSpace(c.RelayToken))
	if err != nil || len(token) != relayTokenLen {
		return nil, "", errors.New("röle token'ı geçersiz (64 hex karakter olmalı)")
	}
	fp = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(c.RelayFingerprint), ":", ""))
	if len(fp) != 64 {
		return nil, "", errors.New("röle sertifika parmak izi geçersiz (SHA-256, 64 hex)")
	}
	return token, fp, nil
}

// dialRelay opens an authenticated TLS session to the relay over a CONNECT
// tunnel through the university proxy.
func dialRelay(target string, timeout time.Duration) (net.Conn, error) {
	token, fp, err := relayCredentials()
	if err != nil {
		return nil, err
	}
	conn, code, line, err := connectTunnel(target, timeout)
	if err != nil {
		return nil, err
	}
	if code != 200 {
		conn.Close()
		return nil, fmt.Errorf("röle CONNECT reddedildi: %s", line)
	}
	// The relay sits on a datacenter IP that the campus firewall subjects to
	// TLS inspection; without obfuscation it answers with its own MITM
	// certificate. Mutating the first ClientHello record (same treatment the
	// bridge gives ordinary client TLS) lets the real relay certificate
	// through, which the fingerprint pin below then verifies.
	c := getConfig()
	if c.WarpShieldEnabled || c.TLSFragmentEnabled {
		conn = &firstWriteMutatedConn{Conn: conn, mode: c.TLSFragmentMode}
	}
	tc := tls.Client(conn, &tls.Config{
		// The relay uses a self-signed certificate; trust is established by
		// pinning its SHA-256 fingerprint instead of a CA chain.
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("röle sertifika göndermedi")
			}
			sum := sha256.Sum256(raw[0])
			if got := hex.EncodeToString(sum[:]); got != fp {
				return fmt.Errorf("röle sertifika parmak izi eşleşmiyor (görülen %s, beklenen %s)", got, fp)
			}
			return nil
		},
		MinVersion: tls.VersionTLS12,
	})
	tc.SetDeadline(time.Now().Add(timeout))
	if err := tc.Handshake(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("röle TLS: %w", err)
	}
	if _, err := tc.Write(token); err != nil {
		tc.Close()
		return nil, err
	}
	tc.SetDeadline(time.Time{})
	return tc, nil
}

// bridgeRelay serves a CONNECT from gsb-core to the relay address.
func bridgeRelay(client net.Conn, upstreamOut *net.Conn, target string) error {
	rc, err := dialRelay(target, 10*time.Second)
	if err != nil {
		client.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return err
	}
	*upstreamOut = rc
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return err
	}
	pipe(client, rc, false, "")
	return nil
}

// testRelay sends one DNS query for example.com to 1.1.1.1:53 through the
// relay and reports the round-trip time; used by the panel's test button.
func testRelay() (int64, error) {
	target := strings.TrimSpace(getConfig().UDPGWServer)
	if target == "" {
		return 0, errors.New("röle adresi ayarlı değil")
	}
	t0 := time.Now()
	rc, err := dialRelay(target, 10*time.Second)
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	rc.SetDeadline(time.Now().Add(8 * time.Second))

	query := []byte{0x12, 0x34, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0,
		7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0, 0, 1, 0, 1}
	frame := []byte{0, 0, 0x02, 0, 1, 0x01, 1, 1, 1, 1, 0, 53}
	frame = append(frame, query...)
	n := len(frame) - 2
	frame[0], frame[1] = byte(n>>8), byte(n)
	if _, err := rc.Write(frame); err != nil {
		return 0, err
	}
	hdr := make([]byte, 2)
	if _, err := readFull(rc, hdr); err != nil {
		return 0, err
	}
	body := make([]byte, int(hdr[0])<<8|int(hdr[1]))
	if _, err := readFull(rc, body); err != nil {
		return 0, err
	}
	if len(body) < 3 || body[0]&0x02 == 0 {
		return 0, errors.New("röle UDP yanıtı vermedi")
	}
	return time.Since(t0).Milliseconds(), nil
}

// firstWriteMutatedConn applies the ClientHello obfuscation to the first
// write only (that write carries the TLS ClientHello), then is transparent.
type firstWriteMutatedConn struct {
	net.Conn
	mode string
	done bool
}

func (m *firstWriteMutatedConn) Write(b []byte) (int, error) {
	if m.done {
		return m.Conn.Write(b)
	}
	m.done = true
	out := mutateClientHello(b, m.mode)
	if _, err := m.Conn.Write(out); err != nil {
		return 0, err
	}
	return len(b), nil
}

func readFull(c net.Conn, b []byte) (int, error) {
	got := 0
	for got < len(b) {
		n, err := c.Read(b[got:])
		got += n
		if err != nil {
			return got, err
		}
	}
	return got, nil
}
