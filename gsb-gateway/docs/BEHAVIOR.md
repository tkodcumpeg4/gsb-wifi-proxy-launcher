# GSB_Gateway.exe (Python "Turbo v2") — davranış belgesi

Kaynak: `GSB_Gateway.exe` (PyInstaller, Python 3.11) içindeki `gsb_gateway` betiğinin
bytecode'undan çıkarıldı. Go kontrol katmanı (`gsb-gateway/control`) bu sözleşmeyi birebir
uygular; Rust çekirdeği (`gsb-gateway/core`) aynı yukarı akış (upstream) kurallarını TUN
modunda kullanır.

## Dosyalar ve portlar

| Ne | Değer |
|---|---|
| Panel | `127.0.0.1:8765` (`/`, `/index.html` → `ui.html`) |
| Yerel köprü proxy | `127.0.0.1:8880` |
| Çalışma dizini | exe'nin bulunduğu klasör (`list.txt`, `dns_cache.json`, `config.json`, `warp_wg.conf`, `ui.html`) |
| `--no-browser` | Tarayıcı otomatik açılmaz |

`config.json` anahtarları: `active_proxy_host`, `active_proxy_port`, `active_proxy_uni`,
`username`, `password`, `proxy_credentials{"host:port":{username,password}}`,
`warp_shield_enabled`, `ip_connect_mode`, `tls_fragment_enabled`, `bind_to_wifi`,
`system_proxy_enabled`, `custom_proxies[{uni,host,port}]`.
Her yüklemede `bind_to_wifi=true` ve `ip_connect_mode=false` zorlanır.

## Yukarı akış (upstream) kuralları

- **Kimlik:** `proxy_credentials["host:port"]`, yoksa genel `username/password` →
  `Proxy-Authorization: Basic base64(user:pw)`. Kullanıcı yoksa başlık yok.
- **Giden soket:** `TCP_NODELAY`, `SO_RCVBUF/SO_SNDBUF = 512 KiB`; `bind_to_wifi` açıksa
  ve Wi-Fi bağlıysa Wi-Fi IPv4 adresine `bind`. Proxy host adı `dns_cache.json` ile çözülür.
- **Soket havuzu (WARP-Shield):** kuyruk max 10; arka plan döngüsü her 1.2 sn'de havuz < 3
  ise aktif proxy'ye yeni TCP açar. Kullanımda 8 sn'den eski veya farklı proxy'ye ait soket
  atılır; havuz boşsa doğrudan bağlanılır. Her isabet `pooled_hits++`.
- **DoH:** `CONNECT 1.1.1.1:443` → TLS (SNI `cloudflare-dns.com`) → kalıcı bağlantı üzerinden
  `GET /dns-query?name=<h>&type=A` (`application/dns-json`). Sonuç 1800 sn önbellekte.
  2 deneme; hata olursa bağlantı sıfırlanır.

## Yerel köprü (8880)

1. İstemciden başlık sonuna kadar oku (max 16 KiB, 12 sn zaman aşımı).
2. **CONNECT host:port** (port yoksa 443):
   - `warp_shield` açık ve hedef IP değilse: DoH önbelleğinde varsa `ip:port` kullanılır;
     `ip_connect_mode` açıksa `doh_resolve_fast` (2 sn) ile çözülür. Her iki durumda ve
     ad ile CONNECT'te `anonymized_connects++`.
   - `tls_bypass = tls_fragment_enabled || warp_shield` ise `Host:` başlığı
     `www.google.com:443` olarak sahtelenir (istek satırı gerçek hedefi taşır).
   - Upstream yanıtı: `200` → istemciye `HTTP/1.1 200 Connection Established`;
     `407` → `502 Proxy Auth Failed` + `last_error`; diğer → yanıt aynen iletilir.
   - **TLS mutasyonu:** `tls_bypass` açıksa ilk istemci→sunucu paketi TLS ClientHello ise
     (`16 03 xx .. .. 01`) kayıt katmanı sürümü `16 03 04` olarak yeniden yazılır
     (DPI'ın ClientHello imzasını kaçırması için). Gerçek parçalama yapılmaz.
3. **Düz HTTP:** `Proxy-Authorization`, `Proxy-Connection`, `Via`, `X-Forwarded-For`
   başlıkları atılır, kimlik ve `Connection: close` eklenip istek proxy'ye iletilir.
4. **Boru:** iki yönlü 128 KiB okuma, her soket 15 sn boşta zaman aşımı (Squid `maxconn`
   dolmasın diye), `bytes_transferred` güncellenir.

## Tanılama

- **test_single_proxy(host,port,[user,pw],timeout=3.5):** `CONNECT 8.8.8.8:443`;
  200 → "200 OK (İnternet Tüneli Açık!)", 407 → "407 Açık (Giriş Bilgisi Gerekiyor)",
  403 → "403 Erişim Reddedildi (Yasaklı)"; hata → `tcp_ok=false`, "Kapalı (<Hata>)".
- **8888 testi:** [1] Wi-Fi, [2] bağlama modu, [3] doğrudan `8.8.8.8:443` (1.8 sn, Wi-Fi'ye
  zorunlu), [4] proxy `CONNECT 8.8.8.8:443`, [5] 200 ise `dns.google` üzerinden TLS + DoH,
  [6] `CONNECT 1.1.1.1:443` → `one.one.one.one/cdn-cgi/trace` ile çıkış IP / colo.
  `last_proxy_check` ve `last_8888_check` günceller; yanıt `{logs, proxy_check, check_8888}`.
- **Hız testi:** 4 paralel `CONNECT speed.cloudflare.com:443` → `/__down?bytes=600000`
  akışı, en fazla 4.5 sn; Mbps = bayt·8 / (sn·1e6).
- **Tarama:** `list.txt` + `custom_proxies`, 25 paralel `test_single_proxy(timeout=3)`.

## Sistem proxy

Açılırken: HKCU `Internet Settings` → `ProxyEnable=1`, `ProxyServer=127.0.0.1:8880`,
`ProxyOverride=localhost;127.*;10.*;192.168.*;<local>`; `InternetSetOptionW(39)` ve `(37)`;
HKCU `Environment` → `HTTP_PROXY/HTTPS_PROXY/ALL_PROXY=http://127.0.0.1:8880`,
`NO_PROXY=localhost,127.0.0.1,10.*,192.168.*`; `WM_SETTINGCHANGE("Environment")`;
`netsh winhttp set proxy 127.0.0.1:8880 "localhost;127.*;10.*;192.168.*"`.
Kapatılırken bunların tersi (`netsh winhttp reset proxy`).

## WireSock

`C:\Program Files\WireSock Secure Connect\bin\wiresock-client.exe run -config warp_wg.conf
-log-level none`; 0.8 sn sonra hâlâ çalışıyorsa başarılı. Durdururken `taskkill /F`.

## HTTP API

| Yol | Gövde | Yanıt |
|---|---|---|
| `GET /api/status` | – | `{wifi, config, sys_proxy, stats, proxies}` |
| `POST /api/proxy/select` | `{host?,port?,uni?,username?,password?,clear_password?}` | `{ok, diag}` (8888 testi çalışır) |
| `POST /api/proxy/add` | `{uni, host_port}` | `{ok, result}` / 400 `{ok:false,error}` |
| `POST /api/proxy/scan` | – | `{ok:true}` (arka planda) |
| `POST /api/test/8888` | – | `{ok, diag}` |
| `POST /api/test/speed` | – | `{ok, mbps, total_kb, seconds, log}` |
| `POST /api/warp/toggle` | `{mode:"shield", enable, ip_connect_mode?, tls_fragment?}` / `{mode:"wiresock", enable}` | `{ok, warp_shield_enabled}` / `{ok, message, wiresock_running}` |
| `POST /api/sysproxy/toggle` | `{enable}` | `{ok, sys_proxy}` |
| `POST /api/wifi/options` | `{bind_to_wifi?, connect_gsb?}` | `{ok, wifi, bind_to_wifi}` |

`config` (status içinde) şifreyi göndermez, yalnızca `has_password` döner. Her `proxies` öğesi:
`{uni, host, port, ip, key, scan, is_active, has_Saved_creds}`.

**Python sürümündeki hata:** `ui.html` `POST /api/wifi/bind` ve `POST /api/wifi/connect`
çağırır, Python ise yalnızca `/api/wifi/options` tanır (404). Go sürümü üçünü de karşılar;
`/api/wifi/connect` yanıtına `output` (netsh çıktısı) ekler.

## TUN modu ve UDP rölesi (Go/Rust sürümüne özgü)

- `gsb-core.exe` GSB-TUN Wintun adaptörünü kurar: `0.0.0.0/1` ve `128.0.0.0/1` TUN'a,
  aktif proxy IP'si `/32` olarak Wi-Fi (yoksa proxy'ye en iyi rotası olan arayüz)
  ağ geçidine yönlendirilir. Arayüzün DNS sunucuları da TUN'a çekilir.
- DNS: TUN DNS adresi `10.255.77.1`; tüm UDP/53 sorguları `198.18.0.0/15` havuzundan
  sahte IP alır. TCP akışları köprüye `CONNECT alan_adı:port` olarak gelir.
- `CONNECT *:80` köprüde mutlak URI'li normal proxy isteğine çevrilir (Squid yalnızca
  443'e CONNECT izni verir; 443 dışındaki portlar 403 alır).
- UDP: `udpgw_server` ayarlıysa gsb-core udpgw çerçevelerini bu adrese CONNECT ile
  gönderir. Köprü bu hedefi tanır, üniversite proxy'si üzerinden `CONNECT relay:443`
  açar, sertifika SHA-256 parmak izine sabitlenmiş TLS kurar, 32 baytlık token'ı yollar
  ve akışı borular. Ayar yoksa UDP düşürülür.
- Uplink izleme: Go her 5 sn'de bir uplink'i (Wi-Fi ya da proxy'ye en iyi rotası olan
  arayüz; arayüz index + IP + ağ geçidi) kontrol eder. Değişirse TUN durdurulup yeni
  uplink için yeniden kurulur (ör. Ethernet'ten GSB Wi-Fi'ye geçiş).
- Arayüzün DNS sunucularından loopback (`127.0.0.1` gibi yerel DNS vekilleri), link-local
  ve uplink'in kendi IP'si TUN'a çekilmez; aksi halde çekirdeğin 127.0.0.1'deki köprüye
  bağlantısı döngüye girer.
- Röle TLS'i: ilk ClientHello kaydı köprüdeki istemci TLS'iyle aynı şekilde işlenir
  (`tls_fragment_mode`). Röle sertifikası SHA-256 parmak iziyle doğrulanır; parmak izi
  tutmazsa bağlantı kurulmaz ve hata iki değeri de gösterir.
- `gsb-relay` (Linux): TLS :443, token doğrulaması, udpgw çözümü, akış (conn_id) başına
  kalıcı UDP soketi; özel/loopback hedefler varsayılan olarak reddedilir.

| Yol | Gövde | Yanıt |
|---|---|---|
| `POST /api/tun/toggle` | `{enable}` | `{ok, tun, error?, need_admin?}` |
| `POST /api/tun/udpgw` | `{server:"IP:443", token?, fingerprint?}` | `{ok, note}` |
| `POST /api/relay/test` | – | `{ok, latency_ms}` / `{ok:false, error}` |
| `POST /api/app/elevate` | – | UAC ile `--tun` bayrağıyla yeniden başlatır |
| `POST /api/app/quit` | – | TUN'u kapatır, süreçten çıkar |

## Tepsi (tray) uygulaması ve force-TUN

- `GSB_Gateway.exe` artık konsolsuz (`-H windowsgui`) bir tepsi uygulaması. `main` HTTP
  sunucusunu goroutine'de başlatır, ana goroutine'de `systray.Run` bloke eder. `--no-tray`
  bayrağı başsız/konsol modudur (sunucu + `select{}`), test ve servis senaryoları için.
- Tepsi: sol tık paneli Edge/Chrome `--app=` kipinde (çerçevesiz uygulama penceresi) açar,
  sağ tık menü (Sanal Adaptör, Trafiği TUN'a Zorla, Sistem Proxy, Çıkış). Menü durumları
  3 sn'de bir canlı güncellenir. Pencere kapatmak süreci kapatmaz; Çıkış kapatır.
- `openPanel` Edge → Chrome → varsayılan tarayıcı sırasını dener; `--user-data-dir` olarak
  `<veri>\webview` kullanır.
- **force_tun** (config `force_tun`, `POST /api/tun/force {enable}`): TUN açılınca uplink
  IPv4 arayüz metriği 9999'a çekilir (`Set-NetIPInterface`, orijinal metrik saklanır ve
  TUN kapanınca geri yüklenir). Proxy/röle `/32` baypas rotaları önek uzunluğuyla kazandığı
  için uplink onları taşımaya devam eder; geri kalan her şey GSB-TUN'u tercih eder.

## Gömülü panel (WebView2) ve kalıcı kullanım

- Panel artık süreç içinde gömülü bir WebView2 penceresi (go-webview2); Edge dışarıdan
  açılmaz. Tek örnek: `openPanel` açıksa (veya oluşturuluyorsa) ikinci pencere açmaz,
  mevcut pencereyi öne getirir (`Dispatch`+`SetForegroundWindow`). WebView2 çalışma zamanı
  yoksa Edge/Chrome `--app` penceresine düşer. Pencere kendi kilitli OS thread'inde `Run()`
  ile çalışır; systray ana goroutine'de.
- `total_bytes`: ömür boyu veri `usage.json`'da (AppData) tutulur; `baseTotalBytes` açılışta
  yüklenir, panelde "Toplam Veri" olarak gösterilir (oturum verisi ayrı). 30 sn'de bir ve
  çıkışta kaydedilir.
