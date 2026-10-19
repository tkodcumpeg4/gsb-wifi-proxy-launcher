package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
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

//go:embed embedded/list.txt
var embeddedList []byte

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

func writeIfChanged(path string, data []byte) error {
	if len(data) == 0 {
		return nil // dev build without embedded binary; caller falls back to disk
	}
	if cur, err := os.ReadFile(path); err == nil && sameBytes(cur, data) {
		return nil
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return err
	}
	// A running gsb-core.exe locks the file; renaming over it fails, so remove
	// first (the old copy was already killed by stopTUN before a restart).
	os.Remove(path)
	return os.Rename(tmp, path)
}

func sameBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	ha, hb := sha256.Sum256(a), sha256.Sum256(b)
	return hex.EncodeToString(ha[:]) == hex.EncodeToString(hb[:])
}
