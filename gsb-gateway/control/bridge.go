package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	headerReadTimeout = 12 * time.Second
	// A tunnel is closed once neither direction has moved data for this long,
	// so idle sockets do not pin the university Squid's maxconn. The Python
	// build timed each direction separately (15 s), which half-closed busy
	// downloads whose client side was quiet; activity is now shared. 90 s
	// keeps heartbeat-based apps (Discord, WebSockets) alive in TUN mode.
	tunnelIdleTimeout = 90 * time.Second
	pipeBufSize       = 128 * 1024
)

func runLocalBridge() {
	ln, err := net.Listen("tcp4", localProxyAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[!] Yerel proxy %s açılamadı: %v\n", localProxyAddr, err)
		return
	}
	fmt.Printf("[+] Yerel proxy ve WARP-Shield köprüsü aktif: http://%s\n", localProxyAddr)
	for {
		c, err := ln.Accept()
		if err != nil {
			continue
		}
		go handleBridge(c)
	}
}

func handleBridge(client net.Conn) {
	stats.TotalConnections.Add(1)
	stats.ActiveConnections.Add(1)
	var upstream net.Conn
	defer func() {
		if upstream != nil {
			upstream.Close()
		}
		client.Close()
		stats.ActiveConnections.Add(-1)
	}()
	if err := bridgeOnce(client, &upstream); err != nil {
		stats.setError(fmt.Sprintf("%T: %v", err, err))
	}
}

func bridgeOnce(client net.Conn, upstreamOut *net.Conn) error {
	client.(*net.TCPConn).SetNoDelay(true)
	client.SetReadDeadline(time.Now().Add(headerReadTimeout))

	initial := make([]byte, 0, 4096)
	buf := make([]byte, 4096)
	for !bytes.Contains(initial, []byte("\r\n\r\n")) && len(initial) < 16384 {
		n, err := client.Read(buf)
		if n == 0 || err != nil {
			return nil
		}
		initial = append(initial, buf[:n]...)
	}
	client.SetReadDeadline(time.Time{})

	headerPart, body, ok := bytes.Cut(initial, []byte("\r\n\r\n"))
	if !ok {
		return nil
	}
	lines := strings.Split(string(headerPart), "\r\n")
	parts := strings.Split(lines[0], " ")
	if len(parts) < 3 {
		return nil
	}
	method, target := strings.ToUpper(parts[0]), parts[1]

	proxyHost, proxyPort, proxyIP := activeProxy()
	if proxyIP == "" {
		client.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\nProxy DNS cozumlenemedi."))
		return nil
	}
	proxyAddr := net.JoinHostPort(proxyIP, strconv.Itoa(proxyPort))
	authHdr := proxyAuthHeader(proxyHost, proxyPort)
	c := getConfig()

	if method != "CONNECT" {
		upstream, err := getUpstream(proxyAddr, 10*time.Second)
		if err != nil {
			return err
		}
		*upstreamOut = upstream
		return forwardPlainHTTP(client, upstream, lines, body, authHdr)
	}

	tHost, tPort := target, "443"
	if i := strings.LastIndex(target, ":"); i >= 0 {
		tHost, tPort = target[:i], target[i+1:]
	}
	if isRelayTarget(target) {
		return bridgeRelay(client, upstreamOut, target)
	}
	if tPort == "80" {
		// Squid only allows CONNECT to SSL ports; TUN mode tunnels plain HTTP
		// as CONNECT :80, so turn it back into an ordinary proxied request.
		return transparentHTTP(client, upstreamOut, proxyAddr, authHdr, tHost)
	}
	connectTarget := tHost + ":" + tPort
	if c.WarpShieldEnabled && !isIPv4(tHost) {
		if ip := dohCached(tHost); ip != "" {
			connectTarget = ip + ":" + tPort
		} else if c.IPConnectMode {
			if ip := dohResolve(tHost, 2*time.Second); ip != "" {
				connectTarget = ip + ":" + tPort
			}
		}
		stats.AnonymizedConnects.Add(1)
	}

	upstream, err := getUpstream(proxyAddr, 10*time.Second)
	if err != nil {
		return err
	}
	*upstreamOut = upstream

	tlsBypass := c.TLSFragmentEnabled || c.WarpShieldEnabled
	hostHdr := connectTarget
	if tlsBypass {
		hostHdr = "www.google.com:443"
	}
	req := "CONNECT " + connectTarget + " HTTP/1.1\r\nHost: " + hostHdr + "\r\n" + authHdr +
		"Proxy-Connection: keep-alive\r\n\r\n"
	upstream.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := upstream.Write([]byte(req)); err != nil {
		return err
	}
	resp, err := readHeaderBlock(upstream)
	if err != nil || len(resp) == 0 {
		client.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return err
	}
	upstream.SetDeadline(time.Time{})
	firstLine, _, _ := bytes.Cut(resp, []byte("\r\n"))
	switch statusCode(string(firstLine)) {
	case 200:
	case 407:
		stats.setError("407 Proxy Kimlik Doğrulama Hatası")
		msg := "Proxy kimlik dogrulamasi basarisiz (407)."
		client.Write([]byte("HTTP/1.1 502 Proxy Auth Failed\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: " +
			strconv.Itoa(len(msg)) + "\r\n\r\n" + msg))
		return nil
	default:
		client.Write(resp)
		return nil
	}

	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return err
	}
	mutateFirst := tlsBypass
	if len(body) > 0 {
		if mutateFirst {
			body = mutateClientHello(body, c.TLSFragmentMode)
		}
		if _, err := upstream.Write(body); err != nil {
			return err
		}
		mutateFirst = false
	}
	pipe(client, upstream, mutateFirst, c.TLSFragmentMode)
	return nil
}

var droppedHeaders = []string{"proxy-authorization:", "proxy-connection:", "via:", "x-forwarded-for:"}

// transparentHTTP accepts a CONNECT to port 80 (Squid only allows CONNECT to
// SSL ports, so TUN-tunnelled plain HTTP arrives this way) and relays it as an
// ordinary proxied connection: each request on the tunnel is rewritten to
// absolute-URI form and forwarded through the university proxy. http.Transport
// keeps the upstream proxy connection alive, so keep-alive clients that send
// many requests on one tunnel (Steam's content downloads, APT, etc.) work.
func transparentHTTP(client net.Conn, upstreamOut *net.Conn, proxyAddr, authHdr, connectHost string) error {
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return err
	}
	tr := &http.Transport{
		Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: proxyAddr}),
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialOutbound(addr, 10*time.Second, nil)
		},
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	defer tr.CloseIdleConnections()

	br := bufio.NewReader(client)
	for {
		client.SetReadDeadline(time.Now().Add(tunnelIdleTimeout))
		req, err := http.ReadRequest(br)
		if err != nil {
			return nil
		}
		client.SetReadDeadline(time.Time{})

		host := req.Host
		if host == "" {
			host = connectHost
		}
		req.URL.Scheme = "http"
		req.URL.Host = host
		req.RequestURI = ""
		req.Header.Del("Proxy-Connection")
		for _, h := range droppedHeaders {
			req.Header.Del(strings.TrimSuffix(h, ":"))
		}
		if authHdr != "" {
			// authHdr is "Proxy-Authorization: Basic xxx\r\n".
			if _, v, ok := strings.Cut(strings.TrimSpace(authHdr), ": "); ok {
				req.Header.Set("Proxy-Authorization", v)
			}
		}

		clientKeepAlive := !req.Close
		resp, err := tr.RoundTrip(req)
		if err != nil {
			client.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\n\r\n"))
			return err
		}
		// The upstream proxy (Squid) often closes after each response; that is
		// its connection, which http.Transport pools independently. Keep the
		// client tunnel open as long as the client wants it, and strip
		// hop-by-hop headers so framing stays consistent across requests.
		resp.Header.Del("Connection")
		resp.Header.Del("Proxy-Connection")
		resp.Header.Del("Keep-Alive")
		resp.Close = !clientKeepAlive
		n, _ := io.Copy(countingWriter{client}, respReader(resp))
		stats.BytesTransferred.Add(n)
		resp.Body.Close()
		if !clientKeepAlive {
			return nil
		}
	}
}

type countingWriter struct{ w io.Writer }

func (c countingWriter) Write(p []byte) (int, error) { return c.w.Write(p) }

// respReader serializes an http.Response back onto the wire for the client.
func respReader(resp *http.Response) io.Reader {
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(resp.Write(pw)) }()
	return pr
}

func forwardPlainHTTP(client, upstream net.Conn, lines []string, body []byte, authHdr string) error {
	var b strings.Builder
	b.WriteString(lines[0] + "\r\n")
	for _, h := range lines[1:] {
		hl := strings.ToLower(h)
		drop := false
		for _, p := range droppedHeaders {
			if strings.HasPrefix(hl, p) {
				drop = true
				break
			}
		}
		if !drop {
			b.WriteString(h + "\r\n")
		}
	}
	b.WriteString(authHdr)
	b.WriteString("Connection: close\r\n\r\n")
	if _, err := upstream.Write(append([]byte(b.String()), body...)); err != nil {
		return err
	}
	pipe(client, upstream, false, "")
	return nil
}

// pipe copies both directions until EOF or until the tunnel has been idle
// for tunnelIdleTimeout. The first client→upstream chunk may be rewritten.
func pipe(client, upstream net.Conn, mutateFirst bool, tlsMode string) {
	var lastActive atomic.Int64
	lastActive.Store(time.Now().UnixNano())
	done := make(chan int64, 2)

	copyDir := func(src, dst net.Conn, mutate bool) {
		var total int64
		buf := make([]byte, pipeBufSize)
		defer func() {
			if tc, ok := dst.(*net.TCPConn); ok {
				tc.CloseWrite()
			}
			done <- total
		}()
		for {
			src.SetReadDeadline(time.Now().Add(5 * time.Second))
			n, err := src.Read(buf)
			if n > 0 {
				lastActive.Store(time.Now().UnixNano())
				data := buf[:n]
				if mutate {
					data = mutateClientHello(data, tlsMode)
					mutate = false
				}
				if _, werr := dst.Write(data); werr != nil {
					return
				}
				total += int64(len(data))
			}
			if err != nil {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() &&
					time.Since(time.Unix(0, lastActive.Load())) < tunnelIdleTimeout {
					continue
				}
				return
			}
		}
	}

	go copyDir(client, upstream, mutateFirst)
	go copyDir(upstream, client, false)
	total := <-done
	// Give the other direction a moment to drain after a half-close.
	select {
	case n := <-done:
		total += n
	case <-time.After(2 * time.Second):
		client.Close()
		upstream.Close()
		total += <-done
	}
	stats.BytesTransferred.Add(total)
}
