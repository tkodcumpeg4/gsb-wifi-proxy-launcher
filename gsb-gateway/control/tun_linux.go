//go:build linux && !android

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// On Linux only gsb-core needs privileges (CAP_NET_ADMIN for /dev/net/tun and
// the routes), so the panel and tray keep running as the desktop user and the
// core alone is started through pkexec. Its stdin/stdout pipes survive pkexec,
// so the control protocol is unchanged; a closed stdin still stops it.

func init() {
	coreReadyTimeout = 2 * time.Minute // includes the polkit password prompt
}

// isElevated reports whether TUN can be started from this process: as root,
// or through pkexec. The panel only offers a relaunch when this is false.
func isElevated() bool {
	if os.Geteuid() == 0 {
		return true
	}
	_, err := exec.LookPath("pkexec")
	return err == nil
}

func coreBinaries() (core string, args []string, err error) {
	if c, e := extractBundled(); e == nil && fileExists(c) {
		return c, nil, nil
	}
	for _, dir := range []string{baseDir, exeDir()} {
		if c := filepath.Join(dir, "gsb-core"); fileExists(c) {
			return c, nil, nil
		}
	}
	return "", nil, errors.New("gsb-core çıkarılamadı veya bulunamadı")
}

func coreCommand(core string, args ...string) *exec.Cmd {
	if os.Geteuid() == 0 {
		return exec.Command(core, args...)
	}
	return exec.Command("pkexec", append([]string{core}, args...)...)
}

// relaunchElevated is never needed on Linux (see isElevated); it reports
// false so callers keep running.
func relaunchElevated(extra ...string) bool { return false }
