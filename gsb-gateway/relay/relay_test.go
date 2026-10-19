package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"net"
	"testing"
	"time"
)

func startEcho(t *testing.T) *net.UDPAddr {
	t.Helper()
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, a, err := pc.ReadFromUDP(buf)
			if err != nil {
				return
			}
			pc.WriteToUDP(buf[:n], a)
		}
	}()
	return pc.LocalAddr().(*net.UDPAddr)
}

func startRelay(t *testing.T, token []byte) string {
	t.Helper()
	cert, _, err := loadOrCreateCert(t.TempDir()+"/c.pem", t.TempDir()+"/k.pem")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	cfg := relayConfig{token: token, allowPrivate: true, udpTimeout: 5 * time.Second, mtu: 65535}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serve(c, cfg)
		}
	}()
	return ln.Addr().String()
}

func dialRelay(t *testing.T, addr string, token []byte) (*tls.Conn, *bufio.Reader) {
	t.Helper()
	c, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	c.Write(token)
	return c, bufio.NewReader(c)
}

func TestRelayForwardsDatagrams(t *testing.T) {
	token := bytes.Repeat([]byte{7}, tokenLen)
	echo := startEcho(t)
	c, r := dialRelay(t, startRelay(t, token), token)
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))

	// Several datagrams on one flow must each come back.
	for _, msg := range []string{"bir", "iki", "üç"} {
		(&udpgwPacket{flags: flagData, connID: 9, addr: echo, data: []byte(msg)}).writeTo(c)
		p, err := readPacket(r)
		if err != nil {
			t.Fatal(err)
		}
		if p.connID != 9 || string(p.data) != msg {
			t.Fatalf("got conn %d data %q", p.connID, p.data)
		}
	}

	(&udpgwPacket{flags: flagKeepalive, connID: 3}).writeTo(c)
	p, err := readPacket(r)
	if err != nil || p.flags&flagKeepalive == 0 || p.connID != 3 {
		t.Fatalf("keepalive reply wrong: %+v %v", p, err)
	}
}

func TestRelayRejectsBadToken(t *testing.T) {
	token := bytes.Repeat([]byte{1}, tokenLen)
	echo := startEcho(t)
	c, r := dialRelay(t, startRelay(t, token), bytes.Repeat([]byte{2}, tokenLen))
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	(&udpgwPacket{flags: flagData, connID: 1, addr: echo, data: []byte("x")}).writeTo(c)
	if _, err := readPacket(r); err == nil {
		t.Fatal("relay answered with a wrong token")
	}
}

func TestRestrictedDestinations(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.0.0.5", "192.168.1.1", "169.254.1.1"} {
		if !isRestricted(net.ParseIP(s)) {
			t.Errorf("%s should be restricted", s)
		}
	}
	if isRestricted(net.ParseIP("1.1.1.1")) {
		t.Error("public address restricted")
	}
}
