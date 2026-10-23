package main

import (
	"errors"
	"os/exec"
)

// On Android the app owns the VPN: VpnService creates the TUN interface and
// runs gsb-core in-process, pointing it at this bridge on 127.0.0.1:8880. The
// panel switches it through the app's JavaScript bridge (window.GSB), so the
// TUN helpers here only report that this process does not manage it.

var errAppManagesTUN = errors.New("Android'de sanal adaptörü (VPN) uygulama yönetir")

// isElevated is true so the panel never offers a relaunch.
func isElevated() bool { return true }

func coreBinaries() (core string, args []string, err error) {
	return "", nil, errAppManagesTUN
}

func coreCommand(core string, args ...string) *exec.Cmd { return exec.Command(core, args...) }

func relaunchElevated(extra ...string) bool { return false }
