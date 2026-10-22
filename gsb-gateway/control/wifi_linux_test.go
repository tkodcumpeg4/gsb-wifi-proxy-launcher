package main

import "testing"

func TestRouteField(t *testing.T) {
	out := "8.8.8.8 via 10.50.0.1 dev wlp2s0 src 10.50.3.7 uid 1000 \n    cache \n"
	if got := routeField(out, "dev"); got != "wlp2s0" {
		t.Fatalf("dev = %q", got)
	}
	if got := routeField(out, "via"); got != "10.50.0.1" {
		t.Fatalf("via = %q", got)
	}
	if got := routeField("1.1.1.1 dev eth0", "via"); got != "" {
		t.Fatalf("via without gateway = %q", got)
	}
}

// TestFindUplink runs against the host's real routing table.
func TestFindUplink(t *testing.T) {
	w := findUplink("8.8.8.8")
	if w == nil {
		t.Skip("no IPv4 route to 8.8.8.8")
	}
	if w.IfIndex == 0 || w.IP == "" || w.Gateway == "" {
		t.Fatalf("incomplete uplink: %+v", *w)
	}
	if gw := defaultGateway(w.AdapterName); gw != "" && gw != w.Gateway {
		t.Logf("/proc/net/route gateway %s differs from ip route get %s", gw, w.Gateway)
	}
	t.Logf("uplink: %+v", *w)
}
