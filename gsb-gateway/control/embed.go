package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"os"
)

// The proxy list ships inside the binary; the platform files embed gsb-core
// (and wintun.dll on Windows) the same way.

//go:embed embedded/list.txt
var embeddedList []byte

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
