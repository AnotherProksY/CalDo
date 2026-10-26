package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"caldo/internal/config"
	"caldo/internal/notify"
	"caldo/internal/store"
)

func runNotify(cfg *config.Config, st *store.Store) error {
	_, err := notify.Run(st, cfg.DefaultReminders(), time.Now(), func(n notify.Notification) error {
		return desktopNotify(cfg.Reminders.Urgency, n.Title, n.Body)
	})
	return err
}

// desktopNotify shows a notification with notify-send, or with osascript on
// macOS (which has no urgency levels).
func desktopNotify(urgency, title, body string) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		// Pass the text as arguments so it needs no AppleScript escaping.
		cmd = exec.Command("osascript",
			"-e", "on run argv",
			"-e", "display notification (item 2 of argv) with title \"CalDo\" subtitle (item 1 of argv)",
			"-e", "end run", title, body)
	} else {
		cmd = exec.Command("notify-send", "-a", "CalDo", "-i", "x-office-calendar",
			"-u", urgency, "--", title, body)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", filepath.Base(cmd.Path), err, strings.TrimSpace(string(out)))
	}
	return nil
}
