package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"caldo/assets"
	"caldo/internal/config"
	"caldo/internal/notify"
	"caldo/internal/store"
)

func runNotify(cfg *config.Config, st *store.Store, paths config.Paths) error {
	icon := notifyIcon(paths.DataDir)
	_, err := notify.Run(st, cfg.DefaultReminders(), time.Now(), func(n notify.Notification) error {
		return desktopNotify(cfg.Reminders.Urgency, icon, n.Title, n.Body)
	})
	return err
}

// notifyIcon writes the embedded logo next to the database, so notifications
// show it without installing anything into an icon theme, and returns its
// path. It falls back to a stock icon name if the file can't be written.
func notifyIcon(dataDir string) string {
	path := filepath.Join(dataDir, "caldo.png")
	if b, err := os.ReadFile(path); err == nil && bytes.Equal(b, assets.NotifyIcon) {
		return path
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "x-office-calendar"
	}
	if err := os.WriteFile(path, assets.NotifyIcon, 0o644); err != nil {
		return "x-office-calendar"
	}
	return path
}

// desktopNotify shows a notification with notify-send, or with osascript on
// macOS (which has no urgency levels).
func desktopNotify(urgency, icon, title, body string) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		// Pass the text as arguments so it needs no AppleScript escaping.
		cmd = exec.Command("osascript",
			"-e", "on run argv",
			"-e", "display notification (item 2 of argv) with title \"CalDo\" subtitle (item 1 of argv)",
			"-e", "end run", title, body)
	} else {
		cmd = exec.Command("notify-send", "-a", "CalDo", "-i", icon,
			"-u", urgency, "--", title, body)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", filepath.Base(cmd.Path), err, strings.TrimSpace(string(out)))
	}
	return nil
}
