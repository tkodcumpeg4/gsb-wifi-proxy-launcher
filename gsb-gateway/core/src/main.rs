//! gsb-core — GSB Gateway data plane.
//!
//! Creates the "GSB-TUN" adapter (Wintun on Windows, /dev/net/tun on Linux),
//! routes all IPv4 traffic into it and
//! terminates it in a userspace TCP/IP stack (tun2proxy/ipstack):
//!
//! * TCP  → `CONNECT host:port` to the Go bridge (127.0.0.1:8880), which
//!          applies WARP-Shield (socket pool, auth, TLS obfuscation) and talks
//!          to the university proxy. Host names come from the fake-IP DNS.
//! * DNS  → answered locally (at 10.255.77.1) with fake IPs from 198.18.0.0/15, so the real name
//!          reaches the proxy and the GSB DNS filter is never consulted.
//! * UDP  → tunnelled as udpgw frames over a CONNECT tunnel when `--udpgw` is
//!          given; otherwise dropped (QUIC then falls back to TCP).
//!
//! Routes are added non-persistently and removed on exit; when the process
//! dies the adapter disappears and the OS drops every route through it.
//!
//! Control protocol: the parent writes `stop` (or closes stdin) to shut down.
//! Events are JSON lines on stdout: ready / traffic / error / stopped.

mod routes;

use std::net::{IpAddr, Ipv4Addr, SocketAddr};
use std::str::FromStr;

use anyhow::{bail, Context, Result};
use serde_json::json;
use tokio::io::AsyncBufReadExt;
use tun2proxy::{ArgDns, Args, CancellationToken, ProxyParameters, TrafficStatus};

const TUN_NAME: &str = "GSB-TUN";
const TUN_ADDR: Ipv4Addr = Ipv4Addr::new(10, 255, 77, 2);
const TUN_GW: Ipv4Addr = Ipv4Addr::new(10, 255, 77, 1);
const TUN_MASK: Ipv4Addr = Ipv4Addr::new(255, 255, 255, 0);
/// Address advertised as the adapter's DNS server; any UDP/53 is intercepted.
/// It must lie outside FAKE_IP_POOL, whose first allocations are 198.18.0.x.
const TUN_DNS: Ipv4Addr = TUN_GW;
const FAKE_IP_POOL: &str = "198.18.0.0/15";

struct Opts {
    bridge: SocketAddr,
    wifi_if: u32,
    wifi_gw: Ipv4Addr,
    wifi_ip: Option<Ipv4Addr>,
    bypass: Vec<String>,
    dns_hijack: Vec<Ipv4Addr>,
    udpgw: Option<SocketAddr>,
    wintun: Option<String>,
    mtu: u16,
    log: Option<String>,
}

fn usage() -> ! {
    eprintln!(
        "kullanım: gsb-core --bridge 127.0.0.1:8880 --wifi-if <index> --wifi-gw <ip> \
         [--wifi-ip <ip>] [--bypass <ip[/len]>]... [--dns-hijack <ip>]... [--udpgw <ip:port>] [--wintun <dll>] \
         [--mtu 1500] [--log <RUST_LOG filtresi>]"
    );
    std::process::exit(2);
}

fn parse_opts() -> Result<Opts> {
    let mut o = Opts {
        bridge: "127.0.0.1:8880".parse()?,
        wifi_if: 0,
        wifi_gw: Ipv4Addr::UNSPECIFIED,
        wifi_ip: None,
        bypass: vec![],
        dns_hijack: vec![],
        udpgw: None,
        wintun: None,
        mtu: 1500,
        log: None,
    };
    let mut it = std::env::args().skip(1);
    while let Some(flag) = it.next() {
        let mut val = || it.next().unwrap_or_else(|| usage());
        match flag.as_str() {
            "--bridge" => o.bridge = val().parse().context("--bridge")?,
            "--wifi-if" => o.wifi_if = val().parse().context("--wifi-if")?,
            "--wifi-gw" => o.wifi_gw = val().parse().context("--wifi-gw")?,
            "--wifi-ip" => o.wifi_ip = Some(val().parse().context("--wifi-ip")?),
            "--bypass" => o.bypass.push(val()),
            "--dns-hijack" => o.dns_hijack.push(val().parse().context("--dns-hijack")?),
            "--udpgw" => o.udpgw = Some(val().parse().context("--udpgw")?),
            "--wintun" => o.wintun = Some(val()),
            "--mtu" => o.mtu = val().parse().context("--mtu")?,
            "--log" => o.log = Some(val()),
            _ => usage(),
        }
    }
    if o.wifi_if == 0 || o.wifi_gw.is_unspecified() {
        bail!("--wifi-if ve --wifi-gw zorunlu (Wi-Fi bağlı değil mi?)");
    }
    Ok(o)
}

fn emit(v: serde_json::Value) {
    use std::io::Write;
    let mut out = std::io::stdout().lock();
    let _ = writeln!(out, "{v}");
    let _ = out.flush();
}

unsafe extern "C" fn on_traffic(st: *const TrafficStatus, _ctx: *mut std::ffi::c_void) {
    if let Some(st) = unsafe { st.as_ref() } {
        emit(json!({"event": "traffic", "tx": st.tx, "rx": st.rx}));
    }
}

#[tokio::main]
async fn main() {
    let opts = parse_opts();
    // --log beats RUST_LOG: pkexec (Linux) does not pass the environment on.
    let mut logger = env_logger::Builder::from_env(env_logger::Env::default().default_filter_or("warn,tun2proxy=off,ipstack=off"));
    if let Some(filter) = opts.as_ref().ok().and_then(|o| o.log.as_deref()) {
        logger.parse_filters(filter);
    }
    logger.init();
    let code = match async { run(opts?).await }.await {
        Ok(()) => 0,
        Err(e) => {
            emit(json!({"event": "error", "message": format!("{e:#}")}));
            1
        }
    };
    emit(json!({"event": "stopped"}));
    std::process::exit(code);
}

async fn run(o: Opts) -> Result<()> {
    let mut cfg = tun::Configuration::default();
    cfg.tun_name(TUN_NAME)
        .address(TUN_ADDR)
        .netmask(TUN_MASK)
        .destination(TUN_GW)
        .mtu(o.mtu)
        .up();
    #[cfg(windows)]
    cfg.metric(1); // Windows-only knob; Linux routes carry their own metric
    #[cfg(windows)]
    cfg.platform_config(|p| {
        // Fixed GUID so Windows reuses one network profile for the adapter.
        p.device_guid(0x6753_425f_5455_4e5f_6773_625f_7475_6e30);
        p.dns_servers(&[IpAddr::V4(TUN_DNS)]);
        if let Some(dll) = &o.wintun {
            p.wintun_file(dll.clone());
        }
    });
    #[cfg(target_os = "linux")]
    cfg.platform_config(|p| {
        // CAP_NET_ADMIN is enough; the crate would otherwise demand euid 0.
        p.ensure_root_privileges(false);
    });
    let device = tun::create_as_async(&cfg).context(if cfg!(windows) {
        "Wintun adaptörü oluşturulamadı (yönetici yetkisi ve wintun.dll gerekli)"
    } else {
        "TUN adaptörü oluşturulamadı (root veya CAP_NET_ADMIN ve /dev/net/tun gerekli)"
    })?;
    let tun_index = {
        use tun::AbstractDevice;
        device.tun_index().context("adaptör index alınamadı")? as u32
    };

    routes::link_up(TUN_NAME).context("adaptör açılamadı")?;
    let mut table = routes::RouteTable::new(tun_index, TUN_GW, o.wifi_if, o.wifi_gw);
    // Bypass first so the upstream proxy never loops through the tunnel.
    for b in &o.bypass {
        table.bypass(b).with_context(|| format!("bypass route {b}"))?;
    }
    // Pull the Wi-Fi DNS servers into the tunnel too, otherwise the OS may ask
    // the (filtering) GSB resolver directly over the on-link Wi-Fi route.
    for d in &o.dns_hijack {
        table.into_tunnel(&format!("{d}/32"))?;
    }
    table.into_tunnel("0.0.0.0/1")?;
    table.into_tunnel("128.0.0.0/1")?;
    #[cfg(target_os = "linux")]
    if let Some(src) = o.wifi_ip {
        table.pin_source(src).context("Wi-Fi kaynak kuralı")?;
    }
    routes::set_tunnel_dns(TUN_NAME, TUN_DNS);
    routes::flush_dns();

    let mut args = Args::default();
    args.proxy(ProxyParameters::from_str(&format!("http://{}", o.bridge)).map_err(|e| anyhow::anyhow!("{e}"))?)
        .dns(ArgDns::Virtual)
        .dns_addr(IpAddr::V4(TUN_DNS))
        .setup(false)
        .ipv6_enabled(false);
    args.virtual_dns_pool = FAKE_IP_POOL.parse().map_err(|e| anyhow::anyhow!("{e:?}"))?;
    args.max_sessions = 4096;
    args.tcp_timeout = 600;
    args.udp_timeout = 30;
    if let Some(gw) = o.udpgw {
        args.udpgw_server(gw);
    }

    unsafe { tun2proxy::tun2proxy_set_traffic_status_callback(1, Some(on_traffic), std::ptr::null_mut()) };

    let token = CancellationToken::new();
    let stopper = token.clone();
    tokio::spawn(async move {
        let mut lines = tokio::io::BufReader::new(tokio::io::stdin()).lines();
        loop {
            match lines.next_line().await {
                Ok(Some(l)) if l.trim() != "stop" => continue,
                _ => break,
            }
        }
        stopper.cancel();
    });
    let ctrl_c = token.clone();
    tokio::spawn(async move {
        let _ = tokio::signal::ctrl_c().await;
        ctrl_c.cancel();
    });

    emit(json!({
        "event": "ready",
        "tun_name": TUN_NAME,
        "tun_index": tun_index,
        "tun_addr": TUN_ADDR.to_string(),
        "dns": TUN_DNS.to_string(),
        "udp": o.udpgw.is_some(),
    }));

    let result = tun2proxy::run(device, o.mtu, args, token).await;
    table.restore();
    routes::flush_dns();
    result.map(|_| ()).map_err(|e| anyhow::anyhow!("tun2proxy: {e}"))
}
