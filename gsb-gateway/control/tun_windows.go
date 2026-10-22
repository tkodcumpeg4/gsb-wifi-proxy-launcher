package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func isElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// coreBinaries returns gsb-core.exe and the extra flags it needs (the path of
// the signed wintun.dll it loads).
func coreBinaries() (core string, args []string, err error) {
	// Prefer the embedded copies extracted to <data>\bin (single-exe build).
	if c, w, e := extractBundled(); e == nil && fileExists(c) && fileExists(w) {
		return c, []string{"--wintun", w}, nil
	}
	// Fallback: binaries sitting next to the exe or in the data dir.
	for _, dir := range []string{baseDir, exeDir()} {
		c, w := filepath.Join(dir, "gsb-core.exe"), filepath.Join(dir, "wintun.dll")
		if fileExists(c) && fileExists(w) {
			return c, []string{"--wintun", w}, nil
		}
	}
	return "", nil, errors.New("gsb-core.exe/wintun.dll çıkarılamadı veya bulunamadı")
}

// coreCommand runs gsb-core directly: startTUN already checked isElevated.
func coreCommand(core string, args ...string) *exec.Cmd {
	return hiddenCmd(core, args...)
}

// relaunchElevated restarts GSB Gateway through UAC ("runas") and returns
// true if the elevated copy was started; the caller then exits.
func relaunchElevated(extra ...string) bool {
	exe, _ := os.Executable()
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	params, _ := windows.UTF16PtrFromString(strings.Join(append([]string{"--no-browser", "--data-dir", `"` + baseDir + `"`}, extra...), " "))
	dir, _ := windows.UTF16PtrFromString(exeDir())
	return windows.ShellExecute(0, verb, file, params, dir, windows.SW_SHOWNORMAL) == nil
}
