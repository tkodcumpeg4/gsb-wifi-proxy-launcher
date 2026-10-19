package main

import (
	"encoding/json"
	"os"
	"time"
)

// Lifetime data usage is persisted in usage.json so the "Toplam Veri" metric
// survives restarts, unlike the per-session bytes_transferred counter.

type usageFile struct {
	TotalBytes int64 `json:"total_bytes"`
}

func usagePath() string { return dataPath("usage.json") }

// loadUsage seeds baseTotalBytes from disk at startup.
func loadUsage() {
	raw, err := os.ReadFile(usagePath())
	if err != nil {
		return
	}
	var u usageFile
	if json.Unmarshal(raw, &u) == nil && u.TotalBytes > 0 {
		stats.baseTotalBytes.Store(u.TotalBytes)
	}
}

// saveUsage writes the current lifetime total (base + this session) to disk.
func saveUsage() {
	total := stats.baseTotalBytes.Load() + stats.BytesTransferred.Load()
	raw, err := json.Marshal(usageFile{TotalBytes: total})
	if err != nil {
		return
	}
	tmp := usagePath() + ".tmp"
	if os.WriteFile(tmp, raw, 0o644) == nil {
		os.Rename(tmp, usagePath())
	}
}

// usagePersistLoop flushes the lifetime total periodically so a crash loses at
// most one interval of data.
func usagePersistLoop() {
	for range time.Tick(30 * time.Second) {
		saveUsage()
	}
}
