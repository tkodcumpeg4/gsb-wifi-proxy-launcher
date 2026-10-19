package main

import (
	"crypto/tls"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---- Proxy list (list.txt + custom_proxies) ----

type ProxyItem struct {
	Uni           string      `json:"uni"`
	Host          string      `json:"host"`
	Port          int         `json:"port"`
	IP            string      `json:"ip"`
	Key           string      `json:"key"`
	Scan          *ProxyCheck `json:"scan"`
	IsActive      bool        `json:"is_active"`
	HasSavedCreds bool        `json:"has_Saved_creds"`
}

var (
	hostPortRe  = regexp.MustCompile(`^([a-zA-Z0-9.\-]+):(\d+)$`)
	uniSkipPref = []string{"Burada bulunan", "<br>", "Kaynak:", "Üniversite\t", "Üniversite Proxy Sunucuları", "Aşağıdaki tabloda", "Proxy sunucularının"}
)

// parseProxyList reads list.txt (forum paste format: university name line
// followed by host:port lines) plus custom proxies. resolve=false only uses
// the DNS cache so /api/status never blocks on lookups.
func parseProxyList(resolve bool) []ProxyItem {
	var items []ProxyItem
	seen := map[string]bool{}
	ipOf := func(h string) string {
		ip := cachedProxyIP(h)
		if ip == "" && resolve {
			ip = resolveProxyHost(h)
		}
		if ip == "" {
			return h
		}
		return ip
	}
	listRaw, err := os.ReadFile(dataPath("list.txt"))
	if err != nil {
		listRaw, err = os.ReadFile(resourcePath("list.txt"))
	}
	if err == nil {
		raw := listRaw
		uni := "Üniversite Proxy"
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if m := hostPortRe.FindStringSubmatch(line); m != nil {
				port, _ := strconv.Atoi(m[2])
				key := fmtHostPort(m[1], port)
				if !seen[key] {
					seen[key] = true
					items = append(items, ProxyItem{Uni: uni, Host: m[1], Port: port, IP: ipOf(m[1]), Key: key})
				}
				continue
			}
			skip := false
			for _, p := range uniSkipPref {
				if strings.HasPrefix(line, p) {
					skip = true
					break
				}
			}
			if !skip {
				uni = line
			}
		}
	}
	for _, cp := range getConfig().CustomProxies {
		key := fmtHostPort(cp.Host, cp.Port)
		if seen[key] {
			continue
		}
		seen[key] = true
		uni := cp.Uni
		if uni == "" {
			uni = "Özel Proxy"
		}
		items = append(items, ProxyItem{Uni: uni, Host: cp.Host, Port: cp.Port, IP: ipOf(cp.Host), Key: key})
	}
	return items
}

// ---- Single proxy probe ----

// testSingleProxy sends CONNECT 8.8.8.8:443. user==nil uses saved creds.
func testSingleProxy(host string, port int, user, pass *string, timeout time.Duration) *ProxyCheck {
	ip := resolveProxyHost(host)
	if ip == "" {
		return &ProxyCheck{StatusText: "DNS Çözümlenemedi", IP: host}
	}
	auth := ""
	if user == nil {
		auth = proxyAuthHeader(host, port)
	} else if *user != "" {
		auth = basicAuthLine(*user, *pass)
	}
	t0 := time.Now()
	conn, err := dialOutbound(net.JoinHostPort(ip, strconv.Itoa(port)), timeout, nil)
	if err != nil {
		return &ProxyCheck{StatusText: "Kapalı (" + errName(err) + ")", IP: ip}
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(timeout))
	conn.Write([]byte("CONNECT 8.8.8.8:443 HTTP/1.1\r\nHost: 8.8.8.8:443\r\n" + auth + "\r\n"))
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil && n == 0 {
		return &ProxyCheck{StatusText: "Kapalı (" + errName(err) + ")", IP: ip}
	}
	dt := msSince(t0)
	first := "Boş Yanıt"
	if n > 0 {
		first = strings.SplitN(string(buf[:n]), "\r\n", 2)[0]
	}
	code := statusCode(first)
	text := first
	switch code {
	case 200:
		text = "200 OK (İnternet Tüneli Açık!)"
	case 407:
		text = "407 Açık (Giriş Bilgisi Gerekiyor)"
	case 403:
		text = "403 Erişim Reddedildi (Yasaklı)"
	}
	return &ProxyCheck{TCPOK: true, StatusCode: code, StatusText: text, LatencyMs: dt, IP: ip}
}

func errName(err error) string {
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return "timeout"
	}
	if oe, ok := err.(*net.OpError); ok && oe.Err != nil {
		return oe.Err.Error()
	}
	return err.Error()
}

// ---- 8.8.8.8 end-to-end diagnosis ----

type DiagResult struct {
	Logs       []string    `json:"logs"`
	ProxyCheck *ProxyCheck `json:"proxy_check"`
	Check8888  *Check8888  `json:"check_8888"`
}

var diagMu sync.Mutex

func run8888Test() DiagResult {
	diagMu.Lock()
	defer diagMu.Unlock()
	var logs []string
	logf := func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }

	wifi := getWifiInfo(true)
	if wifi.Connected {
		logf("[1] GSB Wi-Fi Adaptörü: Bağlı (%s, GW: %s)", wifi.IP, wifi.Gateway)
	} else {
		logf("[1] GSB Wi-Fi Adaptörü: Bağlı Değil")
	}
	if getConfig().BindToWifi {
		logf("[2] Soket Bağlama Modu: GSB Wi-Fi Adaptörüne Zorunlu (%s)", wifi.IP)
	} else {
		logf("[2] Soket Bağlama Modu: Varsayılan Windows Rotası")
	}

	res := &Check8888{ExitIP: "-", ExitOrg: "-", Proxy8888Detail: "Bağlantı kurulamadı"}
	force := true
	t0 := time.Now()
	if c, err := dialOutbound("8.8.8.8:443", 1800*time.Millisecond, &force); err == nil {
		c.Close()
		res.Direct8888OK, res.DirectLatencyMs = true, msSince(t0)
		logf("[3] Doğrudan 8.8.8.8 (Proxysiz GSB Wi-Fi): ULAŞILDI (%d ms)", *res.DirectLatencyMs)
	} else {
		logf("[3] Doğrudan 8.8.8.8 (Proxysiz GSB Wi-Fi): ENGELLİ (%s)", errName(err))
	}

	pHost, pPort, pIP := activeProxy()
	var pc *ProxyCheck
	if pIP == "" {
		res.Proxy8888Detail = "Proxy DNS çözümlenemedi"
		logf("[4] Proxy (%s:%d): DNS çözümlenemedi!", pHost, pPort)
	} else {
		t1 := time.Now()
		conn, code, first, err := connectTunnel("8.8.8.8:443", 5*time.Second)
		if err != nil {
			pc = &ProxyCheck{Timestamp: nowUnix(), StatusText: "Bağlanamadı (" + errName(err) + ")"}
			res.Proxy8888Detail = "Proxy'ye ulaşılamadı (" + errName(err) + ")"
			logf("[4] Proxy Hatası: %v", err)
		} else {
			tcpMs := msSince(t1)
			text := first
			switch code {
			case 200:
				text = "200 Tünel Açık"
			case 407:
				text = "407 Kimlik Doğrulama Gerekli"
			}
			pc = &ProxyCheck{Timestamp: nowUnix(), TCPOK: true, StatusCode: code, StatusText: text, LatencyMs: tcpMs}
			switch code {
			case 200:
				logf("[4] Proxy (%s:%d): HTTP 200 Tünel Açıldı (%d ms)", pHost, pPort, *tcpMs)
				if err := dohProbe(conn, "dns.google", "/resolve?name=youtube.com&type=A"); err == nil {
					res.Proxy8888OK, res.Proxy8888LatencyMs = true, msSince(t1)
					res.Proxy8888Detail = fmt.Sprintf("8.8.8.8 Şifreli Tünel Başarılı (%d ms)", *res.Proxy8888LatencyMs)
					logf("[5] 8.8.8.8 Doğrulaması: BAŞARILI! (Uçtan uca TLS + DNS yanıtı: %d ms)", *res.Proxy8888LatencyMs)
				} else {
					res.Proxy8888Detail = "TLS doğrulaması başarısız (" + errName(err) + ")"
					logf("[5] 8.8.8.8 Doğrulaması: BAŞARISIZ (%v)", err)
				}
			case 407:
				conn.Close()
				if proxyAuthHeader(pHost, pPort) != "" {
					res.Proxy8888Detail = "Proxy 407 Döndü: Kullanıcı adı/şifre hatalı!"
				} else {
					res.Proxy8888Detail = "Proxy Açık (407): İnternete çıkmak için kullanıcı adı ve şifrenizi girin."
				}
				logf("[4] Proxy (%s:%d) Erişilebilir (%d ms) fakat %s", pHost, pPort, *tcpMs, res.Proxy8888Detail)
			default:
				conn.Close()
				res.Proxy8888Detail = "Proxy Yanıtı: " + first
				logf("[4] Proxy Yanıtı: %s", first)
			}
		}
		stats.mu.Lock()
		stats.lastProxyCheck = pc
		stats.mu.Unlock()
	}

	if res.Proxy8888OK {
		if ip, colo, err := cloudflareTrace(); err == nil {
			res.ExitIP = ip
			if colo != "" {
				res.ExitOrg = "Cloudflare " + colo + " (TLS 1.3)"
			}
			logf("[6] Çıkış IP Adresi: %s (%s)", res.ExitIP, res.ExitOrg)
			logf("[NOT] fast.com Netflix (nflxvideo.net) sunucularını kullandığı ve üniversitenin FortiGate güvenlik duvarı Netflix'i blokladığı için fast.com 0 gösterir. Gerçek hız testi için paneldeki 'Gerçek Hız Testi' butonunu veya speed.cloudflare.com'u kullanın!")
		}
	}

	res.Timestamp = nowUnix()
	stats.mu.Lock()
	stats.last8888Check = res
	pcOut := stats.lastProxyCheck
	stats.mu.Unlock()
	return DiagResult{Logs: logs, ProxyCheck: pcOut, Check8888: res}
}

// dohProbe performs a TLS GET on an open tunnel and requires an HTTP answer.
func dohProbe(conn net.Conn, sni, path string) error {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	tc := tls.Client(conn, &tls.Config{ServerName: sni})
	if _, err := tc.Write([]byte("GET " + path + " HTTP/1.1\r\nHost: " + sni + "\r\nAccept: application/dns-json\r\nConnection: close\r\n\r\n")); err != nil {
		return err
	}
	buf := make([]byte, 2048)
	n, err := tc.Read(buf)
	if n == 0 {
		return err
	}
	if !strings.HasPrefix(string(buf[:n]), "HTTP/") {
		return fmt.Errorf("beklenmeyen yanıt")
	}
	return nil
}

var (
	traceIPRe   = regexp.MustCompile(`ip=([^\r\n]+)`)
	traceColoRe = regexp.MustCompile(`colo=([^\r\n]+)`)
)

func cloudflareTrace() (ip, colo string, err error) {
	conn, code, line, err := connectTunnel("1.1.1.1:443", 4*time.Second)
	if err != nil {
		return "", "", err
	}
	defer conn.Close()
	if code != 200 {
		return "", "", fmt.Errorf("%s", line)
	}
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	tc := tls.Client(conn, &tls.Config{ServerName: "one.one.one.one"})
	tc.Write([]byte("GET /cdn-cgi/trace HTTP/1.1\r\nHost: one.one.one.one\r\nConnection: close\r\n\r\n"))
	raw, _ := io.ReadAll(io.LimitReader(tc, 8192))
	if m := traceIPRe.FindSubmatch(raw); m != nil {
		ip = strings.TrimSpace(string(m[1]))
	}
	if m := traceColoRe.FindSubmatch(raw); m != nil {
		colo = strings.TrimSpace(string(m[1]))
	}
	if ip == "" {
		return "", "", fmt.Errorf("trace yanıtı okunamadı")
	}
	return ip, colo, nil
}

// ---- Multi-stream speed test ----

func runSpeedTest() map[string]any {
	if _, _, ip := activeProxy(); ip == "" {
		return map[string]any{"ok": false, "error": "Proxy IP çözümlenemedi"}
	}
	const streams = 4
	var wg sync.WaitGroup
	totals := make([]int64, streams)
	t0 := time.Now()
	for i := range streams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			totals[i] = speedWorker()
		}()
	}
	wg.Wait()
	dt := math.Max(0.1, time.Since(t0).Seconds())
	var total int64
	for _, t := range totals {
		total += t
	}
	mbps := math.Round(float64(total)*8/(dt*1e6)*100) / 100
	stats.mu.Lock()
	stats.lastSpeedMbps = mbps
	stats.mu.Unlock()
	return map[string]any{
		"ok":       true,
		"mbps":     mbps,
		"total_kb": total / 1024,
		"seconds":  math.Round(dt*100) / 100,
		"log":      fmt.Sprintf("[SPEED] %d Paralel Tünel Hız Testi Tamamlandı: %v Mbps (%d KB / %.2f sn)", streams, mbps, total/1024, dt),
	}
}

func speedWorker() int64 {
	conn, code, _, err := connectTunnel("speed.cloudflare.com:443", 6*time.Second)
	if err != nil {
		return 0
	}
	defer conn.Close()
	if code != 200 {
		return 0
	}
	end := time.Now().Add(4500 * time.Millisecond)
	conn.SetDeadline(end)
	tc := tls.Client(conn, &tls.Config{ServerName: "speed.cloudflare.com"})
	if _, err := tc.Write([]byte("GET /__down?bytes=25000000 HTTP/1.1\r\nHost: speed.cloudflare.com\r\nConnection: close\r\n\r\n")); err != nil {
		return 0
	}
	var tot int64
	buf := make([]byte, 64*1024)
	for time.Now().Before(end) {
		n, err := tc.Read(buf)
		tot += int64(n)
		if err != nil {
			break
		}
	}
	return tot
}

// ---- Background scan ----

func scanAllProxies() {
	stats.mu.Lock()
	if stats.scanInProgress {
		stats.mu.Unlock()
		return
	}
	stats.scanInProgress = true
	stats.mu.Unlock()
	defer func() {
		stats.mu.Lock()
		stats.scanInProgress = false
		stats.mu.Unlock()
	}()

	items := parseProxyList(true)
	sem := make(chan struct{}, 25)
	var wg sync.WaitGroup
	for _, it := range items {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			stats.setScanResult(it.Key, testSingleProxy(it.Host, it.Port, nil, nil, 3*time.Second))
		}()
	}
	wg.Wait()
}
