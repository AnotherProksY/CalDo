package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Background jobs: `caldo notify` every minute and `caldo sync` every
// 15 minutes, as systemd user timers on Linux and launchd agents on macOS.
type job struct {
	name     string // "notify" or "sync"
	desc     string
	interval int    // seconds, for launchd
	timer    string // [Timer] section body for systemd
}

var jobs = []job{
	{"notify", "CalDo reminder notifications", 60,
		"OnCalendar=*-*-* *:*:00\nAccuracySec=5s\n"},
	{"sync", "CalDo sync with Google Calendar and Todoist", 15 * 60,
		"OnStartupSec=1min\nOnUnitActiveSec=15min\n"},
}

const launchdPrefix = "io.github.anotherproksy.caldo."

func runService(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: caldo service install|uninstall")
	}
	switch args[0] {
	case "install":
		if err := installService(); err != nil {
			return err
		}
		fmt.Println("Background sync (every 15 min) and reminders (every minute) are enabled.")
	case "uninstall":
		if err := uninstallService(); err != nil {
			return err
		}
		fmt.Println("Background sync and reminders are disabled.")
	default:
		return fmt.Errorf("unknown service command %q (use install or uninstall)", args[0])
	}
	return nil
}

// serviceInstalled reports whether the jobs' unit files exist.
func serviceInstalled() bool {
	dir, err := serviceDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(dir, serviceFile(jobs[0])))
	return err == nil
}

func serviceSupported() bool { return runtime.GOOS == "linux" || runtime.GOOS == "darwin" }

func serviceDir() (string, error) {
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir()
		return filepath.Join(home, "Library", "LaunchAgents"), err
	}
	cfg, err := os.UserConfigDir()
	return filepath.Join(cfg, "systemd", "user"), err
}

func serviceFile(j job) string {
	if runtime.GOOS == "darwin" {
		return launchdPrefix + j.name + ".plist"
	}
	return "caldo-" + j.name + ".service"
}

// executablePath prefers the `caldo` found in PATH when it is this binary, so
// units keep working after package-manager upgrades swap the real file.
func executablePath() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if p, err := exec.LookPath("caldo"); err == nil {
		if abs, err := filepath.Abs(p); err == nil && sameFile(abs, self) {
			return abs, nil
		}
	}
	return self, nil
}

func sameFile(a, b string) bool {
	fa, err1 := os.Stat(a)
	fb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(fa, fb)
}

func installService() error {
	if !serviceSupported() {
		return fmt.Errorf("background jobs are not supported on %s", runtime.GOOS)
	}
	exe, err := executablePath()
	if err != nil {
		return err
	}
	dir, err := serviceDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if runtime.GOOS == "darwin" {
		return installLaunchd(dir, exe)
	}
	return installSystemd(dir, exe)
}

func uninstallService() error {
	if !serviceSupported() {
		return fmt.Errorf("background jobs are not supported on %s", runtime.GOOS)
	}
	dir, err := serviceDir()
	if err != nil {
		return err
	}
	if runtime.GOOS == "darwin" {
		for _, j := range jobs {
			path := filepath.Join(dir, serviceFile(j))
			_ = exec.Command("launchctl", "bootout", launchdDomain(), path).Run() // fails when not loaded
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		return nil
	}
	var timers []string
	for _, j := range jobs {
		timers = append(timers, "caldo-"+j.name+".timer")
	}
	_ = systemctl(append([]string{"disable", "--now"}, timers...)...) // fails when not installed
	for _, j := range jobs {
		for _, f := range []string{"caldo-" + j.name + ".service", "caldo-" + j.name + ".timer"} {
			if err := os.Remove(filepath.Join(dir, f)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return systemctl("daemon-reload")
}

// --- systemd -------------------------------------------------------------

func installSystemd(dir, exe string) error {
	timers, err := writeSystemdUnits(dir, exe)
	if err != nil {
		return err
	}
	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	return systemctl(append([]string{"enable", "--now"}, timers...)...)
}

// writeSystemdUnits writes a service and a timer per job and returns the timers.
func writeSystemdUnits(dir, exe string) ([]string, error) {
	var timers []string
	for _, j := range jobs {
		svc := fmt.Sprintf("[Unit]\nDescription=%s\nAfter=network-online.target\n\n"+
			"[Service]\nType=oneshot\nExecStart=%s %s\n", j.desc, systemdQuote(exe), j.name)
		timer := fmt.Sprintf("[Unit]\nDescription=%s (timer)\n\n[Timer]\n%s\n"+
			"[Install]\nWantedBy=timers.target\n", j.desc, j.timer)
		if err := os.WriteFile(filepath.Join(dir, "caldo-"+j.name+".service"), []byte(svc), 0o644); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dir, "caldo-"+j.name+".timer"), []byte(timer), 0o644); err != nil {
			return nil, err
		}
		timers = append(timers, "caldo-"+j.name+".timer")
	}
	return timers, nil
}

func systemctl(args ...string) error {
	out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl --user %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func systemdQuote(s string) string {
	if !strings.ContainsAny(s, " \t\"\\") {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// --- launchd -------------------------------------------------------------

func installLaunchd(dir, exe string) error {
	logDir, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	logDir = filepath.Join(logDir, "caldo")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	for _, j := range jobs {
		label := launchdPrefix + j.name
		plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>%s</string>
	</array>
	<key>StartInterval</key>
	<integer>%d</integer>
	<key>RunAtLoad</key>
	<true/>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, label, xmlEscape(exe), j.name, j.interval, xmlEscape(filepath.Join(logDir, j.name+".log")))
		path := filepath.Join(dir, serviceFile(j))
		_ = exec.Command("launchctl", "bootout", launchdDomain(), path).Run() // reload an older copy
		if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
			return err
		}
		if out, err := exec.Command("launchctl", "bootstrap", launchdDomain(), path).CombinedOutput(); err != nil {
			return fmt.Errorf("launchctl bootstrap %s: %w: %s", label, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func launchdDomain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
