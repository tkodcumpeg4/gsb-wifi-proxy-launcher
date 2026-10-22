package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ---- Proxy host DNS cache (dns_cache.json) ----

var dnsCache = struct {
	sync.Mutex
	m map[string]string
}{m: map[string]string{}}

func loadDNSCache() {
	raw, err := os.ReadFile(dataPath("dns_cache.json"))
	if err != nil {
		return
	}
	dnsCache.Lock()
	json.Unmarshal(raw, &dnsCache.m)
	dnsCache.Unlock()
}

func saveDNSCacheLocked() {
	raw, err := json.MarshalIndent(dnsCache.m, "", "  ")
	if err == nil {
		os.WriteFile(dataPath("dns_cache.json"), raw, 0o644)
	}
}

func isIPv4(h string) bool {
	ip := net.ParseIP(h)
	return ip != nil && ip.To4() != nil && strings.Count(h, ".") == 3
}

// cachedProxyIP returns the cached address without touching the network.
func cachedProxyIP(host string) string {
	if isIPv4(host) {
		return host
	}
	dnsCache.Lock()
	defer dnsCache.Unlock()
	return dnsCache.m[host]
}

// resolveProxyHost resolves a university proxy host name through the system
// resolver (GSB DNS can see *.edu.tr) and persists the answer.
func resolveProxyHost(host string) string {
	if ip := cachedProxyIP(host); ip != "" {
		return ip
	}
	addrs, err := net.LookupIP(host)
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		// While TUN mode is on the system resolver hands out fake IPs from
		// 198.18.0.0/15; never cache those as the proxy's address.
		if v4 := a.To4(); v4 != nil && !fakeIPNet.Contains(v4) {
			ip := v4.String()
			dnsCache.Lock()
			dnsCache.m[host] = ip
			saveDNSCacheLocked()
			dnsCache.Unlock()
			return ip
		}
	}
	return ""
}

// ---- Outbound sockets ----

// dialOutbound mirrors create_outbound_socket: NODELAY, 512 KiB buffers, and
// bound to the Wi-Fi IPv4 so a wired/IPv6 route can never steal the traffic.
// forceWifi=nil follows the bind_to_wifi setting.
func dialOutbound(addr string, timeout time.Duration, forceWifi *bool) (net.Conn, error) {
	useWifi := getConfig().BindToWifi
	if forceWifi != nil {
		useWifi = *forceWifi
	}
	d := net.Dialer{
		Timeout: timeout,
		Control: func(_, _ string, c syscall.RawConn) error {
			c.Control(func(fd uintptr) { setSockBuffers(fd, 512*1024) })
			return nil
		},
	}
	if useWifi {
		if w := getWifiInfo(false); w.Connected && w.IP != "" {
			d.LocalAddr = &net.TCPAddr{IP: net.ParseIP(w.IP)}
		}
	}
	conn, err := d.Dial("tcp4", addr)
	if err != nil {
		return nil, err
	}
	conn.(*net.TCPConn).SetNoDelay(true)
	return conn, nil
}

func proxyAuthHeader(host string, port int) string {
	user, pass := credentialsFor(host, port)
	return basicAuthLine(user, pass)
}

func basicAuthLine(user, pass string) string {
	if user == "" {
		return ""
	}
	return "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass)) + "\r\n"
}

// activeProxy returns the configured proxy host, port and resolved IP.
func activeProxy() (host string, port int, ip string) {
	c := getConfig()
	host, port = c.ActiveProxyHost, c.ActiveProxyPort
	if port == 0 {
		port = 8080
	}
	return host, port, resolveProxyHost(host)
}

var fakeIPNet = &net.IPNet{IP: net.IPv4(198, 18, 0, 0), Mask: net.CIDRMask(15, 32)}

var errProxyDNS = errors.New("Proxy DNS çözümlenemedi")

// connectTunnel opens a CONNECT tunnel through the active proxy using a fresh
// socket. It returns the status code and the first response line.
func connectTunnel(target string, timeout time.Duration) (net.Conn, int, string, error) {
	host, port, ip := activeProxy()
	if ip == "" {
		return nil, 0, "", errProxyDNS
	}
	conn, err := dialOutbound(net.JoinHostPort(ip, strconv.Itoa(port)), timeout, nil)
	if err != nil {
		return nil, 0, "", err
	}
	conn.SetDeadline(time.Now().Add(timeout))
	req := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n" + proxyAuthHeader(host, port) +
		"Proxy-Connection: keep-alive\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		return nil, 0, "", err
	}
	code, line, err := readConnectResponse(conn)
	if err != nil {
		conn.Close()
		return nil, 0, "", err
	}
	conn.SetDeadline(time.Time{})
	return conn, code, line, nil
}

func readConnectResponse(conn net.Conn) (int, string, error) {
	raw, err := readHeaderBlock(conn)
	if err != nil {
		return 0, "", err
	}
	line, _, _ := bufio.NewReader(bytes.NewReader(raw)).ReadLine()
	return statusCode(string(line)), string(line), nil
}

// readHeaderBlock reads the proxy's response headers byte-wise so no tunnel
// payload is consumed past the blank line.
func readHeaderBlock(conn net.Conn) ([]byte, error) {
	var buf bytes.Buffer
	b := make([]byte, 1)
	for buf.Len() < 8192 && !bytes.HasSuffix(buf.Bytes(), []byte("\r\n\r\n")) {
		if _, err := conn.Read(b); err != nil {
			if buf.Len() == 0 {
				return nil, err
			}
			break
		}
		buf.WriteByte(b[0])
	}
	return buf.Bytes(), nil
}

func statusCode(line string) int {
	parts := strings.SplitN(line, " ", 3)
	if len(parts) >= 2 {
		if c, err := strconv.Atoi(parts[1]); err == nil {
			return c
		}
	}
	return 0
}

// ---- Pre-warmed upstream pool (WARP-Shield) ----

type pooledConn struct {
	conn    net.Conn
	addr    string
	created time.Time
}

var upstreamPool = make(chan pooledConn, 10)

const (
	poolTarget = 3
	poolMaxAge = 8 * time.Second
)

// getUpstream hands out a fresh pre-connected socket to addr, or dials one.
func getUpstream(addr string, timeout time.Duration) (net.Conn, error) {
	for {
		select {
		case p := <-upstreamPool:
			if p.addr == addr && time.Since(p.created) < poolMaxAge {
				stats.PooledHits.Add(1)
				return p.conn, nil
			}
			p.conn.Close()
			continue
		default:
		}
		return dialOutbound(addr, timeout, nil)
	}
}

func poolWarmerLoop() {
	for {
		if len(upstreamPool) < poolTarget {
			if _, port, ip := activeProxy(); ip != "" {
				addr := net.JoinHostPort(ip, strconv.Itoa(port))
				if c, err := dialOutbound(addr, 4*time.Second, nil); err == nil {
					select {
					case upstreamPool <- pooledConn{c, addr, time.Now()}:
					default:
						c.Close()
					}
				}
			}
		}
		time.Sleep(1200 * time.Millisecond)
	}
}

func fmtHostPort(host string, port int) string { return fmt.Sprintf("%s:%d", host, port) }
