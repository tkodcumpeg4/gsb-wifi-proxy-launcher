//! Non-persistent IPv4 routes that send traffic into GSB-TUN, plus bypass
//! routes that pin the upstream proxy to the Wi-Fi gateway. Every route added
//! here is recorded and deleted again by `restore` (also on drop).
//!
//! Windows drives `route.exe`; Linux drives iproute2 (`ip`) and, when
//! systemd-resolved is running, points the system resolver at the tunnel.

use std::net::Ipv4Addr;
use std::process::Command;

use anyhow::{anyhow, bail, Result};

struct Added {
    dest: Ipv4Addr,
    mask: Ipv4Addr,
    gw: Ipv4Addr,
    ifidx: u32,
}

pub struct RouteTable {
    tun_if: u32,
    tun_gw: Ipv4Addr,
    wifi_if: u32,
    wifi_gw: Ipv4Addr,
    added: Vec<Added>,
    #[cfg(target_os = "linux")]
    source_rule: Option<Ipv4Addr>,
}

impl RouteTable {
    pub fn new(tun_if: u32, tun_gw: Ipv4Addr, wifi_if: u32, wifi_gw: Ipv4Addr) -> Self {
        Self {
            tun_if,
            tun_gw,
            wifi_if,
            wifi_gw,
            added: vec![],
            #[cfg(target_os = "linux")]
            source_rule: None,
        }
    }

    pub fn into_tunnel(&mut self, cidr: &str) -> Result<()> {
        let (dest, mask) = parse_cidr(cidr)?;
        self.add(dest, mask, self.tun_gw, self.tun_if)
    }

    pub fn bypass(&mut self, cidr: &str) -> Result<()> {
        let (dest, mask) = parse_cidr(cidr)?;
        self.add(dest, mask, self.wifi_gw, self.wifi_if)
    }

    fn add(&mut self, dest: Ipv4Addr, mask: Ipv4Addr, gw: Ipv4Addr, ifidx: u32) -> Result<()> {
        sys::add(dest, mask, gw, ifidx)?;
        self.added.push(Added { dest, mask, gw, ifidx });
        Ok(())
    }

    /// Linux only: sockets bound to the Wi-Fi address (the Go bridge with
    /// bind_to_wifi) leave through the Wi-Fi gateway even while the tunnel
    /// owns the main table. Windows does this by itself (strong host send).
    #[cfg(target_os = "linux")]
    pub fn pin_source(&mut self, src: Ipv4Addr) -> Result<()> {
        sys::add_source_rule(src, self.wifi_gw, self.wifi_if)?;
        self.source_rule = Some(src);
        Ok(())
    }

    pub fn restore(&mut self) {
        for a in self.added.drain(..).rev() {
            sys::delete(a.dest, a.mask, a.gw, a.ifidx);
        }
        #[cfg(target_os = "linux")]
        if let Some(src) = self.source_rule.take() {
            sys::delete_source_rule(src);
        }
    }
}

impl Drop for RouteTable {
    fn drop(&mut self) {
        self.restore();
    }
}

fn parse_cidr(s: &str) -> Result<(Ipv4Addr, Ipv4Addr)> {
    let (ip, len) = match s.split_once('/') {
        Some((ip, len)) => (ip, len.parse::<u32>().map_err(|_| anyhow!("geçersiz önek: {s}"))?),
        None => (s, 32),
    };
    if len > 32 {
        bail!("geçersiz önek: {s}");
    }
    let ip: Ipv4Addr = ip.parse().map_err(|_| anyhow!("geçersiz IPv4: {s}"))?;
    let mask = if len == 0 { 0 } else { u32::MAX << (32 - len) };
    Ok((Ipv4Addr::from(u32::from(ip) & mask), Ipv4Addr::from(mask)))
}

pub use sys::{flush_dns, link_up, set_tunnel_dns};

#[cfg(windows)]
mod sys {
    use super::*;
    use std::os::windows::process::CommandExt;

    const CREATE_NO_WINDOW: u32 = 0x0800_0000;

    pub fn add(dest: Ipv4Addr, mask: Ipv4Addr, gw: Ipv4Addr, ifidx: u32) -> Result<()> {
        // A stale copy (e.g. from a crashed run) would make `route add` fail.
        let _ = route(&["delete", &dest.to_string(), "mask", &mask.to_string(), &gw.to_string()]);
        route(&[
            "add",
            &dest.to_string(),
            "mask",
            &mask.to_string(),
            &gw.to_string(),
            "metric",
            "1",
            "if",
            &ifidx.to_string(),
        ])
    }

    pub fn delete(dest: Ipv4Addr, mask: Ipv4Addr, gw: Ipv4Addr, ifidx: u32) {
        let _ = route(&[
            "delete",
            &dest.to_string(),
            "mask",
            &mask.to_string(),
            &gw.to_string(),
            "if",
            &ifidx.to_string(),
        ]);
    }

    fn route(args: &[&str]) -> Result<()> {
        let out = Command::new("route").args(args).creation_flags(CREATE_NO_WINDOW).output()?;
        // route.exe reports most failures on stdout with exit code 0.
        let text = String::from_utf8_lossy(&out.stdout).to_lowercase();
        if !out.status.success() || text.contains("failed") || text.contains("başarısız") {
            bail!("route {} → {}", args.join(" "), text.trim());
        }
        Ok(())
    }

    /// Wintun adapters come up with the configuration.
    pub fn link_up(_name: &str) -> Result<()> {
        Ok(())
    }

    /// Windows takes the adapter's DNS server from the Wintun configuration.
    pub fn set_tunnel_dns(_tun_name: &str, _dns: Ipv4Addr) {}

    pub fn flush_dns() {
        let _ = Command::new("ipconfig").arg("/flushdns").creation_flags(CREATE_NO_WINDOW).output();
    }
}

#[cfg(target_os = "linux")]
mod sys {
    use super::*;

    /// Policy-routing table/priority for `pin_source` ("GS" = 0x4753).
    const SOURCE_TABLE: &str = "18259";
    const SOURCE_RULE_MAIN: &str = "18258";

    fn prefix(dest: Ipv4Addr, mask: Ipv4Addr) -> String {
        format!("{dest}/{}", u32::from(mask).count_ones())
    }

    /// Interface name for an ifindex, from sysfs (no libc needed).
    fn ifname(ifidx: u32) -> Result<String> {
        for e in std::fs::read_dir("/sys/class/net")?.flatten() {
            let idx = std::fs::read_to_string(e.path().join("ifindex")).unwrap_or_default();
            if idx.trim().parse::<u32>().ok() == Some(ifidx) {
                return Ok(e.file_name().to_string_lossy().into_owned());
            }
        }
        bail!("ifindex {ifidx} için arayüz bulunamadı")
    }

    pub fn add(dest: Ipv4Addr, mask: Ipv4Addr, gw: Ipv4Addr, ifidx: u32) -> Result<()> {
        // `replace` also overwrites a stale copy left by a crashed run.
        // `onlink`: GSB-TUN has no peer route, so the kernel would reject
        // its gateway as unreachable.
        let dev = ifname(ifidx)?;
        ip(&["-4", "route", "replace", &prefix(dest, mask), "via", &gw.to_string(), "dev", &dev, "metric", "1", "onlink"])
    }

    pub fn delete(dest: Ipv4Addr, mask: Ipv4Addr, gw: Ipv4Addr, ifidx: u32) {
        let (dest, gw) = (prefix(dest, mask), gw.to_string());
        let mut args = vec!["-4", "route", "del", &dest, "via", &gw, "metric", "1"];
        let dev = ifname(ifidx).unwrap_or_default();
        if !dev.is_empty() {
            args.extend(["dev", &dev]);
        }
        let _ = ip(&args);
    }

    /// Two rules for packets from `src`: first the main table minus its /0
    /// and /1 routes (keeps the LAN and the /32 bypasses), then a table whose
    /// only route is the Wi-Fi default.
    pub fn add_source_rule(src: Ipv4Addr, gw: Ipv4Addr, ifidx: u32) -> Result<()> {
        let dev = ifname(ifidx)?;
        delete_source_rule(src);
        let from = format!("{src}/32");
        ip(&["-4", "route", "replace", "default", "via", &gw.to_string(), "dev", &dev, "table", SOURCE_TABLE])?;
        ip(&["-4", "rule", "add", "from", &from, "lookup", "main", "suppress_prefixlength", "1", "priority", SOURCE_RULE_MAIN])?;
        ip(&["-4", "rule", "add", "from", &from, "lookup", SOURCE_TABLE, "priority", SOURCE_TABLE])
    }

    pub fn delete_source_rule(src: Ipv4Addr) {
        let from = format!("{src}/32");
        while ip(&["-4", "rule", "del", "from", &from, "priority", SOURCE_RULE_MAIN]).is_ok() {}
        while ip(&["-4", "rule", "del", "from", &from, "priority", SOURCE_TABLE]).is_ok() {}
        let _ = ip(&["-4", "route", "flush", "table", SOURCE_TABLE]);
    }

    fn ip(args: &[&str]) -> Result<()> {
        let out = Command::new("ip").args(args).output().map_err(|e| anyhow!("ip çalıştırılamadı: {e}"))?;
        if !out.status.success() {
            bail!("ip {} → {}", args.join(" "), String::from_utf8_lossy(&out.stderr).trim());
        }
        Ok(())
    }

    /// The tun crate's `.up()` does not reliably leave the Linux link up, and
    /// the kernel refuses routes through a down device.
    pub fn link_up(name: &str) -> Result<()> {
        ip(&["link", "set", "dev", name, "up"])
    }

    /// With systemd-resolved, make the tunnel the only DNS route ("~."), so
    /// the resolver never asks the (filtering) Wi-Fi DNS over its own link.
    /// The settings vanish with the link when gsb-core exits.
    pub fn set_tunnel_dns(tun_name: &str, dns: Ipv4Addr) {
        let run = |args: &[&str]| Command::new("resolvectl").args(args).output().map(|o| o.status.success()).unwrap_or(false);
        if run(&["dns", tun_name, &dns.to_string()]) {
            run(&["domain", tun_name, "~."]);
            run(&["default-route", tun_name, "yes"]);
        }
    }

    pub fn flush_dns() {
        let _ = Command::new("resolvectl").arg("flush-caches").output();
    }
}

#[cfg(test)]
mod tests {
    use super::parse_cidr;

    #[test]
    fn cidr() {
        assert_eq!(parse_cidr("0.0.0.0/1").unwrap(), ("0.0.0.0".parse().unwrap(), "128.0.0.0".parse().unwrap()));
        assert_eq!(parse_cidr("203.0.113.10").unwrap().1, "255.255.255.255".parse::<std::net::Ipv4Addr>().unwrap());
        assert_eq!(parse_cidr("10.50.3.7/21").unwrap().0, "10.50.0.0".parse::<std::net::Ipv4Addr>().unwrap());
        assert!(parse_cidr("1.2.3.4/33").is_err());
    }
}
