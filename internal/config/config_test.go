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
