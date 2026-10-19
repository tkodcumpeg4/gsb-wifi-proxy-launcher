package main

import (
	"sync"
	"sync/atomic"
	"time"
)

type ProxyCheck struct {
	Timestamp  float64 `json:"timestamp,omitempty"`
	TCPOK      bool    `json:"tcp_ok"`
	StatusCode int     `json:"status_code"`
	StatusText string  `json:"status_text"`
	LatencyMs  *int64  `json:"latency_ms"`
	IP         string  `json:"ip,omitempty"`
}

type Check8888 struct {
	Timestamp          float64 `json:"timestamp"`
	Direct8888OK       bool    `json:"direct_8888_ok"`
	DirectLatencyMs    *int64  `json:"direct_latency_ms"`
	Proxy8888OK        bool    `json:"proxy_8888_ok"`
	Proxy8888LatencyMs *int64  `json:"proxy_8888_latency_ms"`
	Proxy8888Detail    string  `json:"proxy_8888_detail"`
	ExitIP             string  `json:"exit_ip"`
	ExitOrg            string  `json:"exit_org"`
}

type Stats struct {
	TotalConnections   atomic.Int64
	ActiveConnections  atomic.Int64
	DohQueries         atomic.Int64
	AnonymizedConnects atomic.Int64
	PooledHits         atomic.Int64
	BytesTransferred   atomic.Int64
	// baseTotalBytes is the cumulative data from previous sessions, loaded at
	// startup; the lifetime total shown to the user is base + BytesTransferred.
	baseTotalBytes atomic.Int64

	mu              sync.Mutex
	lastError       string
	lastSpeedMbps   float64
	lastProxyCheck  *ProxyCheck
	last8888Check   *Check8888
	wiresockRunning bool
	scanInProgress  bool
	scanResults     map[string]*ProxyCheck
}

var stats = &Stats{
	lastProxyCheck: &ProxyCheck{StatusText: "Henüz test edilmedi"},
	last8888Check:  &Check8888{Proxy8888Detail: "Henüz test edilmedi", ExitIP: "-", ExitOrg: "-"},
	scanResults:    map[string]*ProxyCheck{},
}

func (s *Stats) setError(msg string) {
	s.mu.Lock()
	s.lastError = msg
	s.mu.Unlock()
}

func (s *Stats) scanResult(key string) *ProxyCheck {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scanResults[key]
}

func (s *Stats) setScanResult(key string, r *ProxyCheck) {
	s.mu.Lock()
	s.scanResults[key] = r
	s.mu.Unlock()
}

// snapshot returns the JSON shape the panel reads under data.stats.
func (s *Stats) snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	scan := make(map[string]*ProxyCheck, len(s.scanResults))
	for k, v := range s.scanResults {
		scan[k] = v
	}
	return map[string]any{
		"total_connections":        s.TotalConnections.Load(),
		"active_connections":       s.ActiveConnections.Load(),
		"doh_queries":              s.DohQueries.Load(),
		"anonymized_connects":      s.AnonymizedConnects.Load(),
		"pooled_hits":              s.PooledHits.Load(),
		"bytes_transferred":        s.BytesTransferred.Load(),
		"total_bytes":              s.baseTotalBytes.Load() + s.BytesTransferred.Load(),
		"last_error":               s.lastError,
		"fortinet_blocked_domains": []string{},
		"last_speed_mbps":          s.lastSpeedMbps,
		"last_proxy_check":         s.lastProxyCheck,
		"last_8888_check":          s.last8888Check,
		"wiresock_running":         s.wiresockRunning,
		"scan_in_progress":         s.scanInProgress,
		"scan_results":             scan,
	}
}

func nowUnix() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func msSince(t time.Time) *int64 {
	v := time.Since(t).Milliseconds()
	return &v
}
