package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"caldo/assets"
)

func TestNotifyIcon(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "caldo")
	path := notifyIcon(dir)
	if path != filepath.Join(dir, "caldo.png") {
		t.Fatalf("icon path %q", path)
	}
	if b, err := os.ReadFile(path); err != nil || !bytes.Equal(b, assets.NotifyIcon) {
		t.Fatalf("icon not written: %v", err)
	}
	// A stale icon from an older release is replaced.
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	notifyIcon(dir)
	if b, _ := os.ReadFile(path); !bytes.Equal(b, assets.NotifyIcon) {
		t.Fatal("stale icon not replaced")
	}
}
