package main

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"caldo/internal/config"
	"caldo/internal/notify"
	"caldo/internal/store"
)

func runNotify(cfg *config.Config, st *store.Store) error {
	_, err := notify.Run(st, cfg.DefaultReminders(), time.Now(), func(n notify.Notification) error {
		out, err := exec.Command("notify-send", "-a", "CalDo", "-i", "x-office-calendar",
			"-u", cfg.Reminders.Urgency, "--", n.Title, n.Body).CombinedOutput()
		if err != nil {
			return fmt.Errorf("notify-send: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	})
	return err
}
