package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[google]\nclient_secret_file = \"~/.config/caldo/client_secret.json\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".config/caldo/client_secret.json"); c.Google.ClientSecretFile != want {
		t.Fatalf("ClientSecretFile = %q, want %q", c.Google.ClientSecretFile, want)
	}
}

func TestTaskViewGrouping(t *testing.T) {
	load := func(t *testing.T, body string) *Config {
		t.Helper()
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		c, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	c := load(t, "")
	if c.Tasks.DefaultView != "today" || c.Tasks.GroupToday != "date" || c.Tasks.GroupUpcoming != "day" || c.Tasks.GroupAll != "project" {
		t.Fatalf("defaults: %+v", c.Tasks)
	}
	// The pre-views group_by still sets the All view's grouping.
	if c := load(t, "[tasks]\ngroup_by = \"label\"\n"); c.Tasks.GroupAll != "label" {
		t.Fatalf("group_by alias: %+v", c.Tasks)
	}
	if c := load(t, "[tasks]\ngroup_by = \"label\"\ngroup_all = \"date\"\n"); c.Tasks.GroupAll != "date" {
		t.Fatalf("group_all wins over group_by: %+v", c.Tasks)
	}
}
