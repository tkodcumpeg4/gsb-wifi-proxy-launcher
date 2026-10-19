package main

import (
	"bytes"
	"crypto/tls"
	"net"
	"testing"
	"time"
)

// captureClientHello records the first flight a real crypto/tls client sends.
func captureClientHello(t *testing.T, sni string) []byte {
	t.Helper()
	a, b := net.Pipe()
	go func() {
		c := tls.Client(a, &tls.Config{ServerName: sni, InsecureSkipVerify: true})
		c.SetDeadline(time.Now().Add(time.Second))
		c.Handshake()
	}()
	buf := make([]byte, 8192)
	b.SetReadDeadline(time.Now().Add(time.Second))
	n, err := b.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
	b.Close()
	return buf[:n]
}

func TestVersionMode(t *testing.T) {
	ch := captureClientHello(t, "www.netflix.com")
	out := mutateClientHello(ch, tlsModeVersion)
	if out[2] != 0x04 || !bytes.Equal(out[3:], ch[3:]) {
		t.Fatalf("version mode should only rewrite byte 2")
	}
	if ch[2] == 0x04 {
		t.Fatalf("input must not be modified in place")
	}
}

func TestSplitModeCutsSNI(t *testing.T) {
	sni := "www.netflix.com"
	ch := captureClientHello(t, sni)
	off, n := sniOffset(ch[5:])
	if string(ch[5+off:5+off+n]) != sni {
		t.Fatalf("sniOffset found %q", ch[5+off:5+off+n])
	}
	out := mutateClientHello(ch, tlsModeSplit)
	l1 := int(out[3])<<8 | int(out[4])
	rec1, rec2 := out[5:5+l1], out[5+l1:]
	if bytes.Contains(rec1, []byte(sni)) || bytes.Contains(rec2, []byte(sni)) {
		t.Fatalf("SNI still present in a single record")
	}
	if rec2[0] != 0x16 {
		t.Fatalf("second record header missing")
	}
	// Reassembled payload must equal the original handshake bytes.
	l2 := int(rec2[3])<<8 | int(rec2[4])
	joined := append(append([]byte{}, rec1...), rec2[5:5+l2]...)
	if !bytes.Equal(joined, ch[5:]) {
		t.Fatalf("reassembled handshake differs")
	}
}

func TestNonTLSUntouched(t *testing.T) {
	in := []byte("GET / HTTP/1.1\r\n\r\n")
	if !bytes.Equal(mutateClientHello(in, tlsModeSplit), in) {
		t.Fatal("non-TLS data changed")
	}
}
