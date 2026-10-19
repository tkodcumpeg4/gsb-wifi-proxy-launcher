package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// DoH runs over a CONNECT tunnel to 1.1.1.1:443; http.Transport keeps the
// TLS session alive between queries, like the Python DOH_TLS_SOCK did.
var dohClient = &http.Client{
	Timeout: 3 * time.Second,
	Transport: &http.Transport{
		DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			conn, code, line, err := connectTunnel("1.1.1.1:443", 4*time.Second)
			if err != nil {
				return nil, err
			}
			if code != 200 {
				conn.Close()
				return nil, fmt.Errorf("DoH CONNECT reddedildi: %s", line)
			}
			tc := tls.Client(conn, &tls.Config{ServerName: "cloudflare-dns.com"})
			if err := tc.HandshakeContext(ctx); err != nil {
				conn.Close()
				return nil, err
			}
			return tc, nil
		},
		MaxIdleConnsPerHost: 1,
		IdleConnTimeout:     90 * time.Second,
	},
}

type dohEntry struct {
	ip  string
	exp time.Time
}

var dohCache = struct {
	sync.Mutex
	m map[string]dohEntry
}{m: map[string]dohEntry{}}

const dohTTL = 30 * time.Minute

// dohCached returns a still-valid cached answer without querying.
func dohCached(host string) string {
	dohCache.Lock()
	defer dohCache.Unlock()
	if e, ok := dohCache.m[host]; ok && time.Now().Before(e.exp) {
		return e.ip
	}
	return ""
}

// dohResolve resolves an A record through Cloudflare DoH (2 attempts).
func dohResolve(host string, timeout time.Duration) string {
	if isIPv4(host) {
		return host
	}
	if ip := dohCached(host); ip != "" {
		return ip
	}
	u := "https://cloudflare-dns.com/dns-query?name=" + url.QueryEscape(host) + "&type=A"
	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		req.Header.Set("Accept", "application/dns-json")
		resp, err := dohClient.Do(req)
		if err != nil {
			cancel()
			continue
		}
		var data struct {
			Answer []struct {
				Type int    `json:"type"`
				Data string `json:"data"`
			} `json:"Answer"`
		}
		err = json.NewDecoder(resp.Body).Decode(&data)
		resp.Body.Close()
		cancel()
		if err != nil {
			continue
		}
		for _, a := range data.Answer {
			if a.Type == 1 && isIPv4(a.Data) {
				dohCache.Lock()
				dohCache.m[host] = dohEntry{a.Data, time.Now().Add(dohTTL)}
				dohCache.Unlock()
				stats.DohQueries.Add(1)
				return a.Data
			}
		}
		return ""
	}
	return ""
}
