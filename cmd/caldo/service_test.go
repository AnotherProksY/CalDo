package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteSystemdUnits(t *testing.T) {
	dir := t.TempDir()
	timers, err := writeSystemdUnits(dir, "/opt/my apps/caldo")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(timers, " ") != "caldo-notify.timer caldo-sync.timer" {
		t.Fatalf("timers = %v", timers)
	}
	svc, _ := os.ReadFile(filepath.Join(dir, "caldo-sync.service"))
	if !strings.Contains(string(svc), `ExecStart="/opt/my apps/caldo" sync`) {
		t.Fatalf("sync service:\n%s", svc)
	}
	timer, _ := os.ReadFile(filepath.Join(dir, "caldo-notify.timer"))
	if !strings.Contains(string(timer), "OnCalendar=*-*-* *:*:00\n") || !strings.Contains(string(timer), "WantedBy=timers.target") {
		t.Fatalf("notify timer:\n%s", timer)
	}
}
