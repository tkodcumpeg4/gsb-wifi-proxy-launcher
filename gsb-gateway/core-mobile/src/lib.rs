//! gsb-core-mobile — the GSB Gateway data plane on Android.
//!
//! VpnService creates the TUN interface and routes; this library only takes
//! its file descriptor and terminates the traffic in tun2proxy, exactly like
//! the desktop gsb-core does after it has set up Wintun / /dev/net/tun:
//!
//! * TCP → `CONNECT host:port` to the proxy given by the app (the university
//!   proxy directly in phase 0, the Go bridge later).
//! * DNS → answered locally with fake IPs from 198.18.0.0/15, so the real name
//!   reaches the proxy.
//! * UDP → udpgw frames to the relay when one is configured, else dropped.
//!
//! JNI surface (class `com.gsb.gateway.Core`): `start` blocks until `stop`
//! is called or the proxy fails; `rx` / `tx` report traffic counters.

#![cfg(target_os = "android")]

use std::net::{IpAddr, Ipv4Addr};
use std::str::FromStr;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Mutex;

use jni::{
    objects::{JClass, JString},
    sys::{jint, jlong},
    Env, EnvUnowned,
};
use tun2proxy::{ArgDns, Args, CancellationToken, ProxyParameters, TrafficStatus};

/// Matches the address the Kotlin side gives the VPN interface as its DNS
/// server; tun2proxy answers every query sent there.
const TUN_DNS: Ipv4Addr = Ipv4Addr::new(10, 255, 77, 1);
const FAKE_IP_POOL: &str = "198.18.0.0/15";

static RUNNING: Mutex<Option<CancellationToken>> = Mutex::new(None);
static TX: AtomicU64 = AtomicU64::new(0);
static RX: AtomicU64 = AtomicU64::new(0);

unsafe extern "C" fn on_traffic(st: *const TrafficStatus, _ctx: *mut std::ffi::c_void) {
    if let Some(st) = unsafe { st.as_ref() } {
        TX.store(st.tx, Ordering::Relaxed);
        RX.store(st.rx, Ordering::Relaxed);
    }
}

/// Runs the tunnel on `tun_fd` until `stop`. `proxy_url` is
/// `http://user:pass@host:port` (credentials URL-encoded); `udpgw` is
/// `ip:port` or empty. Returns 0 on a clean stop, a negative code on error.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn Java_com_gsb_gateway_Core_start(
    mut env: EnvUnowned<'_>,
    _class: JClass<'_>,
    tun_fd: jint,
    proxy_url: JString<'_>,
    mtu: jint,
    udpgw: JString<'_>,
) -> jint {
    env.with_env(|env: &mut Env| -> jni::errors::Result<jint> {
        let proxy_url = proxy_url.try_to_string(env)?;
        let udpgw = udpgw.try_to_string(env)?;
        Ok(run(tun_fd, &proxy_url, mtu, &udpgw))
    })
    .resolve::<jni::errors::LogErrorAndDefault>()
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn Java_com_gsb_gateway_Core_stop(_env: EnvUnowned<'_>, _class: JClass<'_>) -> jint {
    match RUNNING.lock().ok().and_then(|mut g| g.take()) {
        Some(token) => {
            token.cancel();
            0
        }
        None => -1,
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn Java_com_gsb_gateway_Core_tx(_env: EnvUnowned<'_>, _class: JClass<'_>) -> jlong {
    TX.load(Ordering::Relaxed) as jlong
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn Java_com_gsb_gateway_Core_rx(_env: EnvUnowned<'_>, _class: JClass<'_>) -> jlong {
    RX.load(Ordering::Relaxed) as jlong
}

fn run(tun_fd: i32, proxy_url: &str, mtu: i32, udpgw: &str) -> i32 {
    android_logger::init_once(
        android_logger::Config::default()
            .with_tag("gsbcore")
            .with_max_level(log::LevelFilter::Info),
    );

    let token = CancellationToken::new();
    {
        let Ok(mut running) = RUNNING.lock() else { return -2 };
        if running.is_some() {
            log::error!("tunnel already running");
            return -3;
        }
        *running = Some(token.clone());
    }
    TX.store(0, Ordering::Relaxed);
    RX.store(0, Ordering::Relaxed);

    let code = match build_args(tun_fd, proxy_url, udpgw) {
        Ok(args) => {
            unsafe { tun2proxy::tun2proxy_set_traffic_status_callback(1, Some(on_traffic), std::ptr::null_mut()) };
            match tokio::runtime::Builder::new_multi_thread().enable_all().build() {
                Ok(rt) => match rt.block_on(tun2proxy::general_run_async(args, mtu as u16, false, token)) {
                    Ok(_) => 0,
                    Err(e) => {
                        log::error!("tun2proxy: {e}");
                        -10
                    }
                },
                Err(e) => {
                    log::error!("runtime: {e}");
                    -11
                }
            }
        }
        Err(e) => {
            log::error!("arguments: {e}");
            -12
        }
    };
    if let Ok(mut running) = RUNNING.lock() {
        *running = None;
    }
    code
}

fn build_args(tun_fd: i32, proxy_url: &str, udpgw: &str) -> Result<Args, String> {
    let mut args = Args::default();
    args.proxy(ProxyParameters::from_str(proxy_url).map_err(|e| e.to_string())?)
        .dns(ArgDns::Virtual)
        .dns_addr(IpAddr::V4(TUN_DNS))
        .tun_fd(Some(tun_fd))
        // Kotlin owns the ParcelFileDescriptor and closes it after stop.
        .close_fd_on_drop(false)
        .ipv6_enabled(false);
    args.virtual_dns_pool = FAKE_IP_POOL.parse().map_err(|e| format!("{e:?}"))?;
    args.max_sessions = 4096;
    args.tcp_timeout = 600;
    args.udp_timeout = 30;
    if !udpgw.is_empty() {
        args.udpgw_server(udpgw.parse().map_err(|e| format!("udpgw: {e}"))?);
    }
    Ok(args)
}
