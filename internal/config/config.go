// Package config loads CalDo's TOML configuration and resolves XDG paths.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/adrg/xdg"
)

const appName = "caldo"

type Config struct {
	General  General             `toml:"general"`
	Calendar CalendarConfig      `toml:"calendar"`
	Tasks    TasksConfig         `toml:"tasks"`
	Sync     SyncConfig          `toml:"sync"`
	Google   GoogleConfig        `toml:"google"`
	Todoist  TodoistConfig       `toml:"todoist"`
	Theme    Theme               `toml:"theme"`
	Keys     map[string][]string `toml:"keys"`
}

type General struct {
	// Editor overrides $VISUAL/$EDITOR when set.
	Editor string `toml:"editor"`
	// Layout is "split" or "full" at startup.
	Layout string `toml:"layout"`
	// SplitRatio is the calendar pane share of the width in split mode (0.2..0.8).
	SplitRatio float64 `toml:"split_ratio"`
}

type CalendarConfig struct {
	// WeekStart is "monday" or "sunday".
	WeekStart string `toml:"week_start"`
	// DefaultView is "month", "week" or "agenda".
	DefaultView string `toml:"default_view"`
	AgendaDays  int    `toml:"agenda_days"`
	// DefaultDuration of new timed events.
	DefaultDuration duration `toml:"default_duration"`
	TimeFormat      string   `toml:"time_format"`
}

type TasksConfig struct {
	// GroupBy is "project", "date" or "label".
	GroupBy       string `toml:"group_by"`
	ShowCompleted bool   `toml:"show_completed"`
}

type SyncConfig struct {
	Interval duration `toml:"interval"`
}

type GoogleConfig struct {
	// ClientSecretFile is the path to the Desktop OAuth client JSON from Google Cloud Console.
	ClientSecretFile string `toml:"client_secret_file"`
	// Calendars restricts sync to these calendar names/ids; empty means all.
	Calendars []string `toml:"calendars"`
	// PastDays bounds how far back the initial full sync reaches.
	PastDays int `toml:"past_days"`
}

type TodoistConfig struct {
	// BaseURL is overridable for tests.
	BaseURL string `toml:"base_url"`
}

type Theme struct {
	Accent    string `toml:"accent"`
	Muted     string `toml:"muted"`
	Border    string `toml:"border"`
	Today     string `toml:"today"`
	Success   string `toml:"success"`
	Pending   string `toml:"pending"`
	Warning   string `toml:"warning"`
	Danger    string `toml:"danger"`
	Selection string `toml:"selection"`
}

// duration lets TOML hold values like "15m".
type duration struct{ time.Duration }

func (d *duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

func (d duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// Paths holds every on-disk location CalDo uses.
type Paths struct {
	ConfigFile  string
	DataDir     string
	DBFile      string
	SecretsFile string
}

func DefaultPaths() (Paths, error) {
	cfg, err := xdg.ConfigFile(filepath.Join(appName, "config.toml"))
	if err != nil {
		return Paths{}, err
	}
	db, err := xdg.DataFile(filepath.Join(appName, "caldo.db"))
	if err != nil {
		return Paths{}, err
	}
	dataDir := filepath.Dir(db)
	return Paths{
		ConfigFile:  cfg,
		DataDir:     dataDir,
		DBFile:      db,
		SecretsFile: filepath.Join(dataDir, "secrets.json"),
	}, nil
}

// Load reads the config file at path, falling back to defaults when it does not exist.
func Load(path string) (*Config, error) {
	c := Default()
	if _, err := os.Stat(path); err == nil {
		if _, err := toml.DecodeFile(path, c); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	c.normalize()
	return c, nil
}

func (c *Config) normalize() {
	d := Default()
	if c.General.SplitRatio < 0.2 || c.General.SplitRatio > 0.8 {
		c.General.SplitRatio = d.General.SplitRatio
	}
	if c.Calendar.AgendaDays <= 0 {
		c.Calendar.AgendaDays = d.Calendar.AgendaDays
	}
	if c.Calendar.DefaultDuration.Duration <= 0 {
		c.Calendar.DefaultDuration = d.Calendar.DefaultDuration
	}
	if c.Sync.Interval.Duration < 30*time.Second {
		c.Sync.Interval = d.Sync.Interval
	}
	if c.Todoist.BaseURL == "" {
		c.Todoist.BaseURL = d.Todoist.BaseURL
	}
	c.Google.ClientSecretFile = expandHome(c.Google.ClientSecretFile)
	// User key bindings replace defaults per action; untouched actions keep defaults.
	keys := DefaultKeys()
	for action, ks := range c.Keys {
		keys[action] = ks
	}
	c.Keys = keys
}

// expandHome resolves a leading "~/" against the user's home directory.
func expandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[1:])
}

func (c *Config) SyncInterval() time.Duration { return c.Sync.Interval.Duration }

func (c *Config) DefaultEventDuration() time.Duration { return c.Calendar.DefaultDuration.Duration }
