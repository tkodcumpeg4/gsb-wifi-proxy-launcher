// gsb-relay is the server half of GSB Gateway's UDP support. It runs on a
// machine the user controls, terminates TLS on :443, checks a shared token,
// and then speaks udpgw: every UDP datagram the TUN adapter captures is
// forwarded from here and the replies are sent back on the same connection.
//
// Only authenticated clients are served, and by default destinations in
// private/loopback ranges are refused so the relay cannot reach the host's
// own network.
package main

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const tokenLen = 32

type relayConfig struct {
	token        []byte
	allowPrivate bool
	udpTimeout   time.Duration
	mtu          int
}

func main() {
	listen := flag.String("listen", ":443", "TLS listen address")
	dir := flag.String("dir", "/etc/gsb-relay", "directory for token, cert and key")
	allowPrivate := flag.Bool("allow-private", false, "allow private/loopback UDP destinations")
	udpTimeout := flag.Duration("udp-timeout", 60*time.Second, "idle timeout per UDP flow")
	flag.Parse()

	if err := os.MkdirAll(*dir, 0o700); err != nil {
		log.Fatal(err)
	}
	token, err := loadOrCreateToken(filepath.Join(*dir, "token"))
	if err != nil {
		log.Fatal(err)
	}
	cert, fp, err := loadOrCreateCert(filepath.Join(*dir, "cert.pem"), filepath.Join(*dir, "key.pem"))
	if err != nil {
		log.Fatal(err)
	}

	ln, err := tls.Listen("tcp", *listen, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("gsb-relay %s üzerinde dinliyor", *listen)
	log.Printf("sertifika SHA-256: %s", fp)
	// The token itself is never logged; read it from the file once.
	log.Printf("token dosyası: %s", filepath.Join(*dir, "token"))

	cfg := relayConfig{token: token, allowPrivate: *allowPrivate, udpTimeout: *udpTimeout, mtu: 65535}
	for {
		c, err := ln.Accept()
		if err != nil {
			continue
		}
		go serve(c, cfg)
	}
}

func serve(c net.Conn, cfg relayConfig) {
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	got := make([]byte, tokenLen)
	if _, err := io.ReadFull(c, got); err != nil {
		return
	}
	if subtle.ConstantTimeCompare(got, cfg.token) != 1 {
		return
	}
	c.SetReadDeadline(time.Time{})

	out := make(chan *udpgwPacket, 256)
	flows := newFlowTable(out, cfg.udpTimeout, cfg.mtu)
	defer flows.closeAll()

	done := make(chan struct{})
	go func() {
		w := bufio.NewWriter(c)
		for {
			select {
			case p := <-out:
				if p.writeTo(w) != nil {
					return
				}
				// Flush when nothing else is queued, to batch bursts.
				if len(out) == 0 && w.Flush() != nil {
					return
				}
			case <-done:
				return
			}
		}
	}()
	defer close(done)

	r := bufio.NewReader(c)
	for {
		p, err := readPacket(r)
		if err != nil {
			return
		}
		if p.flags&flagData != 0 && p.addr != nil && !cfg.allowPrivate && isRestricted(p.addr.IP) {
			out <- &udpgwPacket{flags: flagErr, connID: p.connID}
			continue
		}
		flows.handle(p)
	}
}

func isRestricted(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified()
}

func loadOrCreateToken(path string) ([]byte, error) {
	if raw, err := os.ReadFile(path); err == nil {
		return hex.DecodeString(strings.TrimSpace(string(raw)))
	}
	t := make([]byte, tokenLen)
	if _, err := rand.Read(t); err != nil {
		return nil, err
	}
	return t, os.WriteFile(path, []byte(hex.EncodeToString(t)+"\n"), 0o600)
}

func loadOrCreateCert(certPath, keyPath string) (tls.Certificate, string, error) {
	if c, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		return c, fingerprint(c.Certificate[0]), nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "gsb-relay"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600)
	c, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("sertifika yüklenemedi: %w", err)
	}
	return c, fingerprint(der), nil
}

func fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}
