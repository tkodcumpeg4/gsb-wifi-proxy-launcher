package main

import (
	_ "embed"
	"os"
	"path/filepath"
)

// gsb-core.exe and wintun.dll are embedded so GSB_Gateway.exe ships as a single
// file. They are written to <data>\bin on startup (only when missing or
// changed) and run from there.

//go:embed embedded/gsb-core.exe
var embeddedCore []byte

//go:embed embedded/wintun.dll
var embeddedWintun []byte

// extractBundled writes the embedded binaries to <baseDir>\bin and returns
// their paths. It rewrites a file only when its content differs, so a running
// gsb-core.exe is not disturbed unnecessarily.
func extractBundled() (core, wintun string, err error) {
	dir := filepath.Join(baseDir, "bin")
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	core = filepath.Join(dir, "gsb-core.exe")
	wintun = filepath.Join(dir, "wintun.dll")
	if err = writeIfChanged(core, embeddedCore); err != nil {
		return "", "", err
	}
	if err = writeIfChanged(wintun, embeddedWintun); err != nil {
		return "", "", err
	}
	return core, wintun, nil
}
