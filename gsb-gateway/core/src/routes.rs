//! Non-persistent IPv4 routes that send traffic into GSB-TUN, plus bypass
//! routes that pin the upstream proxy to the Wi-Fi gateway. Every route added
//! here is recorded and deleted again by `restore` (also on drop).

use std::net::Ipv4Addr;
use std::os::windows::process::CommandExt;
use std::process::Command;

use anyhow::{anyhow, bail, Result};

const CREATE_NO_WINDOW: u32 = 0x0800_0000;

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
}

impl RouteTable {
    pub fn new(tun_if: u32, tun_gw: Ipv4Addr, wifi_if: u32, wifi_gw: Ipv4Addr) -> Self {
        Self { tun_if, tun_gw, wifi_if, wifi_gw, added: vec![] }
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
        ])?;
        self.added.push(Added { dest, mask, gw, ifidx });
        Ok(())
    }

    pub fn restore(&mut self) {
        for a in self.added.drain(..).rev() {
            let _ = route(&[
                "delete",
                &a.dest.to_string(),
                "mask",
                &a.mask.to_string(),
                &a.gw.to_string(),
                "if",
                &a.ifidx.to_string(),
            ]);
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

fn route(args: &[&str]) -> Result<()> {
    let out = Command::new("route").args(args).creation_flags(CREATE_NO_WINDOW).output()?;
    // route.exe reports most failures on stdout with exit code 0.
    let text = String::from_utf8_lossy(&out.stdout).to_lowercase();
    if !out.status.success() || text.contains("failed") || text.contains("başarısız") {
        bail!("route {} → {}", args.join(" "), text.trim());
    }
    Ok(())
}

pub fn flush_dns() {
    let _ = Command::new("ipconfig").arg("/flushdns").creation_flags(CREATE_NO_WINDOW).output();
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
