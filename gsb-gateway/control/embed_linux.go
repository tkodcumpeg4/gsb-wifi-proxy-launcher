package main

import (
	_ "embed"
	"os"
	"path/filepath"
)

// gsb-core is embedded so the Linux build ships as a single file too. It is
// written to <data>/bin on startup (only when missing or changed) and run
// from there.

//go:embed embedded/gsb-core
var embeddedCore []byte

func extractBundled() (core string, err error) {
	dir := filepath.Join(baseDir, "bin")
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	core = filepath.Join(dir, "gsb-core")
	if err = writeIfChanged(core, embeddedCore); err != nil {
		return "", err
	}
	return core, nil
}
